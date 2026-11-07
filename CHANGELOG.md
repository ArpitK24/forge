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
- OpenAI adapter: request `stream_options.include_usage` and read prompt
  tokens from the final usage chunk, so input tokens, cost and the
  auto-compaction trigger no longer read as zero on the default provider.
- Auto-compaction circuit breaker now trips: its state was recreated every
  turn, so a failing summarizer was retried on every turn forever. The
  state now lives for the whole loop, and `query.Config.CompactState` lets
  multi-call sessions share it.
- Compaction summaries (auto and `/compact`) now use the session's active
  model instead of a hard-coded NIM model that does not exist on
  Anthropic/OpenAI, so compaction no longer always fails off NIM.
  `query.Config.SummaryModel` can override it.
- Compaction no longer splits a tool call from its result: the head/tail
  cut moves earlier when the tail would start with a `tool_result`, which
  providers reject once its `tool_use` has been summarized.
- Denied and unknown tool calls are reported back to the model as
  `is_error` tool results ("permission denied" / "unknown tool") and the
  loop continues, as the README describes, instead of ending the session
  silently. `MaxTurns` still bounds a model that keeps retrying.

### Added
- CI quality job: gofmt check, golangci-lint, race detector, coverage
  report; Docker image build.
- Dockerfile, Makefile, GoReleaser config, security policy, contributing
  guide, issue and PR templates, Dependabot and CodeQL.
- Apache-2.0 license.
