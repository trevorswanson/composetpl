// Package action implements composetpl's commands on top of the chart engine,
// docker compose and release storage, mirroring Helm's pkg/action.
package action

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/trevorswanson/composetpl/internal/chart"
	"github.com/trevorswanson/composetpl/internal/compose"
	"github.com/trevorswanson/composetpl/internal/engine"
	"github.com/trevorswanson/composetpl/internal/hooks"
	"github.com/trevorswanson/composetpl/internal/output"
	"github.com/trevorswanson/composetpl/internal/registry"
	"github.com/trevorswanson/composetpl/internal/release"
	"github.com/trevorswanson/composetpl/internal/storage"
	"github.com/trevorswanson/composetpl/internal/values"
)

// Configuration is shared by every action that touches docker or release storage.
type Configuration struct {
	Store   storage.Driver
	Compose compose.Runner
	// Log receives warnings and progress meant for humans.
	Log io.Writer
	Now func() time.Time
	// Registry pulls oci:// charts; nil uses a default client.
	Registry *registry.Client
	// HookSleep waits between hook retries; nil sleeps for real (tests skip the wait).
	HookSleep func(ctx context.Context, d time.Duration) error

	caps map[string]interface{}
}

// capabilities queries the engine once for .Capabilities.
func (c *Configuration) capabilities(ctx context.Context) (map[string]interface{}, error) {
	if c.caps != nil {
		return c.caps, nil
	}
	cv, err := c.Compose.Version(ctx)
	if err != nil {
		return nil, err
	}
	dv, err := c.Compose.ServerVersion(ctx)
	if err != nil {
		return nil, err
	}
	c.caps = map[string]interface{}{"ComposeVersion": cv, "DockerVersion": dv}
	return c.caps, nil
}

// liveRenderOptions fills in engine-backed .Capabilities and, unless dryRun,
// a lookup function that inspects the engine.
func (c *Configuration) liveRenderOptions(ctx context.Context, o RenderOptions, dryRun bool) (RenderOptions, error) {
	caps, err := c.capabilities(ctx)
	if err != nil {
		return o, err
	}
	o.Capabilities = caps
	o.Context = ctx
	if o.Registry == nil {
		o.Registry = c.Registry
	}
	if !dryRun {
		o.Lookup = func(kind, name string) (map[string]interface{}, error) {
			return c.Compose.Inspect(ctx, kind, name)
		}
	}
	return o, nil
}

func (c *Configuration) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Configuration) warnf(format string, args ...interface{}) {
	if c.Log != nil {
		fmt.Fprintf(c.Log, "WARNING: "+format+"\n", args...)
	}
}

// RenderOptions describe how to render a chart.
type RenderOptions struct {
	// ChartPath is a chart directory, .tgz, or oci:// reference.
	ChartPath string
	// Version is a semver constraint for oci:// charts without a tag.
	Version string
	// Registry pulls oci:// charts; nil uses a default client.
	Registry *registry.Client
	// HookSleep waits between hook retries; nil sleeps for real (tests skip the wait).
	HookSleep func(ctx context.Context, d time.Duration) error
	// Context bounds registry access; nil means context.Background().
	Context     context.Context
	ReleaseName string
	// Revision and IsUpgrade populate .Release; revision 0 renders as 1 (an install).
	Revision  int
	IsUpgrade bool
	// UserValues are the -f/--set values.
	UserValues map[string]interface{}
	// Defaults, when non-nil, replaces the chart's values.yaml (used by --reuse-values).
	Defaults map[string]interface{}
	Strict   bool
	NoEnv    bool
	// ShowOnly limits output to these templates, e.g. "templates/apps.yaml".
	ShowOnly []string
	// SubchartNotes also renders subcharts' NOTES.txt.
	SubchartNotes bool
	// Capabilities populates .Capabilities; nil uses DefaultCapabilities.
	Capabilities map[string]interface{}
	// Lookup backs the lookup function; nil makes lookup return empty maps.
	Lookup func(kind, name string) (map[string]interface{}, error)
}

