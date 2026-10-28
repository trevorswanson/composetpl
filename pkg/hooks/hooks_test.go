package hooks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/trevorswanson/composetpl/pkg/compose/composetest"
	"github.com/trevorswanson/composetpl/pkg/release"
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
		`{hook: test, attempts: 3}`:          `unknown key "attempts"`,
		`{hook: test, retries: -1}`:          "retries must be an integer from 0 to 100",
		`{hook: test, retries: many}`:        "retries must be an integer",
		`{hook: test, backoff: soon}`:        "backoff must be a positive duration",
		`{hook: test, timeout: 0s}`:          "timeout must be a positive duration",
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

func retryRelease(t *testing.T, ext string) *release.Release {
	t.Helper()
	root := parseRoot(t, "services:\n  web:\n    image: nginx\n  migrate:\n    image: app\n    x-composetpl: "+ext+"\n")
	hks, err := Extract(root)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := encode(root)
	return &release.Release{Name: "app", Manifest: m, Hooks: hks}
}

func TestRetries(t *testing.T) {
	rel := retryRelease(t, "{hook: pre-upgrade, retries: 3, backoff: 15s, delete-policy: hook-succeeded}")
	if h := rel.Hooks[0]; h.Retries != 3 || h.Backoff != "15s" {
		t.Fatalf("hook = %+v", h)
	}

	// Fails twice, then succeeds on the third attempt.
	fake := composetest.New()
	fake.FailRun, fake.FailRunTimes = "migrate", 2
	var slept []time.Duration
	var log strings.Builder
	r := &Runner{Compose: fake, Log: &log, Sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}}
	res, err := r.Run(context.Background(), rel, PreUpgrade)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Attempts != 3 || fmt.Sprint(slept) != "[15s 30s]" {
		t.Errorf("attempts=%d slept=%v", res[0].Attempts, slept)
	}
	if res[0].Finished.Before(res[0].Started) || res[0].Finished.IsZero() {
		t.Errorf("finished time not recorded: %+v", res[0])
	}
	// Every retry clears the previous container; hook-succeeded removes the last one.
	want := "run app migrate name=app-hook-migrate nodeps=false;rm app-hook-migrate;" +
		"run app migrate name=app-hook-migrate nodeps=false;rm app-hook-migrate;" +
		"run app migrate name=app-hook-migrate nodeps=false;rm app-hook-migrate"
	if fake.CallLog() != want {
		t.Errorf("calls:\n%s\nwant\n%s", fake.CallLog(), want)
	}
	expectLog := []string{"Running pre-upgrade hook migrate\n", "Hook migrate failed (attempt 1/4)", "retrying in 15s",
		"Running pre-upgrade hook migrate (attempt 3/4)"}
	for _, w := range expectLog {
		if !strings.Contains(log.String(), w) {
			t.Errorf("log missing %q:\n%s", w, log.String())
		}
	}

	// Always failing: gives up after retries+1 attempts.
	fake = composetest.New()
	fake.FailRun = "migrate"
	r = &Runner{Compose: fake, Sleep: func(context.Context, time.Duration) error { return nil }}
	res, err = r.Run(context.Background(), rel, PreUpgrade)
	if err == nil || !strings.Contains(err.Error(), "after 4 attempts") || res[0].Attempts != 4 {
		t.Errorf("err=%v attempts=%d", err, res[0].Attempts)
	}

	// Cancellation while waiting to retry stops the retries.
	fake = composetest.New()
	fake.FailRun = "migrate"
	r = &Runner{Compose: fake, Sleep: func(context.Context, time.Duration) error { return context.Canceled }}
	res, err = r.Run(context.Background(), rel, PreUpgrade)
	if err == nil || res[0].Attempts != 1 || !strings.Contains(err.Error(), "gave up waiting to retry") {
		t.Errorf("err=%v attempts=%d", err, res[0].Attempts)
	}
}

func TestBackoffDoublesAndCaps(t *testing.T) {
	var got []string
	for n := 1; n <= 7; n++ {
		got = append(got, Backoff(DefaultBackoff, n).String())
	}
	if strings.Join(got, ",") != "10s,20s,40s,1m20s,2m40s,5m0s,5m0s" {
		t.Errorf("backoff = %v", got)
	}
}

func TestPerHookTimeout(t *testing.T) {
	rel := retryRelease(t, "{hook: test, timeout: 50ms}")
	fake := composetest.New()
	fake.RunDelay = true
	r := &Runner{Compose: fake, Timeout: time.Hour}
	start := time.Now()
	_, err := r.Run(context.Background(), rel, Test)
	if err == nil || !strings.Contains(err.Error(), "deadline exceeded") || time.Since(start) > 10*time.Second {
		t.Errorf("per-hook timeout should cut the run short: %v after %s", err, time.Since(start))
	}
}
