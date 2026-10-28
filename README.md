# composetpl

Helm for docker compose. Write your compose files as Helm-style charts, then install,
upgrade, roll back and uninstall them as versioned releases.

- **Templates:** `values.yaml`, `_helpers.tpl`, `define`/`include`, `range`/`if`/`with`, and the full
  Helm + [Sprig](https://masterminds.github.io/sprig/) function set. It is the same engine Helm uses
  (Go `text/template`).
- **Releases:** `install`, `upgrade`, `rollback`, `uninstall`, `history`, `status`, `get` and `diff`,
  backed by `docker compose`, with history stored on the Docker host.
- **Hooks and tests:** pre/post install, upgrade, rollback and delete hooks, plus `composetpl test`.
- **Distribution:** `package`, OCI `push`/`pull`, and `dependency update`/`build` with a `Chart.lock`.

```bash
composetpl install media ./stack --wait
composetpl upgrade media ./stack --set apps.jellyfin.enabled=true
composetpl rollback media
```

## Install

```bash
go install ./cmd/composetpl
```

Or build a static binary (about 12 MB):

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

Every template of a dependency is parsed into the same template set, but none of them are rendered.
If a chart `define`s the same name as one of its dependencies, the chart's version wins. That lets a library
expose "hooks" for stacks to override (see `common.extraLabels` in the example).

Where a dependency is loaded from, in order:
1. Vendored copies in `charts/`.
2. `file://` paths.

`oci://` dependencies must be vendored first with `dependency update` or `dependency build`.

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
```

- **Events:** `pre-install`, `post-install`, `pre-upgrade`, `post-upgrade`, `pre-rollback`, `post-rollback`,
  `pre-delete`, `post-delete` and `test`.
- **Failures:** a failed hook fails the revision, and triggers recovery if `--atomic` was given.
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
| hooks, `helm test` | `x-composetpl` hook services; no Job-style retries or backoff |
| package / push / pull / dependency / Chart.lock | Same, OCI only |
| `--kube-context`, namespaces | `--context` / `-H`; each release is a compose project |
| `lookup` | Docker objects instead of Kubernetes resources |
| Rolling updates | Not possible: compose recreates changed containers (brief downtime) |
| HTTP chart repos, `search`, plugins, provenance | Not supported |
| Application subcharts (umbrella charts with scoped values) | Not supported; dependencies are library charts |

### Things to know

- **`${VAR}` interpolation:** stored manifests keep compose's own `${VAR}` interpolation unresolved, so
  `rollback` uses the environment of the machine running it.
- **Relative paths** (`./data`, `build: .`) resolve against the chart directory. A packaged or OCI chart has no
  chart directory, so they resolve against the working directory. Use `--project-directory` to change it.
- **`template` output** can still be piped straight to compose: `composetpl template ./stack | docker compose -f - up -d`.
  `--output-dir` writes one file per template instead of a merged file.

## Example

`examples/homelab` has a `common` library chart and a `media` stack. The stack:
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
```

## Development

```bash
go test ./...
go test ./cmd/composetpl -update                              # regenerate golden files
go test -tags integration -run Integration -v ./cmd/composetpl   # needs Docker + compose
```

## License

Apache License 2.0; see [LICENSE](LICENSE). Portions of the template engine are adapted from
[Helm](https://helm.sh) (Apache-2.0); see [NOTICE](NOTICE).
