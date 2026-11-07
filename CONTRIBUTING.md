# Contributing to Forge

Thanks for helping build Forge! This guide gets you from clone to PR.

## Setup

Requirements: Go 1.26+, git, and bash (Git Bash works on Windows).

```bash
git clone https://github.com/ArpitK24/forge
cd forge
make build      # ./dist/forge
make test       # unit tests, no API keys or network needed
```

## Before you open a PR

```bash
make check      # fmt-check + vet + lint + race tests
```

CI runs the same checks on Linux, macOS and Windows, so if `make check`
passes locally, CI almost always does too.

- **Formatting:** `gofmt` (run `make fmt`).
- **Lint:** [golangci-lint](https://golangci-lint.run) v2, config in `.golangci.yml`.
- **Tests:** new behaviour needs a test. Tests must be hermetic: use
  `t.TempDir()`, the mock API servers, and the embedded MCP mock. No real
  API keys, no network.
- **No cgo:** release binaries are built with `CGO_ENABLED=0`. Don't add
  dependencies that need cgo.

## Commits and PRs

- Use [Conventional Commits](https://www.conventionalcommits.org):
  `fix(plugins): ...`, `feat(tui): ...`, `docs: ...`. The changelog is built
  from them.
- Keep PRs small and focused, one logical change each.
- Fill in the PR template, including how you tested it.
- `main` is protected: every change lands through a PR with green CI.

## Reporting bugs and ideas

Use the issue templates. For security problems, follow [SECURITY.md](SECURITY.md)
instead of opening an issue.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
