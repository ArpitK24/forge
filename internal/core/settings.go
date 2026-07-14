// Package core, settings.go — the persistence layer for the user's
// preferences file at ~/.forge/settings.json (with project-level
// overrides at .forge/settings.json and .forge/settings.local.json
// stacked on top, per spec §2.3).
//
// Phase 2 ships only the Load/Save primitives and the
// LayerSettings() merger. The CLI layer (internal/cli) is the only
// caller in Phase 2.
package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SettingsLoadResult is what Settings.Load returns. The Settings
// field is always populated — either with the parsed contents or
// with DefaultSettings() if the file is missing or empty. The
// Path field is the resolved file path, for logging/diagnostics.
// The CreatedParent field is true if Load() had to create the
// parent directory (so callers can warn the user about a new
// config dir, if they want to).
type SettingsLoadResult struct {
	// Settings is the populated value (never nil).
	Settings Settings
	// Path is the absolute path the settings were loaded from.
	Path string
	// CreatedParent is true if the parent directory had to be
	// created (a fresh install). Always false on a missing file
	// in an existing config dir.
	CreatedParent bool
}

// Load reads the settings file at the given path. Behavior:
//
//   - If path is empty, ~/.forge/settings.json is used.
//   - If the file does not exist, DefaultSettings() is returned
//     with no error (the absence of a settings file is not a
//     failure).
//   - If the file exists but contains malformed JSON, a
//     *Error{Kind: KindConfig} is returned with the parse error
//     wrapped in the Cause. Callers should surface this to the
//     user — silently falling back to defaults would mask real
//     problems.
//   - If a non-existent parent directory is encountered, it is
//     created (with mode 0755) so the file can be saved later.
//
// The function is safe to call repeatedly; it never panics and
// never modifies the file.
func LoadSettings(path string) (SettingsLoadResult, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return SettingsLoadResult{}, fmt.Errorf("resolve home: %w", err)
		}
		path = filepath.Join(home, ConfigDirName, SettingsFilename)
	}

	res := SettingsLoadResult{Path: path}

	// Make sure the parent dir exists; the user can save into it
	// later without a separate mkdir step.
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); os.IsNotExist(err) {
		if err := os.MkdirAll(parent, 0755); err != nil {
			return res, Wrap(KindIO, err, "create settings parent dir")
		}
		res.CreatedParent = true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			res.Settings = DefaultSettings()
			return res, nil
		}
		return res, Wrap(KindIO, err, "read settings file")
	}

	if len(data) == 0 {
		res.Settings = DefaultSettings()
		return res, nil
	}

	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return res, Wrap(KindConfig, err, "parse settings.json")
	}
	res.Settings = s
	return res, nil
}

// Save writes the settings to the given path. Creates parent
// directories as needed. The file is written with mode 0600
// (owner read/write only) since it may contain API keys in
// later phases.
//
// If path is empty, ~/.forge/settings.json is used.
//
// Returns a *Error on failure. On success, path is returned for
// caller convenience.
func (s Settings) Save(path string) (string, error) {
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		path = filepath.Join(home, ConfigDirName, SettingsFilename)
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return "", Wrap(KindIO, err, "create settings parent dir")
	}
	data, err := s.ToJSON()
	if err != nil {
		return "", Wrap(KindConfig, err, "marshal settings")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", Wrap(KindIO, err, "write settings file")
	}
	return path, nil
}

// LayerSettings merges a chain of settings into one effective
// Settings, where the *first* argument wins for any field it
// populates. The order is highest-to-lowest priority: managed
// (admin-injected) > local (gitignored) > project (committed) >
// global (~/.forge/settings.json).
//
// The managed layer is reserved for Phase 4 (enterprise
// deployment); the Phase 2 call sites pass nil for it.
//
// "First wins" semantics: the function is a simple non-nil-overrides
// nil loop. We treat an empty Model as "not set" (because "" is
// the zero value for that field) but a 0 MaxTokens as "use default"
// rather than as "explicitly zero." The latter case is resolved by
// the *Config methods (EffectiveMaxTokens, etc.) so LayerSettings
// itself stays dumb.
func LayerSettings(managed, local, project, global Settings) Settings {
	// Start with the lowest-priority layer; each higher layer
	// overlays fields that are non-zero.
	out := global
	overlaySettings(&out, project)
	overlaySettings(&out, local)
	if !settingsIsEmpty(managed) {
		overlaySettings(&out, managed)
	}
	return out
}

// settingsIsEmpty reports whether a Settings is the zero value.
// We can't compare Settings structs directly because they contain
// slices (which are not comparable with ==), so we check each
// field by hand. Used by LayerSettings to decide whether a
// particular layer was supplied.
//
// An empty string, zero int, zero value-type enum, and nil
// slices/pointers are all "empty."
func settingsIsEmpty(s Settings) bool {
	if s.Model != "" ||
		s.MaxTokens != 0 ||
		s.PermissionMode != 0 ||
		s.Verbose ||
		s.Theme != "" ||
		s.AutoCompact != nil ||
		len(s.McpServers) != 0 ||
		len(s.PermissionRules) != 0 {
		return false
	}
	return true
}

// overlaySettings copies non-zero fields from src onto dst. The
// "is it set" check is field-specific because the zero value
// sometimes carries meaning (e.g. an explicit empty API key
// should NOT overwrite a real one).
func overlaySettings(dst *Settings, src Settings) {
	if src.Model != "" {
		dst.Model = src.Model
	}
	if src.MaxTokens != 0 {
		dst.MaxTokens = src.MaxTokens
	}
	// PermissionMode is a value type, not a pointer, so we cannot
	// distinguish "unset" from "default" by zero-check. We treat
	// the zero value (PermissionDefault = 0) as "unset" because
	// the default is also the result of zero; this is fine for
	// Phase 2 and will be revisited when managed settings ship.
	if src.PermissionMode != 0 {
		dst.PermissionMode = src.PermissionMode
	}
	if src.Verbose {
		dst.Verbose = true
	}
	if src.AutoCompact != nil {
		dst.AutoCompact = src.AutoCompact
	}
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if len(src.McpServers) > 0 {
		dst.McpServers = src.McpServers
	}
	if len(src.PermissionRules) > 0 {
		dst.PermissionRules = src.PermissionRules
	}
}
