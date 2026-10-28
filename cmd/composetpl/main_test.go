package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/trevorswanson/composetpl/internal/action"
	"github.com/trevorswanson/composetpl/internal/chart"
	"github.com/trevorswanson/composetpl/internal/compose/composetest"
	"github.com/trevorswanson/composetpl/internal/registry/registrytest"
	"github.com/trevorswanson/composetpl/internal/storage"
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

func TestChartAuthoringCLI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "whoami")
	expect(t, mustRun(t, "create", dir), "Creating "+dir)
	out := mustRun(t, "template", dir)
	expect(t, out, "name: whoami", "  whoami:", "image: nginx:1.0.0", "app.composetpl.io/release: \"whoami\"", `test: ["CMD", "wget"`)
	if strings.Contains(out, "smoke-test") {
		t.Error("test hook should be stripped from template output")
	}
	expect(t, mustRun(t, "lint", dir), "[INFO] 1 hook service(s)", "whoami: lint passed")
	expect(t, mustRun(t, "show", "chart", dir), "name: whoami", "version: 0.1.0")
	expect(t, mustRun(t, "show", "values", dir), "repository: nginx")

	dist := t.TempDir()
	out = mustRun(t, "package", exampleChart, "-d", dist, "--version", "0.2.0")
	tgz := filepath.Join(dist, "media-0.2.0.tgz")
	expect(t, out, "saved it to: "+tgz)
	expect(t, mustRun(t, "show", "all", tgz), "name: media", "version: 0.2.0", "---", "global:", `"$schema"`)

	// The packaged chart renders exactly like the directory.
	fromDir := mustRun(t, "template", exampleChart)
	if fromTgz := mustRun(t, "template", tgz); fromTgz != fromDir {
		t.Errorf("packaged chart renders differently:\n%s", fromTgz)
	}

	useFakeDocker(t)
	mustRun(t, "install", "media", tgz)
	cwd, _ := os.Getwd()
	expect(t, mustRun(t, "get", "metadata", "media"), "VERSION: 0.2.0", "PROJECT DIRECTORY: "+cwd, "CHART REF: "+tgz)
	mustRun(t, "upgrade", "media", exampleChart)
	expect(t, mustRun(t, "history", "media"), "media-0.2.0", "media-0.1.0")
}

func useFakeRegistry(t *testing.T) {
	t.Helper()
	testRegistry = registrytest.New().Client()
	t.Cleanup(func() { testRegistry = nil })
}

func TestOCIWorkflowCLI(t *testing.T) {
	useFakeRegistry(t)
	dist := t.TempDir()
	mustRun(t, "package", exampleChart, "-d", dist)
	mustRun(t, "package", exampleChart, "-d", dist, "--version", "0.2.0")
	expect(t, mustRun(t, "push", filepath.Join(dist, "media-0.1.0.tgz"), "oci://registry.lan/charts"),
		"Pushed: oci://registry.lan/charts/media:0.1.0", "Digest: sha256:")
	mustRun(t, "push", filepath.Join(dist, "media-0.2.0.tgz"), "oci://registry.lan/charts")

	pulled := t.TempDir()
	expect(t, mustRun(t, "pull", "oci://registry.lan/charts/media", "--version", "~0.1", "-d", pulled),
		"Pulled: oci://registry.lan/charts/media:0.1.0", "Saved: "+filepath.Join(pulled, "media-0.1.0.tgz"))
	mustRun(t, "pull", "oci://registry.lan/charts/media:0.2.0", "-d", pulled, "--untar")
	expect(t, mustRun(t, "show", "chart", filepath.Join(pulled, "media")), "version: 0.2.0")
	expect(t, mustRun(t, "show", "chart", "oci://registry.lan/charts/media", "--version", "0.1.0"), "version: 0.1.0")

	if got := mustRun(t, "template", "oci://registry.lan/charts/media"); got != mustRun(t, "template", exampleChart) {
		t.Errorf("oci chart renders differently:\n%s", got)
	}

	useFakeDocker(t)
	mustRun(t, "install", "media", "oci://registry.lan/charts/media", "--version", "^0.1")
	expect(t, mustRun(t, "get", "metadata", "media"), "VERSION: 0.1.0", "CHART REF: oci://registry.lan/charts/media:0.1.0@sha256:")
	mustRun(t, "upgrade", "media", "oci://registry.lan/charts/media")
	expect(t, mustRun(t, "history", "media"), "media-0.1.0", "media-0.2.0")
	if _, err := run(t, "install", "other", exampleChart, "--version", "1.0.0"); err == nil ||
		!strings.Contains(err.Error(), "--version only applies to oci:// charts") {
		t.Errorf("got %v", err)
	}
}

