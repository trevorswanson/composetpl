package chart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeChart(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	writeChart(t, filepath.Join(root, "lib"), map[string]string{
		"Chart.yaml":             "name: lib\ntype: library\n",
		"templates/_helpers.tpl": `{{ define "lib.x" }}{{ end }}`,
	})
	writeChart(t, filepath.Join(root, "app"), map[string]string{
		"Chart.yaml":           "name: app\nappVersion: '1.2'\ndependencies:\n  - name: lib\n    repository: file://../lib\n",
		"values.yaml":          "a: 1\n",
		"templates/b.yaml":     "b: 1",
		"templates/sub/a.yaml": "a: 1",
		"files/conf.txt":       "conf",
		".hidden/skip.txt":     "nope",
	})

	c, err := Load(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Metadata.AppVersion != "1.2" || c.Values["a"] != 1.0 {
		t.Errorf("metadata/values not loaded: %+v %v", c.Metadata, c.Values)
	}
	var names []string
	for _, tpl := range c.AllTemplates() {
		names = append(names, tpl.Name)
	}
	want := "app/charts/lib/templates/_helpers.tpl,app/templates/b.yaml,app/templates/sub/a.yaml"
	if got := strings.Join(names, ","); got != want {
		t.Errorf("templates = %s, want %s", got, want)
	}
	if len(c.Files) != 1 || string(c.Files["files/conf.txt"]) != "conf" {
		t.Errorf("files = %v", c.Files)
	}
}

func TestLoadErrors(t *testing.T) {
	root := t.TempDir()
	writeChart(t, filepath.Join(root, "a"), map[string]string{
		"Chart.yaml": "name: a\ndependencies:\n  - name: b\n    repository: file://../b\n",
	})
	writeChart(t, filepath.Join(root, "b"), map[string]string{
		"Chart.yaml": "name: b\ndependencies:\n  - name: a\n    repository: file://../a\n",
	})
	writeChart(t, filepath.Join(root, "remote"), map[string]string{
		"Chart.yaml": "name: remote\ndependencies:\n  - name: x\n    repository: https://charts.example.com\n",
	})
	writeChart(t, filepath.Join(root, "noname"), map[string]string{"Chart.yaml": "version: 1\n"})

	for dir, want := range map[string]string{
		"a":       "dependency cycle",
		"remote":  `unsupported repository "https://charts.example.com"`,
		"noname":  "name is required",
		"missing": "is not a chart",
	} {
		if _, err := Load(filepath.Join(root, dir)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want error containing %q", dir, err, want)
		}
	}
}

func TestValidateValues(t *testing.T) {
	c := &Chart{Metadata: &Metadata{Name: "media"}, Schema: []byte(`{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["global"],
  "properties": {
    "global": {
      "type": "object",
      "required": ["domain"],
      "properties": {"domain": {"type": "string", "minLength": 1}, "puid": {"type": "integer"}}
    }
  }
}`)}
	ok := map[string]interface{}{"global": map[string]interface{}{"domain": "home.lan", "puid": int64(1000)}}
	if err := c.ValidateValues(ok); err != nil {
		t.Errorf("valid values rejected: %v", err)
	}
	bad := map[string]interface{}{"global": map[string]interface{}{"domain": "", "puid": "abc"}}
	err := c.ValidateValues(bad)
	if err == nil {
		t.Fatal("expected a schema error")
	}
	for _, w := range []string{"schema in chart media", "/global/domain", "/global/puid"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error missing %q:\n%v", w, err)
		}
	}

	c.Schema = []byte(`{"$ref": "file:///etc/passwd"}`)
	if err := c.ValidateValues(ok); err == nil || !strings.Contains(err.Error(), "external $ref") {
		t.Errorf("external $ref should be refused: %v", err)
	}
	if err := (&Chart{Metadata: &Metadata{Name: "x"}}).ValidateValues(bad); err != nil {
		t.Errorf("no schema should pass: %v", err)
	}
}
