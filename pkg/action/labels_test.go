package action

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReleaseLabels(t *testing.T) {
	e := newEnv(t)
	in := NewInstall(e.cfg)
	in.Labels = map[string]string{"managed-by": "composecd", "composecd/source-revision": "aaa"}
	if _, err := in.Run(context.Background(), "myapp", e.chart, nil); err != nil {
		t.Fatal(err)
	}
	// Upgrade merges over the current labels; "" removes one.
	rel, err := e.upgrade(func(u *Upgrade) {
		u.Labels = map[string]string{"composecd/source-revision": "bbb", "managed-by": "", "x": "1"}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"composecd/source-revision": "bbb", "x": "1"}; !reflect.DeepEqual(rel.Labels, want) {
		t.Errorf("upgrade labels = %v", rel.Labels)
	}
	// An upgrade without labels keeps them.
	if rel, err = e.upgrade(nil, nil); err != nil || rel.Labels["x"] != "1" {
		t.Errorf("unchanged labels = %v (%v)", rel.Labels, err)
	}
	// Rollback starts from the target revision's labels.
	rb := NewRollback(e.cfg)
	rb.Version, rb.Labels = 1, map[string]string{"composecd/operation-id": "op1"}
	if rel, err = rb.Run(context.Background(), "myapp"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"managed-by": "composecd", "composecd/source-revision": "aaa", "composecd/operation-id": "op1"}
	if !reflect.DeepEqual(rel.Labels, want) {
		t.Errorf("rollback labels = %v", rel.Labels)
	}
	stored, err := e.cfg.Store.Get("myapp", rel.Revision)
	if err != nil || !reflect.DeepEqual(stored.Labels, want) {
		t.Errorf("stored labels = %v (%v)", stored.Labels, err)
	}

	l := NewList(e.cfg)
	for sel, n := range map[string]int{"managed-by=composecd": 1, "managed-by!=composecd": 0, "!x": 1} {
		l.Selector = sel
		items, err := l.Run(context.Background())
		if err != nil || len(items) != n {
			t.Errorf("list -l %q: %d items (%v), want %d", sel, len(items), err, n)
		}
	}

	in = NewInstall(e.cfg)
	in.Labels = map[string]string{"bad key": "v"}
	if _, err := in.Run(context.Background(), "other", e.chart, nil); err == nil || !strings.Contains(err.Error(), "invalid label key") {
		t.Errorf("bad label: %v", err)
	}
}

func TestInstallEnv(t *testing.T) {
	e := newEnv(t)
	tpl := "services:\n  web:\n    image: {{ env \"IMAGE_REPO\" }}/{{ .Values.image }}:{{ .Values.tag }}\n"
	if err := os.WriteFile(filepath.Join(e.chart, "templates", "svc.yaml"), []byte(tpl), 0o644); err != nil {
		t.Fatal(err)
	}
	in := NewInstall(e.cfg)
	in.Env = func(k string) (string, bool) {
		if k == "IMAGE_REPO" {
			return "registry.lan", true
		}
		return "", false
	}
	if _, err := in.Run(context.Background(), "myapp", e.chart, nil); err != nil {
		t.Fatal(err)
	}
	// Upgrades render with their own Env (nil here: the process environment).
	t.Setenv("IMAGE_REPO", "ghcr.io")
	if _, err := e.upgrade(nil, nil); err != nil {
		t.Fatal(err)
	}
	h, err := e.cfg.Store.History("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h[0].Manifest, "registry.lan/nginx:1") || !strings.Contains(h[1].Manifest, "ghcr.io/nginx:1") {
		t.Errorf("manifests:\n%s\n%s", h[0].Manifest, h[1].Manifest)
	}
}
