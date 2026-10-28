{{/*
Shared building blocks. Every helper takes a dict:
  name: the service name
  app:  the app's values (one entry of .Values.apps)
  root: the top-level context ($)
*/}}

{{/*
common.service renders one complete service entry.
Usage: {{ include "common.service" (dict "name" $name "app" $app "root" $) | nindent 2 }}
*/}}
{{- define "common.service" -}}
{{- $g := .root.Values.global -}}
{{- $app := .app -}}
{{ .name }}:
  image: {{ required (printf "apps.%s.image is required" .name) $app.image }}:{{ $app.tag | default "latest" }}
  container_name: {{ .name }}
  restart: {{ $app.restart | default $g.restart | default "unless-stopped" }}
  {{- with $app.network_mode }}
  network_mode: {{ . }}
  {{- end }}
  environment:
    TZ: {{ $g.tz | quote }}
    {{- if not $app.skipIds }}
    PUID: {{ $g.puid | quote }}
    PGID: {{ $g.pgid | quote }}
    {{- end }}
    {{- range $k, $v := $app.env }}
    {{ $k }}: {{ tpl (toString $v) $.root | quote }}
    {{- end }}
  {{- $volumes := include "common.volumes" . | trim }}
  {{- if $volumes }}
  volumes:
    {{- $volumes | nindent 4 }}
  {{- end }}
  {{- with $app.ports }}
  ports:
    {{- range . }}
    - {{ . | quote }}
    {{- end }}
  {{- end }}
  {{- if and $app.ingress (not $app.network_mode) }}
  networks:
    - {{ $g.network }}
  {{- end }}
  {{- $labels := include "common.labels" . | trim }}
  {{- if $labels }}
  labels:
    {{- $labels | nindent 4 }}
  {{- end }}
  {{- with $app.extra }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
{{- end -}}

{{/*
common.volumes: a /config mount under global.configRoot (unless config: false),
plus any extra volumes. Extra volume strings are rendered with tpl, so values may
reference other values, e.g. "{{ .Values.global.mediaRoot }}/tv:/tv".
*/}}
{{- define "common.volumes" -}}
{{- if ne (toString .app.config) "false" }}
- {{ printf "%s/%s:/config" .root.Values.global.configRoot .name | quote }}
{{- end }}
{{- range .app.volumes }}
- {{ tpl . $.root | quote }}
{{- end }}
{{- end -}}

{{/*
common.labels: Traefik labels when the app has an ingress, plus common.extraLabels.
*/}}
{{- define "common.labels" -}}
{{- if .app.ingress }}
{{ include "common.traefikLabels" . }}
{{- end }}
{{ include "common.extraLabels" . }}
{{- end -}}

{{- define "common.traefikLabels" -}}
{{- $g := .root.Values.global -}}
{{- $host := .app.ingress.host | default (printf "%s.%s" .name $g.domain) -}}
traefik.enable: "true"
traefik.docker.network: {{ $g.network }}
traefik.http.routers.{{ .name }}.rule: {{ printf "Host(`%s`)" $host | quote }}
traefik.http.routers.{{ .name }}.entrypoints: websecure
traefik.http.routers.{{ .name }}.tls.certresolver: {{ $g.certResolver }}
traefik.http.services.{{ .name }}.loadbalancer.server.port: {{ required (printf "apps.%s.ingress.port is required" .name) .app.ingress.port | quote }}
{{- end -}}

{{/*
common.extraLabels is a hook: it renders nothing here, and a chart that depends on
common can redefine it in its own _helpers.tpl (a chart's defines override its
dependencies').
*/}}
{{- define "common.extraLabels" -}}
{{- end -}}
