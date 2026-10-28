# composetpl

[![CI](https://github.com/trevorswanson/composetpl/actions/workflows/ci.yml/badge.svg)](https://github.com/trevorswanson/composetpl/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/trevorswanson/composetpl.svg)](https://pkg.go.dev/github.com/trevorswanson/composetpl)
[![License](https://img.shields.io/github/license/trevorswanson/composetpl)](LICENSE)

Helm for docker compose. Write your compose files as Helm-style charts, then install,
upgrade, roll back and uninstall them as versioned releases.

- **Templates:** `values.yaml`, `_helpers.tpl`, `define`/`include`, `range`/`if`/`with`, and the full
  Helm + [Sprig](https://masterminds.github.io/sprig/) function set. It is the same engine Helm uses
  (Go `text/template`).
- **Releases:** `install`, `upgrade`, `rollback`, `uninstall`, `history`, `status`, `get` and `diff`,
  backed by `docker compose`, with history stored on the Docker host.
- **Hooks and tests:** pre/post install, upgrade, rollback and delete hooks, plus `composetpl test`.
- **Umbrella charts:** deploy several stacks as one release, with Helm's subchart values, `global`,
  `condition`, `tags`, `alias` and `import-values`.
- **Distribution:** `package`, OCI `push`/`pull`, and `dependency update`/`build` with a `Chart.lock`.

```bash
composetpl install media ./stack --wait
composetpl upgrade media ./stack --set apps.jellyfin.enabled=true
composetpl rollback media
```

## Install

Download a binary for Linux, macOS or Windows (amd64 or arm64) from the
[releases page](https://github.com/trevorswanson/composetpl/releases), or install with Go:

```bash
go install github.com/trevorswanson/composetpl/cmd/composetpl@latest
```

Or build a static binary (about 12 MB) from a checkout:

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o composetpl ./cmd/composetpl
```

Release commands need Docker with the Compose v2 plugin (2.20 or newer). `template`, `lint`,
`package`, `push` and `pull` don't need Docker at all.

## Charts

```
stack/
  Chart.yaml            # name, version, appVersion, dependencies
  values.yaml           # defaults, available as .Values
  values.schema.json    # optional JSON Schema for the values
  .composetplignore     # optional, like .helmignore
  templates/
    _helpers.tpl        # files starting with "_" only define partials; never rendered
    apps.yaml           # each other file renders to compose YAML
    NOTES.txt           # printed after install/upgrade and by status
  files/                # anything outside templates/ is available via .Files
  charts/               # vendored dependencies (see `dependency update`)
```

`composetpl create NAME` scaffolds a chart with all of the above.

### Template context

| Object | Contents |
| --- | --- |
| `.Values` | `values.yaml`, overlaid by `-f` files in order, then `--set` / `--set-string` / `--set-file`. A `null` override deletes a key. Validated against `values.schema.json` if present. |
| `.Chart` | `Chart.yaml`: `.Chart.Name`, `.Chart.Version`, `.Chart.AppVersion`, … |
| `.Release` | `.Name` (also the compose project name), `.Revision`, `.IsInstall`, `.IsUpgrade` |
| `.Capabilities` | `.ComposeVersion` and `.DockerVersion`, read from the engine on install/upgrade; `template` uses `--compose-version` / `--docker-version`. |
| `.Template` | `.Name`, `.BasePath` |
| `.Files` | `Get`, `GetBytes`, `Lines`, `Glob` (supports `**`), `AsConfig`, `AsSecrets`, `Names`. Restricted to files inside the chart. |

Functions:
- **Sprig:** all of it.
- **Helm's extras:** `include`, `tpl`, `required`, `fail`, `toYaml`, `toYamlPretty`, `fromYaml`, `fromYamlArray`,
  `toJson`, `fromJson`, `fromJsonArray`, `toToml` and `fromToml`.
- **`lookup KIND NAME`:** returns `docker inspect` output for a `container`, `image`, `network` or `volume`. On
  install/upgrade it reads the live engine. In `template`, `lint` and `--dry-run` it returns an empty map, as Helm's
  does without a cluster.
- **`env` and `expandenv`:** enabled, unlike in Helm; handy for secrets. Pass `--no-env` to disable them.

### Sharing helpers between stacks

Put shared `define`s in a library chart and list it as a dependency:

```yaml
# stack/Chart.yaml
dependencies:
  - name: common
    repository: file://../common          # or oci://ghcr.io/me/charts with version: ^0.1.0
```

Mark a chart `type: library` in its `Chart.yaml` to use it only for its `define`s: its templates are parsed into
the shared template set but never rendered. If a chart `define`s the same name as one of its dependencies, the
chart's version wins. That lets a library expose "hooks" for stacks to override (see `common.extraLabels` in the
example).

Where a dependency is loaded from, in order:
1. Vendored copies in `charts/`.
2. `file://` paths.

`oci://` dependencies must be vendored first with `dependency update` or `dependency build`.

### Umbrella charts

A dependency that isn't `type: library` is an application subchart. Its templates render into the same release,
as in Helm, so an umbrella chart can deploy several stacks as one compose project:

```yaml
# homelab/Chart.yaml
dependencies:
  - name: media
    repository: file://../stack
    condition: media.enabled            # first boolean among these value paths decides
  - name: monitoring
    repository: file://../monitoring
    tags: [ops]                         # or enable by .Values.tags.ops
  - name: postgres
    alias: photos-db                    # the same chart can appear twice under different aliases
    repository: oci://ghcr.io/me/charts
    version: ^1.2.0
    import-values:
      - conn                            # copies the subchart's exports.conn into the parent's values
      - {child: port, parent: photosDbPort}
```

```yaml
# homelab/values.yaml
global:                 # copied into every subchart's .Values.global (the parent wins)
  domain: home.example.com
media:                  # becomes the media subchart's .Values, over its own values.yaml
  apps:
    jellyfin: {enabled: true}
monitoring:
  enabled: false
```

How subcharts behave:
- **Values:** each subchart sees only its own `.Values`, which is the subchart's `values.yaml` overlaid by the
  parent's `<name>:` section and any `--set <name>.key=...`. Each subchart's `values.schema.json` is checked
  against those values.
- **Template context:** subchart templates get their own `.Chart`, `.Files` and `.Template`, and share
  `.Release` and `.Capabilities` with the parent.
- **Notes:** only the parent's `NOTES.txt` is printed, unless you pass `--render-subchart-notes`.
- **Naming:** all output is merged into one compose project. Two subcharts that define the same service name
  are a conflict, reported with both template names. Name services after `.Chart.Name`, which is the alias,
  if a chart may be used more than once.
- **Split output:** `template --output-dir` writes subchart files under `charts/<name>/`.

## Releases

A release is one compose project. Each `install`, `upgrade` or `rollback`:
1. Renders the chart.
2. Validates it with `docker compose config`.
3. Runs hooks.
4. Runs `docker compose -p NAME -f - up -d --remove-orphans` (manifest on stdin).
5. Records a numbered revision.

Compose recreates only the containers whose configuration changed.

| Command | Notes |
| --- | --- |
| `install NAME CHART` | `--wait`, `--timeout`, `--atomic` (tear down on failure), `--dry-run`, `--pull`, `--force`, `--no-hooks`, `--project-directory` |
| `upgrade NAME CHART` | `-i/--install`, `--reuse-values`, `--reset-values`, `--reset-then-reuse-values`, `--history-max` (default 10), `--atomic` (roll back on failure). With no `-f`/`--set`, the previous values are reused, as in Helm. |
| `rollback NAME [REV]` | Redeploys an earlier revision as a new one. Also recovers a release stuck in a `pending-*` state. |
| `uninstall NAME...` | `docker compose down`; `--keep-history`, `--volumes`, `--rmi` |
| `list` | `-a`, `--deployed`, `--failed`, `--pending`, `--superseded`, `--uninstalled`, `-f REGEX`, `-q`; shows live `running(N)` state |
| `status NAME` | Revision, containers (health, state), NOTES |
| `history NAME` | Every revision with status and description |
| `get values\|manifest\|notes\|hooks\|metadata\|all NAME` | `--revision`; `values --all` shows the computed values |
| `diff upgrade\|rollback\|revision` | Per-resource unified diffs like the helm-diff plugin; `--detailed-exitcode` exits 2 on changes |
| `test NAME` | Runs the release's `test` hooks; `--logs`, `--filter` |

Every listing command takes `-o table|json|yaml`.

### Where releases are stored

`--storage` (or `COMPOSETPL_STORAGE`) picks the driver:

- **`volume` (default):** records live in the Docker volume `composetpl-releases` on the target engine, read
  and written by a short-lived `busybox` helper container. History travels with the host and is shared by every
  client pointed at it, like Helm keeping releases in the cluster. `COMPOSETPL_HELPER_IMAGE` and
  `COMPOSETPL_VOLUME` override the image and the volume name.
- **`local`:** records are JSON files under `$XDG_STATE_HOME/composetpl/<docker context>/`, or `--state-dir`.

Records hold the user-supplied values, which may include secrets. Local files are written `0600`.

To target another engine, use `--context NAME` (like `--kube-context`), `-H/--host`, or the usual
`DOCKER_CONTEXT` and `DOCKER_HOST` variables.

## Hooks and tests

A hook is a service tagged with an `x-composetpl` extension field. Hook services are removed from the deployed
manifest and run to completion with `docker compose run` at their events. They run against the release's
manifest, so they share its networks and volumes.

```yaml
services:
  db-migrate:
    image: app:{{ .Values.tag }}
    command: ["migrate", "up"]
    depends_on: [db]
    x-composetpl:
      hook: [pre-install, pre-upgrade]       # one or more events
      weight: -5                             # lower runs first (default 0)
      delete-policy: [before-hook-creation, hook-succeeded]   # default: before-hook-creation
      no-deps: false                         # true: don't start depends_on services
      retries: 3                             # run again on failure (default 0)
      backoff: 10s                           # first retry delay; doubles each time, capped at 5m
      timeout: 2m                            # per attempt; overrides --timeout
```

- **Events:** `pre-install`, `post-install`, `pre-upgrade`, `post-upgrade`, `pre-rollback`, `post-rollback`,
  `pre-delete`, `post-delete` and `test`.
- **Failures:** a hook that still fails after its retries fails the revision, and triggers recovery if
  `--atomic` was given. Each retry replaces the previous attempt's container; delete policies apply to the
  final outcome.
- **Tests:** `composetpl test NAME` runs the `test` hooks.
- **Template output:** `template` leaves hook services out unless you pass `--include-hooks`.

## Distribution

```bash
composetpl package ./stack                     # media-0.1.0.tgz, reproducible; vendors file:// deps
docker login ghcr.io
composetpl push media-0.1.0.tgz oci://ghcr.io/me/charts
composetpl install media oci://ghcr.io/me/charts/media --version ^0.1
composetpl pull oci://ghcr.io/me/charts/media:0.1.0 --untar
```

- **OCI:** charts work with any OCI registry: GHCR, Docker Hub, Harbor, or a self-hosted `registry:2`.
  - **Credentials** come from the Docker config and credential helpers.
  - **Version constraints:** `--version` takes a semver constraint; with none, you get the newest stable version.
  - **Plain HTTP:** `localhost` registries always use it; for other hosts, pass `--plain-http` or set
    `COMPOSETPL_PLAIN_HTTP`.
- **Releases from a registry** record the exact reference and digest they were installed from.
- **Dependencies:**
  - `dependency update DIR` resolves each dependency's constraint, vendors it as `charts/<name>-<version>.tgz`
    and writes `Chart.lock`.
  - `dependency build DIR` restores exactly the locked versions and verifies their digests.
  - `dependency list DIR` shows what's missing.

## Compared with Helm

| Helm | composetpl |
| --- | --- |
| install / upgrade / rollback / uninstall / history / get / status / list | Same commands and flags, backed by docker compose |
| --wait / --atomic / --dry-run / --reuse-values family | Same semantics (`up --wait`; atomic redeploys the previous revision) |
| helm-diff plugin | `composetpl diff` |
| hooks, `helm test` | `x-composetpl` hook services, with retries and backoff like a Job's `backoffLimit` |
| package / push / pull / dependency / Chart.lock | Same, OCI only |
| `--kube-context`, namespaces | `--context` / `-H`; each release is a compose project |
| `lookup` | Docker objects instead of Kubernetes resources |
| Rolling updates | Not possible: compose recreates changed containers (brief downtime) |
| Umbrella charts: subcharts, `global`, `condition`, `tags`, `alias`, `import-values` | Same, merged into one compose project |
| HTTP chart repos, `search`, plugins, provenance | Not supported |

### Things to know

- **`${VAR}` interpolation:** stored manifests keep compose's own `${VAR}` interpolation unresolved, so
  `rollback` uses the environment of the machine running it.
- **Relative paths** (`./data`, `build: .`) resolve against the chart directory. A packaged or OCI chart has no
  chart directory, so they resolve against the working directory. Use `--project-directory` to change it.
- **`template` output** can still be piped straight to compose: `composetpl template ./stack | docker compose -f - up -d`.
  `--output-dir` writes one file per template instead of a merged file.

## Example

`examples/homelab` has:
- `common`: a library chart of shared helpers;
- `stack`: the `media` stack;
- `monitoring`: a monitoring stack (dozzle and uptime-kuma);
- `umbrella`: a `homelab` umbrella chart that deploys both stacks as one release, with shared `global`
  settings.

The media stack:
- builds one service per entry in `.Values.apps`, with Traefik and homepage labels;
- reads Traefik's arguments from `files/`;
- validates its values with a schema;
- backs up the app config before every upgrade (a hook);
- smoke-tests the dashboard (a `test` hook).

```bash
composetpl template examples/homelab/stack
composetpl install media examples/homelab/stack --wait
composetpl diff upgrade media examples/homelab/stack --set apps.plex.enabled=false
composetpl test media --logs
composetpl install homelab examples/homelab/umbrella --set monitoring.enabled=false
```

## Contributing

Issues and pull requests are welcome! [CONTRIBUTING.md](CONTRIBUTING.md) covers how to report bugs, set up a
development environment, run the tests and get a pull request merged. In short:

```bash
go test ./...                                                     # unit tests, no Docker needed
go test ./cmd/composetpl -update                                  # regenerate golden files
go test -tags integration -run Integration -v ./cmd/composetpl    # needs Docker + compose
golangci-lint run ./...
```

## License

Apache License 2.0; see [LICENSE](LICENSE). Portions of the template engine are adapted from
[Helm](https://helm.sh) (Apache-2.0); see [NOTICE](NOTICE).
