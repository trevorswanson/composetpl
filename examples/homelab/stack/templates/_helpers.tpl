{{/*
Overrides the common.extraLabels hook to add gethomepage.dev discovery labels
for apps that declare a homepage group.
*/}}
{{- define "common.extraLabels" -}}
{{- with .app.homepage }}
homepage.group: {{ .group | quote }}
homepage.name: {{ $.name | title | quote }}
{{- if $.app.ingress }}
homepage.href: {{ printf "https://%s.%s" $.name $.root.Values.global.domain | quote }}
{{- end }}
{{- end }}
{{- end -}}
