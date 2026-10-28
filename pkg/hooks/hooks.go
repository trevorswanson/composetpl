// Package hooks implements Helm-style lifecycle hooks for compose. A hook is a
// service tagged with an x-composetpl extension field:
//
//	services:
//	  db-migrate:
//	    image: app:2
//	    command: ["migrate", "up"]
//	    x-composetpl:
//	      hook: [pre-install, pre-upgrade]  # or a single string
//	      weight: -5                        # lower runs first (default 0)
//	      delete-policy: [before-hook-creation, hook-succeeded]
//	      no-deps: true                     # don't start depends_on services
//	      retries: 3                        # run again on failure (default 0)
//	      backoff: 10s                      # first retry delay, doubling, capped at 5m
//	      timeout: 2m                       # per attempt; overrides --timeout
//
// Hook services are removed from the deployed manifest and run to completion with
// `docker compose run` at their events. Services with hook "test" run on
// `composetpl test`.
package hooks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/trevorswanson/composetpl/pkg/compose"
	"github.com/trevorswanson/composetpl/pkg/release"
)

// ExtensionKey marks a service as a hook.
const ExtensionKey = "x-composetpl"

// Lifecycle events.
const (
	PreInstall   = "pre-install"
	PostInstall  = "post-install"
	PreUpgrade   = "pre-upgrade"
	PostUpgrade  = "post-upgrade"
	PreRollback  = "pre-rollback"
	PostRollback = "post-rollback"
	PreDelete    = "pre-delete"
	PostDelete   = "post-delete"
	Test         = "test"
)

// Delete policies.
const (
	BeforeHookCreation = "before-hook-creation"
	HookSucceeded      = "hook-succeeded"
	HookFailed         = "hook-failed"
)

var events = map[string]bool{
	PreInstall: true, PostInstall: true, PreUpgrade: true, PostUpgrade: true, PreRollback: true,
	PostRollback: true, PreDelete: true, PostDelete: true, Test: true,
}

var policies = map[string]bool{BeforeHookCreation: true, HookSucceeded: true, HookFailed: true}

const (
	// MaxRetries bounds x-composetpl.retries.
	MaxRetries = 100
	// DefaultBackoff is the delay before the first retry, as for Kubernetes Jobs.
	DefaultBackoff = 10 * time.Second
	// MaxBackoff caps the doubling retry delay.
	MaxBackoff = 5 * time.Minute
)

// Extract removes hook services from a merged manifest and returns them sorted
// by weight, then name. Services without the extension are left in place.
func Extract(root *yaml.Node) ([]*release.Hook, error) {
	services := mapValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil, nil
	}
	var hooks []*release.Hook
	kept := services.Content[:0:0]
	for i := 0; i+1 < len(services.Content); i += 2 {
		name, svc := services.Content[i].Value, services.Content[i+1]
		ext := mapValue(svc, ExtensionKey)
		if ext == nil {
			kept = append(kept, services.Content[i], svc)
			continue
		}
		h, err := parse(name, ext)
		if err != nil {
			return nil, err
		}
		removeKey(svc, ExtensionKey)
		if h.Service, err = encode(svc); err != nil {
			return nil, err
		}
		hooks = append(hooks, h)
	}
	services.Content = kept
	sort.SliceStable(hooks, func(i, j int) bool {
		if hooks[i].Weight != hooks[j].Weight {
			return hooks[i].Weight < hooks[j].Weight
		}
		return hooks[i].Name < hooks[j].Name
	})
	return hooks, nil
}

