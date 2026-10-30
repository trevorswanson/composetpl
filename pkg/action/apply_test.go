package action

import (
	"context"
	"strings"
	"testing"

	"github.com/trevorswanson/composetpl/pkg/release"
)

func TestPrepareThenApply(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// upgrade --install on a new release prepares an install.
	u := NewUpgrade(e.cfg)
	u.Install = true
	rel, err := u.Prepare(ctx, "myapp", e.chart, nil)
	must(t, err)
	if rel.Status != release.StatusPendingInstall || e.statuses() != "1:pending-install" || e.fake.Running["myapp"] != "" {
		t.Fatalf("prepare touched containers or saved the wrong status: %s %v", e.statuses(), e.fake.Running)
	}
	rel, err = NewApply(e.cfg).Run(ctx, "myapp", 1)
	must(t, err)
	if rel.Description != "Install complete" || e.statuses() != "1:deployed" || e.runningImage() != "nginx:1" {
		t.Fatalf("apply install: %q %s %s", rel.Description, e.statuses(), e.runningImage())
	}

	rel, err = NewUpgrade(e.cfg).Prepare(ctx, "myapp", e.chart, map[string]interface{}{"tag": "2"})
	must(t, err)
	if e.statuses() != "1:deployed 2:pending-upgrade" || e.runningImage() != "nginx:1" {
		t.Fatalf("prepare upgrade: %s running %s", e.statuses(), e.runningImage())
	}
	// Another upgrade can't start while one is prepared.
	if _, err := e.upgrade(nil, nil); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Errorf("upgrade over a prepared revision: %v", err)
	}
	a := NewApply(e.cfg)
	a.Description = "applied by a helper"
	rel, err = a.Run(ctx, "myapp", rel.Revision)
	must(t, err)
	if rel.Description != "applied by a helper" || e.statuses() != "1:superseded 2:deployed" || e.runningImage() != "nginx:2" {
		t.Errorf("apply upgrade: %q %s %s", rel.Description, e.statuses(), e.runningImage())
	}
}

func TestApplyAtomicRollsBack(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.install(nil)
	must(t, err)

	rel, err := NewUpgrade(e.cfg).Prepare(ctx, "myapp", e.chart, map[string]interface{}{"tag": "bad"})
	must(t, err)
	e.fake.FailUp = "nginx:bad"
	a := NewApply(e.cfg)
	a.Atomic = true
	if _, err := a.Run(ctx, "myapp", rel.Revision); err == nil || !strings.Contains(err.Error(), "rolled back to revision 1") {
		t.Fatalf("atomic apply err = %v", err)
	}
	if got := e.statuses(); got != "1:superseded 2:failed 3:deployed" || e.runningImage() != "nginx:1" {
		t.Errorf("history = %s, running %s", got, e.runningImage())
	}
}

func TestApplyRefusesWhatIsNotPrepared(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := NewApply(e.cfg).Run(ctx, "myapp", 1); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("no release: %v", err)
	}
	_, err := e.install(nil)
	must(t, err)
	if _, err := NewApply(e.cfg).Run(ctx, "myapp", 1); err == nil || !strings.Contains(err.Error(), "is deployed, not pending") {
		t.Errorf("deployed revision: %v", err)
	}
	_, err = NewUpgrade(e.cfg).Prepare(ctx, "myapp", e.chart, nil)
	must(t, err)
	if _, err := NewApply(e.cfg).Run(ctx, "myapp", 1); err == nil || !strings.Contains(err.Error(), "not the latest") {
		t.Errorf("older revision: %v", err)
	}
}

func TestAbandon(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.install(nil)
	must(t, err)
	if err := Abandon(e.cfg, "myapp", 1, "gone"); err == nil || !strings.Contains(err.Error(), "not pending") {
		t.Errorf("abandoning a deployed revision: %v", err)
	}

	rel, err := NewUpgrade(e.cfg).Prepare(ctx, "myapp", e.chart, map[string]interface{}{"tag": "2"})
	must(t, err)
	must(t, Abandon(e.cfg, "myapp", rel.Revision, "the helper died\nwith details"))
	got, err := e.cfg.Store.Get("myapp", rel.Revision)
	must(t, err)
	if got.Status != release.StatusFailed || got.Description != `Release "myapp" failed: the helper died` {
		t.Errorf("abandoned revision: %s %q", got.Status, got.Description)
	}
	if e.runningImage() != "nginx:1" {
		t.Errorf("abandon touched containers: running %s", e.runningImage())
	}
	// The release is no longer locked.
	if _, err := e.upgrade(nil, map[string]interface{}{"tag": "3"}); err != nil {
		t.Errorf("upgrade after abandon: %v", err)
	}
}
