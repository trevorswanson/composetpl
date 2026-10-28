package action

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/trevorswanson/composetpl/internal/chart"
	"github.com/trevorswanson/composetpl/internal/registry"
)

// LoadChart loads a chart directory, .tgz, or oci:// reference. version is a
// semver constraint for oci:// references without a tag. It returns the chart
// and a reference that pins the version loaded.
func LoadChart(ctx context.Context, reg *registry.Client, ref, version string) (*chart.Chart, string, error) {
	if !registry.IsOCI(ref) {
		if version != "" {
			return nil, "", fmt.Errorf("--version only applies to oci:// charts")
		}
		c, err := chart.Load(ref)
		return c, ref, err
	}
	if reg == nil {
		reg = registry.New(nil)
	}
	p, err := reg.Pull(ctx, ref, version)
	if err != nil {
		return nil, "", err
	}
	c, err := chart.LoadArchive(p.Archive, chart.Options{})
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", p.Ref, err)
	}
	pinned := p.Ref
	pinned.Digest = p.Digest
	return c, pinned.String(), nil
}

// DependencyStatus is one row of `dependency list`.
type DependencyStatus struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	Status     string `json:"status"`
}

// Dependencies manages a chart directory's charts/ and Chart.lock.
type Dependencies struct {
	Registry *registry.Client
	// Log receives one line per dependency saved.
	Log io.Writer
}

func (d *Dependencies) logf(format string, args ...interface{}) {
	if d.Log != nil {
		fmt.Fprintf(d.Log, format+"\n", args...)
	}
}

// List reports each declared dependency and whether it is available.
func (d *Dependencies) List(dir string) ([]DependencyStatus, error) {
	md, err := chart.ReadMetadata(dir)
	if err != nil {
		return nil, err
	}
	lock, err := chart.ReadLock(dir)
	if err != nil {
		return nil, err
	}
	var out []DependencyStatus
	for _, dep := range md.Dependencies {
		st := DependencyStatus{Name: dep.Name, Version: dep.Version, Repository: dep.Repository}
		locked := lockedVersion(lock, dep.Name)
		vendored := vendoredArchives(dir, dep.Name)
		switch {
		case isDir(filepath.Join(dir, "charts", dep.Name)):
			st.Status = "unpacked"
		case len(vendored) > 0 && locked != "" && !contains(vendored, archiveName(dep.Name, locked)):
			st.Status = "wrong version"
		case len(vendored) > 0:
			st.Status = "ok"
		case strings.HasPrefix(dep.Repository, "file://"):
			st.Status = "ok (file)"
		default:
			st.Status = "missing"
		}
		out = append(out, st)
	}
	if lock != nil && lock.Digest != chart.DependenciesDigest(md) {
		d.logf("WARNING: %s is out of date with Chart.yaml; run `composetpl dependency update`", chart.LockFile)
	}
	return out, nil
}

// Update resolves every dependency to its newest matching version, vendors it as
// charts/<name>-<version>.tgz and writes Chart.lock.
func (d *Dependencies) Update(ctx context.Context, dir string) (*chart.Lock, error) {
	md, err := chart.ReadMetadata(dir)
	if err != nil {
		return nil, err
	}
	lock := &chart.Lock{Digest: chart.DependenciesDigest(md), Generated: time.Now().UTC().Truncate(time.Second)}
	for _, dep := range md.Dependencies {
		ld, archive, err := d.fetch(ctx, dir, dep, "")
		if err != nil {
			return nil, fmt.Errorf("dependency %q: %w", dep.Name, err)
		}
		if err := d.vendor(dir, ld, archive); err != nil {
			return nil, err
		}
		lock.Dependencies = append(lock.Dependencies, ld)
	}
	return lock, lock.Write(dir)
}

