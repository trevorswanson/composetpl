package engine

import (
	"strings"
	"testing"

	"composetpl/internal/chart"
)

// testChart builds an in-memory chart named "app" with the given templates
// (path under templates/ -> source) and one library dependency "lib".
func testChart(tpls, libTpls map[string]string) *chart.Chart {
	c := &chart.Chart{
		Metadata: &chart.Metadata{Name: "app", Version: "1.0.0"},
		Files: map[string][]byte{
			"files/a.txt":     []byte("one\ntwo\n"),
			"files/sub/b.txt": []byte("bee"),
			"README.md":       []byte("readme"),
		},
	}
	for name, src := range tpls {
		c.Templates = append(c.Templates, chart.Template{Name: "app/templates/" + name, Data: []byte(src)})
	}
	lib := &chart.Chart{Metadata: &chart.Metadata{Name: "lib", Type: "library"}}
	for name, src := range libTpls {
		lib.Templates = append(lib.Templates, chart.Template{Name: "lib/templates/" + name, Data: []byte(src)})
	}
	c.Dependencies = []*chart.Chart{lib}
	return c
}

func render(t *testing.T, c *chart.Chart, vals map[string]interface{}, opts Options) map[string]string {
	t.Helper()
	out, err := Render(c, vals, map[string]interface{}{"Name": "rel"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, r := range out.Templates {
		m[strings.TrimPrefix(r.Name, "app/templates/")] = r.Content
	}
	return m
}

func TestRenderBasics(t *testing.T) {
	vals := map[string]interface{}{
		"apps": map[string]interface{}{
			"b": map[string]interface{}{"enabled": true, "port": 2.0},
			"a": map[string]interface{}{"enabled": true, "port": 1.0},
			"c": map[string]interface{}{"enabled": false},
		},
		"domain": "home.lan",
		"host":   "{{ .Values.domain }}",
	}
	c := testChart(map[string]string{
		"_local.tpl": `{{ define "app.name" }}{{ .Release.Name }}-{{ .Chart.Name }}{{ end }}`,
		"svc.yaml": `services:
{{- range $name, $app := .Values.apps }}
{{- if $app.enabled }}
  {{ $name }}: {{ include "lib.port" $app }}
{{- end }}
{{- end }}
name: {{ include "app.name" . }}
host: {{ tpl .Values.host . }}
tpl: {{ .Template.Name }} {{ .Template.BasePath }}
yaml: {{ dict "k" "v" | toYaml }}
missing: [{{ .Values.nope }}]
env: {{ env "COMPOSETPL_TEST_ENV" }}`,
	}, map[string]string{
		"_helpers.tpl": `{{- define "lib.port" -}}{{ .port | int }}{{- end -}}`,
	})
	t.Setenv("COMPOSETPL_TEST_ENV", "fromenv")

	got := render(t, c, vals, Options{})
	if _, ok := got["_local.tpl"]; ok {
		t.Error("partial template was rendered")
	}
	want := `services:
  a: 1
  b: 2
name: rel-app
host: home.lan
tpl: app/templates/svc.yaml app/templates
yaml: k: v
missing: []
env: fromenv`
	if got["svc.yaml"] != want {
		t.Errorf("got:\n%s\nwant:\n%s", got["svc.yaml"], want)
	}
}

func TestChartDefinesOverrideDependency(t *testing.T) {
	c := testChart(
		map[string]string{
			"_helpers.tpl": `{{ define "lib.hook" }}chart{{ end }}`,
			"x.yaml":       `{{ include "lib.wrap" . }}`,
		},
		map[string]string{
			"_helpers.tpl": `{{ define "lib.hook" }}lib{{ end }}{{ define "lib.wrap" }}[{{ include "lib.hook" . }}]{{ end }}`,
		},
	)
	if got := render(t, c, nil, Options{})["x.yaml"]; got != "[chart]" {
		t.Errorf("got %q, want %q", got, "[chart]")
	}
}

func TestTplCanDefineAndInclude(t *testing.T) {
	c := testChart(map[string]string{
		"x.yaml": `{{ tpl "{{ define \"inner\" }}hi {{ .n }}{{ end }}{{ include \"inner\" . }}" (dict "n" "there") }}`,
	}, nil)
	if got := render(t, c, nil, Options{})["x.yaml"]; got != "hi there" {
		t.Errorf("got %q", got)
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name, src, want string
		opts            Options
	}{
		{"required", `{{ required "need foo" .Values.foo }}`, "app/templates/x.yaml:1:3: need foo", Options{}},
		{"required empty string", `{{ required "need bar" .Values.bar }}`, "need bar", Options{}},
		{"fail", `{{ fail "nope" }}`, "app/templates/x.yaml:1:3: nope", Options{}},
		{"parse", `{{ if }}`, "parse error at app/templates/x.yaml:1", Options{}},
		{"strict", `{{ .Values.missing.deeper }}`, `map has no entry for key "missing"`, Options{Strict: true}},
		{"no env", `{{ env "HOME" }}`, `function "env" not defined`, Options{NoEnv: true}},
		{"recursion", `{{ define "loop" }}{{ include "loop" . }}{{ end }}{{ include "loop" . }}`, "nested reference name: loop", Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := testChart(map[string]string{"x.yaml": tc.src}, nil)
			_, err := Render(c, map[string]interface{}{"bar": ""}, nil, tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got error %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestFiles(t *testing.T) {
	c := testChart(map[string]string{
		"x.yaml": `get: {{ .Files.Get "files/a.txt" | quote }}
escape: [{{ .Files.Get "../../../etc/passwd" }}]
clean: {{ .Files.Get "files/../files/sub/b.txt" }}
lines: {{ .Files.Lines "files/a.txt" | join "," }}
glob: {{ (.Files.Glob "files/**").Names | join "," }}
globOne: {{ (.Files.Glob "files/*.txt").Names | join "," }}
config: {{ (.Files.Glob "files/sub/*").AsConfig }}
secret: {{ (.Files.Glob "files/sub/*").AsSecrets }}`,
	}, nil)
	want := `get: "one\ntwo\n"
escape: []
clean: bee
lines: one,two
glob: files/a.txt,files/sub/b.txt
globOne: files/a.txt
config: b.txt: bee
secret: b.txt: YmVl`
	if got := render(t, c, nil, Options{})["x.yaml"]; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCapabilitiesAndLookup(t *testing.T) {
	c := testChart(map[string]string{
		"x.yaml": `{{ if semverCompare ">=2.24" .Capabilities.ComposeVersion }}new{{ else }}old{{ end }} ` +
			`{{ .Capabilities.DockerVersion }} ` +
			`[{{ (lookup "volume" "data").Name }}]`,
	}, nil)
	caps := map[string]interface{}{"ComposeVersion": "2.30.0", "DockerVersion": "29.0.0"}

	out, err := Render(c, nil, nil, Options{Capabilities: caps})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Templates[0].Content; got != "new 29.0.0 []" {
		t.Errorf("without lookup: %q", got)
	}

	lookup := func(kind, name string) (map[string]interface{}, error) {
		return map[string]interface{}{"Name": kind + ":" + name}, nil
	}
	out, err = Render(c, nil, nil, Options{Capabilities: caps, Lookup: lookup})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Templates[0].Content; got != "new 29.0.0 [volume:data]" {
		t.Errorf("with lookup: %q", got)
	}
}

func TestNotes(t *testing.T) {
	c := testChart(map[string]string{
		"NOTES.txt": "hello {{ .Release.Name }}",
		"x.yaml":    "a: 1",
	}, map[string]string{"NOTES.txt": "library notes are ignored"})
	out, err := Render(c, nil, map[string]interface{}{"Name": "rel"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Notes != "hello rel" || len(out.Templates) != 1 {
		t.Errorf("notes=%q templates=%d", out.Notes, len(out.Templates))
	}
}
