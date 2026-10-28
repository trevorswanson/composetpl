package lint

import (
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCheck(t *testing.T) {
	src := `version: "3"
x-common: {}
servics: {}
services:
  ok:
    image: nginx
  built:
    build: .
  bad:
    restart: always
  scalar: nope
`
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	got := Check(doc.Content[0])
	want := Result{
		Warnings: []string{"top-level `version` is obsolete in the Compose Specification and is ignored"},
		Errors: []string{
			`line 3: unknown top-level key "servics"`,
			`service "bad": needs image, build or extends`,
			`service "scalar": must be a mapping`,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v\nwant %#v", got, want)
	}
}