func parse(name string, ext *yaml.Node) (*release.Hook, error) {
	h := &release.Hook{Name: name}
	errf := func(format string, args ...interface{}) error {
		return fmt.Errorf("service %q: %s: %s", name, ExtensionKey, fmt.Sprintf(format, args...))
	}
	if ext.Kind != yaml.MappingNode {
		return nil, errf("must be a mapping")
	}
	for i := 0; i+1 < len(ext.Content); i += 2 {
		key, val := ext.Content[i].Value, ext.Content[i+1]
		switch key {
		case "hook":
			list, err := stringList(val)
			if err != nil {
				return nil, errf("hook: %v", err)
			}
			for _, e := range list {
				if !events[e] {
					return nil, errf("unknown hook event %q", e)
				}
			}
			h.Events = list
		case "weight":
			w, err := strconv.Atoi(val.Value)
			if err != nil || val.Kind != yaml.ScalarNode {
				return nil, errf("weight must be an integer")
			}
			h.Weight = w
		case "delete-policy":
			list, err := stringList(val)
			if err != nil {
				return nil, errf("delete-policy: %v", err)
			}
			for _, p := range list {
				if !policies[p] {
					return nil, errf("unknown delete policy %q", p)
				}
			}
			h.DeletePolicies = list
		case "no-deps":
			var b bool
			if err := val.Decode(&b); err != nil {
				return nil, errf("no-deps must be a boolean")
			}
			h.NoDeps = b
		case "retries":
			n, err := strconv.Atoi(val.Value)
			if err != nil || val.Kind != yaml.ScalarNode || n < 0 || n > MaxRetries {
				return nil, errf("retries must be an integer from 0 to %d", MaxRetries)
			}
			h.Retries = n
		case "backoff", "timeout":
			d, err := time.ParseDuration(val.Value)
			if err != nil || val.Kind != yaml.ScalarNode || d <= 0 {
				return nil, errf("%s must be a positive duration such as 30s or 2m", key)
			}
			if key == "backoff" {
				h.Backoff = d.String()
			} else {
				h.Timeout = d.String()
			}
		default:
			return nil, errf("unknown key %q", key)
		}
	}
	if len(h.Events) == 0 {
		return nil, errf("hook is required")
	}
	if len(h.DeletePolicies) == 0 {
		h.DeletePolicies = []string{BeforeHookCreation}
	}
	return h, nil
}

func stringList(n *yaml.Node) ([]string, error) {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}, nil
	case yaml.SequenceNode:
		var out []string
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("must be a string or a list of strings")
			}
			out = append(out, c.Value)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a string or a list of strings")
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func removeKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func encode(n *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	return buf.String(), enc.Close()
}

// manifestWith returns the release manifest with the hook service added back, so
// the hook can use the release's networks, volumes and depends_on services.
func manifestWith(manifest string, h *release.Hook) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(manifest), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	services := mapValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		removeKey(root, "services")
		services = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "services"}, services)
	}
	var svc yaml.Node
	if err := yaml.Unmarshal([]byte(h.Service), &svc); err != nil {
		return nil, fmt.Errorf("hook %q: %w", h.Name, err)
	}
	removeKey(services, h.Name)
	services.Content = append(services.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: h.Name}, svc.Content[0])
	out, err := encode(root)
	return []byte(out), err
}

