package engine

import (
	"encoding/base64"
	"path"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"composetpl/internal/glob"
)

// Files is the .Files object: the chart's non-template files keyed by relative slash path.
// Lookups never touch the filesystem, so paths cannot escape the chart directory.
type Files map[string][]byte

// GetBytes returns a file's contents, or nil if it does not exist.
func (f Files) GetBytes(name string) []byte {
	return f[path.Clean(name)]
}

// Get returns a file's contents as a string, or "" if it does not exist.
func (f Files) Get(name string) string {
	return string(f.GetBytes(name))
}

// Glob returns the files whose paths match pattern. "*" matches within a path
// segment and "**" matches across segments.
func (f Files) Glob(pattern string) Files {
	re, err := glob.Compile(pattern)
	if err != nil {
		return Files{}
	}
	out := Files{}
	for name, data := range f {
		if re.MatchString(name) {
			out[name] = data
		}
	}
	return out
}

// Lines splits a file into lines.
func (f Files) Lines(name string) []string {
	s := f.Get(name)
	if s == "" {
		return []string{}
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// AsConfig renders the files as a YAML map of base name to contents.
func (f Files) AsConfig() string {
	return f.asMap(func(b []byte) string { return string(b) })
}

// AsSecrets renders the files as a YAML map of base name to base64 contents.
func (f Files) AsSecrets() string {
	return f.asMap(func(b []byte) string { return base64.StdEncoding.EncodeToString(b) })
}

func (f Files) asMap(enc func([]byte) string) string {
	if len(f) == 0 {
		return ""
	}
	m := make(map[string]string, len(f))
	for name, data := range f {
		m[path.Base(name)] = enc(data)
	}
	out, _ := yaml.Marshal(m)
	return strings.TrimSuffix(string(out), "\n")
}

// Names returns the file paths in sorted order, for ranging deterministically.
func (f Files) Names() []string {
	names := make([]string, 0, len(f))
	for n := range f {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
