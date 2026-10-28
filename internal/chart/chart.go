// Package chart loads Helm-style charts — Chart.yaml, values.yaml, templates/**
// and supporting files — from a directory or a packaged .tgz, together with their
// library-chart dependencies.
package chart

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Metadata mirrors the subset of Helm's Chart.yaml that applies to compose.
// Field names match Helm so templates can use .Chart.Name, .Chart.AppVersion, etc.
type Metadata struct {
	APIVersion   string        `json:"apiVersion,omitempty"`
	Name         string        `json:"name"`
	Version      string        `json:"version,omitempty"`
	AppVersion   string        `json:"appVersion,omitempty"`
	Description  string        `json:"description,omitempty"`
	Type         string        `json:"type,omitempty"`
	Dependencies []*Dependency `json:"dependencies,omitempty"`
}

// Dependency is a Chart.yaml dependency entry.
type Dependency struct {
	Name string `json:"name"`
	// Version is a semver constraint, used for oci:// repositories.
	Version string `json:"version,omitempty"`
	// Repository is file://<path> or oci://<registry>/<repo>.
	Repository string `json:"repository"`
}

// Template is a file under templates/, named "<chart>/templates/<relpath>".
type Template struct {
	Name string
	Data []byte
}

// IsPartial reports whether the template only provides defines and is never rendered.
func (t Template) IsPartial() bool {
	base := path.Base(t.Name)
	return strings.HasPrefix(base, "_") || base == "NOTES.txt"
}

// Chart is a loaded chart.
type Chart struct {
	Metadata *Metadata
	// Dir is the chart's directory, or "" for a chart loaded from an archive.
	Dir string
	// Raw holds every file of the chart (after .composetplignore), by slash path.
	Raw    map[string][]byte
	Values map[string]interface{}
	// Schema is the chart's values.schema.json, if any.
	Schema    []byte
	Templates []Template
	// Files holds every non-template file (relative slash path -> contents), for .Files.
	Files map[string][]byte
	// Dependencies are library charts whose templates are parsed but never rendered.
	Dependencies []*Chart
}

// Resolver loads a dependency that is neither vendored under charts/ nor a
// file:// path, e.g. one from an OCI registry.
type Resolver func(dep *Dependency) (*Chart, error)

// Options control loading.
type Options struct {
	// Resolve loads remote dependencies; nil means they must be vendored.
	Resolve Resolver
}

// Load reads the chart at p: a directory or a packaged .tgz.
func Load(p string) (*Chart, error) {
	return LoadWith(p, Options{})
}

// LoadWith is Load with options.
func LoadWith(p string, o Options) (*Chart, error) {
	return load(p, o, nil)
}

func load(p string, o Options, stack []string) (*Chart, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	for _, s := range stack {
		if s == abs {
			return nil, fmt.Errorf("dependency cycle: %s -> %s", strings.Join(stack, " -> "), abs)
		}
	}
	stack = append(stack, abs)

	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%s is not a chart: %w", p, err)
	}
	var files map[string][]byte
	dir := ""
	if fi.IsDir() {
		if files, err = ReadDir(abs); err != nil {
			return nil, err
		}
		dir = abs
	} else {
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		if files, err = ReadArchive(data); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	return fromFiles(files, dir, p, o, stack)
}

// LoadArchive loads a packaged chart from memory.
func LoadArchive(data []byte, o Options) (*Chart, error) {
	files, err := ReadArchive(data)
	if err != nil {
		return nil, err
	}
	return fromFiles(files, "", "archive", o, nil)
}

