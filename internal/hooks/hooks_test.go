package hooks

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/trevorswanson/composetpl/internal/compose/composetest"
	"github.com/trevorswanson/composetpl/internal/release"
)

const manifest = `name: app
services:
  web:
    image: nginx
  migrate:
    image: app:2
    depends_on: [db]
    x-composetpl:
      hook: [pre-install, pre-upgrade]
      weight: 5
  backup:
    image: busybox
    x-composetpl:
      hook: pre-upgrade
      weight: -1
      delete-policy: [hook-succeeded, hook-failed]
  smoke:
    image: busybox
    x-composetpl: {hook: test, no-deps: true}
`

func parseRoot(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Content[0]
}

func TestExtract(t *testing.T) {
	root := parseRoot(t, manifest)
	hks, err := Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, h := range hks {
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "backup,smoke,migrate" {
		t.Errorf("order = %v (want weight, then name)", names)
	}
	out, _ := encode(root)
	if strings.Contains(out, "migrate") || !strings.Contains(out, "web:") {
		t.Errorf("hooks should be removed from the manifest:\n%s", out)
	}
	m := hks[2]
	if strings.Join(m.Events, ",") != "pre-install,pre-upgrade" || m.Weight != 5 ||
		strings.Join(m.DeletePolicies, ",") != BeforeHookCreation || strings.Contains(m.Service, ExtensionKey) ||
		!strings.Contains(m.Service, "depends_on") {
		t.Errorf("migrate hook = %+v", m)
	}
	if !hks[1].NoDeps || hks[1].Events[0] != Test {
		t.Errorf("smoke hook = %+v", hks[1])
	}
}

func TestExtractErrors(t *testing.T) {
	for ext, want := range map[string]string{
		`{weight: 1}`:                        "hook is required",
		`{hook: pre-launch}`:                 `unknown hook event "pre-launch"`,
		`{hook: test, weight: heavy}`:        "weight must be an integer",
		`{hook: test, delete-policy: never}`: `unknown delete policy "never"`,
		`{hook: test, retries: 3}`:           `unknown key "retries"`,
		`{hook: test, no-deps: maybe}`:       "no-deps must be a boolean",
		`yes`:                                "must be a mapping",
		`{hook: [test, {a: b}]}`:             "must be a string or a list of strings",
	} {
		root := parseRoot(t, "services:\n  x:\n    image: a\n    x-composetpl: "+ext+"\n")
		if _, err := Extract(root); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", ext, err, want)
		}
	}
}

func TestRunner(t *testing.T) {
	root := parseRoot(t, manifest)
	hks, err := Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := encode(root)
	rel := &release.Release{Name: "app", Manifest: m, Hooks: hks, ProjectDir: "/srv/app"}

	fake := composetest.New()
	r := &Runner{Compose: fake}
	res, err := r.Run(context.Background(), rel, PreUpgrade)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("results = %+v", res)
	}
	want := "run app backup name=app-hook-backup nodeps=false;rm app-hook-backup;" +
		"rm app-hook-migrate;run app migrate name=app-hook-migrate nodeps=false"
	if fake.CallLog() != want {
		t.Errorf("calls:\n%s\nwant\n%s", fake.CallLog(), want)
	}

	fake = composetest.New()
	fake.FailRun = "backup"
	r = &Runner{Compose: fake}
	if _, err := r.Run(context.Background(), rel, PreUpgrade); err == nil || !strings.Contains(err.Error(), `pre-upgrade hook "backup" failed`) {
		t.Errorf("got %v", err)
	}
	// hook-failed removes the container; migrate never runs after the failure.
	if fake.CallLog() != "run app backup name=app-hook-backup nodeps=false;rm app-hook-backup" {
		t.Errorf("calls = %s", fake.CallLog())
	}
}

func TestManifestWith(t *testing.T) {
	h := &release.Hook{Name: "smoke", Service: "image: busybox\nnetworks: [proxy]\n"}
	out, err := manifestWith("name: app\nservices:\n  web:\n    image: nginx\n", h)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "  web:\n") || !strings.Contains(string(out), "  smoke:\n    image: busybox\n") {
		t.Errorf("manifest:\n%s", out)
	}
	out, err = manifestWith("name: app\n", h)
	if err != nil || !strings.Contains(string(out), "services:\n  smoke:") {
		t.Errorf("manifest without services: %v\n%s", err, out)
	}
}
