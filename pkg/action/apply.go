package action

import (
	"context"
	"fmt"

	"github.com/trevorswanson/composetpl/pkg/compose"
	"github.com/trevorswanson/composetpl/pkg/hooks"
	"github.com/trevorswanson/composetpl/pkg/release"
)

// Apply deploys a revision that Install.Prepare or Upgrade.Prepare saved as
// pending. Together they split Install.Run and Upgrade.Run in two, so one
// process can render a release and another, such as a helper container that
// outlives the one being recreated, can deploy it.
type Apply struct {
	cfg *Configuration
	DeployOptions
	// Atomic undoes a failed apply: an install is torn down and purged, and an
	// upgrade is rolled back to the last deployed revision.
	Atomic bool
	// HistoryMax is how many revisions an upgrade keeps.
	HistoryMax  int
	Description string
}

// NewApply returns an Apply action with Helm's default history limit.
func NewApply(cfg *Configuration) *Apply { return &Apply{cfg: cfg, HistoryMax: 10} }

// Run deploys revision of release name, which must be its latest revision and
// pending install or upgrade: it runs the pre hooks, compose up and the post
// hooks, then marks the revision deployed, or failed.
func (a *Apply) Run(ctx context.Context, name string, revision int) (*release.Release, error) {
	h, err := a.cfg.history(name)
	if err != nil {
		return nil, err
	}
	if len(h) == 0 {
		return nil, fmt.Errorf("release %q not found", name)
	}
	rel := h[len(h)-1]
	if rel.Revision != revision {
		return nil, fmt.Errorf("release %q revision %d is not the latest (%d)", name, revision, rel.Revision)
	}
	if rel.Status != release.StatusPendingInstall && rel.Status != release.StatusPendingUpgrade {
		return nil, fmt.Errorf("release %q revision %d is %s, not pending install or upgrade", name, revision, rel.Status)
	}
	return a.apply(ctx, rel, h)
}

// apply deploys rel, the latest revision in h.
func (a *Apply) apply(ctx context.Context, rel *release.Release, h []*release.Release) (*release.Release, error) {
	pre, post, desc := hooks.PreUpgrade, hooks.PostUpgrade, "Upgrade complete"
	if rel.Status == release.StatusPendingInstall {
		pre, post, desc = hooks.PreInstall, hooks.PostInstall, "Install complete"
	}
	if a.Description != "" {
		desc = a.Description
	}
	if err := a.cfg.runHooks(ctx, rel, pre, a.Timeout, a.NoHooks); err != nil {
		return rel, a.failed(ctx, rel, h, err)
	}
	if err := a.cfg.up(ctx, rel, a.DeployOptions); err != nil {
		return rel, a.failed(ctx, rel, h, err)
	}
	if err := a.cfg.runHooks(ctx, rel, post, a.Timeout, a.NoHooks); err != nil {
		return rel, a.failed(ctx, rel, h, err)
	}
	install := rel.Status == release.StatusPendingInstall
	if err := a.cfg.markDeployed(rel, h, desc); err != nil {
		return rel, err
	}
	if install {
		return rel, nil
	}
	return rel, a.cfg.prune(rel.Name, a.HistoryMax)
}

func (a *Apply) failed(ctx context.Context, rel *release.Release, h []*release.Release, cause error) error {
	install := rel.Status == release.StatusPendingInstall
	err := a.cfg.markFailed(rel, cause)
	if !a.Atomic {
		return err
	}
	if install {
		// Like `helm install --atomic`: tear everything down and purge the release.
		if derr := a.cfg.Compose.Down(ctx, project(rel), compose.DownOptions{}); derr != nil {
			return fmt.Errorf("release %q failed: %w; atomic cleanup also failed: %v", rel.Name, cause, derr)
		}
		if h, herr := a.cfg.history(rel.Name); herr == nil {
			if derr := a.cfg.Store.Delete(h...); derr != nil {
				return fmt.Errorf("release %q failed: %w; deleting its history also failed: %v", rel.Name, cause, derr)
			}
		}
		return fmt.Errorf("release %q failed, and has been uninstalled due to atomic being set: %w", rel.Name, cause)
	}
	// Like `helm upgrade --atomic`: roll back to the last deployed revision.
	var current *release.Release
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Revision != rel.Revision && h[i].Status == release.StatusDeployed {
			current = h[i]
			break
		}
	}
	if current == nil {
		return err
	}
	rb := &Rollback{cfg: a.cfg, DeployOptions: a.DeployOptions, Version: current.Revision, HistoryMax: a.HistoryMax}
	if _, rerr := rb.Run(ctx, rel.Name); rerr != nil {
		return fmt.Errorf("upgrade of %q failed: %w; rollback to revision %d also failed: %v", rel.Name, cause, current.Revision, rerr)
	}
	return fmt.Errorf("upgrade of %q failed and was rolled back to revision %d due to atomic being set: %w", rel.Name, current.Revision, cause)
}

// Abandon marks a pending revision failed without touching containers, for
// when the process applying it died. reason is recorded in its description.
func Abandon(cfg *Configuration, name string, revision int, reason string) error {
	rel, err := cfg.Store.Get(name, revision)
	if err != nil {
		return err
	}
	if !rel.Status.IsPending() {
		return fmt.Errorf("release %q revision %d is %s, not pending", name, revision, rel.Status)
	}
	rel.Status = release.StatusFailed
	rel.Description = fmt.Sprintf("Release %q failed: %s", name, firstLine(reason))
	return cfg.Store.Save(rel)
}
