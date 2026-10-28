// Package chart loads a Helm-style chart directory: Chart.yaml, values.yaml,
// templates/** and supporting files, plus local library-chart dependencies.
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

// Dependency is a Chart.yaml dependency entry. Only file:// repositories are supported.
type Dependency struct {
	Name       string `json:"name"`
	Version    string `json:"version,omitempty"`
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

// Chart is a loaded chart directory.
type Chart struct {
	Metadata  *Metadata
	Dir       string
	Values    map[string]interface{}
	Templates []Template
	// Files holds every non-template file (relative slash path -> contents), for .Files.
	Files map[string][]byte
	// Dependencies are library charts whose templates are parsed but never rendered.
	Dependencies []*Chart
}

// Load reads the chart at dir and, recursively, its local dependencies.
func Load(dir string) (*Chart, error) {
	return load(dir, nil)
}

func load(dir string, stack []string) (*Chart, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for _, s := range stack {
		if s == abs {
			return nil, fmt.Errorf("dependency cycle: %s -> %s", strings.Join(stack, " -> "), abs)
		}
	}
	stack = append(stack, abs)

	raw, err := os.ReadFile(filepath.Join(abs, "Chart.yaml"))
	if err != nil {
		return nil, fmt.Errorf("%s is not a chart: %w", dir, err)
	}
	md := &Metadata{}
	if err := yaml.Unmarshal(raw, md); err != nil {
		return nil, fmt.Errorf("parsing %s/Chart.yaml: %w", dir, err)
	}
	if md.Name == "" {
		return nil, fmt.Errorf("%s/Chart.yaml: name is required", dir)
	}

	c := &Chart{Metadata: md, Dir: abs, Values: map[string]interface{}{}, Files: map[string][]byte{}}

	if raw, err := os.ReadFile(filepath.Join(abs, "values.yaml")); err == nil {
		if err := yaml.Unmarshal(raw, &c.Values); err != nil {
			return nil, fmt.Errorf("parsing %s/values.yaml: %w", dir, err)
		}
		if c.Values == nil {
			c.Values = map[string]interface{}{}
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(abs, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(rel, "templates/"):
			c.Templates = append(c.Templates, Template{Name: md.Name + "/" + rel, Data: data})
		case rel == "Chart.yaml" || rel == "values.yaml":
		default:
			c.Files[rel] = data
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(c.Templates, func(i, j int) bool { return c.Templates[i].Name < c.Templates[j].Name })

	for _, dep := range md.Dependencies {
		if !strings.HasPrefix(dep.Repository, "file://") {
			return nil, fmt.Errorf("%s: dependency %q: only file:// repositories are supported", md.Name, dep.Name)
		}
		depDir := strings.TrimPrefix(dep.Repository, "file://")
		if !filepath.IsAbs(depDir) {
			depDir = filepath.Join(abs, depDir)
		}
		dc, err := load(depDir, stack)
		if err != nil {
			return nil, fmt.Errorf("%s: dependency %q: %w", md.Name, dep.Name, err)
		}
		c.Dependencies = append(c.Dependencies, dc)
	}
	return c, nil
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
