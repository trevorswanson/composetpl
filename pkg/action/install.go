package action

import (
	"context"
	"fmt"

	"github.com/trevorswanson/composetpl/pkg/release"
)

// Install deploys a chart as a new release.
type Install struct {
	cfg *Configuration
	DeployOptions
	Strict bool
	NoEnv  bool
	// Env backs the env and expandenv template functions; nil reads the process environment.
	Env         func(key string) (string, bool)
	Atomic      bool
	DryRun      bool
	Description string
	// ChartVersion is a semver constraint for oci:// charts.
	ChartVersion string
	// SubchartNotes also renders subcharts' NOTES.txt.
	SubchartNotes bool
	// ProjectDir overrides the compose project directory (default: the chart dir).
	ProjectDir string
	// Labels are recorded on the release (release.Release.Labels).
	Labels map[string]string
}

// NewInstall returns an Install action.
func NewInstall(cfg *Configuration) *Install { return &Install{cfg: cfg} }

// Run installs chartPath as release name with the given user values.
func (i *Install) Run(ctx context.Context, name, chartPath string, userValues map[string]interface{}) (*release.Release, error) {
	rel, err := i.Prepare(ctx, name, chartPath, userValues)
	if err != nil || i.DryRun {
		return rel, err
	}
	return i.apply().Run(ctx, name, rel.Revision)
}

// Prepare renders chartPath as the next revision of release name, validates it
// with `docker compose config`, and saves it as pending install, without
// running hooks or touching containers. Apply deploys it. With DryRun, the
// revision is returned without being saved.
func (i *Install) Prepare(ctx context.Context, name, chartPath string, userValues map[string]interface{}) (*release.Release, error) {
	if err := release.ValidateName(name); err != nil {
		return nil, err
	}
	if err := release.ValidateLabels(i.Labels); err != nil {
		return nil, err
	}
	h, err := i.cfg.history(name)
	if err != nil {
		return nil, err
	}
	revision := 1
	if len(h) > 0 {
		latest := h[len(h)-1]
		if latest.Status != release.StatusUninstalled {
			return nil, fmt.Errorf("cannot re-use a name that is still in use: release %q exists (status %s)", name, latest.Status)
		}
		revision = latest.Revision + 1
	}

	ro, err := i.cfg.liveRenderOptions(ctx, RenderOptions{
		ChartPath: chartPath, Version: i.ChartVersion, SubchartNotes: i.SubchartNotes, ReleaseName: name, Revision: revision, UserValues: userValues,
		Strict: i.Strict, NoEnv: i.NoEnv, Env: i.Env,
	}, i.DryRun)
	if err != nil {
		return nil, err
	}
	rendered, err := Render(ro)
	if err != nil {
		return nil, err
	}
	rel, err := i.cfg.releaseFrom(rendered, name, revision, userValues, i.ProjectDir)
	if err != nil {
		return nil, err
	}
	rel.Status = release.StatusPendingInstall
	rel.Labels = release.MergeLabels(nil, i.Labels)
	if err := i.cfg.Compose.Config(ctx, project(rel)); err != nil {
		return nil, err
	}
	if i.DryRun {
		rel.Description = "Dry run complete"
		return rel, nil
	}

	if err := i.cfg.Store.Save(rel); err != nil {
		return nil, err
	}
	return rel, nil
}

// apply is the Apply that deploys what Prepare saved.
func (i *Install) apply() *Apply {
	return &Apply{cfg: i.cfg, DeployOptions: i.DeployOptions, Atomic: i.Atomic, Description: i.Description}
}
