package action

import (
	"context"
	"fmt"

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
	rel, err := u.Prepare(ctx, name, chartPath, userValues)
	if err != nil || u.DryRun {
		return rel, err
	}
	a := &Apply{cfg: u.cfg, DeployOptions: u.DeployOptions, Atomic: u.Atomic, HistoryMax: u.HistoryMax, Description: u.Description}
	return a.Run(ctx, name, rel.Revision)
}

// Prepare renders chartPath as the next revision of release name, validates it
// with `docker compose config`, and saves it as pending upgrade (or pending
// install, with Install and no release), without running hooks or touching
// containers. Apply deploys it. With DryRun, the revision is returned without
// being saved.
func (u *Upgrade) Prepare(ctx context.Context, name, chartPath string, userValues map[string]interface{}) (*release.Release, error) {
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
		return in.Prepare(ctx, name, chartPath, userValues)
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
	return rel, nil
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
