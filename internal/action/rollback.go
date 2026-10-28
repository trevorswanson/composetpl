package action

import (
	"context"
	"fmt"

	"github.com/trevorswanson/composetpl/internal/hooks"
	"github.com/trevorswanson/composetpl/internal/release"
)

// Rollback redeploys an earlier revision as a new revision.
type Rollback struct {
	cfg *Configuration
	DeployOptions
	// Version is the revision to roll back to; 0 means the one before the latest.
	Version    int
	HistoryMax int
	DryRun     bool
}

// NewRollback returns a Rollback action with Helm's default history limit.
func NewRollback(cfg *Configuration) *Rollback { return &Rollback{cfg: cfg, HistoryMax: 10} }

// Run rolls release name back. Unlike install and upgrade it ignores a pending
// revision, so it can recover a release stuck mid-operation.
func (r *Rollback) Run(ctx context.Context, name string) (*release.Release, error) {
	h, err := r.cfg.Store.History(name)
	if err != nil {
		return nil, err
	}
	latest := h[len(h)-1]
	version := r.Version
	if version == 0 {
		version = latest.Revision - 1
	}
	if version < 1 {
		return nil, fmt.Errorf("release %q has no previous revision to roll back to", name)
	}
	target, err := r.cfg.Store.Get(name, version)
	if err != nil {
		return nil, err
	}

	now := r.cfg.now()
	rel := *target
	rel.Revision = latest.Revision + 1
	rel.Status = release.StatusPendingRollback
	rel.Description = fmt.Sprintf("Rollback to %d", version)
	rel.FirstDeployed = h[0].FirstDeployed
	rel.LastDeployed = now
	if r.DryRun {
		return &rel, nil
	}

	if err := r.cfg.Store.Save(&rel); err != nil {
		return nil, err
	}
	if err := r.cfg.runHooks(ctx, &rel, hooks.PreRollback, r.Timeout, r.NoHooks); err != nil {
		return &rel, r.cfg.markFailed(&rel, err)
	}
	if err := r.cfg.up(ctx, &rel, r.DeployOptions); err != nil {
		return &rel, r.cfg.markFailed(&rel, err)
	}
	if err := r.cfg.runHooks(ctx, &rel, hooks.PostRollback, r.Timeout, r.NoHooks); err != nil {
		return &rel, r.cfg.markFailed(&rel, err)
	}
	if err := r.cfg.markDeployed(&rel, h, rel.Description); err != nil {
		return &rel, err
	}
	return &rel, r.cfg.prune(name, r.HistoryMax)
}
