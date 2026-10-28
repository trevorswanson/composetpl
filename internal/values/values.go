// Package values merges chart defaults with user-supplied values the way Helm does:
// values.yaml < -f files (in order) < --set / --set-string / --set-file.
package values

import (
	"fmt"
	"os"

	"helm.sh/helm/v3/pkg/strvals"
	"sigs.k8s.io/yaml"
)

// Options holds the user-supplied value sources.
type Options struct {
	ValueFiles   []string
	Values       []string // --set
	StringValues []string // --set-string
	FileValues   []string // --set-file
}

// MergeValues combines the user-supplied sources into one map, later sources winning.
func (o Options) MergeValues() (map[string]interface{}, error) {
	base := map[string]interface{}{}
	for _, f := range o.ValueFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		cur := map[string]interface{}{}
		if err := yaml.Unmarshal(raw, &cur); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", f, err)
		}
		base = mergeMaps(base, cur)
	}
	for _, v := range o.Values {
		if err := strvals.ParseInto(v, base); err != nil {
			return nil, fmt.Errorf("parsing --set %q: %w", v, err)
		}
	}
	for _, v := range o.StringValues {
		if err := strvals.ParseIntoString(v, base); err != nil {
			return nil, fmt.Errorf("parsing --set-string %q: %w", v, err)
		}
	}
	for _, v := range o.FileValues {
		reader := func(rs []rune) (interface{}, error) {
			b, err := os.ReadFile(string(rs))
			return string(b), err
		}
		if err := strvals.ParseIntoFile(v, base, reader); err != nil {
			return nil, fmt.Errorf("parsing --set-file %q: %w", v, err)
		}
	}
	return base, nil
}

// Coalesce overlays user values onto chart defaults. A null user value deletes the key.
// Neither input is modified.
func Coalesce(user, defaults map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(defaults))
	for k, v := range defaults {
		out[k] = v
	}
	for k, uv := range user {
		if uv == nil {
			delete(out, k)
			continue
		}
		um, uok := uv.(map[string]interface{})
		dm, dok := out[k].(map[string]interface{})
		if uok && dok {
			out[k] = Coalesce(um, dm)
		} else if uok {
			out[k] = Coalesce(um, nil)
		} else {
			out[k] = uv
		}
	}
	return out
}

// mergeMaps deep-merges b onto a, keeping nulls so Coalesce can delete keys later.
func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(a))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		if vm, ok := v.(map[string]interface{}); ok {
			if am, ok := out[k].(map[string]interface{}); ok {
				out[k] = mergeMaps(am, vm)
				continue
			}
		}
		out[k] = v
	}
	return out
}