// DefaultDockerVersion is .Capabilities.DockerVersion when no engine is queried
// (template, lint), like Helm's default KubeVersion.
const DefaultDockerVersion = "25.0.0"

// DefaultCapabilities returns .Capabilities for rendering without an engine.
func DefaultCapabilities() map[string]interface{} {
	return map[string]interface{}{"ComposeVersion": compose.MinVersion, "DockerVersion": DefaultDockerVersion}
}

// Rendered is a rendered chart, before its templates are merged.
type Rendered struct {
	Chart     *chart.Chart
	ChartRef  string
	Values    map[string]interface{}
	Templates []engine.Rendered
	Notes     string
}

// Render loads and renders a chart.
func Render(o RenderOptions) (*Rendered, error) {
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	c, ref, err := LoadChart(ctx, o.Registry, o.ChartPath, o.Version)
	if err != nil {
		return nil, err
	}
	defaults := c.Values
	if o.Defaults != nil {
		defaults = o.Defaults
	}
	vals := values.Coalesce(o.UserValues, defaults)
	if err := c.ValidateValues(vals); err != nil {
		return nil, err
	}
	// Drop disabled subcharts and coalesce the enabled ones' values under .Values.<name>.
	if c, vals, err = chart.ProcessDependencies(c, vals); err != nil {
		return nil, err
	}
	caps := o.Capabilities
	if caps == nil {
		caps = DefaultCapabilities()
	}

	name := o.ReleaseName
	if name == "" {
		name = c.Metadata.Name
	}
	revision := o.Revision
	if revision == 0 {
		revision = 1
	}
	rel := map[string]interface{}{
		"Name": name, "Revision": revision, "IsInstall": !o.IsUpgrade, "IsUpgrade": o.IsUpgrade,
		"Service": "composetpl",
	}
	res, err := engine.Render(c, vals, rel, engine.Options{
		Strict: o.Strict, NoEnv: o.NoEnv, Capabilities: caps, Lookup: o.Lookup, SubchartNotes: o.SubchartNotes,
	})
	if err != nil {
		return nil, err
	}
	templates := res.Templates
	if len(o.ShowOnly) > 0 {
		if templates, err = filterShowOnly(c.Metadata.Name, templates, o.ShowOnly); err != nil {
			return nil, err
		}
	}
	return &Rendered{Chart: c, ChartRef: ref, Values: vals, Templates: templates, Notes: res.Notes}, nil
}

