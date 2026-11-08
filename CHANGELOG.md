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
- Default model: `meta/llama-3.3-70b-instruct` reached end of life on NVIDIA
  NIM on 2026-08-26 and returns HTTP 410, so a fresh install failed on its
  first request. The default is now `nvidia/nemotron-3.5-lightning-30b-a3b`;
  the fast and smart tiers move to `nvidia/nemotron-3.5-bolt-e4b` and
  `nvidia/nemotron-3-ultra-550b-a55b` (the old Llama 3.1 tiers and
  `openai/gpt-oss-120b` are also retired on NIM). The model table and
  pricing rows are updated to match.
- A retired (HTTP 410) or unavailable (HTTP 404) model now produces an
  actionable error naming the model and pointing at `--model` and the
  `model` settings key, with the provider's own explanation, instead of
  raw problem+json.
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
- Headless retry after a 429/529 resumes from the history the failed
  attempt reached and only re-issues the model request; it no longer
  restarts from the original prompt and re-runs tools that already ran
  (side effects twice, history lost). The json output's `error` field is
  cleared when the retry succeeds.

### Added
- CI quality job: gofmt check, golangci-lint, race detector, coverage
  report; Docker image build.
- Dockerfile, Makefile, GoReleaser config, security policy, contributing
  guide, issue and PR templates, Dependabot and CodeQL.
- Apache-2.0 license.
