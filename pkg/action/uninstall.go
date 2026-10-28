package action

import (
	"context"
	"fmt"
	"time"

	"github.com/trevorswanson/composetpl/pkg/compose"
	"github.com/trevorswanson/composetpl/pkg/hooks"
	"github.com/trevorswanson/composetpl/pkg/release"
)

// Uninstall stops and removes a release's containers and networks.
type Uninstall struct {
	cfg *Configuration
	// KeepHistory marks the release uninstalled instead of deleting its records.
	KeepHistory  bool
	Volumes      bool
	RemoveImages string
	DryRun       bool
	NoHooks      bool
	// Timeout bounds each delete hook.
	Timeout time.Duration
}

// NewUninstall returns an Uninstall action.
func NewUninstall(cfg *Configuration) *Uninstall { return &Uninstall{cfg: cfg} }

// Run uninstalls release name.
func (u *Uninstall) Run(ctx context.Context, name string) (*release.Release, error) {
	h, err := u.cfg.Store.History(name)
	if err != nil {
		return nil, err
	}
	latest := h[len(h)-1]
	if latest.Status == release.StatusUninstalled {
		if u.KeepHistory {
			return nil, fmt.Errorf("release %q is already uninstalled", name)
		}
		if u.DryRun {
			return latest, nil
		}
		// Uninstalling a release kept with --keep-history purges its records.
		return latest, u.cfg.Store.Delete(h...)
	}

	// Prefer the last deployed manifest: a failed upgrade's manifest may never have run.
	// down --remove-orphans removes every container of the project either way.
	target := latest
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Status == release.StatusDeployed {
			target = h[i]
			break
		}
	}
	if u.DryRun {
		return target, nil
	}
	if err := u.cfg.runHooks(ctx, target, hooks.PreDelete, u.Timeout, u.NoHooks); err != nil {
		return nil, err
	}
	down := compose.DownOptions{Volumes: u.Volumes, RemoveImages: u.RemoveImages}
	if err := u.cfg.Compose.Down(ctx, project(target), down); err != nil {
		return nil, err
	}
	if !u.NoHooks && len(hooks.For(target, hooks.PostDelete)) > 0 {
		if err := u.cfg.runHooks(ctx, target, hooks.PostDelete, u.Timeout, false); err != nil {
			return nil, err
		}
		// Post-delete hooks recreate the project network; clean up after them.
		if err := u.cfg.Compose.Down(ctx, project(target), compose.DownOptions{}); err != nil {
			return nil, err
		}
	}
	if !u.KeepHistory {
		return latest, u.cfg.Store.Delete(h...)
	}
	for _, r := range h {
		if r.Status == release.StatusDeployed {
			r.Status = release.StatusSuperseded
		}
	}
	latest.Status = release.StatusUninstalled
	latest.Description = "Uninstallation complete"
	return latest, u.cfg.Store.Save(h...)
}