// Build vendors exactly the versions in Chart.lock, verifying their digests.
func (d *Dependencies) Build(ctx context.Context, dir string) (*chart.Lock, error) {
	md, err := chart.ReadMetadata(dir)
	if err != nil {
		return nil, err
	}
	lock, err := chart.ReadLock(dir)
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return nil, fmt.Errorf("%s/%s not found; run `composetpl dependency update` first", dir, chart.LockFile)
	}
	if lock.Digest != chart.DependenciesDigest(md) {
		return nil, fmt.Errorf("%s is out of date with Chart.yaml; run `composetpl dependency update`", chart.LockFile)
	}
	for _, ld := range lock.Dependencies {
		p := filepath.Join(dir, "charts", archiveName(ld.Name, ld.Version))
		if data, err := os.ReadFile(p); err == nil && registry.ArchiveDigest(data) == ld.Digest {
			d.logf("%s %s is up to date", ld.Name, ld.Version)
			continue
		}
		dep := &chart.Dependency{Name: ld.Name, Version: ld.Version, Repository: ld.Repository}
		got, archive, err := d.fetch(ctx, dir, dep, ld.Version)
		if err != nil {
			return nil, fmt.Errorf("dependency %q: %w", ld.Name, err)
		}
		if strings.HasPrefix(ld.Repository, "oci://") && got.Digest != ld.Digest {
			return nil, fmt.Errorf("dependency %q %s: digest %s does not match %s in %s",
				ld.Name, ld.Version, got.Digest, ld.Digest, chart.LockFile)
		}
		if err := d.vendor(dir, got, archive); err != nil {
			return nil, err
		}
	}
	return lock, nil
}

// fetch produces the archive of one dependency. exact, when set, is the version
// to fetch instead of resolving the dependency's constraint.
func (d *Dependencies) fetch(ctx context.Context, dir string, dep *chart.Dependency, exact string) (*chart.LockedDependency, []byte, error) {
	switch {
	case strings.HasPrefix(dep.Repository, "file://"):
		p := strings.TrimPrefix(dep.Repository, "file://")
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		c, err := chart.Load(p)
		if err != nil {
			return nil, nil, err
		}
		if c.Metadata.Version == "" {
			return nil, nil, fmt.Errorf("%s/Chart.yaml: version is required to vendor a dependency", p)
		}
		archive, err := chart.Archive(c)
		if err != nil {
			return nil, nil, err
		}
		return &chart.LockedDependency{Name: dep.Name, Version: c.Metadata.Version, Repository: dep.Repository,
			Digest: registry.ArchiveDigest(archive)}, archive, nil
	case registry.IsOCI(dep.Repository):
		reg := d.Registry
		if reg == nil {
			reg = registry.New(d.Log)
		}
		repo := strings.TrimSuffix(strings.TrimPrefix(dep.Repository, registry.Scheme), "/") + "/" + dep.Name
		tag := exact
		if tag == "" {
			var err error
			if tag, err = reg.Resolve(ctx, repo, dep.Version); err != nil {
				return nil, nil, err
			}
		}
		p, err := reg.Pull(ctx, registry.Scheme+repo+":"+tag, "")
		if err != nil {
			return nil, nil, err
		}
		return &chart.LockedDependency{Name: dep.Name, Version: tag, Repository: dep.Repository,
			Digest: registry.ArchiveDigest(p.Archive)}, p.Archive, nil
	}
	return nil, nil, fmt.Errorf("unsupported repository %q (want file:// or oci://)", dep.Repository)
}

// vendor writes charts/<name>-<version>.tgz, removing other versions of the dependency.
func (d *Dependencies) vendor(dir string, ld *chart.LockedDependency, archive []byte) error {
	chartsDir := filepath.Join(dir, "charts")
	if err := os.MkdirAll(chartsDir, 0o755); err != nil {
		return err
	}
	for _, old := range vendoredArchives(dir, ld.Name) {
		if err := os.Remove(filepath.Join(chartsDir, old)); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(chartsDir, archiveName(ld.Name, ld.Version)), archive, 0o644); err != nil {
		return err
	}
	d.logf("Saving %s %s from %s", ld.Name, ld.Version, ld.Repository)
	return nil
}

func archiveName(name, version string) string { return name + "-" + version + ".tgz" }

// vendoredArchives lists charts/<name>-<semver>.tgz files.
func vendoredArchives(dir, name string) []string {
	entries, err := os.ReadDir(filepath.Join(dir, "charts"))
	if errors.Is(err, fs.ErrNotExist) || err != nil {
		return nil
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `-v?[0-9][^/]*\.tgz$`)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && re.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func lockedVersion(l *chart.Lock, name string) string {
	if l == nil {
		return ""
	}
	for _, d := range l.Dependencies {
		if d.Name == name {
			return d.Version
		}
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
