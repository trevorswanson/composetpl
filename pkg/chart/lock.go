package chart

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/yaml"
)

// LockFile records the exact dependency versions `dependency update` resolved.
const LockFile = "Chart.lock"

// Lock is the Chart.lock file.
type Lock struct {
	Dependencies []*LockedDependency `json:"dependencies"`
	// Digest fingerprints Chart.yaml's dependency list, to detect a stale lock.
	Digest    string    `json:"digest"`
	Generated time.Time `json:"generated"`
}

// LockedDependency pins one dependency.
type LockedDependency struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	// Digest is the sha256 of the vendored archive.
	Digest string `json:"digest"`
}

// DependenciesDigest fingerprints a chart's declared dependencies.
func DependenciesDigest(md *Metadata) string {
	data, _ := json.Marshal(md.Dependencies)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(data))
}

// ReadLock reads dir/Chart.lock; it returns nil, nil if there is none.
func ReadLock(dir string) (*Lock, error) {
	data, err := os.ReadFile(filepath.Join(dir, LockFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l := &Lock{}
	if err := yaml.Unmarshal(data, l); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", LockFile, err)
	}
	return l, nil
}

// Write saves the lock to dir/Chart.lock.
func (l *Lock) Write(dir string) error {
	data, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, LockFile), data, 0o644)
}

// ReadMetadata parses dir/Chart.yaml without loading templates or dependencies.
func ReadMetadata(dir string) (*Metadata, error) {
	data, err := os.ReadFile(filepath.Join(dir, "Chart.yaml"))
	if err != nil {
		return nil, fmt.Errorf("%s is not a chart: %w", dir, err)
	}
	md := &Metadata{}
	if err := yaml.Unmarshal(data, md); err != nil {
		return nil, fmt.Errorf("parsing %s/Chart.yaml: %w", dir, err)
	}
	if err := md.Validate(); err != nil {
		return nil, fmt.Errorf("%s/Chart.yaml: %w", dir, err)
	}
	return md, nil
}

// Archive packs a loaded chart, vendoring its file:// dependencies.
func Archive(c *Chart) ([]byte, error) {
	files, err := vendorFiles(c)
	if err != nil {
		return nil, err
	}
	return WriteArchive(c.Metadata.Name, files)
}
