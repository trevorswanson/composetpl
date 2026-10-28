package storage

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"composetpl/internal/compose"
	"composetpl/internal/release"
)

const (
	// DefaultVolume is the Docker volume that holds release records.
	DefaultVolume = "composetpl-releases"
	// DefaultHelperImage runs tar/rm against the volume.
	DefaultHelperImage = "busybox:1.37"
	volumeLabel        = "io.composetpl.storage=v1"
)

// Volume stores records in a Docker volume on the target engine, so release history
// travels with the host and is shared by every client that talks to it. Each
// operation runs a short-lived helper container with no network; reads tar the
// volume out and writes pipe a tar back in. Records are cached for the process.
type Volume struct {
	// Global are docker-level flags, e.g. --context X.
	Global      []string
	VolumeName  string
	HelperImage string
	Exec        compose.ExecFunc
	Ctx         context.Context

	cache   records
	ensured bool
}

// NewVolume returns a Volume driver using the docker binary on PATH.
func NewVolume(global []string, helperImage string) *Volume {
	if helperImage == "" {
		helperImage = DefaultHelperImage
	}
	return &Volume{
		Global:      global,
		VolumeName:  DefaultVolume,
		HelperImage: helperImage,
		Exec:        compose.NewCLI(nil, nil).Exec,
		Ctx:         context.Background(),
	}
}

func (v *Volume) Name() string { return "volume" }

func (v *Volume) docker(stdin io.Reader, stdout io.Writer, args ...string) error {
	var stderr bytes.Buffer
	full := append(append([]string{}, v.Global...), args...)
	ctx, cancel := context.WithTimeout(v.Ctx, 2*time.Minute)
	defer cancel()
	if err := v.Exec(ctx, full, stdin, stdout, &stderr); err != nil {
		return fmt.Errorf("volume storage: docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (v *Volume) helper(rw bool, interactive bool, cmd ...string) []string {
	mount := v.VolumeName + ":/data"
	if !rw {
		mount += ":ro"
	}
	args := []string{"run", "--rm", "--network", "none", "--pull", "missing", "-v", mount}
	if interactive {
		args = append(args, "-i")
	}
	return append(append(args, v.HelperImage), cmd...)
}

func (v *Volume) ensure() error {
	if v.ensured {
		return nil
	}
	if err := v.docker(nil, io.Discard, "volume", "create", "--label", volumeLabel, v.VolumeName); err != nil {
		return err
	}
	v.ensured = true
	return nil
}

func (v *Volume) load() (records, error) {
	if v.cache != nil {
		return v.cache, nil
	}
	if err := v.ensure(); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := v.docker(nil, &out, v.helper(false, false, "tar", "-C", "/data", "-cf", "-", ".")...); err != nil {
		return nil, err
	}
	rs := records{}
	tr := tar.NewReader(&out)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("volume storage: reading archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if _, _, ok := parseRecordPath(h.Name); !ok {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		r, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("volume storage: %s: %w", h.Name, err)
		}
		rs.put(r)
	}
	v.cache = rs
	return rs, nil
}

func (v *Volume) Get(name string, revision int) (*release.Release, error) {
	rs, err := v.load()
	if err != nil {
		return nil, err
	}
	return rs.get(name, revision)
}

func (v *Volume) History(name string) ([]*release.Release, error) {
	rs, err := v.load()
	if err != nil {
		return nil, err
	}
	return rs.history(name)
}

func (v *Volume) List() ([]*release.Release, error) {
	rs, err := v.load()
	if err != nil {
		return nil, err
	}
	return rs.list(), nil
}

func (v *Volume) Save(rels ...*release.Release) error {
	rs, err := v.load()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	dirs := map[string]bool{}
	for _, r := range rels {
		data, err := encode(r)
		if err != nil {
			return err
		}
		if !dirs[r.Name] {
			dirs[r.Name] = true
			if err := tw.WriteHeader(&tar.Header{Name: r.Name + "/", Typeflag: tar.TypeDir, Mode: 0o700, ModTime: time.Now()}); err != nil {
				return err
			}
		}
		h := &tar.Header{Name: recordPath(r.Name, r.Revision), Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := v.docker(&buf, io.Discard, v.helper(true, true, "tar", "-C", "/data", "-xf", "-")...); err != nil {
		return err
	}
	for _, r := range rels {
		rs.put(r)
	}
	return nil
}

func (v *Volume) Delete(rels ...*release.Release) error {
	if len(rels) == 0 {
		return nil
	}
	rs, err := v.load()
	if err != nil {
		return err
	}
	var files []string
	dirs := map[string]bool{}
	for _, r := range rels {
		// Names are validated, so they are safe to place in a shell command.
		if err := release.ValidateName(r.Name); err != nil {
			return err
		}
		files = append(files, path.Join("/data", recordPath(r.Name, r.Revision)))
		dirs[path.Join("/data", r.Name)] = true
	}
	script := "rm -f " + strings.Join(files, " ")
	for d := range dirs {
		script += "; rmdir " + d + " 2>/dev/null"
	}
	script += "; true"
	if err := v.docker(nil, io.Discard, v.helper(true, false, "sh", "-c", script)...); err != nil {
		return err
	}
	for _, r := range rels {
		rs.remove(r)
	}
	return nil
}
