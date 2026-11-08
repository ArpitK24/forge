# Changelog

All notable changes to Forge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Forge uses
[Semantic Versioning](https://semver.org).

## [Unreleased]

### Security
- Settings: a repository's `.forge/settings.json` or `.forge/settings.local.json`
  can no longer raise the permission mode, add permission rules, or launch MCP
  servers. These keys are ignored (with a warning) unless the project's absolute
  path is listed in `trusted_projects` in `~/.forge/settings.json`. An explicit
  `--permission-mode` flag now always wins over every settings file.

### Fixed
- Settings layering: a missing project or local settings file no longer resets
  global/project values to compiled-in defaults, and loading settings no longer
  creates `.forge/` in the working directory.
- `--no-auto-compact` works again; settings can only turn auto-compaction off.
- Plugins: host and plugin now both speak Content-Length framing, so plugins
  initialise; tool descriptions and schemas are kept; tool params are no
  longer double-encoded.
- Plugins: data race (and possible nil-pointer panic) between `Close` and
  an in-flight `Recv`.
- `forge --version` can now be stamped at build time (the version was a
  const, which `-ldflags -X` cannot set).

### Added
- CI quality job: gofmt check, golangci-lint, race detector, coverage
  report; Docker image build.
- Dockerfile, Makefile, GoReleaser config, security policy, contributing
  guide, issue and PR templates, Dependabot and CodeQL.
- Apache-2.0 license.
