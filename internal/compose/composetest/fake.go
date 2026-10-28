// Package composetest provides an in-memory compose.Runner for tests.
package composetest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/trevorswanson/composetpl/internal/compose"
)

// Fake records calls and keeps the manifests of "running" projects in memory.
type Fake struct {
	mu      sync.Mutex
	Calls   []string
	Running map[string]string
	// FailUp makes Up fail when the manifest contains this string.
	FailUp string
	// FailRun makes Run fail for this service.
	FailRun string
	// FailRunTimes, when > 0, limits FailRun to that many failures (0 = always fail).
	FailRunTimes int
	// RunDelay makes Run block until its context is done (for timeout tests).
	RunDelay bool
	// RunOutput is written to RunOptions.Output by Run.
	RunOutput string
	// Objects are returned by Inspect, keyed "kind/name".
	Objects map[string]map[string]interface{}
}

// New returns an empty Fake.
func New() *Fake {
	return &Fake{Running: map[string]string{}, Objects: map[string]map[string]interface{}{}}
}

func (f *Fake) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, s)
}

// CallLog returns the calls joined by ";".
func (f *Fake) CallLog() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.Calls, ";")
}

func (f *Fake) Version(context.Context) (string, error) { return "2.30.0", nil }

func (f *Fake) Config(_ context.Context, p compose.Project) error {
	f.record("config " + p.Name)
	return nil
}

func (f *Fake) Up(_ context.Context, p compose.Project, o compose.UpOptions) error {
	f.record(fmt.Sprintf("up %s wait=%v", p.Name, o.Wait))
	if f.FailUp != "" && strings.Contains(string(p.Manifest), f.FailUp) {
		return errors.New("container web is unhealthy")
	}
	f.mu.Lock()
	f.Running[p.Name] = string(p.Manifest)
	f.mu.Unlock()
	return nil
}

func (f *Fake) Down(_ context.Context, p compose.Project, o compose.DownOptions) error {
	f.record(fmt.Sprintf("down %s volumes=%v", p.Name, o.Volumes))
	f.mu.Lock()
	delete(f.Running, p.Name)
	f.mu.Unlock()
	return nil
}

func (f *Fake) Run(ctx context.Context, p compose.Project, o compose.RunOptions) error {
	f.record(fmt.Sprintf("run %s %s name=%s nodeps=%v", p.Name, o.Service, o.ContainerName, o.NoDeps))
	if o.Output != nil && f.RunOutput != "" {
		_, _ = o.Output.Write([]byte(f.RunOutput))
	}
	if f.RunDelay {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.FailRun != "" && o.Service == f.FailRun {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case f.FailRunTimes == 0: // always fail
			return fmt.Errorf("service %s exited with code 1", o.Service)
		case f.FailRunTimes > 0:
			if f.FailRunTimes--; f.FailRunTimes == 0 {
				f.FailRunTimes = -1 // failures used up
			}
			return fmt.Errorf("service %s exited with code 1", o.Service)
		}
	}
	return nil
}

func (f *Fake) RemoveContainer(_ context.Context, name string) error {
	f.record("rm " + name)
	return nil
}

func (f *Fake) PS(_ context.Context, project string) ([]compose.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.Running[project]; !ok {
		return nil, nil
	}
	return []compose.Container{{Name: project + "-web-1", Service: "web", State: "running", Status: "Up 1 minute"}}, nil
}

func (f *Fake) Ls(context.Context) ([]compose.ProjectStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []compose.ProjectStatus
	for name := range f.Running {
		out = append(out, compose.ProjectStatus{Name: name, Status: "running(1)"})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (f *Fake) ServerVersion(context.Context) (string, error) { return "29.0.0", nil }

func (f *Fake) Inspect(_ context.Context, kind, name string) (map[string]interface{}, error) {
	f.record("inspect " + kind + " " + name)
	if !compose.LookupKinds[kind] {
		return nil, fmt.Errorf("lookup: unsupported kind %q", kind)
	}
	if o, ok := f.Objects[kind+"/"+name]; ok {
		return o, nil
	}
	return map[string]interface{}{}, nil
}
