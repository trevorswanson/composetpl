package chart

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// umbrella writes an "app" chart that uses the "db" chart twice (aliases primary
// and replica) plus a library chart, and loads it.
func umbrella(t *testing.T, appValues string) *Chart {
	t.Helper()
	root := t.TempDir()
	writeChart(t, filepath.Join(root, "db"), map[string]string{
		"Chart.yaml":         "name: db\nversion: 1.0.0\n",
		"values.yaml":        "port: 5432\nglobal: {tz: UTC, dbOnly: yes}\nexports:\n  conn: {dbHost: db, dbPort: 5432}\n",
		"values.schema.json": `{"type": "object", "properties": {"port": {"type": "integer"}}}`,
		"templates/svc.yaml": "services: {}\n",
	})
	writeChart(t, filepath.Join(root, "lib"), map[string]string{
		"Chart.yaml":             "name: lib\nversion: 1.0.0\ntype: library\n",
		"templates/_helpers.tpl": `{{ define "lib.x" }}{{ end }}`,
	})
	writeChart(t, filepath.Join(root, "app"), map[string]string{
		"Chart.yaml": `name: app
version: 1.0.0
dependencies:
  - name: db
    alias: primary
    repository: file://../db
    condition: primary.enabled,global.primary
    import-values: [conn]
  - name: db
    alias: replica
    repository: file://../db
    tags: [replicas]
    import-values:
      - {child: port, parent: replicaPort}
  - name: lib
    repository: file://../lib
`,
		"values.yaml": appValues,
	})
	c, err := Load(filepath.Join(root, "app"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func names(c *Chart) string {
	var out []string
	for _, d := range c.Dependencies {
		out = append(out, d.Metadata.Name)
	}
	return strings.Join(out, ",")
}

func TestAliasesLoadIndependentCopies(t *testing.T) {
	c := umbrella(t, "")
	if names(c) != "primary,replica,lib" {
		t.Fatalf("dependencies = %s", names(c))
	}
	p, r := c.Dependencies[0], c.Dependencies[1]
	if p.Templates[0].Name != "app/charts/primary/templates/svc.yaml" ||
		r.Templates[0].Name != "app/charts/replica/templates/svc.yaml" {
		t.Errorf("template names: %s, %s", p.Templates[0].Name, r.Templates[0].Name)
	}
	if p.Spec.Alias != "primary" || r.Spec.Tags[0] != "replicas" {
		t.Errorf("specs not attached: %+v %+v", p.Spec, r.Spec)
	}
}

func TestEnablement(t *testing.T) {
	cases := []struct {
		values, want string
	}{
		{"", "primary,replica,lib"},
		{"primary: {enabled: false}", "replica,lib"},
		// The first condition path that is a boolean decides.
		{"global: {primary: false}", "replica,lib"},
		{"primary: {enabled: true}\nglobal: {primary: false}", "primary,replica,lib"},
		// Non-boolean condition values are ignored.
		{"primary: {enabled: maybe}", "primary,replica,lib"},
		{"tags: {replicas: false}", "primary,lib"},
		{"tags: {replicas: true}", "primary,replica,lib"},
	}
	for _, tc := range cases {
		c := umbrella(t, tc.values)
		pruned, _, err := ProcessDependencies(c, c.Values)
		if err != nil {
			t.Fatal(err)
		}
		if got := names(pruned); got != tc.want {
			t.Errorf("values %q: enabled = %s, want %s", tc.values, got, tc.want)
		}
		if names(c) != "primary,replica,lib" {
			t.Error("ProcessDependencies modified its input")
		}
	}
}

func TestScopedValuesGlobalsAndImports(t *testing.T) {
	c := umbrella(t, `
global: {tz: America/Chicago, domain: lan}
primary: {port: 6543}
dbHost: explicit
`)
	_, vals, err := ProcessDependencies(c, c.Values)
	if err != nil {
		t.Fatal(err)
	}
	primary := vals["primary"].(map[string]interface{})
	replica := vals["replica"].(map[string]interface{})

	// Parent values win over the subchart's defaults; defaults fill the rest.
	if primary["port"] != 6543.0 || replica["port"] != 5432.0 {
		t.Errorf("ports: primary=%v replica=%v", primary["port"], replica["port"])
	}
	// Parent globals win and are copied down; the subchart's own globals stay.
	want := map[string]interface{}{"tz": "America/Chicago", "domain": "lan", "dbOnly": true}
	if g := primary["global"]; !reflect.DeepEqual(g, want) {
		t.Errorf("primary global = %v", g)
	}
	// import-values: the string form merges exports.conn into the root, under the
	// parent's own values; the map form copies a path.
	if vals["dbHost"] != "explicit" || vals["dbPort"] != 5432.0 {
		t.Errorf("string import: dbHost=%v dbPort=%v", vals["dbHost"], vals["dbPort"])
	}
	if vals["replicaPort"] != 5432.0 {
		t.Errorf("map import: replicaPort=%v", vals["replicaPort"])
	}
}

func TestSubchartSchema(t *testing.T) {
	c := umbrella(t, "replica: {port: high}")
	_, _, err := ProcessDependencies(c, c.Values)
	if err == nil || !strings.Contains(err.Error(), "app/charts/replica") || !strings.Contains(err.Error(), "/port") {
		t.Errorf("got %v", err)
	}
}

func TestDuplicateDependencyNeedsAlias(t *testing.T) {
	md := &Metadata{Name: "app", Dependencies: []*Dependency{
		{Name: "db", Repository: "file://a"}, {Name: "db", Repository: "file://b"},
	}}
	if err := md.Validate(); err == nil || !strings.Contains(err.Error(), "give one an alias") {
		t.Errorf("got %v", err)
	}
	md.Dependencies[1].Alias = "../x"
	if err := md.Validate(); err == nil || !strings.Contains(err.Error(), "invalid alias") {
		t.Errorf("got %v", err)
	}
}