// ReadDir reads a chart directory into a file map, skipping hidden files and
// anything matched by .composetplignore.
func ReadDir(dir string) (map[string][]byte, error) {
	ign, err := loadIgnore(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || ign.ignored(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[rel] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// fromFiles builds a chart from its file map. dir is the chart directory, if any.
func fromFiles(files map[string][]byte, dir, ref string, o Options, stack []string) (*Chart, error) {
	raw, ok := files["Chart.yaml"]
	if !ok {
		return nil, fmt.Errorf("%s is not a chart: Chart.yaml not found", ref)
	}
	md := &Metadata{}
	if err := yaml.Unmarshal(raw, md); err != nil {
		return nil, fmt.Errorf("parsing %s/Chart.yaml: %w", ref, err)
	}
	if md.Name == "" {
		return nil, fmt.Errorf("%s/Chart.yaml: name is required", ref)
	}

	c := &Chart{Metadata: md, Dir: dir, Raw: files, Values: map[string]interface{}{}, Files: map[string][]byte{}}
	if raw, ok := files["values.yaml"]; ok {
		if err := yaml.Unmarshal(raw, &c.Values); err != nil {
			return nil, fmt.Errorf("parsing %s/values.yaml: %w", md.Name, err)
		}
		if c.Values == nil {
			c.Values = map[string]interface{}{}
		}
	}

	vendored := map[string]map[string][]byte{} // charts/<entry> -> files
	for rel, data := range files {
		switch {
		case strings.HasPrefix(rel, "templates/"):
			c.Templates = append(c.Templates, Template{Name: md.Name + "/" + rel, Data: data})
		case strings.HasPrefix(rel, "charts/"):
			entry := strings.SplitN(strings.TrimPrefix(rel, "charts/"), "/", 2)
			if len(entry) == 2 {
				if vendored[entry[0]] == nil {
					vendored[entry[0]] = map[string][]byte{}
				}
				vendored[entry[0]][entry[1]] = data
			} else if strings.HasSuffix(entry[0], ".tgz") {
				vendored[entry[0]] = map[string][]byte{"": data}
			}
		case rel == "Chart.yaml" || rel == "values.yaml" || rel == "Chart.lock":
		case rel == "values.schema.json":
			c.Schema = data
		default:
			c.Files[rel] = data
		}
	}
	sort.Slice(c.Templates, func(i, j int) bool { return c.Templates[i].Name < c.Templates[j].Name })

	for _, dep := range md.Dependencies {
		dc, err := c.loadDependency(dep, vendored, o, stack)
		if err != nil {
			return nil, fmt.Errorf("%s: dependency %q: %w", md.Name, dep.Name, err)
		}
		c.Dependencies = append(c.Dependencies, dc)
	}
	return c, nil
}

// loadDependency resolves dep from, in order: a vendored charts/<name>/ directory
// or charts/*.tgz archive, a file:// path, or the remote resolver.
func (c *Chart) loadDependency(dep *Dependency, vendored map[string]map[string][]byte, o Options, stack []string) (*Chart, error) {
	if files, ok := vendored[dep.Name]; ok {
		return fromFiles(files, "", "charts/"+dep.Name, o, stack)
	}
	names := make([]string, 0, len(vendored))
	for n := range vendored {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !strings.HasPrefix(n, dep.Name+"-") || !strings.HasSuffix(n, ".tgz") {
			continue
		}
		dc, err := LoadArchive(vendored[n][""], o)
		if err != nil {
			return nil, fmt.Errorf("charts/%s: %w", n, err)
		}
		if dc.Metadata.Name == dep.Name {
			return dc, nil
		}
	}

	switch {
	case strings.HasPrefix(dep.Repository, "file://"):
		if c.Dir == "" {
			return nil, fmt.Errorf("file:// dependencies of a packaged chart must be vendored in charts/")
		}
		depDir := strings.TrimPrefix(dep.Repository, "file://")
		if !filepath.IsAbs(depDir) {
			depDir = filepath.Join(c.Dir, depDir)
		}
		return load(depDir, o, stack)
	case o.Resolve != nil:
		return o.Resolve(dep)
	case strings.HasPrefix(dep.Repository, "oci://"):
		return nil, fmt.Errorf("not vendored; run `composetpl dependency build` to fetch %s", dep.Repository)
	}
	return nil, fmt.Errorf("unsupported repository %q (want file:// or oci://)", dep.Repository)
}

// AllTemplates returns the templates of all dependencies (recursively) followed by this
// chart's own, so that when parsed in order a chart's defines override its dependencies'.
func (c *Chart) AllTemplates() []Template {
	var out []Template
	for _, d := range c.Dependencies {
		out = append(out, d.AllTemplates()...)
	}
	return append(out, c.Templates...)
}

// Readme returns the chart's README, if any.
func (c *Chart) Readme() []byte {
	names := make([]string, 0, len(c.Files))
	for n := range c.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !strings.Contains(n, "/") && strings.HasPrefix(strings.ToLower(n), "readme") {
			return c.Files[n]
		}
	}
	return nil
}
