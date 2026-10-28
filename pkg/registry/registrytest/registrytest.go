// Package registrytest provides in-memory OCI repositories for tests.
package registrytest

import (
	"context"
	"sort"
	"sync"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/trevorswanson/composetpl/pkg/registry"
)

// Registry holds in-memory repositories keyed by "host/path/name".
type Registry struct {
	mu    sync.Mutex
	Repos map[string]*Repo
}

// Repo is an in-memory repository that can list its tags.
type Repo struct {
	*memory.Store
	mu        sync.Mutex
	tags      map[string]bool
	manifests map[string]ocispec.Descriptor
}

// Tag records the tag as well as storing it.
func (r *Repo) Tag(ctx context.Context, desc ocispec.Descriptor, ref string) error {
	r.mu.Lock()
	r.tags[ref] = true
	r.manifests[desc.Digest.String()] = desc
	r.mu.Unlock()
	return r.Store.Tag(ctx, desc, ref)
}

// Resolve also accepts manifest digests, as remote registries do.
func (r *Repo) Resolve(ctx context.Context, ref string) (ocispec.Descriptor, error) {
	r.mu.Lock()
	desc, ok := r.manifests[ref]
	r.mu.Unlock()
	if ok {
		return desc, nil
	}
	return r.Store.Resolve(ctx, ref)
}

// Tags lists every tag in one page.
func (r *Repo) Tags(_ context.Context, _ string, fn func([]string) error) error {
	r.mu.Lock()
	var out []string
	for t := range r.tags {
		out = append(out, t)
	}
	r.mu.Unlock()
	sort.Strings(out)
	return fn(out)
}

// New returns an empty registry.
func New() *Registry { return &Registry{Repos: map[string]*Repo{}} }

// Client returns a registry client backed by this registry, with caching off.
func (r *Registry) Client() *registry.Client {
	return &registry.Client{Repository: func(repo string) (registry.Target, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.Repos[repo] == nil {
			r.Repos[repo] = &Repo{Store: memory.New(), tags: map[string]bool{}, manifests: map[string]ocispec.Descriptor{}}
		}
		return r.Repos[repo], nil
	}}
}
