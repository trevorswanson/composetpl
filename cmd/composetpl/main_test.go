package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"composetpl/internal/action"
	"composetpl/internal/compose/composetest"
	"composetpl/internal/storage"
)

var update = flag.Bool("update", false, "rewrite golden files")

const exampleChart = "../../examples/homelab/stack"

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run go test ./cmd/composetpl -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s mismatch (run with -update if intended)\n--- got ---\n%s", name, got)
	}
}

func TestTemplateMergedGolden(t *testing.T) {
	out, err := run(t, "template", exampleChart)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "homelab-merged.yaml", out)
}

func TestTemplateSetOverrides(t *testing.T) {
	out, err := run(t, "template", exampleChart, "--name", "prod",
		"--set", "apps.plex.enabled=false", "--set", "apps.jellyfin.enabled=true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "name: prod\n") {
		t.Errorf("expected project name prod, got:\n%s", out)
	}
	if strings.Contains(out, "  plex:") || !strings.Contains(out, "  jellyfin:") {
		t.Errorf("--set did not toggle apps:\n%s", out)
	}
}

func TestTemplateSplitGolden(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, "template", exampleChart, "--output-dir", dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"apps.yaml", "networks.yaml", "traefik.yaml"} {
		got, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, filepath.Join("homelab-split", f), string(got))
	}
}

func TestLint(t *testing.T) {
	out, err := run(t, "lint", exampleChart)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "lint passed") {
		t.Errorf("unexpected output:\n%s", out)
	}

	out, err = run(t, "lint", exampleChart, "--set", "apps.sonarr.image=null")
	if err == nil || !strings.Contains(err.Error(), "apps.sonarr.image is required") {
		t.Errorf("expected required error, got %v\n%s", err, out)
	}
}

func TestOutputFlagsExclusive(t *testing.T) {
	_, err := run(t, "template", exampleChart, "-o", "x", "--output-dir", "y")
	if err == nil {
		t.Fatal("expected an error")
	}
}

// useFakeDocker routes release commands to an in-memory compose and a local store.
func useFakeDocker(t *testing.T) *composetest.Fake {
	t.Helper()
	fake := composetest.New()
	store := storage.NewLocal(t.TempDir())
	testConfig = func(cmd *cobra.Command) (*action.Configuration, error) {
		return &action.Configuration{Store: store, Compose: fake, Log: cmd.ErrOrStderr()}, nil
	}
	t.Cleanup(func() { testConfig = nil })
	return fake
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := run(t, args...)
	if err != nil {
		t.Fatalf("composetpl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func expect(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output missing %q:\n%s", w, out)
		}
	}
}

func TestReleaseLifecycleCLI(t *testing.T) {
	fake := useFakeDocker(t)

	out := mustRun(t, "install", "media", exampleChart)
	expect(t, out, "NAME: media", "STATUS: deployed", "REVISION: 1", "NOTES:",
		"media (revision 1) is up", "sonarr: https://sonarr.home.example.com")

	out = mustRun(t, "upgrade", "media", exampleChart, "--set", "apps.sonarr.enabled=false", "--wait")
	expect(t, out, `Release "media" has been upgraded.`, "REVISION: 2", "(revision 2)")
	if strings.Contains(fake.Running["media"], "sonarr:") {
		t.Error("sonarr should have been removed by the upgrade")
	}
	expect(t, fake.CallLog(), "up media wait=true")

	expect(t, mustRun(t, "history", "media"),
		"REVISION", "1 ", "superseded", "Install complete", "2 ", "deployed", "Upgrade complete")
	expect(t, mustRun(t, "get", "values", "media"), "apps:", "sonarr:", "enabled: false")
	expect(t, mustRun(t, "get", "values", "media", "--all", "-o", "json"), `"domain": "home.example.com"`)
	expect(t, mustRun(t, "get", "manifest", "media", "--revision", "1"), "name: media", "  sonarr:")
	expect(t, mustRun(t, "get", "notes", "media"), "(revision 2)")
	expect(t, mustRun(t, "get", "metadata", "media"), "CHART: media", "VERSION: 0.1.0", "REVISION: 2")
	expect(t, mustRun(t, "status", "media"), "STATUS: deployed", "CONTAINERS:", "media-web-1", "NOTES:")
	expect(t, mustRun(t, "list"), "NAME", "media", "media-0.1.0", "2026.10", "running(1)")
	expect(t, mustRun(t, "list", "-o", "yaml"), "name: media", "live: running(1)")

	out = mustRun(t, "upgrade", "media", exampleChart, "--dry-run", "--set", "apps.jellyfin.enabled=true")
	expect(t, out, "MANIFEST:", "jellyfin:")
	expect(t, mustRun(t, "history", "media", "-o", "json"), `"revision": 2`)
	if strings.Contains(mustRun(t, "history", "media"), "Dry run") {
		t.Error("dry run should not be recorded")
	}

	expect(t, mustRun(t, "rollback", "media", "1"), `"media" is now at revision 3`)
	if !strings.Contains(fake.Running["media"], "sonarr:") {
		t.Error("rollback should restore sonarr")
	}

	expect(t, mustRun(t, "uninstall", "media", "--keep-history"), `release "media" uninstalled`)
	if mustRun(t, "list", "-q") != "" {
		t.Error("uninstalled release should be hidden from list")
	}
	expect(t, mustRun(t, "list", "--uninstalled", "-q"), "media")
	expect(t, mustRun(t, "uninstall", "media"), "uninstalled")
	if _, err := run(t, "status", "media"); err == nil {
		t.Error("status after purge should fail")
	}
}

func TestInstallErrors(t *testing.T) {
	useFakeDocker(t)
	if _, err := run(t, "install", "Media", exampleChart); err == nil || !strings.Contains(err.Error(), "invalid release name") {
		t.Errorf("got %v", err)
	}
	if _, err := run(t, "upgrade", "media", exampleChart); err == nil || !strings.Contains(err.Error(), "has no deployed releases") {
		t.Errorf("got %v", err)
	}
	if _, err := run(t, "install", "media", exampleChart, "--set", "traefik.acmeEmail="); err == nil ||
		!strings.Contains(err.Error(), "traefik.acmeEmail is required") {
		t.Errorf("got %v", err)
	}
	if _, err := run(t, "rollback", "media", "zero"); err == nil {
		t.Error("expected invalid revision error")
	}
}
