// Package registry pushes and pulls packaged charts to and from OCI registries
// (GHCR, Docker Hub, Harbor, a self-hosted registry:2, ...). Credentials come from
// the Docker config and its credential helpers, so `docker login` is all that is needed.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/credentials"
	"oras.land/oras-go/v2/registry/remote/retry"

	"composetpl/internal/chart"
)

// Media types of a composetpl chart artifact. They are deliberately not Helm's,
// so `helm pull` won't mistake a compose chart for a Kubernetes one.
const (
	ArtifactType     = "application/vnd.composetpl.chart.v1"
	ConfigMediaType  = "application/vnd.composetpl.chart.config.v1+json"
	ContentMediaType = "application/vnd.composetpl.chart.content.v1.tar+gzip"
)

// Scheme prefixes chart references in registries.
const Scheme = "oci://"

// IsOCI reports whether ref is an oci:// reference.
func IsOCI(ref string) bool { return strings.HasPrefix(ref, Scheme) }

// Target is a repository charts are pushed to and pulled from.
type Target interface {
	oras.Target
	registry.TagLister
}

// Client talks to OCI registries.
type Client struct {
	// PlainHTTP uses http:// for every registry. Registries on localhost always do.
	PlainHTTP bool
	// CacheDir caches pulled archives by digest; "" disables the cache.
	CacheDir string
	// Log receives warnings.
	Log io.Writer
	// Repository opens a repository ("host/path/name"); nil uses remote registries.
	Repository func(repo string) (Target, error)
}

// New returns a client with the default cache directory and Docker credentials.
func New(log io.Writer) *Client {
	c := &Client{Log: log, PlainHTTP: os.Getenv("COMPOSETPL_PLAIN_HTTP") != ""}
	if dir, err := os.UserCacheDir(); err == nil {
		c.CacheDir = filepath.Join(dir, "composetpl", "charts")
	}
	return c
}

func (c *Client) warnf(format string, args ...interface{}) {
	if c.Log != nil {
		fmt.Fprintf(c.Log, "WARNING: "+format+"\n", args...)
	}
}

func (c *Client) open(repo string) (Target, error) {
	if c.Repository != nil {
		return c.Repository(repo)
	}
	r, err := remote.NewRepository(repo)
	if err != nil {
		return nil, fmt.Errorf("invalid OCI repository %q: %w", repo, err)
	}
	r.PlainHTTP = c.PlainHTTP || isLocalhost(r.Reference.Registry)
	cred := auth.EmptyCredential
	var credFunc auth.CredentialFunc
	if store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{}); err == nil {
		inner := credentials.Credential(store)
		credFunc = func(ctx context.Context, host string) (auth.Credential, error) {
			got, err := inner(ctx, host)
			if err != nil {
				// A broken credential helper shouldn't block anonymous pulls.
				c.warnf("reading Docker credentials for %s: %v; continuing anonymously", host, err)
				return cred, nil
			}
			return got, nil
		}
	} else {
		c.warnf("reading Docker config: %v; continuing anonymously", err)
	}
	r.Client = &auth.Client{Client: retry.DefaultClient, Cache: auth.NewCache(), Credential: credFunc}
	return r, nil
}

func isLocalhost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// Reference is a parsed oci:// chart reference.
type Reference struct {
	// Repository is "host/path/name".
	Repository string
	// Tag or Digest selects the version; both may be empty.
	Tag    string
	Digest string
}

// String formats the reference with its scheme.
func (r Reference) String() string {
	s := Scheme + r.Repository
	if r.Tag != "" {
		s += ":" + r.Tag
	}
	if r.Digest != "" {
		s += "@" + r.Digest
	}
	return s
}

