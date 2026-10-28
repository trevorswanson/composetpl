package action

import (
	"context"
	"errors"
	"fmt"

	"github.com/trevorswanson/composetpl/internal/diff"
	"github.com/trevorswanson/composetpl/internal/release"
	"github.com/trevorswanson/composetpl/internal/storage"
)

// deployedManifest is the manifest currently deployed for name, or "" if none is.
func (c *Configuration) deployedManifest(name string) (string, error) {
	r, err := storage.LastDeployed(c.Store, name)
	if errors.Is(err, storage.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return r.Manifest, nil
}

// DiffUpgrade shows what `upgrade` would change. u carries the upgrade flags; it
// runs as a dry run and treats a missing release as an install.
func DiffUpgrade(ctx context.Context, cfg *Configuration, u *Upgrade, name, chartPath string, userValues map[string]interface{}) ([]diff.Change, error) {
	cp := *u
	cp.cfg, cp.DryRun, cp.Install = cfg, true, true
	next, err := cp.Run(ctx, name, chartPath, userValues)
	if err != nil {
		return nil, err
	}
	current, err := cfg.deployedManifest(name)
	if err != nil {
		return nil, err
	}
	return diff.Manifests(current, next.Manifest)
}

// DiffRollback shows what rolling back to revision (0 = previous) would change.
func DiffRollback(cfg *Configuration, name string, revision int) ([]diff.Change, error) {
	rb := &Rollback{cfg: cfg, Version: revision, DryRun: true}
	target, err := rb.Run(context.Background(), name)
	if err != nil {
		return nil, err
	}
	current, err := cfg.deployedManifest(name)
	if err != nil {
		return nil, err
	}
	return diff.Manifests(current, target.Manifest)
}

// DiffRevisions compares two stored revisions; b = 0 means the latest.
func DiffRevisions(cfg *Configuration, name string, a, b int) ([]diff.Change, error) {
	ra, err := cfg.Store.Get(name, a)
	if err != nil {
		return nil, err
	}
	var rb *release.Release
	if b == 0 {
		if rb, err = storage.Latest(cfg.Store, name); err != nil {
			return nil, err
		}
	} else if rb, err = cfg.Store.Get(name, b); err != nil {
		return nil, err
	}
	if ra.Revision == rb.Revision {
		return nil, fmt.Errorf("both revisions are %d; give two different revisions", ra.Revision)
	}
	return diff.Manifests(ra.Manifest, rb.Manifest)
}
