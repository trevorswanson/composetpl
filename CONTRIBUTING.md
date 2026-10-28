# Contributing to composetpl

Thanks for your interest in composetpl! Bug reports, feature ideas, documentation fixes and
pull requests are all welcome, whether it's your first open-source contribution or your hundredth.

## Issues

- **Bugs:** [open a bug report](https://github.com/trevorswanson/composetpl/issues/new?template=bug_report.yml).
  The most useful reports include:
  - `composetpl version` and `docker compose version`;
  - the smallest chart and values that reproduce the problem;
  - what you expected, and what happened instead.
- **Ideas:** [open a feature request](https://github.com/trevorswanson/composetpl/issues/new?template=feature_request.yml).
  Say what you're trying to do, not just the feature you have in mind. If Helm already does it, say how Helm does
  it; composetpl follows Helm's behaviour wherever compose allows.
- **Questions** are fine as issues too.

Please don't report security vulnerabilities in public issues. Use GitHub's
[private vulnerability reporting](https://github.com/trevorswanson/composetpl/security/advisories/new) instead.

## Pull requests

For anything larger than a small fix, please open an issue first, so we can agree on the approach before
you invest time in it.

1. Fork the repository and create a branch from `main`.
2. Make your change, with tests (see below).
3. Update the README if users will notice the change.
4. Run the checks locally (see below), then open a pull request.

`main` is protected. Every change lands through a pull request, and the **`ci-ok`** check must pass. That check
covers:
- lint and formatting;
- unit tests on Linux, macOS and Windows;
- integration tests against a real Docker engine;
- `govulncheck`;
- a GoReleaser build.

Small, focused pull requests are much easier to review than large ones. Several small PRs beat one big one.

## Development setup

You need:
- **Go:** the version in [`go.mod`](go.mod).
- **Docker with Compose v2.20 or newer:** only for the integration tests and for trying release commands by
  hand. Everything else, including the unit tests, runs without Docker.
- **[golangci-lint](https://golangci-lint.run/) v2:** optional locally, since CI runs it too.

```bash
git clone https://github.com/<you>/composetpl && cd composetpl
go build ./cmd/composetpl
./composetpl template examples/homelab/stack
```

## Tests

```bash
go test ./...                                                     # unit tests (no Docker needed)
go test ./cmd/composetpl -update                                  # regenerate golden files, then review the diff
go test -tags integration -run Integration -v ./cmd/composetpl    # real Docker engine
golangci-lint run ./...                                           # lint and formatting
```

Some guidelines:
- **Unit tests don't need Docker.**
  - Release logic is tested with the fake compose runner in `pkg/compose/composetest`.
  - OCI code is tested with the in-memory registry in `pkg/registry/registrytest`.
  - CLI behaviour is tested end to end through cobra in `cmd/composetpl/main_test.go`.
- **Golden files** in `cmd/composetpl/testdata` capture the rendered example charts. If you change rendering on
  purpose, regenerate them with `-update`, and check that the diff is what you meant.
- **Integration tests** create uniquely named compose projects and volumes, and remove them afterwards. They pull
  `busybox`, `traefik/whoami` and `registry` images.

## Code style

- Match the code around you: its naming, comment density and structure.
- `gofmt` and `goimports` formatting; CI enforces both.
- Wrap errors with context (`fmt.Errorf("doing X: %w", err)`), and keep error messages useful to someone who
  only sees the CLI output.
- Prefer Helm's names and semantics for anything Helm also has.
- Commit messages: a short imperative summary line, then a body that explains *why*.

## Dependencies

Before adding a dependency:
- Prefer the standard library, or a dependency we already use; the binary aims to stay small.
- The dependency must be under a permissive license (MIT, BSD, Apache-2.0 or similar). No copyleft.
- If it's Apache-2.0 and ships a `NOTICE` file, add that notice's text to our [`NOTICE`](NOTICE).
- Run `go mod tidy`; CI fails if `go.mod` or `go.sum` are out of date.

## License

composetpl is licensed under the [Apache License 2.0](LICENSE). By contributing, you agree that your
contributions are licensed under the same license.
