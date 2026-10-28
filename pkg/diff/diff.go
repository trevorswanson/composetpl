// Package diff compares two compose manifests resource by resource
// (services/web, networks/proxy, ...), like the helm-diff plugin.
package diff

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"
	"gopkg.in/yaml.v3"
)

// Op is how a resource changed.
type Op string

const (
	Added   Op = "added"
	Removed Op = "removed"
	Changed Op = "changed"
)

// Change is one added, removed or changed resource.
type Change struct {
	// Resource is "<section>/<name>" for map sections, or the top-level key otherwise.
	Resource string
	Op       Op
	Old, New string
}

// sections are top-level keys whose entries are diffed individually.
var sections = map[string]bool{
	"services": true, "networks": true, "volumes": true, "configs": true, "secrets": true, "models": true,
}

// Manifests returns the changes from old to new, ordered by resource.
// Either manifest may be empty.
func Manifests(old, new string) ([]Change, error) {
	oldRes, err := resources(old)
	if err != nil {
		return nil, fmt.Errorf("old manifest: %w", err)
	}
	newRes, err := resources(new)
	if err != nil {
		return nil, fmt.Errorf("new manifest: %w", err)
	}
	keys := map[string]bool{}
	for k := range oldRes {
		keys[k] = true
	}
	for k := range newRes {
		keys[k] = true
	}
	var out []Change
	for k := range keys {
		o, inOld := oldRes[k]
		n, inNew := newRes[k]
		switch {
		case !inOld:
			out = append(out, Change{Resource: k, Op: Added, New: n})
		case !inNew:
			out = append(out, Change{Resource: k, Op: Removed, Old: o})
		case o != n:
			out = append(out, Change{Resource: k, Op: Changed, Old: o, New: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out, nil
}

// resources splits a manifest into resource name -> YAML.
func resources(manifest string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(manifest) == "" {
		return out, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(manifest), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return out, nil
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		if sections[key] && val.Kind == yaml.MappingNode {
			for j := 0; j+1 < len(val.Content); j += 2 {
				s, err := encode(val.Content[j+1])
				if err != nil {
					return nil, err
				}
				out[key+"/"+val.Content[j].Value] = s
			}
			continue
		}
		s, err := encode(val)
		if err != nil {
			return nil, err
		}
		out[key] = s
	}
	return out, nil
}

func encode(n *yaml.Node) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// PrintOptions control Print.
type PrintOptions struct {
	// Release prefixes each header, as helm-diff does with the namespace.
	Release string
	Color   bool
	// Context is the number of unchanged lines shown around changes.
	Context int
}

const (
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	reset  = "\x1b[0m"
)

// Print writes the changes as unified diffs under one header per resource.
func Print(w io.Writer, changes []Change, o PrintOptions) error {
	for _, c := range changes {
		header := fmt.Sprintf("%s, %s, %s:", o.Release, c.Resource, c.Op)
		if o.Color {
			header = yellow + header + reset
		}
		fmt.Fprintln(w, header)
		edits := udiff.Lines(c.Old, c.New)
		text, err := udiff.ToUnified("a/"+c.Resource, "b/"+c.Resource, c.Old, edits, o.Context)
		if err != nil {
			return err
		}
		for _, line := range strings.SplitAfter(text, "\n") {
			if line == "" || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
				continue
			}
			if o.Color {
				switch line[0] {
				case '+':
					line = green + strings.TrimSuffix(line, "\n") + reset + "\n"
				case '-':
					line = red + strings.TrimSuffix(line, "\n") + reset + "\n"
				}
			}
			if _, err := io.WriteString(w, line); err != nil {
				return err
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}
