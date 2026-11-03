package output

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ContentHashLabel is set on each service that mounts a config or secret with
// inline content. Compose's config hash covers a service's definition, not the
// content of what it mounts, so without it `docker compose up` leaves a
// container running with old content.
const ContentHashLabel = "io.composetpl.content-hash"

// SetContentHashes labels each service with a hash of the inline content of the
// configs and secrets it mounts, so a change to that content recreates it.
// Services that mount none are left alone.
func SetContentHashes(root *yaml.Node) {
	services := mapValue(root, "services")
	if services == nil {
		return
	}
	defs := map[string]*yaml.Node{"configs": mapValue(root, "configs"), "secrets": mapValue(root, "secrets")}
	for i := 0; i+1 < len(services.Content); i += 2 {
		svc := services.Content[i+1]
		if svc.Kind != yaml.MappingNode {
			continue
		}
		var parts []string
		for _, kind := range []string{"configs", "secrets"} {
			for _, source := range mountSources(mapValue(svc, kind)) {
				if content := mapValue(mapValue(defs[kind], source), "content"); content != nil && content.Kind == yaml.ScalarNode {
					parts = append(parts, kind+"\x00"+source+"\x00"+content.Value)
				}
			}
		}
		if len(parts) == 0 {
			continue
		}
		sort.Strings(parts)
		h := sha256.New()
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
		setLabel(svc, ContentHashLabel, hex.EncodeToString(h.Sum(nil))[:16])
	}
}

// mapValue returns the value of key in mapping m, or nil.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	if i := findKey(m, key); i >= 0 {
		return m.Content[i+1]
	}
	return nil
}

// mountSources lists the sources of a service's configs or secrets, in short
// ("name") or long ({source: name}) form.
func mountSources(list *yaml.Node) []string {
	if list == nil || list.Kind != yaml.SequenceNode {
		return nil
	}
	var out []string
	for _, m := range list.Content {
		switch m.Kind {
		case yaml.ScalarNode:
			out = append(out, m.Value)
		case yaml.MappingNode:
			if s := mapValue(m, "source"); s != nil {
				out = append(out, s.Value)
			}
		}
	}
	return out
}

// setLabel sets a label on a service whose labels are a map, a list of
// "key=value", or absent.
func setLabel(svc *yaml.Node, key, value string) {
	str := func(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s} }
	labels := mapValue(svc, "labels")
	switch {
	case labels == nil:
		svc.Content = append(svc.Content, str("labels"), &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{str(key), str(value)}})
	case labels.Kind == yaml.MappingNode:
		if i := findKey(labels, key); i >= 0 {
			labels.Content[i+1] = str(value)
		} else {
			labels.Content = append(labels.Content, str(key), str(value))
		}
	case labels.Kind == yaml.SequenceNode:
		prefix := key + "="
		for i, l := range labels.Content {
			if strings.HasPrefix(l.Value, prefix) {
				labels.Content[i] = str(prefix + value)
				return
			}
		}
		labels.Content = append(labels.Content, str(prefix+value))
	}
}
