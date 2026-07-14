// Package core, context.go — system-prompt assembly per spec §2.5.
//
// build_system_context captures the runtime environment (platform,
// cwd, short git status) that every assistant turn needs to know
// about in order to make grounded decisions. build_user_context
// captures the current time and any project memory-file content
// (FORGE.md by convention, walked upward from the working dir).
// BuildSystemPrompt is the public entry point that composes
// everything into the final system string the query loop sends
// on the wire.
package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// build_system_context returns a short, structured "where you are"
// block: platform (OS+arch), working directory, optional short git
// status, and the last five git log --oneline lines. Skips the
// git section when the working dir is not inside a git repo.
//
// The output is rendered in a small, plain-text block — meant to
// be read by the model, not parsed. A future version may switch
// to a structured (YAML/JSON) block for easier consumption by
// structured-output providers, but the spec is currently
// language-agnostic and the model handles plain-text fine.
func build_system_context(workingDir string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Environment\n")
	fmt.Fprintf(&b, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if workingDir == "" {
		workingDir = "."
	}
	fmt.Fprintf(&b, "- Working directory: %s\n", workingDir)

	// Git status and log are best-effort. If `git` is not installed
	// or workingDir is not a repo, we silently omit that section
	// rather than spamming the prompt with errors.
	gitSection := buildGitSection(workingDir)
	if gitSection != "" {
		b.WriteString("\n")
		b.WriteString(gitSection)
	}

	return b.String()
}

// buildGitSection returns a two-line status + five-line log block
// for a git repo, or "" if workingDir isn't in one. All output is
// captured via short `git` invocations; nothing is mutated.
func buildGitSection(workingDir string) string {
	// Check repo first — cheap test, avoids the cost of running
	// status on a non-repo and printing confusing errors.
	if !isGitRepo(workingDir) {
		return ""
	}

	runIn := func(name string, args ...string) string {
		cmd := exec.Command(name, args...)
		cmd.Dir = workingDir
		// Hide stderr so a misconfigured git doesn't pollute the
		// prompt; we already verified we're in a repo above.
		cmd.Stderr = nil
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimRight(string(out), "\n")
	}

	var b strings.Builder
	status := runIn("git", "status", "-s")
	if status != "" {
		fmt.Fprintf(&b, "## Git status\n```\n%s\n```\n", status)
	}
	log := runIn("git", "log", "--oneline", "-n", "5")
	if log != "" {
		fmt.Fprintf(&b, "## Recent commits\n%s\n", log)
	}
	return b.String()
}

// isGitRepo returns true when workingDir is inside a git working
// tree. We use `git rev-parse --is-inside-work-tree` rather than
// looking for a .git directory so it handles worktrees and submodules
// correctly.
func isGitRepo(workingDir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = workingDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// build_user_context returns the current local time and the
// concatenated contents of every FORGE.md file found by walking
// upward from workingDir to the filesystem root, plus the global
// ~/.forge/FORGE.md if present.
//
// If skipMemoryFile is true, returns only the timestamp. The model
// can rely on the time being in every prompt.
func build_user_context(workingDir string, skipMemoryFile bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current local time: %s\n", time.Now().Format(time.RFC3339))

	if skipMemoryFile {
		return b.String()
	}

	files := findMemoryFiles(workingDir)
	if len(files) == 0 {
		return b.String()
	}

	b.WriteString("\n## Project instructions\n")
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			// If a file became unreadable between find and read,
			// skip it silently — a single broken file should not
			// block the rest of the prompt.
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n", p)
		b.Write(data)
		// Ensure file content is followed by a newline so the
		// next "###" header doesn't run into file content.
		if len(data) == 0 || data[len(data)-1] != '\n' {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// findMemoryFiles walks upward from workingDir collecting every
// FORGE.md found, then appends the global one at
// ~/.forge/FORGE.md if it exists. Order is: closest to workingDir
// first, then upward, then global. The walker stops at the
// filesystem root or after hitting a non-existent parent.
//
// On permission errors or non-existent intermediates we silently
// skip — only present+readable files are returned.
func findMemoryFiles(workingDir string) []string {
	if workingDir == "" {
		workingDir = "."
	}
	abs, err := filepath.Abs(workingDir)
	if err != nil {
		abs = workingDir
	}

	var found []string
	seen := make(map[string]bool)
	dir := abs
	for {
		candidate := filepath.Join(dir, MemoryFileName)
		if !seen[candidate] {
			seen[candidate] = true
			if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
				found = append(found, candidate)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // reached root
		}
		dir = parent
	}

	// Global override.
	if home, err := os.UserHomeDir(); err == nil {
		global := filepath.Join(home, ConfigDirName, MemoryFileName)
		if !seen[global] {
			if fi, err := os.Stat(global); err == nil && !fi.IsDir() {
				found = append(found, global)
			}
		}
	}
	return found
}

// BuildSystemPrompt assembles the final system prompt for a turn.
// Join order, highest priority last (so the model sees the most
// specific instructions closest to the end):
//
//  1. cfg.SystemPrompt — if set, REPLACES everything else
//     (full system-prompt override via -s / --system-prompt).
//  2. BaseSystemPrompt() — the compiled-in baseline.
//  3. build_system_context(workingDir) — environment block.
//  4. build_user_context(workingDir, cfg.SkipProjectMemoryFile) —
//     time + project memory files.
//  5. cfg.AppendSystemPrompt — appended verbatim at the end.
//
// Working-dir resolution: Config.WorkingDir wins; falls back to "."
// which build_system_context then resolves to the process cwd at
// the time the OS call is made.
func BuildSystemPrompt(cfg *Config, workingDir string) string {
	if cfg.SystemPrompt != "" {
		return cfg.SystemPrompt
	}
	wd := cfg.WorkingDir
	if wd == "" {
		wd = workingDir
	}
	if wd == "" {
		wd = "."
	}
	return strings.Join([]string{
		BaseSystemPrompt(),
		build_system_context(wd),
		build_user_context(wd, cfg.SkipProjectMemoryFile),
		cfg.AppendSystemPrompt,
	}, "\n")
}
