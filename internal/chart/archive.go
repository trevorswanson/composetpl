package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// drivePath matches Windows drive-qualified paths such as C:/x or C:x.
var drivePath = regexp.MustCompile(`^[A-Za-z]:`)

// MaxArchiveSize bounds the decompressed size of a chart archive.
const MaxArchiveSize = 64 << 20

// archiveTime is the fixed modification time of archive entries, so the same chart
// always packages to the same bytes (and the same OCI digest).
var archiveTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// ReadArchive unpacks a chart .tgz into a file map. As with Helm archives, every
// entry sits under one top-level directory, which is stripped.
func ReadArchive(data []byte) (map[string][]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("not a gzipped chart archive: %w", err)
	}
	tr := tar.NewReader(io.LimitReader(zr, MaxArchiveSize+1))
	files := map[string][]byte{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading chart archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// Treat "\" as a separator, as Helm does, so a Windows-style entry such as
		// chart/..\..\evil can't hide a parent reference.
		name := strings.ReplaceAll(h.Name, "\\", "/")
		if strings.Contains(name, "..") || path.IsAbs(name) || drivePath.MatchString(name) {
			return nil, fmt.Errorf("chart archive entry %q escapes the chart (names may not contain \"..\")", h.Name)
		}
		name = path.Clean(strings.TrimPrefix(name, "./"))
		parts := strings.SplitN(name, "/", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("chart archive entry %q is not inside a chart directory", h.Name)
		}
		total += h.Size
		if total > MaxArchiveSize {
			return nil, fmt.Errorf("chart archive is larger than %d bytes", MaxArchiveSize)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[parts[1]] = b
	}
	return files, nil
}

// WriteArchive packs a file map as <name>/<path> entries in a deterministic .tgz.
func WriteArchive(name string, files map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.ModTime = archiveTime
	tw := tar.NewWriter(zw)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h := &tar.Header{
			Name: name + "/" + n, Typeflag: tar.TypeReg, Mode: 0o644,
			Size: int64(len(files[n])), ModTime: archiveTime, Format: tar.FormatPAX,
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// PackageOptions control Package.
type PackageOptions struct {
	// Destination directory for the archive (default: current directory).
	Destination string
	// Version and AppVersion override Chart.yaml when set.
	Version    string
	AppVersion string
}

// Package archives the chart directory dir as <name>-<version>.tgz and returns its path.
func Package(dir string, o PackageOptions) (string, error) {
	c, err := Load(dir)
	if err != nil {
		return "", err
	}
	if c.Dir == "" {
		return "", fmt.Errorf("%s is already packaged", dir)
	}
	files, err := vendorFiles(c)
	if err != nil {
		return "", err
	}
	if o.Version != "" || o.AppVersion != "" {
		md := *c.Metadata
		if o.Version != "" {
			md.Version = o.Version
		}
		if o.AppVersion != "" {
			md.AppVersion = o.AppVersion
		}
		out, err := yaml.Marshal(&md)
		if err != nil {
			return "", err
		}
		files["Chart.yaml"] = out
		c.Metadata = &md
	}
	if c.Metadata.Version == "" {
		return "", fmt.Errorf("%s/Chart.yaml: version is required to package a chart", c.Metadata.Name)
	}
	data, err := WriteArchive(c.Metadata.Name, files)
	if err != nil {
		return "", err
	}
	dest := o.Destination
	if dest == "" {
		dest = "."
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dest, fmt.Sprintf("%s-%s.tgz", c.Metadata.Name, c.Metadata.Version))
	return out, os.WriteFile(out, data, 0o644)
}

// vendorFiles returns c's files with every file:// dependency that is not already
// vendored packed into charts/<name>-<version>.tgz, recursively, so the archive
// is self-contained.
func vendorFiles(c *Chart) (map[string][]byte, error) {
	files := make(map[string][]byte, len(c.Raw))
	for k, v := range c.Raw {
		files[k] = v
	}
	for i, dep := range c.Metadata.Dependencies {
		if !strings.HasPrefix(dep.Repository, "file://") || hasVendored(files, dep.Name) {
			continue
		}
		dc := c.Dependencies[i]
		depFiles, err := vendorFiles(dc)
		if err != nil {
			return nil, err
		}
		data, err := WriteArchive(dc.Metadata.Name, depFiles)
		if err != nil {
			return nil, err
		}
		name := dc.Metadata.Name
		if dc.Metadata.Version != "" {
			name += "-" + dc.Metadata.Version
		}
		files["charts/"+name+".tgz"] = data
	}
	return files, nil
}

func hasVendored(files map[string][]byte, name string) bool {
	for f := range files {
		if strings.HasPrefix(f, "charts/"+name+"/") ||
			(strings.HasPrefix(f, "charts/"+name+"-") && strings.HasSuffix(f, ".tgz")) {
			return true
		}
	}
	return false
}
