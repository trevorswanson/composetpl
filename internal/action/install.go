package action

import (
	"context"
	"fmt"

	"composetpl/internal/compose"
	"composetpl/internal/release"
)

// Install deploys a chart as a new release.
type Install struct {
	cfg *Configuration
	DeployOptions
	Strict      bool
	NoEnv       bool
	Atomic      bool
	DryRun      bool
	Description string
	// ProjectDir overrides the compose project directory (default: the chart dir).
	ProjectDir string
}

// NewInstall returns an Install action.
func NewInstall(cfg *Configuration) *Install { return &Install{cfg: cfg} }

// Run installs chartPath as release name with the given user values.
func (i *Install) Run(ctx context.Context, name, chartPath string, userValues map[string]interface{}) (*release.Release, error) {
	if err := release.ValidateName(name); err != nil {
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

	rendered, err := Render(RenderOptions{
		ChartPath: chartPath, ReleaseName: name, Revision: revision, UserValues: userValues,
		Strict: i.Strict, NoEnv: i.NoEnv,
	})
	if err != nil {
		return nil, err
	}
	rel, err := i.cfg.releaseFrom(rendered, name, revision, userValues, i.ProjectDir)
	if err != nil {
		return nil, err
	}
	rel.Status = release.StatusPendingInstall
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
	if err := i.cfg.up(ctx, rel, i.DeployOptions); err != nil {
		return rel, i.failed(ctx, rel, err)
	}
	desc := i.Description
	if desc == "" {
		desc = "Install complete"
	}
	return rel, i.cfg.markDeployed(rel, h, desc)
}

func (i *Install) failed(ctx context.Context, rel *release.Release, cause error) error {
	err := i.cfg.markFailed(rel, cause)
	if !i.Atomic {
		return err
	}
	// Like `helm install --atomic`: tear everything down and purge the release.
	if derr := i.cfg.Compose.Down(ctx, project(rel), compose.DownOptions{}); derr != nil {
		return fmt.Errorf("release %q failed: %w; atomic cleanup also failed: %v", rel.Name, cause, derr)
	}
	if h, herr := i.cfg.history(rel.Name); herr == nil {
		if derr := i.cfg.Store.Delete(h...); derr != nil {
			return fmt.Errorf("release %q failed: %w; deleting its history also failed: %v", rel.Name, cause, derr)
		}
	}
	return fmt.Errorf("release %q failed, and has been uninstalled due to atomic being set: %w", rel.Name, cause)
}
