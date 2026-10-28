# composetpl

Render docker-compose files from Helm-style charts: `values.yaml`, `_helpers.tpl`,
`define`/`include`, `range`/`if`/`with`, and the full Helm + Sprig function set.

It uses the same engine Helm does (Go `text/template` + [Sprig](https://masterminds.github.io/sprig/)),
so anything you know from Helm charts works here.

```bash
composetpl template ./stack | docker compose -f - up -d
```

## Install

```bash
go install ./cmd/composetpl
```

Or build a small static binary (about 8 MB):

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o composetpl ./cmd/composetpl
```

## Chart layout

```
stack/
  Chart.yaml          # name, version, appVersion, dependencies
  values.yaml         # defaults, available as .Values
  templates/
    _helpers.tpl      # files starting with "_" only define partials; never rendered
    apps.yaml         # each other file renders to compose YAML
  files/              # anything outside templates/ is available via .Files
```

### Sharing helpers between stacks (library charts)

Put shared `define`s in a chart of their own and depend on it with a `file://` path:

```yaml
# stack/Chart.yaml
name: media
dependencies:
  - name: common
    repository: file://../common
```

Every template of a dependency is parsed into the same template set, but none of them are rendered.
If a chart `define`s the same name as one of its dependencies, the chart's version wins. That lets a library expose
"hook" templates for stacks to override (see `common.extraLabels` in the example).

## Template context

| Object | Contents |
| --- | --- |
| `.Values` | `values.yaml`, overlaid by `-f` files in order, then `--set` / `--set-string` / `--set-file`. A `null` override deletes a key. |
| `.Chart` | `Chart.yaml` fields: `.Chart.Name`, `.Chart.Version`, `.Chart.AppVersion`, `.Chart.Description`, … |
| `.Release.Name` | `--name`, defaulting to the chart name. In merged mode it also becomes the compose project `name:` unless a template sets one. |
| `.Template.Name`, `.Template.BasePath` | The template being rendered. |
| `.Files` | `Get`, `GetBytes`, `Lines`, `Glob` (supports `**`), `AsConfig`, `AsSecrets`, `Names`. Restricted to files inside the chart. |

Functions:
- **Sprig:** all of it.
- **Helm's extras:** `include`, `tpl`, `required`, `fail`, `toYaml`, `toYamlPretty`, `fromYaml`, `fromYamlArray`, `toJson`, `fromJson`, `fromJsonArray`, `toToml`, `fromToml`.
- **Unlike Helm, `env` and `expandenv` are enabled** (handy for secrets). Pass `--no-env` to disable them.

## Output modes

- **Merged (default).** Every document from every template is deep-merged, in template-name order, into one compose file on stdout or `-o FILE`:
  - Maps merge recursively, so several templates can each add to `services:`.
  - The same path set to two different values is an error that names both templates.
- **Split.** `--output-dir DIR` writes each template to its own file, e.g. `DIR/apps.yaml`. Templates that render empty are skipped.

## Commands

```
composetpl template CHART [-f values.yaml]... [--set k=v]... [--set-string k=v]... [--set-file k=path]...
                          [--name NAME] [-o FILE | --output-dir DIR] [-s templates/x.yaml]...
                          [--strict] [--no-env] [--debug]
composetpl lint CHART [same value flags]
composetpl version
```

- `--strict` fails on references to missing keys (Helm's `--strict`).
- `--debug` prints the raw, line-numbered output of a template when it isn't valid YAML.
- `lint` renders and merges the chart, then:
  - checks that top-level keys are valid Compose keys (or `x-*`);
  - checks that every service has `image`, `build` or `extends`;
  - runs `docker compose config -q` when `docker` is installed.

## Example

`examples/homelab` contains a `common` library chart and a `media` stack. The stack builds one service per entry in
`.Values.apps`:

```yaml
# stack/templates/apps.yaml
services:
{{- range $name, $app := .Values.apps }}
{{- if $app.enabled }}
  {{- include "common.service" (dict "name" $name "app" $app "root" $) | nindent 2 }}
{{- end }}
{{- end }}
```

```bash
composetpl template examples/homelab/stack
composetpl template examples/homelab/stack --set apps.jellyfin.enabled=true --set apps.plex.enabled=false
composetpl template examples/homelab/stack --output-dir out/
```

## Development

```bash
go test ./...
go test ./cmd/composetpl -update   # regenerate golden files after changing the example
```

## License

Apache License 2.0; see [LICENSE](LICENSE). Portions of the template engine are adapted from
[Helm](https://helm.sh) (Apache-2.0); see [NOTICE](NOTICE).
