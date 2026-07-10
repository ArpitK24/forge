package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

func TestParseBasicFlags(t *testing.T) {
	p := NewParser("forge", []string{
		"-p", "-m", "claude-opus-4-7", "--max-turns", "5", "hello world",
	})
	a, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !a.Print {
		t.Errorf("Print = false, want true")
	}
	if a.Model != "claude-opus-4-7" {
		t.Errorf("Model = %q, want claude-opus-4-7", a.Model)
	}
	if a.MaxTurns != 5 {
		t.Errorf("MaxTurns = %d, want 5", a.MaxTurns)
	}
	if a.PositionalPrompt != "hello world" {
		t.Errorf("PositionalPrompt = %q, want %q", a.PositionalPrompt, "hello world")
	}
}

func TestParsePermissionMode(t *testing.T) {
	cases := []struct {
		flag string
		want core.PermissionMode
	}{
		{"--permission-mode=default", core.PermissionDefault},
		{"--permission-mode=accept-edits", core.PermissionAcceptEdits},
		{"--permission-mode=plan", core.PermissionPlan},
		{"--permission-mode=bypass-permissions", core.PermissionBypassPermissions},
	}
	for _, tc := range cases {
		t.Run(tc.flag, func(t *testing.T) {
			p := NewParser("forge", []string{tc.flag, "-p"})
			a, err := p.Parse()
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if a.PermissionMode != tc.want {
				t.Errorf("PermissionMode = %v, want %v", a.PermissionMode, tc.want)
			}
			if !a.PermissionModeSet {
				t.Errorf("PermissionModeSet = false, want true")
			}
		})
	}
}

func TestDangerouslySkipPermissions(t *testing.T) {
	p := NewParser("forge", []string{"--dangerously-skip-permissions", "-p"})
	a, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if a.PermissionMode != core.PermissionBypassPermissions {
		t.Errorf("PermissionMode = %v, want bypass", a.PermissionMode)
	}
	if !a.PermissionModeSet {
		t.Errorf("PermissionModeSet = false, want true")
	}
}

func TestInvalidFlags(t *testing.T) {
	cases := [][]string{
		{"--permission-mode=nonsense"},
		{"--output-format=yaml"},
		{"--max-turns=0"},
		{"--max-turns=abc"},
		{"--max-tokens=-1"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			p := NewParser("forge", args)
			_, err := p.Parse()
			if err == nil {
				t.Errorf("expected error for %v, got nil", args)
			}
		})
	}
}

func TestToConfig(t *testing.T) {
	p := NewParser("forge", []string{
		"-p",
		"-m", "claude-haiku-4-5",
		"--max-tokens", "4096",
		"--max-turns", "20",
		"--permission-mode", "accept-edits",
		"--cwd", "/tmp/work",
		"--no-project-memory",
		"--no-auto-compact",
		"--api-key", "sk-test",
	})
	a, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := a.ToConfig()
	if c.Model != "claude-haiku-4-5" {
		t.Errorf("Config.Model = %q", c.Model)
	}
	if c.MaxTokens != 4096 {
		t.Errorf("Config.MaxTokens = %d", c.MaxTokens)
	}
	if c.MaxTurns != 20 {
		t.Errorf("Config.MaxTurns = %d", c.MaxTurns)
	}
	if c.PermissionMode != core.PermissionAcceptEdits {
		t.Errorf("Config.PermissionMode = %v", c.PermissionMode)
	}
	if c.WorkingDir != "/tmp/work" {
		t.Errorf("Config.WorkingDir = %q", c.WorkingDir)
	}
	if !c.SkipProjectMemoryFile {
		t.Errorf("Config.SkipProjectMemoryFile = false, want true")
	}
	if c.AutoCompact {
		t.Errorf("Config.AutoCompact = true, want false (--no-auto-compact)")
	}
	if c.APIKey != "sk-test" {
		t.Errorf("Config.APIKey = %q", c.APIKey)
	}
}

func TestToConfigDefaults(t *testing.T) {
	// No flags → sensible defaults.
	p := NewParser("forge", []string{"-p", "hi"})
	a, err := p.Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := a.ToConfig()
	if c.PermissionMode != core.PermissionDefault {
		t.Errorf("default PermissionMode = %v, want %v", c.PermissionMode, core.PermissionDefault)
	}
	if c.OutputFormat != core.OutputText {
		t.Errorf("default OutputFormat = %v, want %v", c.OutputFormat, core.OutputText)
	}
	if !c.AutoCompact {
		t.Errorf("default AutoCompact = false, want true")
	}
	if c.SkipProjectMemoryFile {
		t.Errorf("default SkipProjectMemoryFile = true, want false")
	}
}

func TestOutputFormatFlag(t *testing.T) {
	for _, want := range []string{"text", "json", "stream-json"} {
		t.Run(want, func(t *testing.T) {
			p := NewParser("forge", []string{"--output-format", want, "-p"})
			a, err := p.Parse()
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !a.OutputFormatSet {
				t.Errorf("OutputFormatSet = false")
			}
			if a.OutputFormat.String() != want {
				t.Errorf("OutputFormat = %v, want %v", a.OutputFormat.String(), want)
			}
		})
	}
}

func TestConfigDirResolves(t *testing.T) {
	// We don't pin a specific path (it's OS-dependent) but we require
	// the function to succeed and return a non-empty, absolute path
	// ending in .forge.
	dir, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if dir == "" {
		t.Fatalf("ConfigDir returned empty string")
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("ConfigDir = %q, want absolute path", dir)
	}
	if filepath.Base(dir) != core.ConfigDirName {
		t.Errorf("ConfigDir base = %q, want %q", filepath.Base(dir), core.ConfigDirName)
	}
}

func TestUsageContains(t *testing.T) {
	u := Usage("forge")
	// Spot-check that the most important flags are documented.
	mustContain := []string{
		"-p, --print",
		"--permission-mode",
		"--resume",
		"--max-turns",
		"--system-prompt",
		"--append-system-prompt",
		"--no-project-memory",
		"--output-format",
		"--cwd",
		"--dangerously-skip-permissions",
		"--dump-system-prompt",
		"--mcp-config",
		"--no-auto-compact",
		"--version",
		"--help",
		"FORGE_API_KEY",
	}
	for _, s := range mustContain {
		if !strings.Contains(u, s) {
			t.Errorf("Usage output missing %q", s)
		}
	}
}
