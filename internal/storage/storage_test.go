package storage

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/trevorswanson/composetpl/internal/release"
)

// fakeVolume emulates the docker commands the Volume driver issues, keeping the
// "volume" contents in memory, so the driver can be tested without Docker.
type fakeVolume struct {
	files   map[string][]byte // path relative to /data
	calls   []string
	created bool
}

func (f *fakeVolume) exec(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	f.calls = append(f.calls, strings.Join(args, " "))
	for len(args) > 0 && args[0] != "volume" && args[0] != "run" {
		args = args[1:] // skip global flags such as --context X
	}
	switch {
	case args[0] == "volume" && args[1] == "create":
		f.created = true
		return nil
	case args[0] != "run":
		return fmt.Errorf("unexpected docker %v", args)
	}
	cmd := args[len(args)-1]
	switch {
	case contains(args, "-cf"):
		tw := tar.NewWriter(stdout)
		names := make([]string, 0, len(f.files))
		for n := range f.files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			_ = tw.WriteHeader(&tar.Header{Name: "./" + n, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(f.files[n]))})
			_, _ = tw.Write(f.files[n])
		}
		return tw.Close()
	case contains(args, "-xf"):
		tr := tar.NewReader(stdin)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			if h.Typeflag == tar.TypeReg {
				data, _ := io.ReadAll(tr)
				f.files[h.Name] = data
			}
		}
	case strings.HasPrefix(cmd, "rm -f "):
		for _, p := range strings.Fields(strings.SplitN(cmd, ";", 2)[0])[2:] {
			delete(f.files, strings.TrimPrefix(p, "/data/"))
		}
		return nil
	}
	return fmt.Errorf("unexpected helper command %v", args)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func drivers(t *testing.T) map[string]func() Driver {
	dir := t.TempDir()
	fv := &fakeVolume{files: map[string][]byte{}}
	return map[string]func() Driver{
		"local": func() Driver { return NewLocal(dir) },
		// A fresh Volume per call shares the fake volume but not the cache,
		// which checks that writes really reach the volume.
		"volume": func() Driver {
			v := NewVolume(nil, "")
			v.Exec = fv.exec
			return v
		},
	}
}

func rel(name string, rev int, status release.Status) *release.Release {
	return &release.Release{Name: name, Revision: rev, Status: status, Manifest: "services: {}\n",
		Config: map[string]interface{}{"secret": "s3cret"}}
}

func TestDriverContract(t *testing.T) {
	for name, newDriver := range drivers(t) {
		t.Run(name, func(t *testing.T) {
			d := newDriver()
			if _, err := d.History("media"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("History on empty store: got %v, want ErrNotFound", err)
			}
			if err := d.Save(rel("media", 1, release.StatusSuperseded), rel("media", 2, release.StatusDeployed),
				rel("media", 10, release.StatusFailed), rel("dns", 1, release.StatusDeployed)); err != nil {
				t.Fatal(err)
			}

			d = newDriver()
			h, err := d.History("media")
			if err != nil {
				t.Fatal(err)
			}
			var revs []int
			for _, r := range h {
				revs = append(revs, r.Revision)
			}
			if fmt.Sprint(revs) != "[1 2 10]" {
				t.Errorf("history revisions = %v", revs)
			}
			if r, _ := LastDeployed(d, "media"); r == nil || r.Revision != 2 {
				t.Errorf("LastDeployed = %+v", r)
			}
			if r, _ := Latest(d, "media"); r.Revision != 10 || r.Config["secret"] != "s3cret" || r.APIVersion != release.APIVersion {
				t.Errorf("Latest = %+v", r)
			}
			list, err := d.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(list) != 2 || list[0].Name != "dns" || list[1].Revision != 10 {
				t.Errorf("List = %v", list)
			}

			if err := d.Delete(h...); err != nil {
				t.Fatal(err)
			}
			d = newDriver()
			if _, err := d.Get("media", 2); !errors.Is(err, ErrNotFound) {
				t.Errorf("after delete: got %v", err)
			}
			if _, err := d.Get("dns", 1); err != nil {
				t.Errorf("dns should survive: %v", err)
			}
			if err := d.Save(rel("Bad Name", 1, release.StatusDeployed)); err == nil {
				t.Error("expected invalid name to be rejected")
			}
		})
	}
}

func TestLocalPermissions(t *testing.T) {
	dir := t.TempDir()
	if err := NewLocal(dir).Save(rel("media", 1, release.StatusDeployed)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "media", "v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("record mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestVolumeHelperInvocations(t *testing.T) {
	fv := &fakeVolume{files: map[string][]byte{}}
	v := NewVolume([]string{"--context", "nas"}, "")
	v.Exec = fv.exec
	if _, err := v.List(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.List(); err != nil { // cached: no second read
		t.Fatal(err)
	}
	want := []string{
		"--context nas volume create --label io.composetpl.storage=v1 composetpl-releases",
		"--context nas run --rm --network none --pull missing -v composetpl-releases:/data:ro busybox:1.37 tar -C /data -cf - .",
	}
	if strings.Join(fv.calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(fv.calls, "\n"), strings.Join(want, "\n"))
	}
}

// TestReadsV2Records guards the stored record format: releases written by earlier
// versions must stay readable (the API version is data, not a Go import path).
func TestReadsV2Records(t *testing.T) {
	dir := t.TempDir()
	rec := `{"apiVersion": "composetpl/v1", "name": "media", "revision": 3, "status": "deployed",
  "chart": {"name": "media", "version": "0.1.0"}, "manifest": "name: media\n",
  "firstDeployed": "2026-10-27T20:00:00Z", "lastDeployed": "2026-10-27T21:00:00Z"}`
	if err := os.MkdirAll(filepath.Join(dir, "media"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "media", "v3.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := NewLocal(dir).Get("media", 3)
	if err != nil {
		t.Fatalf("reading a v2 record: %v", err)
	}
	if r.Status != release.StatusDeployed || r.Chart.Version != "0.1.0" {
		t.Errorf("record = %+v", r)
	}
}
