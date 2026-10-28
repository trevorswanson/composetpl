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

	"github.com/trevorswanson/composetpl/internal/compose"
	"github.com/trevorswanson/composetpl/internal/release"
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
	// Timeout bounds each hook; 0 means no limit.
	Timeout time.Duration
}

// Result is the outcome of one hook.
type Result struct {
	Name     string
	Started  time.Time
	Finished time.Time
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

func (r *Runner) runOne(ctx context.Context, rel *release.Release, h *release.Hook, event string) Result {
	res := Result{Name: h.Name, Started: time.Now()}
	defer func() { res.Finished = time.Now() }()
	container := ContainerName(rel, h)
	if has(h.DeletePolicies, BeforeHookCreation) {
		if res.Err = r.Compose.RemoveContainer(ctx, container); res.Err != nil {
			return res
		}
	}
	manifest, err := manifestWith(rel.Manifest, h)
	if err != nil {
		res.Err = err
		return res
	}
	if r.Log != nil {
		fmt.Fprintf(r.Log, "Running %s hook %s\n", event, h.Name)
	}
	runCtx := ctx
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	out := r.Output
	if out == nil {
		out = io.Discard
	}
	res.Err = r.Compose.Run(runCtx, compose.Project{Name: rel.Name, Manifest: manifest, Dir: rel.ProjectDir},
		compose.RunOptions{Service: h.Name, ContainerName: container, NoDeps: h.NoDeps, Output: out})

	if (res.Err == nil && has(h.DeletePolicies, HookSucceeded)) || (res.Err != nil && has(h.DeletePolicies, HookFailed)) {
		if err := r.Compose.RemoveContainer(ctx, container); err != nil && res.Err == nil {
			res.Err = err
		}
	}
	return res
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
