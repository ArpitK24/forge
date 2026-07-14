// Package core, prompt.go — the compiled-in baseline system prompt.
//
// Per spec §7.4: "Compile a short baseline prompt directly into the
// binary (not loaded from disk at runtime) establishing the agent's
// identity and core behavioral guidelines." This file is where it
// lives, so cmd/forge/main.go and any test can both reference it
// without duplicating the string.
package core

import "strings"

// BaseSystemPrompt returns the compiled-in baseline system prompt.
// It is the foundation of every system prompt Forge sends; the
// per-session BuildSystemPrompt (§2.5) concatenates this with the
// platform/cwd/git context block, the user/project memory file
// contents, and the user's --append-system-prompt override.
func BaseSystemPrompt() string {
	return strings.Join([]string{
		"You are " + AppName + ", " + AppTagline,
		"",
		"Behavioral guidelines:",
		"- Read files before editing them.",
		"- Prefer editing existing files over creating new ones.",
		"- Write idiomatic, clean code that matches the surrounding style.",
		"- Run tests after making changes when tests exist.",
		"- Consult git log/diff for repository context before assuming.",
		"- Be concise. Do not narrate your work.",
		"- Never introduce security vulnerabilities.",
		"- Produce production-quality output, not sketches.",
		"- When invoking tools, prefer the most specific tool for the job.",
		"",
	}, "\n")
}
