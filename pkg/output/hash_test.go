package output

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func contentHashes(t *testing.T, doc string) map[string]string {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatal(err)
	}
	SetContentHashes(root.Content[0])
	out, err := Encode(&root)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Services map[string]struct {
			Labels interface{} `yaml:"labels"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for name, s := range p.Services {
		switch l := s.Labels.(type) {
		case map[string]interface{}:
			if v, ok := l[ContentHashLabel].(string); ok {
				got[name] = v
			}
		case []interface{}:
			for _, e := range l {
				if v, ok := strings.CutPrefix(e.(string), ContentHashLabel+"="); ok {
					got[name] = v
				}
			}
		}
	}
	return got
}

func TestSetContentHashes(t *testing.T) {
	doc := func(content string) string {
		return `services:
  map:
    image: a
    labels: {keep: "1"}
    configs: [app]
  list:
    image: a
    labels: [keep=1]
    configs: [{source: app, target: /etc/app.conf}]
  none:
    image: a
    configs: [app]
  file:
    image: a
    configs: [onDisk]
  plain:
    image: a
configs:
  app: {content: ` + content + `}
  onDisk: {file: ./app.conf}
`
	}
	one, two := contentHashes(t, doc("v1")), contentHashes(t, doc("v2"))
	for _, svc := range []string{"map", "list", "none"} {
		if one[svc] == "" || one[svc] == two[svc] {
			t.Errorf("%s: hash %q -> %q should change with the content", svc, one[svc], two[svc])
		}
	}
	if one["map"] != one["list"] || one["map"] != one["none"] {
		t.Errorf("same content, different hashes: %v", one)
	}
	if _, ok := one["file"]; ok {
		t.Error("a file config is bind-mounted, so it needs no hash")
	}
	if _, ok := one["plain"]; ok {
		t.Error("a service without configs got a hash")
	}
	if again := contentHashes(t, doc("v1")); again["map"] != one["map"] {
		t.Error("hash isn't stable")
	}
}
