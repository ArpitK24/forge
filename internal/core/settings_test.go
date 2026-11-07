package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSettingsSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	original := Settings{
		Model:          "meta/llama-3.3-70b-instruct",
		MaxTokens:      16384,
		PermissionMode: PermissionBypassPermissions,
		Verbose:        true,
		Theme:          "monokai",
	}
	autoCompact := false
	original.AutoCompact = &autoCompact

	saved, err := original.Save(path)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved != path {
		t.Errorf("Save returned %q, want %q", saved, path)
	}

	// File mode should be 0600 on platforms that support it
	// (Windows ignores the mode but the call should not fail).
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size() == 0 {
		t.Errorf("Save wrote empty file")
	}

	res, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if !reflect.DeepEqual(res.Settings, original) {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", res.Settings, original)
	}
}

func TestLoadSettingsMissingFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.json")

	res, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings on missing file: %v", err)
	}
	if res.Path != path {
		t.Errorf("Path = %q, want %q", res.Path, path)
	}
	want := DefaultSettings()
	if !reflect.DeepEqual(res.Settings, want) {
		t.Errorf("missing-file LoadSettings = %+v, want defaults %+v", res.Settings, want)
	}
}

func TestLoadSettingsMalformedReturnsTypedError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadSettings(path)
	if err == nil {
		t.Fatalf("LoadSettings on malformed JSON: expected error, got nil")
	}
	var ferr *Error
	if !errors.As(err, &ferr) {
		t.Fatalf("LoadSettings error is not *core.Error: %T %v", err, err)
	}
	if ferr.Kind != KindConfig {
		t.Errorf("error kind = %v, want KindConfig", ferr.Kind)
	}
}

func TestLoadSettingsEmptyFileReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if !reflect.DeepEqual(res.Settings, DefaultSettings()) {
		t.Errorf("empty file LoadSettings = %+v, want defaults", res.Settings)
	}
}

func TestLoadSettingsAutoCompactFalsePreserved(t *testing.T) {
	// A subtlety: AutoCompact is *bool, so a JSON false is
	// distinguishable from "unset" (nil pointer). The round-trip
	// must preserve that distinction.
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	raw := `{"auto_compact": false}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := LoadSettings(path)
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	if res.Settings.AutoCompact == nil {
		t.Fatalf("AutoCompact = nil, want non-nil pointer to false")
	}
	if *res.Settings.AutoCompact {
		t.Errorf("AutoCompact = true, want false")
	}
}

func TestSettingsToJSONIsStable(t *testing.T) {
	// Sanity check: ToJSON produces valid JSON that we can
	// re-parse without losing the AutoCompact pointer.
	s := DefaultSettings()
	data, err := s.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m["model"] != DefaultModel {
		t.Errorf("JSON model = %v, want %s", m["model"], DefaultModel)
	}
}

func TestLayerSettingsPriorityOrder(t *testing.T) {
	autoCompactFalse := false
	autoCompactTrue := true

	global := Settings{Model: "global-model", MaxTokens: 1024}
	project := Settings{Model: "project-model"}
	local := Settings{Theme: "local-theme"}
	managed := Settings{Model: "managed-model", AutoCompact: &autoCompactFalse}

	got := LayerSettings(managed, local, project, global)

	// Highest priority (managed) wins for Model.
	if got.Model != "managed-model" {
		t.Errorf("Model = %q, want managed-model (managed wins)", got.Model)
	}
	// Project overrides global for MaxTokens (managed didn't set it).
	if got.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024 (project over global)", got.MaxTokens)
	}
	// Local wins for Theme.
	if got.Theme != "local-theme" {
		t.Errorf("Theme = %q, want local-theme", got.Theme)
	}
	// Managed's auto_compact=false wins.
	if got.AutoCompact == nil || *got.AutoCompact != false {
		t.Errorf("AutoCompact = %v, want pointer to false", got.AutoCompact)
	}

	// When managed is empty, project wins for Model.
	got2 := LayerSettings(Settings{}, Settings{}, project, global)
	if got2.Model != "project-model" {
		t.Errorf("without managed, Model = %q, want project-model", got2.Model)
	}

	// And without any overlay, the global value comes through.
	got3 := LayerSettings(Settings{}, Settings{}, Settings{}, global)
	if got3.Model != "global-model" || got3.MaxTokens != 1024 {
		t.Errorf("global-only: %+v", got3)
	}

	// AutoCompact true at global level should also survive when
	// no higher layer overrides it.
	global.AutoCompact = &autoCompactTrue
	got4 := LayerSettings(Settings{}, Settings{}, Settings{}, global)
	if got4.AutoCompact == nil || !*got4.AutoCompact {
		t.Errorf("AutoCompact global true was lost: %v", got4.AutoCompact)
	}
}

func TestLayerSettingsZeroPermissionModeIsTreatedAsUnset(t *testing.T) {
	// PermissionDefault is the zero value, so overlaySettings
	// should NOT overwrite a more-specific mode with the default.
	project := Settings{PermissionMode: PermissionAcceptEdits}
	local := Settings{} // PermissionMode == PermissionDefault (zero)
	got := LayerSettings(Settings{}, local, project, Settings{})
	if got.PermissionMode != PermissionAcceptEdits {
		t.Errorf("PermissionMode = %v, want PermissionAcceptEdits "+
			"(zero-value local should not overwrite project)", got.PermissionMode)
	}
}

func TestReadSettingsFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope", "settings.json")
	s, found, err := ReadSettingsFile(missing)
	if err != nil || found || s.Model != "" || s.AutoCompact != nil {
		t.Errorf("missing file: s=%+v found=%v err=%v, want zero/false/nil", s, found, err)
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Errorf("ReadSettingsFile created the parent dir")
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, found, err = ReadSettingsFile(empty)
	if err != nil || !found || s.Model != "" {
		t.Errorf("empty file: s=%+v found=%v err=%v, want zero/true/nil", s, found, err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ReadSettingsFile(bad); err == nil {
		t.Errorf("malformed file: want error")
	} else {
		var ferr *Error
		if !errors.As(err, &ferr) || ferr.Kind != KindConfig {
			t.Errorf("malformed file: err = %v, want KindConfig", err)
		}
	}

	good := filepath.Join(dir, "good.json")
	if err := os.WriteFile(good, []byte(`{"model":"x","trusted_projects":["/a"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, found, err = ReadSettingsFile(good)
	if err != nil || !found || s.Model != "x" || len(s.TrustedProjects) != 1 {
		t.Errorf("good file: s=%+v found=%v err=%v", s, found, err)
	}
}

func TestLayerSettingsNeverOverlaysTrustedProjects(t *testing.T) {
	global := Settings{TrustedProjects: []string{"/home/me/work"}}
	project := Settings{TrustedProjects: []string{"/evil"}}
	got := LayerSettings(Settings{}, project, project, global)
	if len(got.TrustedProjects) != 1 || got.TrustedProjects[0] != "/home/me/work" {
		t.Errorf("TrustedProjects = %v, want only the global entry", got.TrustedProjects)
	}
}
