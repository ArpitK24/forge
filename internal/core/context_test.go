package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemContextIncludesPlatformAndCwd(t *testing.T) {
	// Create a real temp dir to act as workingDir; not a git repo
	// (we don't init one here), so the git section is omitted.
	dir := t.TempDir()

	ctx := build_system_context(dir)
	if !strings.Contains(ctx, "Platform:") {
		t.Errorf("system context missing platform: %q", ctx)
	}
	if !strings.Contains(ctx, dir) {
		t.Errorf("system context missing working dir %q: %q", dir, ctx)
	}
}

func TestBuildSystemContextIncludesGit(t *testing.T) {
	// Build a temp git repo with one commit so the git section has
	// something to report. Skips the test if `git` is not on PATH.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available on PATH; skipping git-section test")
	}
	_ = gitPath

	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Configure user so commit works in a fresh repo.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=forge-test",
			"GIT_AUTHOR_EMAIL=forge@test.local",
			"GIT_COMMITTER_NAME=forge-test",
			"GIT_COMMITTER_EMAIL=forge@test.local",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "first commit")

	ctx := build_system_context(dir)
	if !strings.Contains(ctx, "Git status") {
		// Empty-status repos legitimately omit the status block;
		// the commit log must be present though.
		if !strings.Contains(ctx, "Recent commits") {
			t.Errorf("git section missing entirely from system context: %q", ctx)
		}
	}
	if !strings.Contains(ctx, "first commit") {
		t.Errorf("expected commit message in system context: %q", ctx)
	}
}

func TestBuildUserContextRespectsSkipMemory(t *testing.T) {
	dir := t.TempDir()
	// Place a FORGE.md in the temp dir; build_user_context should
	// find it unless skipMemoryFile is true.
	if err := os.WriteFile(filepath.Join(dir, MemoryFileName),
		[]byte("PROJECT RULE: use tabs"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	withFile := build_user_context(dir, false)
	if !strings.Contains(withFile, "PROJECT RULE: use tabs") {
		t.Errorf("expected memory content in user context: %q", withFile)
	}

	skipped := build_user_context(dir, true)
	if strings.Contains(skipped, "PROJECT RULE: use tabs") {
		t.Errorf("skipMemoryFile=true should omit memory file content: %q", skipped)
	}
	// But the timestamp line should still be there in both cases.
	if !strings.Contains(withFile, "Current local time:") {
		t.Errorf("expected timestamp in user context: %q", withFile)
	}
	if !strings.Contains(skipped, "Current local time:") {
		t.Errorf("expected timestamp even when memory skipped: %q", skipped)
	}
}

func TestFindMemoryFilesWalksUpward(t *testing.T) {
	// Simulate a layout:
	//   <tmp>/outer/FORGE.md
	//   <tmp>/outer/inner/
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.MkdirAll(inner, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outer, MemoryFileName),
		[]byte("outer rules"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := findMemoryFiles(inner)
	if len(files) != 1 {
		t.Fatalf("findMemoryFiles = %d files, want 1: %v", len(files), files)
	}
	if !strings.HasSuffix(files[0], filepath.Join("outer", MemoryFileName)) {
		t.Errorf("findMemoryFiles returned %q, want suffix .../outer/FORGE.md", files[0])
	}
}

func TestBuildSystemPromptReplacesOnOverride(t *testing.T) {
	cfg := &Config{SystemPrompt: "REPLACED"}
	got := BuildSystemPrompt(cfg, "/tmp")
	if got != "REPLACED" {
		t.Errorf("BuildSystemPrompt with override = %q, want REPLACED", got)
	}
}

func TestBuildSystemPromptJoinsInOrder(t *testing.T) {
	cfg := &Config{
		AppendSystemPrompt: "EXTRA-AT-END",
	}
	prompt := BuildSystemPrompt(cfg, t.TempDir())
	// We can't pin the middle of the prompt exactly (it contains
	// timestamps and platform info that vary), but we can assert
	// the structural order: baseline first, extra last.
	baseIdx := strings.Index(prompt, AppTagline)
	extraIdx := strings.Index(prompt, "EXTRA-AT-END")
	if baseIdx < 0 || extraIdx < 0 {
		t.Fatalf("missing required fragments: base=%d extra=%d in %q",
			baseIdx, extraIdx, prompt)
	}
	if baseIdx >= extraIdx {
		t.Errorf("baseline should appear before append; got base=%d extra=%d",
			baseIdx, extraIdx)
	}
}
