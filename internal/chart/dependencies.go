package chart

import (
	"fmt"
	"strings"

	"github.com/trevorswanson/composetpl/internal/values"
)

// ProcessDependencies prepares a chart tree for rendering, as Helm's
// ProcessDependencies and CoalesceValues do. vals are the root chart's computed
// values (user values over values.yaml). It returns:
//
//   - a copy of the tree without the dependencies that their condition or tags
//     disable;
//   - the values with every enabled subchart's defaults coalesced in under
//     .Values.<name or alias>, the parent's global values copied down into each
//     subchart's global, and import-values applied.
//
// Each subchart's values.schema.json is validated against its scoped values.
func ProcessDependencies(c *Chart, vals map[string]interface{}) (*Chart, map[string]interface{}, error) {
	tags, _ := vals["tags"].(map[string]interface{})
	pruned := c.clone()
	out, err := process(pruned, vals, tags, c.Metadata.Name)
	if err != nil {
		return nil, nil, err
	}
	return pruned, out, nil
}

// process works on chart c whose scoped values are vals, depth first.
func process(c *Chart, vals, tags map[string]interface{}, path string) (map[string]interface{}, error) {
	if vals == nil {
		vals = map[string]interface{}{}
	}
	parentGlobal, _ := vals["global"].(map[string]interface{})

	var enabled []*Chart
	for _, d := range c.Dependencies {
		if !isEnabled(d.Spec, vals, tags) {
			continue
		}
		key := d.Metadata.Name
		sub, _ := vals[key].(map[string]interface{})
		// Parent values (including user --set <key>.x) win over the subchart's defaults.
		sub = values.Coalesce(sub, d.Values)
		if parentGlobal != nil {
			subGlobal, _ := sub["global"].(map[string]interface{})
			sub["global"] = values.Coalesce(parentGlobal, subGlobal)
		}
		subPath := path + "/charts/" + key
		sub, err := process(d, sub, tags, subPath)
		if err != nil {
			return nil, err
		}
		if err := d.ValidateValues(sub); err != nil {
			return nil, fmt.Errorf("%s: %w", subPath, err)
		}
		vals = copyMap(vals)
		vals[key] = sub
		if vals, err = importValues(d, vals, sub); err != nil {
			return nil, fmt.Errorf("%s: import-values: %w", subPath, err)
		}
		enabled = append(enabled, d)
	}
	c.Dependencies = enabled
	return vals, nil
}

// isEnabled applies a dependency's condition, then its tags. A condition path
// that resolves to a boolean decides; otherwise tags do: a dependency is
// disabled only when none of its tags is true and at least one is false.
func isEnabled(spec *Dependency, vals, tags map[string]interface{}) bool {
	if spec == nil {
		return true
	}
	for _, p := range strings.Split(spec.Condition, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if b, ok := lookupPath(vals, p).(bool); ok {
			return b
		}
	}
	anyTrue, anyFalse := false, false
	for _, t := range spec.Tags {
		switch tags[t] {
		case true:
			anyTrue = true
		case false:
			anyFalse = true
		}
	}
	return anyTrue || !anyFalse
}

// importValues copies values from a subchart into its parent, beneath the
// parent's own values (the parent wins, as in Helm 3).
func importValues(d *Chart, parent, child map[string]interface{}) (map[string]interface{}, error) {
	for _, iv := range d.Spec.ImportValues {
		var childPath, parentPath string
		switch v := iv.(type) {
		case string:
			childPath = "exports." + v
		case map[string]interface{}:
			childPath, _ = v["child"].(string)
			parentPath, _ = v["parent"].(string)
			if childPath == "" {
				return nil, fmt.Errorf("entry %v needs a child path", v)
			}
		default:
			return nil, fmt.Errorf("entry %v must be a string or a {child, parent} map", iv)
		}
		got := lookupPath(child, childPath)
		if got == nil {
			continue
		}
		imported := map[string]interface{}{}
		if parentPath == "" {
			m, ok := got.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("%s must be a map to import into the parent's root", childPath)
			}
			imported = m
		} else {
			setPath(imported, parentPath, got)
		}
		parent = values.Coalesce(parent, imported)
	}
	return parent, nil
}

// lookupPath returns the value at a dotted path, or nil.
func lookupPath(m map[string]interface{}, path string) interface{} {
	var cur interface{} = m
	for _, part := range strings.Split(path, ".") {
		mm, ok := cur.(map[string]interface{})
		if !ok {
			return nil
		}
		if cur, ok = mm[part]; !ok {
			return nil
		}
	}
	return cur
}

// setPath sets a dotted path in m, creating maps along the way.
func setPath(m map[string]interface{}, path string, v interface{}) {
	parts := strings.Split(path, ".")
	for _, p := range parts[:len(parts)-1] {
		next, ok := m[p].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			m[p] = next
		}
		m = next
	}
	m[parts[len(parts)-1]] = v
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
