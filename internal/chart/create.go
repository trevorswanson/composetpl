package chart

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var chartNameRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// scaffold is the chart `create` writes. CHARTNAME is replaced with the chart name.
var scaffold = map[string]string{
	"Chart.yaml": `apiVersion: v2
name: CHARTNAME
description: A composetpl chart for CHARTNAME
version: 0.1.0
appVersion: "1.0.0"
`,
	"values.yaml": `# Default values for CHARTNAME.

image:
  repository: nginx
  # Defaults to the chart's appVersion.
  tag: ""

restart: unless-stopped

ports:
  - "8080:80"

env: {}
#  TZ: Etc/UTC

volumes: []
#  - ./data:/data

healthcheck:
  enabled: true
  test: ["CMD", "wget", "-q", "-O", "/dev/null", "http://localhost:80"]

tests:
  enabled: true
  # Port the smoke test connects to inside the compose network.
  port: 80
`,
	"values.schema.json": `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["image"],
  "properties": {
    "image": {
      "type": "object",
      "required": ["repository"],
      "properties": {
        "repository": { "type": "string", "minLength": 1 },
        "tag": { "type": "string" }
      }
    },
    "restart": { "enum": ["no", "always", "on-failure", "unless-stopped"] },
    "ports": { "type": "array", "items": { "type": "string" } },
    "env": { "type": "object" },
    "volumes": { "type": "array", "items": { "type": "string" } }
  }
}
`,
	".composetplignore": `# Patterns to leave out of packaged charts, like .helmignore.
*.tgz
*.swp
*.bak
*~
.DS_Store
`,
	"templates/_helpers.tpl": `{{/*
CHARTNAME.image is the image reference, defaulting the tag to the appVersion.
*/}}
{{- define "CHARTNAME.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end -}}

{{/*
CHARTNAME.labels are added to every service.
*/}}
{{- define "CHARTNAME.labels" -}}
app.composetpl.io/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | quote }}
app.composetpl.io/release: {{ .Release.Name | quote }}
{{- end -}}
`,
	"templates/services.yaml": `services:
  {{ .Chart.Name }}:
    image: {{ include "CHARTNAME.image" . }}
    restart: {{ .Values.restart }}
    {{- with .Values.ports }}
    ports:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- with .Values.env }}
    environment:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- with .Values.volumes }}
    volumes:
      {{- toYaml . | nindent 6 }}
    {{- end }}
    {{- if .Values.healthcheck.enabled }}
    healthcheck:
      test: {{ toJson .Values.healthcheck.test }}
      interval: 30s
      timeout: 5s
      retries: 3
    {{- end }}
    labels:
      {{- include "CHARTNAME.labels" . | nindent 6 }}
`,
	"templates/NOTES.txt": `{{ .Release.Name }} is running {{ include "CHARTNAME.image" . }}.
{{- range .Values.ports }}
  - http://localhost:{{ (split ":" .)._0 }}
{{- end }}

  composetpl status {{ .Release.Name }}
  composetpl test {{ .Release.Name }}
`,
	"templates/tests/smoke.yaml": `{{- if .Values.tests.enabled }}
services:
  {{ .Chart.Name }}-smoke-test:
    image: busybox:1.37
    command: ["wget", "-q", "-O", "/dev/null", "http://{{ .Chart.Name }}:{{ .Values.tests.port }}"]
    depends_on: [{{ .Chart.Name }}]
    x-composetpl:
      hook: test
{{- end }}
`,
}

// Create scaffolds a new chart named after the last element of dir.
func Create(dir string) (string, error) {
	name := filepath.Base(dir)
	if !chartNameRegexp.MatchString(name) {
		return "", fmt.Errorf("invalid chart name %q: use lowercase letters, digits, '-' and '_'", name)
	}
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("%s already exists", dir)
	}
	for rel, content := range scaffold {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(strings.ReplaceAll(content, "CHARTNAME", name)), 0o644); err != nil {
			return "", err
		}
	}
	return dir, nil
}
