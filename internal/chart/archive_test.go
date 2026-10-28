package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestArchiveRoundTrip(t *testing.T) {
	files := map[string][]byte{"Chart.yaml": []byte("name: x\nversion: 1.0.0\n"), "templates/a.yaml": []byte("a: 1")}
	data, err := WriteArchive("x", files)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := WriteArchive("x", files)
	if !bytes.Equal(data, again) {
		t.Error("archives of the same files should be byte-identical")
	}
	got, err := ReadArchive(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || string(got["templates/a.yaml"]) != "a: 1" {
		t.Errorf("round trip = %v", got)
	}
}

func rawArchive(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, n := range names {
		_ = tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o644, Size: 1})
		_, _ = tw.Write([]byte("x"))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestReadArchiveRejectsEscapes(t *testing.T) {
	for _, n := range []string{"../evil", "/etc/passwd", "x/../../evil", "toplevel"} {
		if _, err := ReadArchive(rawArchive(t, n)); err == nil {
			t.Errorf("%q should be rejected", n)
		}
	}
	if _, err := ReadArchive([]byte("not gzip")); err == nil {
		t.Error("expected error for non-gzip data")
	}
}

func TestIgnore(t *testing.T) {
	rules, err := parseIgnore([]byte(`# comment
*.tgz
build/
/docs/*.md
!docs/keep.md
**/secret.txt
`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"x.tgz": true, "charts/dep-1.0.0.tgz": true, "build": true, "docs/a.md": true,
		"docs/keep.md": false, "deep/docs/a.md": false, "a/b/secret.txt": true, "secret.txt": true,
		"values.yaml": false,
	}
	for p, want := range cases {
		isDir := p == "build"
		if got := rules.ignored(p, isDir); got != want {
			t.Errorf("ignored(%q) = %v, want %v", p, got, want)
		}
	}
	if rules.ignored("build", false) {
		t.Error("dir-only rule should not match a file")
	}
}

func TestPackageAndLoadArchive(t *testing.T) {
	root := t.TempDir()
	writeChart(t, filepath.Join(root, "lib"), map[string]string{
		"Chart.yaml":             "name: lib\nversion: 0.2.0\ntype: library\n",
		"templates/_helpers.tpl": `{{ define "lib.x" }}lib{{ end }}`,
	})
	writeChart(t, filepath.Join(root, "app"), map[string]string{
		"Chart.yaml":        "name: app\nversion: 1.0.0\ndependencies:\n  - name: lib\n    repository: file://../lib\n",
		"values.yaml":       "a: 1\n",
		"templates/x.yaml":  "x: 1",
		"notes.bak":         "ignored",
		".composetplignore": "*.bak\n",
	})
	dest := filepath.Join(root, "dist")
	out, err := Package(filepath.Join(root, "app"), PackageOptions{Destination: dest, AppVersion: "9.9"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(out) != "app-1.0.0.tgz" {
		t.Errorf("archive name = %s", out)
	}
	data, _ := os.ReadFile(out)
	files, err := ReadArchive(data)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "Chart.yaml,charts/lib-0.2.0.tgz,templates/x.yaml,values.yaml" {
		t.Errorf("archive files = %v", names)
	}

	c, err := Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dir != "" || c.Metadata.AppVersion != "9.9" || len(c.Dependencies) != 1 || c.Dependencies[0].Metadata.Name != "lib" {
		t.Errorf("loaded archive: dir=%q md=%+v deps=%d", c.Dir, c.Metadata, len(c.Dependencies))
	}

	// Packaging again gives the same bytes.
	out2, err := Package(filepath.Join(root, "app"), PackageOptions{Destination: filepath.Join(root, "dist2"), AppVersion: "9.9"})
	if err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(out2)
	if !bytes.Equal(data, data2) {
		t.Error("packaging is not reproducible")
	}

	// An archive whose file:// dependency is not vendored cannot be loaded.
	raw, _ := WriteArchive("app", map[string][]byte{
		"Chart.yaml": []byte("name: app\ndependencies:\n  - name: lib\n    repository: file://../lib\n"),
	})
	if _, err := LoadArchive(raw, Options{}); err == nil || !strings.Contains(err.Error(), "must be vendored") {
		t.Errorf("got %v", err)
	}
	writeChart(t, filepath.Join(root, "nover"), map[string]string{"Chart.yaml": "name: nover\n"})
	if _, err := Package(filepath.Join(root, "nover"), PackageOptions{Destination: dest}); err == nil {
		t.Error("expected an error packaging a chart without a version")
	}
}

func TestVendoredDirectoryDependency(t *testing.T) {
	dir := t.TempDir()
	writeChart(t, dir, map[string]string{
		"Chart.yaml":                        "name: app\ndependencies:\n  - name: lib\n    repository: oci://example.com/charts/lib\n    version: ^1.0.0\n",
		"charts/lib/Chart.yaml":             "name: lib\nversion: 1.2.0\n",
		"charts/lib/templates/_helpers.tpl": `{{ define "lib.x" }}{{ end }}`,
	})
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Dependencies) != 1 || c.Dependencies[0].Metadata.Version != "1.2.0" || len(c.Files) != 0 {
		t.Errorf("deps=%v files=%v", c.Dependencies, c.Files)
	}
}

func TestCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "whoami")
	if _, err := Create(dir); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata.Name != "whoami" || len(c.Templates) != 4 || c.Schema == nil {
		t.Errorf("scaffold: %+v templates=%d", c.Metadata, len(c.Templates))
	}
	if _, err := Create(dir); err == nil {
		t.Error("expected error creating over an existing directory")
	}
	if _, err := Create(filepath.Join(t.TempDir(), "Bad Name")); err == nil {
		t.Error("expected invalid name error")
	}
}
