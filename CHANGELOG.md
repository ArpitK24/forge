# Changelog

All notable changes to Forge are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Forge uses
[Semantic Versioning](https://semver.org).

## [Unreleased]

### Fixed
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
