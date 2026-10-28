package action

import (
	"context"
	"fmt"
	"regexp"

	"github.com/trevorswanson/composetpl/internal/compose"
	"github.com/trevorswanson/composetpl/internal/release"
)

// ListItem is a release's latest revision plus its live compose status.
type ListItem struct {
	*release.Release
	// Live is `docker compose ls` status such as "running(3)", or "" if not running.
	Live string `json:"live,omitempty"`
}

// List lists releases.
type List struct {
	cfg *Configuration
	// All shows releases in every status.
	All bool
	// Statuses limits output to these statuses; empty means deployed and failed, like Helm.
	Statuses []release.Status
	// Filter is a regular expression matched against release names.
	Filter string
}

// NewList returns a List action.
func NewList(cfg *Configuration) *List { return &List{cfg: cfg} }

// Run returns the matching releases sorted by name.
func (l *List) Run(ctx context.Context) ([]ListItem, error) {
	var filter *regexp.Regexp
	if l.Filter != "" {
		var err error
		if filter, err = regexp.Compile(l.Filter); err != nil {
			return nil, fmt.Errorf("invalid --filter: %w", err)
		}
	}
	want := map[release.Status]bool{}
	for _, s := range l.Statuses {
		want[s] = true
	}
	if len(want) == 0 {
		want[release.StatusDeployed] = true
		want[release.StatusFailed] = true
	}

	rels, err := l.cfg.Store.List()
	if err != nil {
		return nil, err
	}
	live := map[string]string{}
	if projects, err := l.cfg.Compose.Ls(ctx); err == nil {
		for _, p := range projects {
			live[p.Name] = p.Status
		}
	} else {
		l.cfg.warnf("could not read live status: %v", err)
	}

	var out []ListItem
	for _, r := range rels {
		if !l.All && !want[r.Status] {
			continue
		}
		if filter != nil && !filter.MatchString(r.Name) {
			continue
		}
		out = append(out, ListItem{Release: r, Live: live[r.Name]})
	}
	return out, nil
}

// StatusResult is a revision plus the release's live containers.
type StatusResult struct {
	*release.Release
	Containers []compose.Container `json:"containers"`
}

// Status reports a release's state.
type Status struct {
	cfg *Configuration
	// Revision selects a revision; 0 means the latest.
	Revision int
}

// NewStatus returns a Status action.
func NewStatus(cfg *Configuration) *Status { return &Status{cfg: cfg} }

// Run returns the release revision and, for the latest revision, its live containers.
func (s *Status) Run(ctx context.Context, name string) (*StatusResult, error) {
	r, err := Get(s.cfg, name, s.Revision)
	if err != nil {
		return nil, err
	}
	res := &StatusResult{Release: r}
	if r.Status != release.StatusUninstalled {
		if res.Containers, err = s.cfg.Compose.PS(ctx, name); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// Get returns one revision of a release; revision 0 means the latest.
func Get(cfg *Configuration, name string, revision int) (*release.Release, error) {
	if revision > 0 {
		return cfg.Store.Get(name, revision)
	}
	h, err := cfg.Store.History(name)
	if err != nil {
		return nil, err
	}
	return h[len(h)-1], nil
}

// History returns up to max of a release's newest revisions, oldest first (max <= 0: all).
func History(cfg *Configuration, name string, max int) ([]*release.Release, error) {
	h, err := cfg.Store.History(name)
	if err != nil {
		return nil, err
	}
	if max > 0 && len(h) > max {
		h = h[len(h)-max:]
	}
	return h, nil
}
