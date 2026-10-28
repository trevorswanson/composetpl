// Package output turns rendered templates into compose YAML, either merged into a
// single document or written one file per template.
package output

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"

	"composetpl/internal/engine"
)

// ParseError is a rendered template that is not valid YAML.
type ParseError struct {
	Template string
	Content  string
	Err      error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s: rendered output is not valid YAML: %v (re-run with --debug to see it)", e.Template, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

// ParseDocs decodes every non-empty YAML document in a rendered template.
// Each document must be a mapping.
func ParseDocs(r engine.Rendered) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(r.Content))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return nil, &ParseError{Template: r.Name, Content: r.Content, Err: err}
		}
		if len(doc.Content) == 0 {
			continue
		}
		root := doc.Content[0]
		if root.Kind == yaml.ScalarNode && root.Tag == "!!null" {
			continue
		}
		if root.Kind != yaml.MappingNode {
			return nil, &ParseError{Template: r.Name, Content: r.Content,
				Err: fmt.Errorf("line %d: top level of a compose document must be a mapping", root.Line)}
		}
		docs = append(docs, root)
	}
}

// Merge deep-merges every document of every rendered template, in order, into one
// mapping. Maps merge recursively; differing scalars or lists at the same path are
// an error naming both templates. Identical values are allowed.
func Merge(rendered []engine.Rendered) (*yaml.Node, error) {
	m := &merger{
		root:   &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
		origin: map[string]string{},
	}
	for _, r := range rendered {
		docs, err := ParseDocs(r)
		if err != nil {
			return nil, err
		}
		for _, d := range docs {
			if err := m.mergeMap(m.root, d, "", r.Name); err != nil {
				return nil, err
			}
		}
	}
	return m.root, nil
}

type merger struct {
	root *yaml.Node
	// origin records which template introduced each key path.
	origin map[string]string
}

func (m *merger) mergeMap(dst, src *yaml.Node, prefix, source string) error {
	for i := 0; i+1 < len(src.Content); i += 2 {
		k, v := src.Content[i], src.Content[i+1]
		p := k.Value
		if prefix != "" {
			p = prefix + "." + k.Value
		}
		idx := findKey(dst, k.Value)
		if idx < 0 {
			dst.Content = append(dst.Content, k, v)
			m.origin[p] = source
			continue
		}
		dv := dst.Content[idx+1]
		if dv.Kind == yaml.MappingNode && v.Kind == yaml.MappingNode {
			if err := m.mergeMap(dv, v, p, source); err != nil {
				return err
			}
			continue
		}
		if equalNodes(dv, v) {
			continue
		}
		return fmt.Errorf("conflict at %q: set by both %s and %s", p, m.originOf(p), source)
	}
	return nil
}

// originOf finds the template that introduced p or its nearest ancestor.
func (m *merger) originOf(p string) string {
	for {
		if s, ok := m.origin[p]; ok {
			return s
		}
		i := strings.LastIndex(p, ".")
		if i < 0 {
			return "an earlier template"
		}
		p = p[:i]
	}
}

func findKey(mapping *yaml.Node, key string) int {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func equalNodes(a, b *yaml.Node) bool {
	var av, bv interface{}
	if a.Decode(&av) != nil || b.Decode(&bv) != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// SetDefaultName adds a top-level compose project `name:` unless one is present.
func SetDefaultName(root *yaml.Node, name string) {
	if name == "" || findKey(root, "name") >= 0 {
		return
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "name"}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}
	root.Content = append([]*yaml.Node{k, v}, root.Content...)
}

// Encode serializes a node as YAML with 2-space indentation.
func Encode(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
