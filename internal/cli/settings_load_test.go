package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ArpitK24/forge/internal/core"
)

func TestApplySettingsFillsEmptyFields(t *testing.T) {
	cfg := &core.Config{} // everything zero
	settings := core.Settings{
		Model:          "meta/llama-3.3-70b-instruct",
		MaxTokens:      16384,
		PermissionMode: core.PermissionAcceptEdits,
		Verbose:        true,
	}
	got := ApplySettings(cfg, settings)
	if got.Model != "meta/llama-3.3-70b-instruct" {
		t.Errorf("Model = %q, want from settings", got.Model)
	}
	if got.MaxTokens != 16384 {
		t.Errorf("MaxTokens = %d, want 16384", got.MaxTokens)
	}
	if got.PermissionMode != core.PermissionAcceptEdits {
		t.Errorf("PermissionMode = %v, want AcceptEdits", got.PermissionMode)
	}
	if !got.Verbose {
		t.Errorf("Verbose = false, want true from settings")
	}
}

func TestApplySettingsCLIFlagsWin(t *testing.T) {
	// Simulate the user passing --model and --max-tokens on the CLI:
	// those are non-zero on the input Config and must NOT be
	// overwritten by settings.json values.
	cfg := &core.Config{
		Model:     "different-model",
		MaxTokens: 4096,
	}
	settings := core.Settings{
		Model:     "meta/llama-3.3-70b-instruct",
		MaxTokens: 16384,
	}
	got := ApplySettings(cfg, settings)
	if got.Model != "different-model" {
		t.Errorf("Model = %q, want CLI value (different-model)", got.Model)
	}
	if got.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want CLI value (4096)", got.MaxTokens)
	}
}

func TestApplySettingsZeroSettingsLeavesCfgUnchanged(t *testing.T) {
	cfg := &core.Config{
		Model:          "explicit",
		PermissionMode: core.PermissionPlan,
	}
	settings := core.Settings{} // zero
	got := ApplySettings(cfg, settings)
	if got.Model != "explicit" {
		t.Errorf("Model = %q, want unchanged", got.Model)
	}
	if got.PermissionMode != core.PermissionPlan {
		t.Errorf("PermissionMode = %v, want unchanged Plan", got.PermissionMode)
	}
}

func TestApplySettingsPermissionModeDefaultDoesNotOverride(t *testing.T) {
	// The user passed --permission-mode=default explicitly OR didn't
	// pass it; either way cfg.PermissionMode == PermissionDefault.
	// A settings file with PermissionDefault should not "override"
	// the cfg (which is already Default).
	cfg := &core.Config{PermissionMode: core.PermissionDefault}
	settings := core.Settings{PermissionMode: core.PermissionDefault}
	got := ApplySettings(cfg, settings)
	if got.PermissionMode != core.PermissionDefault {
		t.Errorf("PermissionMode = %v, want Default", got.PermissionMode)
	}
}

func TestApplySettingsNilCfg(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("ApplySettings(nil) panicked: %v", r)
		}
	}()
	got := ApplySettings(nil, core.Settings{})
	if got != nil {
		t.Errorf("ApplySettings(nil) = %+v, want nil", got)
	}
}

func TestLoadSettingsMissingFileIsNotAnError(t *testing.T) {
	// Run with HOME pointing at an empty dir so core.LoadSettings("")
	// sees no file. Both HOME (Unix) and USERPROFILE (Windows) are
	// overridden so the test works on any platform.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)
	settings, err := LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings with empty home: %v", err)
	}
	if settings.Model != core.DefaultModel {
		t.Errorf("Model = %q, want default %q", settings.Model, core.DefaultModel)
	}
}

func TestLoadLayeredSettingsMergesInOrder(t *testing.T) {
	// Set up a fake home with a global settings file, and a
	// fake project dir with a project + local file.
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	globalDir := filepath.Join(home, core.ConfigDirName)
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, core.SettingsFilename),
		[]byte(`{"model": "global-model", "max_tokens": 1024}`), 0600); err != nil {
		t.Fatalf("write global: %v", err)
	}

	projectForge := filepath.Join(projectDir, core.ProjectConfigDirName)
	if err := os.MkdirAll(projectForge, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectForge, core.SettingsFilename),
		[]byte(`{"model": "project-model"}`), 0600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectForge, core.LocalSettingsFilename),
		[]byte(`{"theme": "local-theme"}`), 0600); err != nil {
		t.Fatalf("write local: %v", err)
	}

	// Run with the fake home so core.LoadSettings("") hits our file.
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	settings, err := LoadLayeredSettings(projectDir)
	if err != nil {
		t.Fatalf("LoadLayeredSettings: %v", err)
	}
	if settings.Model != "project-model" {
		t.Errorf("Model = %q, want project-model", settings.Model)
	}
	if settings.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024 from global", settings.MaxTokens)
	}
	if settings.Theme != "local-theme" {
		t.Errorf("Theme = %q, want local-theme", settings.Theme)
	}
}

func TestLoadLayeredSettingsMalformedProjectReturnsError(t *testing.T) {
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	projectForge := filepath.Join(projectDir, core.ProjectConfigDirName)
	if err := os.MkdirAll(projectForge, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectForge, core.SettingsFilename),
		[]byte("{not valid json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	_, err := LoadLayeredSettings(projectDir)
	if err == nil {
		t.Fatalf("LoadLayeredSettings: expected error from malformed project file")
	}
	var ferr *core.Error
	if !errors.As(err, &ferr) {
		t.Fatalf("error is not *core.Error: %T %v", err, err)
	}
	if ferr.Kind != core.KindConfig {
		t.Errorf("error kind = %v, want KindConfig", ferr.Kind)
	}
}
