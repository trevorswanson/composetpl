package action

import (
	"context"
	"fmt"
	"sort"

	"github.com/trevorswanson/composetpl/pkg/release"
	"github.com/trevorswanson/composetpl/pkg/storage"
)

// Drift reasons.
const (
	// DriftMissing: the service has no container.
	DriftMissing = "missing"
	// DriftChanged: a container was created from a different configuration.
	DriftChanged = "changed"
	// DriftStopped: a container exists but is not running.
	DriftStopped = "stopped"
	// DriftOrphaned: a container belongs to a service the manifest doesn't have.
	DriftOrphaned = "orphaned"
)

// ServiceDrift is how one service's containers differ from the deployed manifest.
type ServiceDrift struct {
	Service string `json:"service"`
	Reason  string `json:"reason"`
	// Containers are the affected containers (none when Reason is DriftMissing).
	Containers []string `json:"containers,omitempty"`
}

// Repair re-applies the last deployed revision's manifest, recreating containers
// that were removed, stopped or changed outside composetpl. Unlike rollback it
// records no revision and runs no hooks, so it is safe to run repeatedly (for
// example from a GitOps controller's self-heal loop).
type Repair struct {
	cfg *Configuration
	// DeployOptions.NoHooks is ignored: repair never runs hooks.
	DeployOptions
	DryRun bool
}

// NewRepair returns a Repair action.
func NewRepair(cfg *Configuration) *Repair { return &Repair{cfg: cfg} }

// deployed returns the revision repair works on: the last deployed one, even if a
// later revision failed. A pending operation is an error, as for upgrade.
func (r *Repair) deployed(name string) (*release.Release, error) {
	h, err := r.cfg.Store.History(name)
	if err != nil {
		return nil, err
	}
	if latest := h[len(h)-1]; latest.Status.IsPending() {
		return nil, errPending(latest)
	}
	rel, err := storage.LastDeployed(r.cfg.Store, name)
	if err != nil {
		return nil, fmt.Errorf("nothing to repair: %w", err)
	}
	return rel, nil
}

// Drift compares the live containers of release name with its deployed manifest,
// using the config hash compose records on every container it creates. Services
// are sorted by name; an empty result means no drift.
func (r *Repair) Drift(ctx context.Context, name string) ([]ServiceDrift, error) {
	rel, err := r.deployed(name)
	if err != nil {
		return nil, err
	}
	want, err := r.cfg.Compose.ConfigHash(ctx, project(rel))
	if err != nil {
		return nil, err
	}
	containers, err := r.cfg.Compose.PS(ctx, name)
	if err != nil {
		return nil, err
	}
	hookNames := map[string]bool{}
	for _, h := range rel.Hooks {
		hookNames[h.Name] = true
	}

	type key struct{ service, reason string }
	found := map[key][]string{}
	seen := map[string]bool{}
	for _, c := range containers {
		if hookNames[c.Service] {
			continue
		}
		hash, ok := want[c.Service]
		switch {
		case !ok:
			found[key{c.Service, DriftOrphaned}] = append(found[key{c.Service, DriftOrphaned}], c.Name)
			continue
		case c.ConfigHash() != hash:
			found[key{c.Service, DriftChanged}] = append(found[key{c.Service, DriftChanged}], c.Name)
		case c.State != "running":
			found[key{c.Service, DriftStopped}] = append(found[key{c.Service, DriftStopped}], c.Name)
		}
		seen[c.Service] = true
	}
	for service := range want {
		if !seen[service] {
			found[key{service, DriftMissing}] = nil
		}
	}

	out := make([]ServiceDrift, 0, len(found))
	for k, names := range found {
		sort.Strings(names)
		out = append(out, ServiceDrift{Service: k.service, Reason: k.reason, Containers: names})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Reason < out[j].Reason
	})
	return out, nil
}

// Run brings release name's containers back to its deployed manifest with
// `docker compose up`, which recreates only what differs. It returns the revision
// it applied.
func (r *Repair) Run(ctx context.Context, name string) (*release.Release, error) {
	rel, err := r.deployed(name)
	if err != nil {
		return nil, err
	}
	if r.DryRun {
		return rel, nil
	}
	if err := r.cfg.up(ctx, rel, r.DeployOptions); err != nil {
		return rel, fmt.Errorf("repairing release %q (revision %d): %w", name, rel.Revision, err)
	}
	return rel, nil
}
