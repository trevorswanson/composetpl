package action

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pinned = "nginx:2@sha256:0000000000000000000000000000000000000000000000000000000000000002"

func pin(m string) (string, error) { return strings.Replace(m, "nginx:2", pinned, 1), nil }

func TestPostRender(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Install through upgrade --install passes PostRender on.
	_, err := e.upgrade(func(u *Upgrade) {
		u.Install = true
		u.PostRender = func(m string) (string, error) { return strings.Replace(m, "nginx:1", "nginx:1-pinned", 1), nil }
	}, nil)
	must(t, err)
	if e.runningImage() != "nginx:1-pinned" {
		t.Fatalf("install ran %s", e.runningImage())
	}

	// A dry run shows what PostRender makes of the manifest.
	u := NewUpgrade(e.cfg)
	u.PostRender = pin
	changes, err := DiffUpgrade(ctx, e.cfg, u, "myapp", e.chart, map[string]interface{}{"tag": "2"})
	must(t, err)
	if len(changes) != 1 || !strings.Contains(changes[0].New, pinned) {
		t.Fatalf("diff = %+v", changes)
	}

	rel, err := e.upgrade(func(u *Upgrade) { u.PostRender = pin }, map[string]interface{}{"tag": "2"})
	must(t, err)
	if !strings.Contains(rel.Manifest, pinned) || e.runningImage() != pinned {
		t.Errorf("saved %q, running %s", rel.Manifest, e.runningImage())
	}
	stored, err := e.cfg.Store.Get("myapp", rel.Revision)
	must(t, err)
	if stored.Manifest != rel.Manifest {
		t.Errorf("stored manifest differs from the post-rendered one")
	}

	// An error stops the upgrade before anything is saved or run.
	e.fake.Calls = nil
	_, err = e.upgrade(func(u *Upgrade) {
		u.PostRender = func(string) (string, error) { return "", errors.New("no digest") }
	}, map[string]interface{}{"tag": "3"})
	if err == nil || !strings.Contains(err.Error(), "post-render: no digest") {
		t.Fatalf("err = %v", err)
	}
	if e.statuses() != "1:superseded 2:deployed" || len(e.fake.Calls) != 0 {
		t.Errorf("history %s, calls %s", e.statuses(), e.fake.CallLog())
	}
}

func (e *env) addSyncFailHook() {
	e.t.Helper()
	src := `services:
  alert:
    image: curl
    x-composetpl: {hook: sync-fail}
`
	if err := os.WriteFile(filepath.Join(e.chart, "templates", "hooks.yaml"), []byte(src), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func TestSyncFailHook(t *testing.T) {
	e := newEnv(t)
	e.addSyncFailHook()
	_, err := e.install(nil)
	must(t, err)
	if strings.Contains(e.fake.CallLog(), "run ") {
		t.Fatalf("sync-fail ran on success: %s", e.fake.CallLog())
	}

	// A failed upgrade runs sync-fail.
	e.fake.Calls, e.fake.FailUp = nil, "nginx:bad"
	_, err = e.upgrade(nil, map[string]interface{}{"tag": "bad"})
	if err == nil {
		t.Fatal("upgrade should fail")
	}
	if !strings.HasSuffix(e.fake.CallLog(), "run myapp alert name=myapp-hook-alert nodeps=false") {
		t.Errorf("calls = %s", e.fake.CallLog())
	}

	// With --atomic it runs before the rollback, against the failed revision.
	e.fake.Calls = nil
	_, err = e.upgrade(func(u *Upgrade) { u.Atomic = true }, map[string]interface{}{"tag": "bad"})
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("atomic err = %v", err)
	}
	log := e.fake.CallLog()
	hook, rollback := strings.Index(log, "run myapp alert"), strings.LastIndex(log, "up myapp")
	if hook < 0 || hook > rollback {
		t.Errorf("sync-fail should run before the rollback's up: %s", log)
	}

	// Its own failure doesn't hide the sync's error, and --no-hooks skips it.
	e.fake.Calls, e.fake.FailRun = nil, "alert"
	_, err = e.upgrade(nil, map[string]interface{}{"tag": "bad"})
	if err == nil || strings.Contains(err.Error(), "alert") {
		t.Errorf("err = %v", err)
	}
	e.fake.Calls = nil
	_, _ = e.upgrade(func(u *Upgrade) { u.NoHooks = true }, map[string]interface{}{"tag": "bad"})
	if strings.Contains(e.fake.CallLog(), "run ") {
		t.Errorf("--no-hooks ran sync-fail: %s", e.fake.CallLog())
	}

	// A failed rollback runs it too.
	e.fake.Calls, e.fake.FailRun, e.fake.FailUp = nil, "", "nginx:1"
	rb := NewRollback(e.cfg)
	rb.Version = 1
	if _, err := rb.Run(context.Background(), "myapp"); err == nil {
		t.Fatal("rollback should fail")
	}
	if !strings.Contains(e.fake.CallLog(), "run myapp alert") {
		t.Errorf("rollback calls = %s", e.fake.CallLog())
	}
}
