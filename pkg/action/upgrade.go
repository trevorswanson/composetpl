package action

import (
	"context"
	"fmt"

	"github.com/trevorswanson/composetpl/pkg/hooks"
	"github.com/trevorswanson/composetpl/pkg/release"
	"github.com/trevorswanson/composetpl/pkg/values"
)

// Upgrade deploys a new revision of an existing release.
type Upgrade struct {
	cfg *Configuration
	DeployOptions
	// Install installs the release if it does not exist (upgrade --install).
	Install              bool
	ReuseValues          bool
	ResetValues          bool
	ResetThenReuseValues bool
	HistoryMax           int
	Strict               bool
	NoEnv                bool
	// Env backs the env and expandenv template functions; nil reads the process environment.
	Env         func(key string) (string, bool)
	Atomic      bool
	DryRun      bool
	Description string
	ProjectDir  string
	// ChartVersion is a semver constraint for oci:// charts.
	ChartVersion string
	// SubchartNotes also renders subcharts' NOTES.txt.
	SubchartNotes bool
	// Labels are merged over the current revision's labels; a key set to "" is removed.
	Labels map[string]string
}

// NewUpgrade returns an Upgrade action with Helm's default history limit.
func NewUpgrade(cfg *Configuration) *Upgrade { return &Upgrade{cfg: cfg, HistoryMax: 10} }

// Run upgrades release name to chartPath with the given user values.
func (u *Upgrade) Run(ctx context.Context, name, chartPath string, userValues map[string]interface{}) (*release.Release, error) {
	if err := release.ValidateName(name); err != nil {
		return nil, err
	}
	if err := release.ValidateLabels(u.Labels); err != nil {
		return nil, err
	}
	h, err := u.cfg.history(name)
	if err != nil {
		return nil, err
	}
	if len(h) == 0 || h[len(h)-1].Status == release.StatusUninstalled {
		if !u.Install {
			return nil, fmt.Errorf("%q has no deployed releases", name)
		}
		in := &Install{cfg: u.cfg, DeployOptions: u.DeployOptions, Strict: u.Strict, NoEnv: u.NoEnv, Env: u.Env,
			Atomic: u.Atomic, DryRun: u.DryRun, Description: u.Description, ProjectDir: u.ProjectDir,
			ChartVersion: u.ChartVersion, SubchartNotes: u.SubchartNotes, Labels: u.Labels}
		return in.Run(ctx, name, chartPath, userValues)
	}
	latest := h[len(h)-1]
	if latest.Status.IsPending() {
		return nil, errPending(latest)
	}
	current := latest
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Status == release.StatusDeployed {
			current = h[i]
			break
		}
	}

	config, defaults := u.resolveValues(current, userValues)
	ro, err := u.cfg.liveRenderOptions(ctx, RenderOptions{
		ChartPath: chartPath, Version: u.ChartVersion, SubchartNotes: u.SubchartNotes, ReleaseName: name, Revision: latest.Revision + 1, IsUpgrade: true,
		UserValues: config, Defaults: defaults, Strict: u.Strict, NoEnv: u.NoEnv, Env: u.Env,
	}, u.DryRun)
	if err != nil {
		return nil, err
	}
	rendered, err := Render(ro)
	if err != nil {
		return nil, err
	}
	rel, err := u.cfg.releaseFrom(rendered, name, latest.Revision+1, config, u.ProjectDir)
	if err != nil {
		return nil, err
	}
	rel.Status = release.StatusPendingUpgrade
	rel.FirstDeployed = current.FirstDeployed
	rel.Labels = release.MergeLabels(current.Labels, u.Labels)
	if err := u.cfg.Compose.Config(ctx, project(rel)); err != nil {
		return nil, err
	}
	if u.DryRun {
		rel.Description = "Dry run complete"
		return rel, nil
	}

	if err := u.cfg.Store.Save(rel); err != nil {
		return nil, err
	}
	if err := u.cfg.runHooks(ctx, rel, hooks.PreUpgrade, u.Timeout, u.NoHooks); err != nil {
		return rel, u.failed(ctx, rel, current, err)
	}
	if err := u.cfg.up(ctx, rel, u.DeployOptions); err != nil {
		return rel, u.failed(ctx, rel, current, err)
	}
	if err := u.cfg.runHooks(ctx, rel, hooks.PostUpgrade, u.Timeout, u.NoHooks); err != nil {
		return rel, u.failed(ctx, rel, current, err)
	}
	desc := u.Description
	if desc == "" {
		desc = "Upgrade complete"
	}
	if err := u.cfg.markDeployed(rel, h, desc); err != nil {
		return rel, err
	}
	return rel, u.cfg.prune(name, u.HistoryMax)
}

// resolveValues applies Helm's upgrade value semantics. It returns the user
// config to record and, for --reuse-values, the defaults to render against
// (the previous computed values instead of the new chart's values.yaml).
func (u *Upgrade) resolveValues(current *release.Release, userValues map[string]interface{}) (config, defaults map[string]interface{}) {
	switch {
	case u.ResetValues:
		return userValues, nil
	case u.ReuseValues:
		return values.Merge(current.Config, userValues), current.Values
	case u.ResetThenReuseValues:
		return values.Merge(current.Config, userValues), nil
	case len(userValues) == 0 && len(current.Config) > 0:
		// Helm reuses the previous user values when none are given.
		return current.Config, nil
	}
	return userValues, nil
}

func (u *Upgrade) failed(ctx context.Context, rel, current *release.Release, cause error) error {
	err := u.cfg.markFailed(rel, cause)
	if !u.Atomic || current.Status != release.StatusDeployed {
		return err
	}
	// Like `helm upgrade --atomic`: roll back to the last deployed revision.
	rb := &Rollback{cfg: u.cfg, DeployOptions: u.DeployOptions, Version: current.Revision, HistoryMax: u.HistoryMax}
	if _, rerr := rb.Run(ctx, rel.Name); rerr != nil {
		return fmt.Errorf("upgrade of %q failed: %w; rollback to revision %d also failed: %v", rel.Name, cause, current.Revision, rerr)
	}
	return fmt.Errorf("upgrade of %q failed and was rolled back to revision %d due to atomic being set: %w", rel.Name, current.Revision, cause)
}
