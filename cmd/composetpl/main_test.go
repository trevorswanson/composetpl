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
	for _, f := range []string{"apps.yaml", "networks.yaml", "traefik.yaml", "hooks/backup.yaml", "tests/homepage.yaml"} {
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

	// The schema catches the missing image before the template's `required` does.
	out, err = run(t, "lint", exampleChart, "--set", "apps.sonarr.image=null")
	if err == nil || !strings.Contains(err.Error(), "at '/apps/sonarr': missing property 'image'") {
		t.Errorf("expected schema error, got %v\n%s", err, out)
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

func TestSchemaValidation(t *testing.T) {
	_, err := run(t, "template", exampleChart, "--set", "apps.sonarr.ingress.port=high", "--set", "global.restart=sometimes")
	if err == nil {
		t.Fatal("expected schema errors")
	}
	expect(t, err.Error(), "schema in chart media", "/apps/sonarr/ingress/port", "/global/restart")

	useFakeDocker(t)
	if _, err := run(t, "install", "media", exampleChart, "--set", "global.puid=-1"); err == nil ||
		!strings.Contains(err.Error(), "/global/puid") {
		t.Errorf("install should validate the schema: %v", err)
	}
}

func TestDiffCLI(t *testing.T) {
	useFakeDocker(t)
	out := mustRun(t, "diff", "upgrade", "media", exampleChart)
	expect(t, out, "media, services/sonarr, added:", "+image: lscr.io/linuxserver/sonarr:latest", "media, networks/proxy, added:")

	mustRun(t, "install", "media", exampleChart)
	out, err := run(t, "diff", "upgrade", "media", exampleChart, "--detailed-exitcode")
	if err != nil || strings.Contains(out, "services/") {
		t.Errorf("unchanged upgrade should show no changes: %v\n%s", err, out)
	}

	out, err = run(t, "diff", "upgrade", "media", exampleChart, "--set", "apps.sonarr.tag=4.0.0", "--set", "apps.plex.enabled=false", "--detailed-exitcode")
	if exitCode(err) != 2 {
		t.Errorf("--detailed-exitcode should exit 2 on changes, got %v", err)
	}
	expect(t, out, "media, services/plex, removed:", "media, services/sonarr, changed:",
		"-image: lscr.io/linuxserver/sonarr:latest", "+image: lscr.io/linuxserver/sonarr:4.0.0")
	if strings.Contains(out, "\x1b[") {
		t.Error("colour should be off when not writing to a terminal")
	}

	mustRun(t, "upgrade", "media", exampleChart, "--set", "apps.sonarr.tag=4.0.0")
	expect(t, mustRun(t, "diff", "rollback", "media"), "services/sonarr, changed:", "+image: lscr.io/linuxserver/sonarr:latest")
	expect(t, mustRun(t, "diff", "revision", "media", "1", "2"), "-image: lscr.io/linuxserver/sonarr:latest")
	if _, err := run(t, "diff", "revision", "media", "2"); err == nil {
		t.Error("comparing a revision with itself should fail")
	}
}

func TestTemplateCapabilitiesFlags(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"Chart.yaml":       "name: caps\n",
		"templates/x.yaml": "x-caps: {{ .Capabilities.ComposeVersion }}/{{ .Capabilities.DockerVersion }}\nx-lookup: {{ lookup \"volume\" \"data\" | toJson | quote }}\n",
	})
	expect(t, mustRun(t, "template", dir), "x-caps: 2.20.0/25.0.0", `x-lookup: "{}"`)
	expect(t, mustRun(t, "template", dir, "--compose-version", "2.31.0", "--docker-version", "28.1.1"), "x-caps: 2.31.0/28.1.1")
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHooksCLI(t *testing.T) {
	fake := useFakeDocker(t)
	mustRun(t, "install", "media", exampleChart)
	if strings.Contains(fake.CallLog(), "config-backup") {
		t.Error("pre-upgrade backup must not run on install")
	}
	expect(t, mustRun(t, "get", "hooks", "media"),
		"# Hook: config-backup (pre-upgrade,pre-rollback, weight -10)", "# Hook: homepage-smoke-test (test, weight 0)")
	if strings.Contains(mustRun(t, "get", "manifest", "media"), "config-backup") {
		t.Error("hooks must not be in the deployed manifest")
	}

	fake.Calls = nil
	mustRun(t, "upgrade", "media", exampleChart, "--set", "apps.plex.enabled=false")
	expect(t, fake.CallLog(), "run media config-backup name=media-hook-config-backup", "rm media-hook-config-backup;up media")

	fake.RunOutput = "Connecting to homepage:3000\n"
	out := mustRun(t, "test", "media", "--logs")
	expect(t, out, "TEST SUITE:     homepage-smoke-test", "Phase:          Succeeded", "Connecting to homepage:3000")
	expect(t, fake.CallLog(), "run media homepage-smoke-test name=media-hook-homepage-smoke-test nodeps=true")

	fake.FailRun = "homepage-smoke-test"
	out, err := run(t, "test", "media")
	if err == nil {
		t.Error("expected failing test")
	}
	expect(t, out, "Phase:          Failed")
}
