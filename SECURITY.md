# Security Policy

Forge is a coding agent: it reads your files, edits code, and runs shell
commands on your machine. We take security reports seriously.

## Supported versions

Only the latest release receives security fixes while Forge is pre-1.0.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Report privately through GitHub's
[private vulnerability reporting](https://github.com/ArpitK24/forge/security/advisories/new),
or email **arpitkohale2@gmail.com** with "Forge security" in the subject.

Include:

- what you found and its impact,
- steps to reproduce (a minimal repo or prompt helps),
- the Forge version (`forge --version`) and OS.

You'll get an acknowledgement within 72 hours and a status update within
7 days. Once a fix ships, we'll credit you in the release notes unless you
prefer otherwise.

## In scope

- Tool calls escaping the permission model (e.g. a command running without
  the approval it requires)
- Path traversal outside the working directory through file tools
- Leaking API keys or session contents (logs, crash output, session files)
- Prompt-injection paths that trigger unapproved writes or commands
- MCP server or plugin handling that lets a server or plugin exceed its
  intended capabilities

## Out of scope

- Actions you explicitly approved
- Vulnerabilities in third-party MCP servers or plugins themselves
- Model output quality