// ParseReference parses "oci://host/path/name[:tag][@digest]".
func ParseReference(ref string) (Reference, error) {
	if !IsOCI(ref) {
		return Reference{}, fmt.Errorf("%q is not an oci:// reference", ref)
	}
	r, err := registry.ParseReference(strings.TrimPrefix(ref, Scheme))
	if err != nil {
		return Reference{}, fmt.Errorf("invalid reference %q: %w", ref, err)
	}
	out := Reference{Repository: r.Registry + "/" + r.Repository}
	if strings.HasPrefix(r.Reference, "sha256:") {
		out.Digest = r.Reference
	} else {
		out.Tag = r.Reference
	}
	return out, nil
}

// Push uploads a packaged chart to oci://<repo>/<chart name>:<version>, as Helm does,
// and returns the full reference and manifest digest.
func (c *Client) Push(ctx context.Context, archive []byte, repo string) (Reference, string, error) {
	ch, err := chart.LoadArchive(archive, chart.Options{})
	if err != nil {
		return Reference{}, "", err
	}
	md := ch.Metadata
	if md.Version == "" {
		return Reference{}, "", fmt.Errorf("chart %s has no version", md.Name)
	}
	if _, err := semver.StrictNewVersion(md.Version); err != nil {
		return Reference{}, "", fmt.Errorf("chart %s version %q is not valid semver: %w", md.Name, md.Version, err)
	}
	if !IsOCI(repo) {
		return Reference{}, "", fmt.Errorf("push destination %q must start with %s", repo, Scheme)
	}
	ref := Reference{Repository: strings.TrimSuffix(strings.TrimPrefix(repo, Scheme), "/") + "/" + md.Name, Tag: md.Version}
	target, err := c.open(ref.Repository)
	if err != nil {
		return ref, "", err
	}

	config, err := json.Marshal(md)
	if err != nil {
		return ref, "", err
	}
	configDesc, err := oras.PushBytes(ctx, target, ConfigMediaType, config)
	if err != nil {
		return ref, "", fmt.Errorf("pushing chart config: %w", err)
	}
	layerDesc, err := oras.PushBytes(ctx, target, ContentMediaType, archive)
	if err != nil {
		return ref, "", fmt.Errorf("pushing chart archive: %w", err)
	}
	layerDesc.Annotations = map[string]string{ocispec.AnnotationTitle: fmt.Sprintf("%s-%s.tgz", md.Name, md.Version)}
	manifestDesc, err := oras.PackManifest(ctx, target, oras.PackManifestVersion1_1, ArtifactType, oras.PackManifestOptions{
		Layers:           []ocispec.Descriptor{layerDesc},
		ConfigDescriptor: &configDesc,
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationTitle:       md.Name,
			ocispec.AnnotationVersion:     md.Version,
			ocispec.AnnotationDescription: md.Description,
			ocispec.AnnotationCreated:     "2000-01-01T00:00:00Z", // fixed, so pushes are reproducible
		},
	})
	if err != nil {
		return ref, "", fmt.Errorf("pushing manifest: %w", err)
	}
	if err := target.Tag(ctx, manifestDesc, md.Version); err != nil {
		return ref, "", fmt.Errorf("tagging %s: %w", md.Version, err)
	}
	return ref, manifestDesc.Digest.String(), nil
}

// Tags lists a repository's tags that are valid semver, highest first.
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	target, err := c.open(repo)
	if err != nil {
		return nil, err
	}
	all, err := registry.Tags(ctx, target)
	if err != nil {
		return nil, fmt.Errorf("listing tags of %s: %w", repo, err)
	}
	type tv struct {
		tag string
		v   *semver.Version
	}
	var vs []tv
	for _, t := range all {
		if v, err := semver.NewVersion(t); err == nil {
			vs = append(vs, tv{t, v})
		}
	}
	sort.Slice(vs, func(i, j int) bool { return vs[i].v.GreaterThan(vs[j].v) })
	out := make([]string, len(vs))
	for i, x := range vs {
		out[i] = x.tag
	}
	return out, nil
}

