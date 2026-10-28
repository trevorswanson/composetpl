package registry_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2"

	"composetpl/internal/chart"
	"composetpl/internal/registry"
	"composetpl/internal/registry/registrytest"
)

func archive(t *testing.T, name, version string) []byte {
	t.Helper()
	data, err := chart.WriteArchive(name, map[string][]byte{
		"Chart.yaml":       []byte("name: " + name + "\nversion: " + version + "\ndescription: test\n"),
		"templates/x.yaml": []byte("x: " + version),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPushPull(t *testing.T) {
	ctx := context.Background()
	reg := registrytest.New()
	c := reg.Client()
	c.CacheDir = t.TempDir()

	for _, v := range []string{"0.1.0", "0.2.0", "1.0.0", "1.1.0-rc.1"} {
		ref, digest, err := c.Push(ctx, archive(t, "media", v), "oci://ghcr.io/me/charts/")
		if err != nil {
			t.Fatal(err)
		}
		if ref.String() != "oci://ghcr.io/me/charts/media:"+v || !strings.HasPrefix(digest, "sha256:") {
			t.Errorf("push %s = %s %s", v, ref, digest)
		}
	}
	tags, err := c.Tags(ctx, "ghcr.io/me/charts/media")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "1.1.0-rc.1,1.0.0,0.2.0,0.1.0" {
		t.Errorf("tags = %v", tags)
	}

	for constraint, want := range map[string]string{"": "1.0.0", "^0.1.0": "0.1.0", "~0.2": "0.2.0", ">=1.1.0-0": "1.1.0-rc.1"} {
		p, err := c.Pull(ctx, "oci://ghcr.io/me/charts/media", constraint)
		if err != nil {
			t.Fatalf("%q: %v", constraint, err)
		}
		ch, err := chart.LoadArchive(p.Archive, chart.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Ref.Tag != want || ch.Metadata.Version != want {
			t.Errorf("constraint %q pulled %s (chart %s), want %s", constraint, p.Ref.Tag, ch.Metadata.Version, want)
		}
	}
	if _, err := c.Pull(ctx, "oci://ghcr.io/me/charts/media", "^3"); err == nil || !strings.Contains(err.Error(), "no version") {
		t.Errorf("unsatisfiable constraint: %v", err)
	}
	p, err := c.Pull(ctx, "oci://ghcr.io/me/charts/media:0.2.0", "")
	if err != nil {
		t.Fatal(err)
	}
	byDigest, err := c.Pull(ctx, "oci://ghcr.io/me/charts/media@"+p.Digest, "")
	if err != nil || string(byDigest.Archive) != string(p.Archive) {
		t.Errorf("pull by digest: %v", err)
	}
	if _, err := c.Pull(ctx, "oci://ghcr.io/me/charts/media:0.2.0", "^1"); err == nil {
		t.Error("tag and --version together should be rejected")
	}

	// Pulled archives are cached by digest.
	entries, _ := filepath.Glob(filepath.Join(c.CacheDir, "sha256", "*.tgz"))
	if len(entries) == 0 {
		t.Error("expected cached archives")
	}
}

func TestPushRejectsBadCharts(t *testing.T) {
	c := registrytest.New().Client()
	ctx := context.Background()
	if _, _, err := c.Push(ctx, archive(t, "x", "one"), "oci://r/x"); err == nil || !strings.Contains(err.Error(), "not valid semver") {
		t.Errorf("got %v", err)
	}
	if _, _, err := c.Push(ctx, archive(t, "x", "1.0.0"), "ghcr.io/me"); err == nil {
		t.Error("expected error for a destination without oci://")
	}
}

func TestPullRejectsNonCharts(t *testing.T) {
	ctx := context.Background()
	reg := registrytest.New()
	c := reg.Client()
	target, _ := c.Repository("r/image")
	cfg, _ := oras.PushBytes(ctx, target, "application/vnd.oci.image.config.v1+json", []byte("{}"))
	desc, err := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, "", oras.PackManifestOptions{ConfigDescriptor: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	_ = target.Tag(ctx, desc, "1.0.0")
	if _, err := c.Pull(ctx, "oci://r/image:1.0.0", ""); err == nil || !strings.Contains(err.Error(), "not a composetpl chart") {
		t.Errorf("got %v", err)
	}
}

func TestParseReference(t *testing.T) {
	for in, want := range map[string]string{
		"oci://ghcr.io/me/charts/media":      "ghcr.io/me/charts/media||",
		"oci://localhost:5000/media:1.2.3":   "localhost:5000/media|1.2.3|",
		"oci://r.io/a/b@sha256:" + zeros(64): "r.io/a/b||sha256:" + zeros(64),
	} {
		r, err := registry.ParseReference(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got := r.Repository + "|" + r.Tag + "|" + r.Digest; got != want {
			t.Errorf("%s = %s, want %s", in, got, want)
		}
	}
	if _, err := registry.ParseReference("ghcr.io/x"); err == nil {
		t.Error("expected error without oci://")
	}
}

func zeros(n int) string { return strings.Repeat("0", n) }

func TestNewHonoursEnv(t *testing.T) {
	t.Setenv("COMPOSETPL_PLAIN_HTTP", "1")
	if !registry.New(os.Stderr).PlainHTTP {
		t.Error("COMPOSETPL_PLAIN_HTTP should enable plain HTTP")
	}
}