// Runner executes hooks with docker compose.
type Runner struct {
	Compose compose.Runner
	// Output receives hook container output; nil discards it.
	Output io.Writer
	// Log receives one line per hook started.
	Log io.Writer
	// Timeout bounds each attempt unless the hook sets its own; 0 means no limit.
	Timeout time.Duration
	// Sleep waits between retries; nil uses a timer that honours ctx.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (r *Runner) sleep(ctx context.Context, d time.Duration) error {
	if r.Sleep != nil {
		return r.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Backoff returns the delay before retry n (1-based): base doubled n-1 times,
// capped at MaxBackoff.
func Backoff(base time.Duration, n int) time.Duration {
	d := base
	for i := 1; i < n && d < MaxBackoff; i++ {
		d *= 2
	}
	if d > MaxBackoff {
		d = MaxBackoff
	}
	return d
}

// Result is the outcome of one hook.
type Result struct {
	Name     string
	Started  time.Time
	Finished time.Time
	// Attempts is how many times the hook ran.
	Attempts int
	Err      error
}

// ContainerName is the deterministic name of a hook's container.
func ContainerName(rel *release.Release, h *release.Hook) string {
	return rel.Name + "-hook-" + h.Name
}

// Run executes every hook of rel registered for event, in order, stopping at the
// first failure. only, when non-empty, limits which hooks run (for `test --filter`).
func (r *Runner) Run(ctx context.Context, rel *release.Release, event string, only ...string) ([]Result, error) {
	var results []Result
	for _, h := range rel.Hooks {
		if !has(h.Events, event) || (len(only) > 0 && !has(only, h.Name)) {
			continue
		}
		res := r.runOne(ctx, rel, h, event)
		results = append(results, res)
		if res.Err != nil {
			return results, fmt.Errorf("%s hook %q failed: %w", event, h.Name, res.Err)
		}
	}
	return results, nil
}

func (r *Runner) runOne(ctx context.Context, rel *release.Release, h *release.Hook, event string) (res Result) {
	res = Result{Name: h.Name, Started: time.Now()}
	defer func() { res.Finished = time.Now() }()
	container := ContainerName(rel, h)
	manifest, err := manifestWith(rel.Manifest, h)
	if err != nil {
		res.Err = err
		return res
	}
	timeout := r.Timeout
	if h.Timeout != "" {
		if timeout, err = time.ParseDuration(h.Timeout); err != nil {
			res.Err = err
			return res
		}
	}
	backoff := DefaultBackoff
	if h.Backoff != "" {
		if backoff, err = time.ParseDuration(h.Backoff); err != nil {
			res.Err = err
			return res
		}
	}
	out := r.Output
	if out == nil {
		out = io.Discard
	}
	project := compose.Project{Name: rel.Name, Manifest: manifest, Dir: rel.ProjectDir}

	for {
		res.Attempts++
		// `run --name` fails if the container exists, so a retry always clears it.
		if res.Attempts > 1 || has(h.DeletePolicies, BeforeHookCreation) {
			if res.Err = r.Compose.RemoveContainer(ctx, container); res.Err != nil {
				return res
			}
		}
		if r.Log != nil {
			if res.Attempts == 1 {
				fmt.Fprintf(r.Log, "Running %s hook %s\n", event, h.Name)
			} else {
				fmt.Fprintf(r.Log, "Running %s hook %s (attempt %d/%d)\n", event, h.Name, res.Attempts, h.Retries+1)
			}
		}
		res.Err = r.runAttempt(ctx, project, h, container, timeout, out)
		if res.Err == nil || res.Attempts > h.Retries {
			break
		}
		delay := Backoff(backoff, res.Attempts)
		if r.Log != nil {
			fmt.Fprintf(r.Log, "Hook %s failed (attempt %d/%d): %v; retrying in %s\n",
				h.Name, res.Attempts, h.Retries+1, firstLine(res.Err.Error()), delay)
		}
		if err := r.sleep(ctx, delay); err != nil {
			res.Err = fmt.Errorf("%w (gave up waiting to retry: %v)", res.Err, err)
			break
		}
	}

	// Delete policies apply to the final outcome.
	if (res.Err == nil && has(h.DeletePolicies, HookSucceeded)) || (res.Err != nil && has(h.DeletePolicies, HookFailed)) {
		if err := r.Compose.RemoveContainer(ctx, container); err != nil && res.Err == nil {
			res.Err = err
		}
	}
	if res.Err != nil && res.Attempts > 1 {
		res.Err = fmt.Errorf("after %d attempts: %w", res.Attempts, res.Err)
	}
	return res
}

func (r *Runner) runAttempt(ctx context.Context, p compose.Project, h *release.Hook, container string, timeout time.Duration, out io.Writer) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.Compose.Run(ctx, p, compose.RunOptions{Service: h.Name, ContainerName: container, NoDeps: h.NoDeps, Output: out})
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// For returns the hooks of rel registered for event.
func For(rel *release.Release, event string) []*release.Hook {
	var out []*release.Hook
	for _, h := range rel.Hooks {
		if has(h.Events, event) {
			out = append(out, h)
		}
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
