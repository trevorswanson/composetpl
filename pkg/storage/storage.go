// Package storage persists release records. Two drivers exist: "volume" keeps
// records in a Docker volume on the target engine (the default, like Helm keeping
// releases in the cluster) and "local" keeps them in a directory on this machine.
package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/trevorswanson/composetpl/pkg/release"
)

// ErrNotFound is returned when a release or revision does not exist.
var ErrNotFound = errors.New("release: not found")

// Driver stores release revisions.
type Driver interface {
	// Name is the driver name, e.g. "local".
	Name() string
	// Get returns one revision.
	Get(name string, revision int) (*release.Release, error)
	// History returns every stored revision of a release, oldest first.
	History(name string) ([]*release.Release, error)
	// List returns the latest revision of every release, sorted by name.
	List() ([]*release.Release, error)
	// Save creates or replaces revisions.
	Save(rels ...*release.Release) error
	// Delete removes revisions.
	Delete(rels ...*release.Release) error
}

// Latest returns the newest revision of a release.
func Latest(d Driver, name string) (*release.Release, error) {
	h, err := d.History(name)
	if err != nil {
		return nil, err
	}
	return h[len(h)-1], nil
}

// LastDeployed returns the newest revision with status deployed, or ErrNotFound.
func LastDeployed(d Driver, name string) (*release.Release, error) {
	h, err := d.History(name)
	if err != nil {
		return nil, err
	}
	for i := len(h) - 1; i >= 0; i-- {
		if h[i].Status == release.StatusDeployed {
			return h[i], nil
		}
	}
	return nil, fmt.Errorf("%q has no deployed revisions: %w", name, ErrNotFound)
}

// recordPath is where a revision lives relative to the driver root: <name>/v<rev>.json.
func recordPath(name string, revision int) string {
	return path.Join(name, "v"+strconv.Itoa(revision)+".json")
}

// parseRecordPath reverses recordPath.
func parseRecordPath(p string) (name string, revision int, ok bool) {
	dir, file := path.Split(strings.TrimPrefix(path.Clean(p), "./"))
	name = strings.TrimSuffix(dir, "/")
	if name == "" || strings.Contains(name, "/") || !strings.HasPrefix(file, "v") || !strings.HasSuffix(file, ".json") {
		return "", 0, false
	}
	rev, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(file, "v"), ".json"))
	if err != nil {
		return "", 0, false
	}
	return name, rev, true
}

func encode(r *release.Release) ([]byte, error) {
	if r.APIVersion == "" {
		r.APIVersion = release.APIVersion
	}
	if err := release.ValidateName(r.Name); err != nil {
		return nil, err
	}
	return json.MarshalIndent(r, "", "  ")
}

func decode(data []byte) (*release.Release, error) {
	r := &release.Release{}
	if err := json.Unmarshal(data, r); err != nil {
		return nil, err
	}
	if r.APIVersion != release.APIVersion {
		return nil, fmt.Errorf("unsupported release record version %q", r.APIVersion)
	}
	return r, nil
}

// records is an in-memory index of every stored revision, shared by drivers.
type records map[string]map[int]*release.Release

func (rs records) history(name string) ([]*release.Release, error) {
	revs := rs[name]
	if len(revs) == 0 {
		return nil, fmt.Errorf("release %q: %w", name, ErrNotFound)
	}
	out := make([]*release.Release, 0, len(revs))
	for _, r := range revs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Revision < out[j].Revision })
	return out, nil
}

func (rs records) get(name string, revision int) (*release.Release, error) {
	r, ok := rs[name][revision]
	if !ok {
		return nil, fmt.Errorf("release %q revision %d: %w", name, revision, ErrNotFound)
	}
	return r, nil
}

func (rs records) list() []*release.Release {
	var out []*release.Release
	for name := range rs {
		if h, err := rs.history(name); err == nil {
			out = append(out, h[len(h)-1])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (rs records) put(r *release.Release) {
	if rs[r.Name] == nil {
		rs[r.Name] = map[int]*release.Release{}
	}
	rs[r.Name][r.Revision] = r
}

func (rs records) remove(r *release.Release) {
	delete(rs[r.Name], r.Revision)
	if len(rs[r.Name]) == 0 {
		delete(rs, r.Name)
	}
}