// Resolve picks the highest tag satisfying constraint ("" or "*" = latest).
func (c *Client) Resolve(ctx context.Context, repo, constraint string) (string, error) {
	tags, err := c.Tags(ctx, repo)
	if err != nil {
		return "", err
	}
	if constraint == "" || constraint == "*" || constraint == "latest" {
		// Like Helm, "latest" means the newest stable release.
		for _, t := range tags {
			if v, _ := semver.NewVersion(t); v.Prerelease() == "" {
				return t, nil
			}
		}
		return "", fmt.Errorf("%s%s has no stable versions (have %s); give a --version", Scheme, repo, strings.Join(tags, ", "))
	}
	cons, err := semver.NewConstraint(constraint)
	if err != nil {
		return "", fmt.Errorf("invalid version constraint %q: %w", constraint, err)
	}
	for _, t := range tags {
		if v, _ := semver.NewVersion(t); cons.Check(v) {
			return t, nil
		}
	}
	return "", fmt.Errorf("no version of %s%s matches %q (have %s)", Scheme, repo, constraint, strings.Join(tags, ", "))
}

// Pulled is a chart archive fetched from a registry.
type Pulled struct {
	Ref     Reference
	Digest  string // manifest digest
	Archive []byte
}

// Pull fetches a chart. version, when set, is a semver constraint resolved against
// the repository's tags; otherwise the reference's tag or digest is used (or the
// latest version when it has neither).
func (c *Client) Pull(ctx context.Context, ref, version string) (*Pulled, error) {
	r, err := ParseReference(ref)
	if err != nil {
		return nil, err
	}
	if version != "" && (r.Tag != "" || r.Digest != "") {
		return nil, fmt.Errorf("give the version either in %q or with --version, not both", ref)
	}
	if r.Tag == "" && r.Digest == "" {
		if r.Tag, err = c.Resolve(ctx, r.Repository, version); err != nil {
			return nil, err
		}
	}
	target, err := c.open(r.Repository)
	if err != nil {
		return nil, err
	}
	sel := r.Tag
	if r.Digest != "" {
		sel = r.Digest
	}
	mdesc, manifestBytes, err := oras.FetchBytes(ctx, target, sel, oras.DefaultFetchBytesOptions)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", r, err)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return nil, fmt.Errorf("%s: invalid manifest: %w", r, err)
	}
	var layer *ocispec.Descriptor
	for i := range m.Layers {
		if m.Layers[i].MediaType == ContentMediaType {
			layer = &m.Layers[i]
		}
	}
	if layer == nil {
		return nil, fmt.Errorf("%s is not a composetpl chart (no %s layer)", r, ContentMediaType)
	}
	out := &Pulled{Ref: r, Digest: mdesc.Digest.String()}
	if out.Archive = c.cached(layer.Digest); out.Archive != nil {
		return out, nil
	}
	if out.Archive, err = content.FetchAll(ctx, target, *layer); err != nil {
		return nil, fmt.Errorf("fetching chart archive of %s: %w", r, err)
	}
	c.store(layer.Digest, out.Archive)
	return out, nil
}

func (c *Client) cachePath(d digest.Digest) string {
	if c.CacheDir == "" || d.Validate() != nil {
		return ""
	}
	return filepath.Join(c.CacheDir, d.Algorithm().String(), d.Encoded()+".tgz")
}

func (c *Client) cached(d digest.Digest) []byte {
	p := c.cachePath(d)
	if p == "" {
		return nil
	}
	data, err := os.ReadFile(p)
	if err != nil || digest.FromBytes(data) != d {
		return nil
	}
	return data
}

func (c *Client) store(d digest.Digest, data []byte) {
	p := c.cachePath(d)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
		_ = os.WriteFile(p, data, 0o644)
	}
}

// ArchiveDigest returns the sha256 digest of an archive, as recorded in Chart.lock.
func ArchiveDigest(data []byte) string { return digest.FromBytes(data).String() }