func TestDependencyCLI(t *testing.T) {
	useFakeRegistry(t)
	dist := t.TempDir()
	mustRun(t, "package", "../../examples/homelab/common", "-d", dist)
	mustRun(t, "push", filepath.Join(dist, "common-0.1.0.tgz"), "oci://registry.lan/charts")

	app := filepath.Join(t.TempDir(), "app")
	writeFiles(t, app, map[string]string{
		"Chart.yaml": "name: app\nversion: 1.0.0\ndependencies:\n  - name: common\n    version: ^0.1.0\n    repository: oci://registry.lan/charts\n",
		"values.yaml": "global: {tz: UTC, puid: 1, pgid: 1, domain: lan, network: proxy, certResolver: le, configRoot: /data}\n" +
			"apps:\n  whoami: {enabled: true, image: traefik/whoami, ingress: {port: 80}}\n",
		"templates/apps.yaml": "services:\n{{- range $n, $a := .Values.apps }}\n  {{- include \"common.service\" (dict \"name\" $n \"app\" $a \"root\" $) | nindent 2 }}\n{{- end }}\n",
	})

	expect(t, mustRun(t, "dependency", "list", app), "common", "^0.1.0", "oci://registry.lan/charts", "missing")
	if _, err := run(t, "template", app); err == nil || !strings.Contains(err.Error(), "dependency build") {
		t.Errorf("unvendored oci dependency should fail: %v", err)
	}

	expect(t, mustRun(t, "dependency", "update", app), "Saved 1 dependencies and Chart.lock")
	lock, _ := os.ReadFile(filepath.Join(app, "Chart.lock"))
	expect(t, string(lock), "name: common", "version: 0.1.0", "digest: sha256:")
	expect(t, mustRun(t, "dependency", "list", app), "ok")
	expect(t, mustRun(t, "template", app), "whoami:", "image: traefik/whoami:latest", "traefik.http.routers.whoami.rule")

	// build restores exactly what the lock says.
	must(t, os.Remove(filepath.Join(app, "charts", "common-0.1.0.tgz")))
	expect(t, mustRun(t, "dependency", "build", app), "Restored 1 dependencies")
	mustRun(t, "template", app)

	// A tampered lock is refused.
	must(t, os.Remove(filepath.Join(app, "charts", "common-0.1.0.tgz")))
	l, err := chart.ReadLock(app)
	must(t, err)
	l.Dependencies[0].Digest = "sha256:" + strings.Repeat("f", 64)
	must(t, l.Write(app))
	if _, err := run(t, "dependency", "build", app); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("tampered lock: %v", err)
	}

	// Changing Chart.yaml's dependencies makes the lock stale.
	writeFiles(t, app, map[string]string{"Chart.lock": string(lock),
		"Chart.yaml": "name: app\nversion: 1.0.0\ndependencies:\n  - name: common\n    version: ^0.2.0\n    repository: oci://registry.lan/charts\n"})
	if _, err := run(t, "dependency", "build", app); err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Errorf("stale lock: %v", err)
	}
	if _, err := run(t, "dependency", "update", app); err == nil || !strings.Contains(err.Error(), `no version of oci://registry.lan/charts/common matches "^0.2.0"`) {
		t.Errorf("unsatisfiable update: %v", err)
	}
}

func TestFileDependencyUpdate(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"common", "stack"} {
		copyDir(t, filepath.Join("../../examples/homelab", d), filepath.Join(root, d))
	}
	stack := filepath.Join(root, "stack")
	expect(t, mustRun(t, "dependency", "list", stack), "file://../common", "ok (file)")
	mustRun(t, "dependency", "update", stack)
	if _, err := os.Stat(filepath.Join(stack, "charts", "common-0.1.0.tgz")); err != nil {
		t.Fatal(err)
	}
	// With common vendored, the stack renders even if ../common disappears.
	must(t, os.RemoveAll(filepath.Join(root, "common")))
	if got := mustRun(t, "template", stack); got != mustRun(t, "template", exampleChart) {
		t.Error("vendored stack renders differently")
	}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if fi.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const umbrellaChart = "../../examples/homelab/umbrella"

func TestUmbrellaGolden(t *testing.T) {
	golden(t, "homelab-umbrella.yaml", mustRun(t, "template", umbrellaChart))
}

func TestUmbrellaCLI(t *testing.T) {
	out := mustRun(t, "template", umbrellaChart, "--set", "monitoring.enabled=false",
		"--set", "media.apps.sonarr.enabled=false", "--set", "global.domain=lab.example.org")
	expect(t, out, "name: homelab", "  radarr:", "Host(`radarr.lab.example.org`)")
	for _, gone := range []string{"dozzle:", "uptime-kuma:", "  sonarr:"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q should be disabled:\n%s", gone, out)
		}
	}

	dir := t.TempDir()
	mustRun(t, "template", umbrellaChart, "--output-dir", dir)
	for _, f := range []string{"charts/media/apps.yaml", "charts/media/traefik.yaml", "charts/monitoring/apps.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("split output missing %s: %v", f, err)
		}
	}

	useFakeDocker(t)
	out = mustRun(t, "install", "homelab", umbrellaChart)
	expect(t, out, "homelab (revision 1) runs:", "  - media", "  - monitoring")
	if strings.Contains(out, "Monitoring:") {
		t.Error("subchart notes need --render-subchart-notes")
	}
	expect(t, mustRun(t, "upgrade", "homelab", umbrellaChart, "--render-subchart-notes"),
		"Monitoring:", "  - dozzle: https://dozzle.home.example.com", "homelab (revision 2) is up")
	expect(t, mustRun(t, "get", "hooks", "homelab"), "# Hook: config-backup", "# Hook: homepage-smoke-test")
	expect(t, mustRun(t, "get", "values", "homelab", "--all"), "monitoring:", "dozzle:", "tz: America/Chicago")
}

func TestUmbrellaServiceCollision(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"common", "stack"} {
		copyDir(t, filepath.Join("../../examples/homelab", d), filepath.Join(root, d))
	}
	writeFiles(t, filepath.Join(root, "twice"), map[string]string{
		"Chart.yaml": "name: twice\nversion: 0.1.0\ndependencies:\n" +
			"  - {name: media, alias: a, repository: file://../stack}\n" +
			"  - {name: media, alias: b, repository: file://../stack, import-values: [missing]}\n",
		"values.yaml": "b: {apps: {plex: {image: other/plex}}}\n",
	})
	_, err := run(t, "template", filepath.Join(root, "twice"))
	if err == nil {
		t.Fatal("two copies of the same stack should collide on service names")
	}
	expect(t, err.Error(), `conflict at "services.plex.image"`, "twice/charts/a/templates/apps.yaml", "twice/charts/b/templates/apps.yaml")
}
