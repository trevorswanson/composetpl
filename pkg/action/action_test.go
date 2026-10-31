package action

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trevorswanson/composetpl/pkg/compose/composetest"
	"github.com/trevorswanson/composetpl/pkg/release"
	"github.com/trevorswanson/composetpl/pkg/storage"
)

type env struct {
	t     *testing.T
	cfg   *Configuration
	fake  *composetest.Fake
	chart string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{t: t, fake: composetest.New(), chart: filepath.Join(dir, "app")}
	tick := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	e.cfg = &Configuration{
		Store:   storage.NewLocal(filepath.Join(dir, "state")),
		Compose: e.fake,
		Now:     func() time.Time { tick = tick.Add(time.Minute); return tick },
	}
	e.writeChart("nginx", "1.0.0")
	return e
}

func (e *env) writeChart(image, version string) {
	e.t.Helper()
	files := map[string]string{
		"Chart.yaml":          "name: app\nversion: " + version + "\nappVersion: '1'\n",
		"values.yaml":         "image: " + image + "\ntag: '1'\n",
		"templates/svc.yaml":  "services:\n  web:\n    image: {{ .Values.image }}:{{ .Values.tag }}\n",
		"templates/NOTES.txt": "Visit {{ .Release.Name }} at tag {{ .Values.tag }}{{ if .Release.IsUpgrade }} (upgrade {{ .Release.Revision }}){{ end }}",
	}
	for name, content := range files {
		p := filepath.Join(e.chart, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) install(vals map[string]interface{}) (*release.Release, error) {
	return NewInstall(e.cfg).Run(context.Background(), "myapp", e.chart, vals)
}

func (e *env) upgrade(mod func(*Upgrade), vals map[string]interface{}) (*release.Release, error) {
	u := NewUpgrade(e.cfg)
	if mod != nil {
		mod(u)
	}
	return u.Run(context.Background(), "myapp", e.chart, vals)
}

// statuses renders the history as "1:superseded 2:deployed".
func (e *env) statuses() string {
	e.t.Helper()
	h, err := e.cfg.Store.History("myapp")
	if errors.Is(err, storage.ErrNotFound) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var parts []string
	for _, r := range h {
		parts = append(parts, fmt.Sprintf("%d:%s", r.Revision, r.Status))
	}
	return strings.Join(parts, " ")
}

func (e *env) runningImage() string {
	m := e.fake.Running["myapp"]
	i := strings.Index(m, "image: ")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(m[i+len("image: "):], "\n", 2)[0])
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestInstall(t *testing.T) {
	e := newEnv(t)
	rel, err := e.install(map[string]interface{}{"tag": "2"})
	must(t, err)
	if rel.Status != release.StatusDeployed || rel.Description != "Install complete" {
		t.Errorf("release = %s %q", rel.Status, rel.Description)
	}
	if rel.Notes != "Visit myapp at tag 2" {
		t.Errorf("notes = %q", rel.Notes)
	}
	if !strings.HasPrefix(rel.Manifest, "name: myapp\n") {
		t.Errorf("manifest should set the project name:\n%s", rel.Manifest)
	}
	if rel.ProjectDir != e.chart || rel.Chart.String() != "app-1.0.0" {
		t.Errorf("projectDir=%q chart=%s", rel.ProjectDir, rel.Chart)
	}
	if got := e.fake.CallLog(); got != "config myapp;up myapp wait=false" {
		t.Errorf("calls = %s", got)
	}
	if _, err := e.install(nil); err == nil || !strings.Contains(err.Error(), "still in use") {
		t.Errorf("second install: %v", err)
	}
	if err := release.ValidateName("Bad"); err == nil {
		t.Error("expected invalid name")
	}
}

func TestInstallDryRun(t *testing.T) {
	e := newEnv(t)
	i := NewInstall(e.cfg)
	i.DryRun = true
	rel, err := i.Run(context.Background(), "myapp", e.chart, nil)
	must(t, err)
	if rel.Status != release.StatusPendingInstall || e.statuses() != "" || len(e.fake.Running) != 0 {
		t.Errorf("dry run deployed or stored: %s %q", rel.Status, e.statuses())
	}
}

func TestInstallAtomicFailure(t *testing.T) {
	e := newEnv(t)
	e.fake.FailUp = "nginx"
	i := NewInstall(e.cfg)
	_, err := i.Run(context.Background(), "myapp", e.chart, nil)
	if err == nil || e.statuses() != "1:failed" {
		t.Fatalf("non-atomic failure: err=%v history=%q", err, e.statuses())
	}

	e2 := newEnv(t)
	e2.fake.FailUp = "nginx"
	i = NewInstall(e2.cfg)
	i.Atomic = true
	_, err = i.Run(context.Background(), "myapp", e2.chart, nil)
	if err == nil || !strings.Contains(err.Error(), "uninstalled due to atomic") {
		t.Fatalf("atomic failure err = %v", err)
	}
	if e2.statuses() != "" || !strings.Contains(e2.fake.CallLog(), "down myapp") {
		t.Errorf("atomic install should purge and down: history=%q calls=%v", e2.statuses(), e2.fake.Calls)
	}
}

func TestUpgradeValueSemantics(t *testing.T) {
	e := newEnv(t)
	_, err := e.install(map[string]interface{}{"tag": "2"})
	must(t, err)

	// No values given: Helm reuses the previous user values.
	_, err = e.upgrade(nil, nil)
	must(t, err)
	if e.runningImage() != "nginx:2" {
		t.Errorf("reuse-by-default: running %s", e.runningImage())
	}

	// The chart's default image changes.
	e.writeChart("caddy", "1.1.0")

	// --reuse-values keeps the old computed values, including the old default image.
	_, err = e.upgrade(func(u *Upgrade) { u.ReuseValues = true }, map[string]interface{}{"tag": "3"})
	must(t, err)
	if e.runningImage() != "nginx:3" {
		t.Errorf("--reuse-values: running %s", e.runningImage())
	}

	// --reset-then-reuse-values takes new chart defaults but keeps user values.
	rel, err := e.upgrade(func(u *Upgrade) { u.ResetThenReuseValues = true }, nil)
	must(t, err)
	if e.runningImage() != "caddy:3" || rel.Config["tag"] != "3" {
		t.Errorf("--reset-then-reuse-values: running %s config %v", e.runningImage(), rel.Config)
	}

	// --reset-values drops user values.
	rel, err = e.upgrade(func(u *Upgrade) { u.ResetValues = true }, nil)
	must(t, err)
	if e.runningImage() != "caddy:1" || len(rel.Config) != 0 {
		t.Errorf("--reset-values: running %s config %v", e.runningImage(), rel.Config)
	}
	if got := e.statuses(); got != "1:superseded 2:superseded 3:superseded 4:superseded 5:deployed" {
		t.Errorf("history = %s", got)
	}
	if rel.FirstDeployed.After(rel.LastDeployed) || rel.Chart.Version != "1.1.0" {
		t.Errorf("timestamps or chart wrong: %+v", rel)
	}
}

func TestUpgradeFailureAndAtomic(t *testing.T) {
	e := newEnv(t)
	_, err := e.install(nil)
	must(t, err)

	e.fake.FailUp = "nginx:bad"
	_, err = e.upgrade(nil, map[string]interface{}{"tag": "bad"})
	if err == nil || e.statuses() != "1:deployed 2:failed" {
		t.Fatalf("plain failure: err=%v history=%s", err, e.statuses())
	}

	_, err = e.upgrade(func(u *Upgrade) { u.Atomic = true }, map[string]interface{}{"tag": "bad"})
	if err == nil || !strings.Contains(err.Error(), "rolled back to revision 1") {
		t.Fatalf("atomic err = %v", err)
	}
	if got := e.statuses(); got != "1:superseded 2:failed 3:failed 4:deployed" {
		t.Errorf("history = %s", got)
	}
	if e.runningImage() != "nginx:1" {
		t.Errorf("running %s after atomic rollback", e.runningImage())
	}
}

func TestUpgradeRequiresRelease(t *testing.T) {
	e := newEnv(t)
	if _, err := e.upgrade(nil, nil); err == nil || !strings.Contains(err.Error(), "has no deployed releases") {
		t.Errorf("got %v", err)
	}
	rel, err := e.upgrade(func(u *Upgrade) { u.Install = true }, nil)
	must(t, err)
	if rel.Revision != 1 || rel.Description != "Install complete" {
		t.Errorf("upgrade --install: %+v", rel)
	}
}

func TestPendingLockAndRollbackRecovery(t *testing.T) {
	e := newEnv(t)
	_, err := e.install(nil)
	must(t, err)
	stuck, _ := e.cfg.Store.Get("myapp", 1)
	cp := *stuck
	cp.Revision, cp.Status = 2, release.StatusPendingUpgrade
	must(t, e.cfg.Store.Save(&cp))

	if _, err := e.upgrade(nil, nil); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Fatalf("expected pending lock, got %v", err)
	}
	rb := NewRollback(e.cfg)
	rel, err := rb.Run(context.Background(), "myapp")
	must(t, err)
	if rel.Revision != 3 || rel.Description != "Rollback to 1" {
		t.Errorf("rollback = rev %d %q", rel.Revision, rel.Description)
	}
	if got := e.statuses(); got != "1:superseded 2:pending-upgrade 3:deployed" {
		t.Errorf("history = %s", got)
	}
}

func TestRollback(t *testing.T) {
	e := newEnv(t)
	_, err := e.install(nil)
	must(t, err)
	_, err = e.upgrade(nil, map[string]interface{}{"tag": "2"})
	must(t, err)

	rel, err := NewRollback(e.cfg).Run(context.Background(), "myapp")
	must(t, err)
	if e.runningImage() != "nginx:1" || rel.Notes != "Visit myapp at tag 1" || len(rel.Config) != 0 {
		t.Errorf("rollback restored %s, notes %q, config %v", e.runningImage(), rel.Notes, rel.Config)
	}
	rb := NewRollback(e.cfg)
	rb.Version = 2
	_, err = rb.Run(context.Background(), "myapp")
	must(t, err)
	if e.runningImage() != "nginx:2" || e.statuses() != "1:superseded 2:superseded 3:superseded 4:deployed" {
		t.Errorf("rollback to 2: running %s history %s", e.runningImage(), e.statuses())
	}
	rb.Version = 99
	if _, err := rb.Run(context.Background(), "myapp"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("rollback to missing revision: %v", err)
	}
}

func TestHistoryMax(t *testing.T) {
	e := newEnv(t)
	_, err := e.install(nil)
	must(t, err)
	for i := 0; i < 4; i++ {
		_, err = e.upgrade(func(u *Upgrade) { u.HistoryMax = 3 }, map[string]interface{}{"tag": fmt.Sprint(i)})
		must(t, err)
	}
	if got := e.statuses(); got != "3:superseded 4:superseded 5:deployed" {
		t.Errorf("history = %s", got)
	}
}

func TestUninstallListStatus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	_, err := e.install(nil)
	must(t, err)

	st, err := NewStatus(e.cfg).Run(ctx, "myapp")
	must(t, err)
	if len(st.Containers) != 1 || st.Revision != 1 {
		t.Errorf("status = %+v", st)
	}
	items, err := NewList(e.cfg).Run(ctx)
	must(t, err)
	if len(items) != 1 || items[0].Live != "running(1)" {
		t.Errorf("list = %+v", items)
	}

	u := NewUninstall(e.cfg)
	u.KeepHistory, u.Volumes = true, true
	_, err = u.Run(ctx, "myapp")
	must(t, err)
	if e.statuses() != "1:uninstalled" || len(e.fake.Running) != 0 {
		t.Errorf("after uninstall --keep-history: %s running=%v", e.statuses(), e.fake.Running)
	}
	if !strings.Contains(e.fake.CallLog(), "down myapp volumes=true") {
		t.Errorf("calls = %v", e.fake.Calls)
	}
	if items, _ = NewList(e.cfg).Run(ctx); len(items) != 0 {
		t.Errorf("default list should hide uninstalled: %+v", items)
	}
	l := NewList(e.cfg)
	l.All = true
	if items, _ = l.Run(ctx); len(items) != 1 {
		t.Errorf("list -a = %+v", items)
	}

	// Reinstalling continues the revision numbers.
	rel, err := e.install(nil)
	must(t, err)
	if rel.Revision != 2 {
		t.Errorf("reinstall revision = %d", rel.Revision)
	}
	_, err = NewUninstall(e.cfg).Run(ctx, "myapp")
	must(t, err)
	if e.statuses() != "" {
		t.Errorf("uninstall should purge history: %s", e.statuses())
	}
}

