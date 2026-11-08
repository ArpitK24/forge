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
//  1. parse flags -> ToConfig() (CLI overrides on a fresh Config)
//  2. LoadLayeredSettings() (apply file defaults, with project
//     and local overrides on top of the global file)
//  3. ApplySettings() (fill the still-zero fields of cfg)
package cli

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

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

// LayeredSettings is the result of LoadLayered: the merged settings
// plus any warnings the caller should show the user (keys that were
// ignored because they came from an untrusted project file).
type LayeredSettings struct {
	Settings core.Settings
	Warnings []string
}

// untrustedKeys are the settings keys a project-level file
// (.forge/settings.json or .forge/settings.local.json) may only set
// when the project is listed in the global trusted_projects. Each
// one either widens what tools may run without a prompt or launches
// a process at startup. trusted_projects itself is never honoured
// from a project file (see core.LayerSettings).
var untrustedKeys = []string{"permission_mode", "mcp_servers", "permission_rules", "trusted_projects"}

// LoadLayeredSettings loads and merges the settings layers and
// returns just the merged Settings. Callers that can show warnings
// should use LoadLayered instead.
func LoadLayeredSettings(workingDir string) (core.Settings, error) {
	res, err := LoadLayered(workingDir)
	return res.Settings, err
}

// LoadLayered loads settings files in priority order
// (managed > local > project > global) and merges them with
// core.LayerSettings. The "managed" layer is reserved for Phase 4;
// it's always the zero Settings for now.
//
// Trust model: ~/.forge/settings.json belongs to the user and is
// trusted. The two project files live inside the repository, so
// anyone who can commit to it controls them (settings.local.json
// is only gitignored by convention). Unless the project's absolute
// path is listed in the global "trusted_projects", the
// security-sensitive keys (untrustedKeys) are dropped from both
// project files and a warning naming them is returned. Everything
// else (model, max_tokens, theme, verbose, auto_compact) still
// applies.
//
// A missing file is fine and contributes nothing to the merge. A
// malformed file or a real I/O error is returned. (Silent
// fallbacks would mask real problems.) Loading never creates
// .forge/ inside the project.
func LoadLayered(workingDir string) (LayeredSettings, error) {
	globalRes, err := core.LoadSettings("")
	if err != nil {
		return LayeredSettings{}, err
	}
	global := globalRes.Settings
	out := LayeredSettings{}

	var project, local core.Settings
	if workingDir != "" {
		if abs, err := filepath.Abs(workingDir); err == nil {
			workingDir = abs
		}
		trusted := isTrustedProject(workingDir, global.TrustedProjects)
		for _, layer := range []struct {
			name string
			dst  *core.Settings
		}{
			{core.SettingsFilename, &project},
			{core.LocalSettingsFilename, &local},
		} {
			path := filepath.Join(workingDir, core.ProjectConfigDirName, layer.name)
			// Running from $HOME: the "project" file is the
			// global file. Don't load it twice (or distrust it).
			if samePath(path, globalRes.Path) {
				continue
			}
			s, found, err := core.ReadSettingsFile(path)
			if err != nil {
				return LayeredSettings{}, err
			}
			if !found {
				continue
			}
			if !trusted {
				if ignored := stripUntrusted(&s); len(ignored) > 0 {
					out.Warnings = append(out.Warnings, fmt.Sprintf(
						"ignoring %s from %s: project settings can't set these unless %q is listed in \"trusted_projects\" in %s",
						strings.Join(ignored, ", "), path, workingDir, globalRes.Path))
				}
			}
			*layer.dst = s
		}
	}

	out.Settings = core.LayerSettings(core.Settings{}, local, project, global)
	return out, nil
}

// stripUntrusted clears the security-sensitive keys on s and
// returns the JSON names of the ones that were set.
func stripUntrusted(s *core.Settings) []string {
	var ignored []string
	for _, key := range untrustedKeys {
		set := false
		switch key {
		case "permission_mode":
			set = s.PermissionMode != core.PermissionDefault
			s.PermissionMode = core.PermissionDefault
		case "mcp_servers":
			set = len(s.McpServers) > 0
			s.McpServers = nil
		case "permission_rules":
			set = len(s.PermissionRules) > 0
			s.PermissionRules = nil
		case "trusted_projects":
			set = len(s.TrustedProjects) > 0
			s.TrustedProjects = nil
		}
		if set {
			ignored = append(ignored, key)
		}
	}
	return ignored
}

// isTrustedProject reports whether dir is listed in trusted.
// Entries must be absolute paths; relative entries are ignored
// rather than resolved against an attacker-chosen cwd. Paths are
// compared after cleaning and resolving symlinks (and
// case-insensitively on Windows).
func isTrustedProject(dir string, trusted []string) bool {
	for _, t := range trusted {
		if t == "" || !filepath.IsAbs(t) {
			continue
		}
		if samePath(dir, t) {
			return true
		}
	}
	return false
}

// samePath reports whether a and b name the same location.
func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = canonicalPath(a), canonicalPath(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func canonicalPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	return filepath.Clean(p)
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
	// PermissionMode: an explicit --permission-mode (or
	// --dangerously-skip-permissions) always wins, even when it
	// asked for "default". Otherwise a non-default settings value
	// fills in. (Project files only reach here with a
	// permission_mode when the project is trusted; see LoadLayered.)
	if !cfg.PermissionModeFromFlag &&
		cfg.PermissionMode == core.PermissionDefault &&
		settings.PermissionMode != core.PermissionDefault {
		cfg.PermissionMode = settings.PermissionMode
	}
	// Verbose: a true settings value flips cfg on only when
	// the user didn't pass -v.
	if !cfg.Verbose && settings.Verbose {
		cfg.Verbose = true
	}
	// AutoCompact defaults to ON in ToConfig and the only CLI flag
	// (--no-auto-compact) turns it OFF. So settings may only turn
	// it off ("auto_compact": false); they never turn it back on,
	// which is what used to make --no-auto-compact a dead flag.
	if settings.AutoCompact != nil && !*settings.AutoCompact {
		cfg.AutoCompact = false
	}
	// ContextEngine is off by default and --context-engine only
	// turns it on, so settings may only turn it on too.
	if settings.ContextEngine != nil && *settings.ContextEngine {
		cfg.ContextEngine = true
	}
	// McpServers: ToConfig fills cfg.McpServers when --mcp-config
	// was passed; settings.McpServers fills the gap when the CLI
	// flag is absent. The CLI flag always wins — if the user
	// already passed --mcp-config, we leave cfg alone. Per spec
	// §2.3 the layered order is "CLI flag > settings.json > no
	// servers", so this branch only fires when cfg.McpServers is
	// nil.
	if len(cfg.McpServers) == 0 && len(settings.McpServers) > 0 {
		cfg.McpServers = settings.McpServers
	}
	return cfg
}
