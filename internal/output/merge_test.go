package output

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"composetpl/internal/engine"
)

func TestMerge(t *testing.T) {
	rendered := []engine.Rendered{
		{Name: "c/templates/a.yaml", Content: "services:\n  a:\n    image: a\n    ports: [\"1:1\"]\n---\n---\nnetworks:\n  proxy: {}\n"},
		{Name: "c/templates/empty.yaml", Content: "\n  \n"},
		{Name: "c/templates/b.yaml", Content: "services:\n  b:\n    image: b\n  a:\n    ports: [\"1:1\"]\n    restart: always\n"},
	}
	root, err := Merge(rendered)
	if err != nil {
		t.Fatal(err)
	}
	SetDefaultName(root, "stack")
	out, err := Encode(root)
	if err != nil {
		t.Fatal(err)
	}
	want := `name: stack
services:
  a:
    image: a
    ports: ["1:1"]
    restart: always
  b:
    image: b
networks:
  proxy: {}
`
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestMergeKeepsExplicitName(t *testing.T) {
	root, err := Merge([]engine.Rendered{{Name: "x", Content: "services: {}\nname: mine\n"}})
	if err != nil {
		t.Fatal(err)
	}
	SetDefaultName(root, "stack")
	out, _ := Encode(root)
	if !strings.Contains(string(out), "name: mine") || strings.Contains(string(out), "stack") {
		t.Errorf("explicit name was overridden:\n%s", out)
	}
}

func TestMergeConflict(t *testing.T) {
	_, err := Merge([]engine.Rendered{
		{Name: "c/templates/a.yaml", Content: "services:\n  web:\n    image: nginx:1\n"},
		{Name: "c/templates/b.yaml", Content: "services:\n  web:\n    image: nginx:2\n"},
	})
	want := `conflict at "services.web.image": set by both c/templates/a.yaml and c/templates/b.yaml`
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}

func TestParseErrors(t *testing.T) {
	for _, content := range []string{"services: [\n", "- a\n- b\n"} {
		_, err := Merge([]engine.Rendered{{Name: "bad.yaml", Content: content}})
		var pe *ParseError
		if !errors.As(err, &pe) || pe.Template != "bad.yaml" {
			t.Errorf("%q: got %v, want a ParseError for bad.yaml", content, err)
		}
	}
}

func TestWriteSplit(t *testing.T) {
	dir := t.TempDir()
	written, err := WriteSplit(dir, []engine.Rendered{
		{Name: "c/templates/sub/a.yaml", Content: "\nservices: {}"},
		{Name: "c/templates/empty.yaml", Content: "  \n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 1 {
		t.Fatalf("wrote %v, want one file", written)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "a.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "services: {}\n" {
		t.Errorf("got %q", got)
	}
}