func (e *env) addHooks() {
	e.t.Helper()
	p := filepath.Join(e.chart, "templates", "hooks.yaml")
	src := `services:
  migrate:
    image: app-migrate:{{ .Values.tag }}
    x-composetpl: {hook: [pre-install, pre-upgrade], weight: -1}
  notify:
    image: curl
    x-composetpl: {hook: [post-install, post-upgrade, post-rollback, pre-delete, post-delete]}
  smoke:
    image: busybox
    x-composetpl: {hook: test, no-deps: true}
`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func TestHooksLifecycle(t *testing.T) {
	e := newEnv(t)
	e.addHooks()
	ctx := context.Background()

	rel, err := e.install(nil)
	must(t, err)
	if len(rel.Hooks) != 3 || strings.Contains(rel.Manifest, "migrate") {
		t.Fatalf("hooks should be stored apart from the manifest: %d hooks\n%s", len(rel.Hooks), rel.Manifest)
	}
	want := "config myapp;rm myapp-hook-migrate;run myapp migrate name=myapp-hook-migrate nodeps=false;" +
		"up myapp wait=false;rm myapp-hook-notify;run myapp notify name=myapp-hook-notify nodeps=false"
	if e.fake.CallLog() != want {
		t.Errorf("install calls:\n%s\nwant\n%s", e.fake.CallLog(), want)
	}

	// A failing pre-upgrade hook stops the upgrade before compose up.
	e.fake.Calls, e.fake.FailRun = nil, "migrate"
	_, err = e.upgrade(nil, map[string]interface{}{"tag": "2"})
	if err == nil || !strings.Contains(err.Error(), `pre-upgrade hook "migrate" failed`) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(e.fake.CallLog(), "up myapp") || e.statuses() != "1:deployed 2:failed" {
		t.Errorf("calls=%s history=%s", e.fake.CallLog(), e.statuses())
	}

	// --no-hooks skips them.
	e.fake.Calls = nil
	_, err = e.upgrade(func(u *Upgrade) { u.NoHooks = true }, map[string]interface{}{"tag": "2"})
	must(t, err)
	if strings.Contains(e.fake.CallLog(), "run ") {
		t.Errorf("--no-hooks ran hooks: %s", e.fake.CallLog())
	}
	e.fake.FailRun = ""

	// Tests run against the deployed revision.
	e.fake.RunOutput = "ok\n"
	_, results, err := NewReleaseTesting(e.cfg).Run(ctx, "myapp")
	must(t, err)
	if len(results) != 1 || !results[0].Passed || results[0].Logs != "ok\n" {
		t.Errorf("test results = %+v", results)
	}
	e.fake.FailRun = "smoke"
	_, results, err = NewReleaseTesting(e.cfg).Run(ctx, "myapp")
	if err == nil || len(results) != 1 || results[0].Passed {
		t.Errorf("failing test: err=%v results=%+v", err, results)
	}
	e.fake.FailRun = ""

	// Uninstall runs pre-delete, down, post-delete, then down again to clean up.
	e.fake.Calls = nil
	_, err = NewUninstall(e.cfg).Run(ctx, "myapp")
	must(t, err)
	want = "rm myapp-hook-notify;run myapp notify name=myapp-hook-notify nodeps=false;down myapp volumes=false;" +
		"rm myapp-hook-notify;run myapp notify name=myapp-hook-notify nodeps=false;down myapp volumes=false"
	if e.fake.CallLog() != want {
		t.Errorf("uninstall calls:\n%s\nwant\n%s", e.fake.CallLog(), want)
	}
}

func TestLookupOverride(t *testing.T) {
	e := newEnv(t)
	tpl := "services:\n  web:\n    image: {{ (lookup \"custom\" \"x\").image | default \"none\" }}\n"
	must(t, os.WriteFile(filepath.Join(e.chart, "templates/svc.yaml"), []byte(tpl), 0o644))
	var calls []string
	lookup := func(kind, name string) (map[string]interface{}, error) {
		calls = append(calls, kind+"/"+name)
		return map[string]interface{}{"image": "from-lookup"}, nil
	}

	i := NewInstall(e.cfg)
	i.Lookup = lookup
	rel, err := i.Run(context.Background(), "myapp", e.chart, nil)
	must(t, err)
	if !strings.Contains(rel.Manifest, "image: from-lookup") {
		t.Errorf("install ignored Lookup:\n%s", rel.Manifest)
	}
	rel, err = e.upgrade(func(u *Upgrade) { u.Lookup = lookup }, nil)
	must(t, err)
	if !strings.Contains(rel.Manifest, "image: from-lookup") {
		t.Errorf("upgrade ignored Lookup:\n%s", rel.Manifest)
	}
	rel, err = e.upgrade(func(u *Upgrade) { u.Lookup, u.DryRun = lookup, true }, nil)
	must(t, err)
	if !strings.Contains(rel.Manifest, "image: none") {
		t.Errorf("dry run should not call Lookup:\n%s", rel.Manifest)
	}
	if got := strings.Join(calls, ","); got != "custom/x,custom/x" {
		t.Errorf("lookup calls = %s", got)
	}
}
