package action

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/trevorswanson/composetpl/pkg/compose"
	"github.com/trevorswanson/composetpl/pkg/compose/composetest"
	"github.com/trevorswanson/composetpl/pkg/release"
)

func driftString(d []ServiceDrift) string {
	var parts []string
	for _, s := range d {
		parts = append(parts, fmt.Sprintf("%s:%s%v", s.Service, s.Reason, s.Containers))
	}
	return strings.Join(parts, " ")
}

func TestRepairDrift(t *testing.T) {
	e := newEnv(t)
	rel, err := e.install(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	drift := func() string {
		t.Helper()
		d, err := NewRepair(e.cfg).Drift(ctx, "myapp")
		if err != nil {
			t.Fatal(err)
		}
		return driftString(d)
	}
	if got := drift(); got != "" {
		t.Errorf("fresh install drift = %q", got)
	}

	// Someone edited the running project by hand.
	e.fake.Running["myapp"] = "services: {web: {image: hacked}}\n"
	if got := drift(); got != "web:changed[myapp-web-1]" {
		t.Errorf("changed: %q", got)
	}

	hash := composetest.Hash(rel.Manifest)
	e.fake.Containers = map[string][]compose.Container{"myapp": {
		{Name: "myapp-web-1", Service: "web", State: "exited", Labels: compose.Labels{compose.ConfigHashLabel: hash}},
		{Name: "myapp-old-1", Service: "old", State: "running"},
	}}
	if got := drift(); got != "old:orphaned[myapp-old-1] web:stopped[myapp-web-1]" {
		t.Errorf("stopped/orphaned: %q", got)
	}

	e.fake.Containers["myapp"] = nil
	if got := drift(); got != "web:missing[]" {
		t.Errorf("missing: %q", got)
	}
}

func TestRepairRun(t *testing.T) {
	e := newEnv(t)
	if _, err := e.install(nil); err != nil {
		t.Fatal(err)
	}
	e.writeChart("httpd", "1.1.0")
	e.fake.FailUp = "httpd"
	if _, err := e.upgrade(nil, nil); err == nil {
		t.Fatal("upgrade should fail")
	}
	e.fake.FailUp = ""
	delete(e.fake.Running, "myapp")
	before := e.statuses()
	calls := len(e.fake.Calls)

	// Dry run changes nothing.
	r := NewRepair(e.cfg)
	r.DryRun = true
	rel, err := r.Run(context.Background(), "myapp")
	if err != nil || rel.Revision != 1 || e.fake.Running["myapp"] != "" {
		t.Fatalf("dry run: rev %v err %v", rel, err)
	}

	// Repair redeploys the last deployed revision, not the failed one, with no
	// hooks and no new revision.
	rel, err = NewRepair(e.cfg).Run(context.Background(), "myapp")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Revision != 1 || e.runningImage() != "nginx:1" {
		t.Errorf("repaired revision %d, image %q", rel.Revision, e.runningImage())
	}
	if got := e.statuses(); got != before {
		t.Errorf("history changed: %q -> %q", before, got)
	}
	if got := strings.Join(e.fake.Calls[calls:], ";"); got != "up myapp wait=false" {
		t.Errorf("calls = %q", got)
	}
}

func TestRepairRefusesPendingAndUndeployed(t *testing.T) {
	e := newEnv(t)
	if _, err := NewRepair(e.cfg).Run(context.Background(), "myapp"); err == nil {
		t.Error("repair of a missing release should fail")
	}
	rel, err := e.install(nil)
	if err != nil {
		t.Fatal(err)
	}
	rel.Status = release.StatusPendingUpgrade
	if err := e.cfg.Store.Save(rel); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepair(e.cfg).Run(context.Background(), "myapp"); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Errorf("pending: %v", err)
	}
}