func filterShowOnly(chartName string, rendered []engine.Rendered, only []string) ([]engine.Rendered, error) {
	var out []engine.Rendered
	for _, want := range only {
		full := chartName + "/" + strings.TrimPrefix(want, "./")
		found := false
		for _, r := range rendered {
			if r.Name == full {
				out = append(out, r)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("could not find template %s in chart", want)
		}
	}
	return out, nil
}

// Merge deep-merges the rendered templates into one compose document.
func (r *Rendered) Merge() (*yaml.Node, error) {
	return output.Merge(r.Templates)
}

// releaseFrom merges a rendered chart into a new release revision named name.
func (c *Configuration) releaseFrom(r *Rendered, name string, revision int, config map[string]interface{}, projectDir string) (*release.Release, error) {
	root, err := r.Merge()
	if err != nil {
		return nil, err
	}
	hks, err := hooks.Extract(root)
	if err != nil {
		return nil, err
	}
	if prev := output.SetName(root, name); prev != "" && prev != name {
		c.warnf("the chart sets compose project name %q; using the release name %q instead", prev, name)
	}
	manifest, err := output.Encode(root)
	if err != nil {
		return nil, err
	}
	if projectDir == "" {
		// Relative paths resolve against the chart directory; a packaged chart has
		// none, so use the working directory as docker compose would.
		if projectDir = r.Chart.Dir; projectDir == "" {
			if projectDir, err = os.Getwd(); err != nil {
				return nil, err
			}
		}
	}
	md := r.Chart.Metadata
	now := c.now()
	return &release.Release{
		APIVersion: release.APIVersion,
		Name:       name,
		Revision:   revision,
		Chart: release.ChartInfo{
			Name: md.Name, Version: md.Version, AppVersion: md.AppVersion,
			Description: md.Description, Ref: r.ChartRef,
		},
		Config:        config,
		Values:        r.Values,
		Manifest:      string(manifest),
		Hooks:         hks,
		Notes:         r.Notes,
		ProjectDir:    projectDir,
		FirstDeployed: now,
		LastDeployed:  now,
	}, nil
}

func project(r *release.Release) compose.Project {
	return compose.Project{Name: r.Name, Manifest: []byte(r.Manifest), Dir: r.ProjectDir}
}

// DeployOptions are shared by install, upgrade and rollback.
type DeployOptions struct {
	Wait          bool
	Timeout       time.Duration
	Pull          string
	ForceRecreate bool
	// NoHooks skips lifecycle hooks (helm --no-hooks).
	NoHooks bool
}

// runHooks runs rel's hooks for event, streaming their output to the log.
func (c *Configuration) runHooks(ctx context.Context, rel *release.Release, event string, timeout time.Duration, skip bool) error {
	if skip {
		return nil
	}
	r := &hooks.Runner{Compose: c.Compose, Output: c.Log, Log: c.Log, Timeout: timeout, Sleep: c.HookSleep}
	_, err := r.Run(ctx, rel, event)
	return err
}

func (c *Configuration) up(ctx context.Context, r *release.Release, o DeployOptions) error {
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		// Leave compose's own --wait-timeout room to report before we kill it.
		ctx, cancel = context.WithTimeout(ctx, o.Timeout+30*time.Second)
		defer cancel()
	}
	return c.Compose.Up(ctx, project(r), compose.UpOptions{
		Wait: o.Wait, Timeout: o.Timeout, Pull: o.Pull, ForceRecreate: o.ForceRecreate,
	})
}

// history returns the release history, or nil if the release does not exist.
func (c *Configuration) history(name string) ([]*release.Release, error) {
	h, err := c.Store.History(name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return h, err
}

func errPending(r *release.Release) error {
	return fmt.Errorf("another operation (%s) is in progress for release %q at revision %d; "+
		"if it is stuck, run `composetpl rollback %s` to recover", r.Status, r.Name, r.Revision, r.Name)
}

// markDeployed sets r deployed and every other deployed revision superseded, then saves.
func (c *Configuration) markDeployed(r *release.Release, h []*release.Release, description string) error {
	changed := []*release.Release{}
	for _, old := range h {
		if old.Revision != r.Revision && old.Status == release.StatusDeployed {
			old.Status = release.StatusSuperseded
			changed = append(changed, old)
		}
	}
	r.Status = release.StatusDeployed
	r.Description = description
	r.LastDeployed = c.now()
	return c.Store.Save(append(changed, r)...)
}

func (c *Configuration) markFailed(r *release.Release, cause error) error {
	r.Status = release.StatusFailed
	r.Description = fmt.Sprintf("Release %q failed: %v", r.Name, firstLine(cause.Error()))
	if err := c.Store.Save(r); err != nil {
		return fmt.Errorf("%w (and recording the failure also failed: %v)", cause, err)
	}
	return cause
}

// prune deletes the oldest revisions beyond max, never the newest or a deployed one.
func (c *Configuration) prune(name string, max int) error {
	if max <= 0 {
		return nil
	}
	h, err := c.Store.History(name)
	if err != nil {
		return err
	}
	var drop []*release.Release
	for i := 0; i < len(h)-1 && len(h)-len(drop) > max; i++ {
		if h[i].Status != release.StatusDeployed {
			drop = append(drop, h[i])
		}
	}
	return c.Store.Delete(drop...)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
