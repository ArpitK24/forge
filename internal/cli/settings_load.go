// Package cli, settings_load.go — load the user's persisted
// settings.json and layer them under CLI-flag-only config.
//
// The settings file at ~/.forge/settings.json (with project-local
// overrides at .forge/settings.json and .forge/settings.local.json)
// is the place the user expresses their *defaults*: preferred
// model, max tokens, default permission mode, etc. CLI flags
// always win over settings — the spec is "CLI flag > settings.json
// > compiled-in default."
//
// We deliberately do NOT have Args.ToConfig read the file itself:
// that would couple the pure parsing path to the filesystem and
// make the function hard to test. main.go orchestrates the order:
//   1. parse flags -> ToConfig() (CLI overrides on a fresh Config)
//   2. LoadLayeredSettings() (apply file defaults, with project
//      and local overrides on top of the global file)
//   3. ApplySettings() (fill the still-zero fields of cfg)
package cli

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/ArpitK24/forge/internal/core"
)

// LoadSettings is a thin wrapper around core.LoadSettings that
// returns just the Settings. A missing file is not an error;
// only a malformed file or a real I/O error is.
//
// Phase-2 convenience for callers that only care about the
// global file. The full layered loader is LoadLayeredSettings
// below.
func LoadSettings() (core.Settings, error) {
	res, err := core.LoadSettings("")
	if err != nil {
		return core.Settings{}, err
	}
	return res.Settings, nil
}

// LoadLayeredSettings loads settings files in priority order
// (managed > local > project > global) and merges them with
// LayerSettings. The "managed" layer is reserved for Phase 4;
// in Phase 2 it's always the zero Settings.
//
// A missing file is fine. A malformed file or a real I/O error
// is returned. (Silent fallbacks would mask real problems.)
func LoadLayeredSettings(workingDir string) (core.Settings, error) {
	globalRes, err := core.LoadSettings("")
	if err != nil {
		return core.Settings{}, err
	}
	global := globalRes.Settings

	var project, local core.Settings
	if workingDir != "" {
		projPath := filepath.Join(workingDir, core.ProjectConfigDirName, core.SettingsFilename)
		if projRes, err := core.LoadSettings(projPath); err == nil {
			project = projRes.Settings
		} else if isRealLoadError(err) {
			return core.Settings{}, err
		}

		localPath := filepath.Join(workingDir, core.ProjectConfigDirName, core.LocalSettingsFilename)
		if localRes, err := core.LoadSettings(localPath); err == nil {
			local = localRes.Settings
		} else if isRealLoadError(err) {
			return core.Settings{}, err
		}
	}

	return core.LayerSettings(core.Settings{}, local, project, global), nil
}

// isRealLoadError reports whether err from core.LoadSettings is
// a genuine failure (malformed JSON, I/O error) rather than the
// benign "file does not exist" case. We classify by the
// typed *core.Error's Kind: KindIO with an IsNotExist underlying
// cause is "missing file" (benign); anything else is real.
func isRealLoadError(err error) bool {
	if err == nil {
		return false
	}
	var ferr *core.Error
	if !errors.As(err, &ferr) {
		// Unknown error type — treat as real to be safe.
		return true
	}
	if ferr.Kind == core.KindIO && ferr.Cause != nil {
		if os.IsNotExist(ferr.Cause) {
			return false
		}
	}
	return true
}

// ApplySettings copies settings.json values into any still-zero
// fields of cfg. The contract is: CLI flags that have been
// explicitly set always win; settings.json is the fallback.
//
// "Explicitly set" is determined by zero-checking the field on
// the input Config. Boolean fields are tricky — see the
// PermissionMode and AutoCompact cases below for the
// Phase-2 compromise and the Phase-3 path forward.
func ApplySettings(cfg *core.Config, settings core.Settings) *core.Config {
	if cfg == nil {
		return nil
	}
	// Model.
	if cfg.Model == "" && settings.Model != "" {
		cfg.Model = settings.Model
	}
	// MaxTokens.
	if cfg.MaxTokens == 0 && settings.MaxTokens != 0 {
		cfg.MaxTokens = settings.MaxTokens
	}
	// PermissionMode: ToConfig fills cfg.PermissionMode with
	// PermissionDefault if the user didn't pass --permission-mode.
	// We treat that as "the user expressed no preference" and
	// let the settings value win when it is non-default.
	if cfg.PermissionMode == core.PermissionDefault &&
		settings.PermissionMode != core.PermissionDefault {
		cfg.PermissionMode = settings.PermissionMode
	}
	// Verbose: a true settings value flips cfg on only when
	// the user didn't pass -v.
	if !cfg.Verbose && settings.Verbose {
		cfg.Verbose = true
	}
	// AutoCompact: settings can turn it ON when the user didn't
	// pass --no-auto-compact. We never use settings to turn it
	// OFF, because Config.AutoCompact is a value (not a pointer)
	// and we can't distinguish "user didn't pass" from "user
	// passed --no-auto-compact" once the Config is built. Phase
	// 3's TUI exposes a /auto-compact pointer toggle and
	// resolves the ambiguity.
	if !cfg.AutoCompact && settings.AutoCompact != nil && *settings.AutoCompact {
		cfg.AutoCompact = true
	}
	return cfg
}
