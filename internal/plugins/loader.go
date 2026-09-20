package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ArpitK24/forge/internal/core"
	"github.com/ArpitK24/forge/internal/tools"
)

// LoadPlugins discovers and loads all plugins from the configured
// plugin directories. Returns a slice of tools.Tool that can be
// merged with built-in and MCP tools.
//
// The plugin directory structure:
//
//	<config-dir>/plugins/
//	├── plugin-a/
//	│   ├── plugin.json
//	│   └── plugin-a.exe
//	└── plugin-b/
//	    ├── plugin.json
//	    └── plugin-b
//
// If a plugin fails to load, the error is logged (via the provided
// logger) but loading continues for other plugins.
func LoadPlugins(ctx context.Context, cfg *core.Config, log *slog.Logger) ([]tools.Tool, error) {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	dirs := pluginDirs(cfg)
	var allTools []tools.Tool

	for _, dir := range dirs {
		tools, err := loadPluginsFromDir(ctx, dir, log)
		if err != nil {
			log.Debug("plugin: load dir failed", "dir", dir, "err", err)
			continue
		}
		allTools = append(allTools, tools...)
	}

	return allTools, nil
}

// pluginDirs returns the list of directories to scan for plugins.
// Priority: Config.PluginDirs > default <config-dir>/plugins.
func pluginDirs(cfg *core.Config) []string {
	if cfg != nil && len(cfg.PluginDirs) > 0 {
		return cfg.PluginDirs
	}
	// Default: <config-dir>/plugins
	configDir, err := configDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(configDir, "plugins")}
}

// configDir returns the per-user config directory for this OS.
// Mirrors cli.ConfigDir() to avoid import cycles.
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, core.ConfigDirName), nil
}

// loadPluginsFromDir scans one directory for plugin subdirectories.
func loadPluginsFromDir(ctx context.Context, root string, log *slog.Logger) ([]tools.Tool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // no plugins dir is not an error
		}
		return nil, err
	}

	var tools []tools.Tool
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pluginDir := filepath.Join(root, e.Name())
		// Look for plugin.json
		manifestPath := filepath.Join(pluginDir, "plugin.json")
		manifest, err := readManifest(manifestPath)
		if err != nil {
			log.Debug("plugin: skip (no manifest)", "dir", pluginDir, "err", err)
			continue
		}

		// Resolve entrypoint path
		entrypoint := filepath.Join(pluginDir, manifest.Entrypoint)
		if runtime.GOOS == "windows" && !strings.HasSuffix(entrypoint, ".exe") {
			entrypoint += ".exe"
		}

		// Verify entrypoint exists
		if _, err := os.Stat(entrypoint); err != nil {
			log.Debug("plugin: skip (no entrypoint)", "dir", pluginDir, "err", err)
			continue
		}

		// Load the plugin
		pluginTools, err := loadPlugin(ctx, manifest, entrypoint, log)
		if err != nil {
			log.Debug("plugin: load failed", "name", manifest.Name, "err", err)
			continue
		}
		tools = append(tools, pluginTools...)
		log.Info("plugin: loaded", "name", manifest.Name, "tools", len(pluginTools))
	}

	return tools, nil
}

// PluginManifest is the plugin.json schema.
type PluginManifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Entrypoint  string   `json:"entrypoint"` // relative to plugin dir
	Args        []string `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

func readManifest(path string) (PluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PluginManifest{}, err
	}
	var m PluginManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return PluginManifest{}, fmt.Errorf("parse plugin.json: %w", err)
	}
	if strings.TrimSpace(m.Name) == "" {
		return PluginManifest{}, fmt.Errorf("plugin.json: missing name")
	}
	if strings.TrimSpace(m.Entrypoint) == "" {
		return PluginManifest{}, fmt.Errorf("plugin.json: missing entrypoint")
	}
	return m, nil
}

// loadPlugin spawns the plugin subprocess, calls initialize,
// and wraps the returned tools as tools.Tool.
func loadPlugin(ctx context.Context, manifest PluginManifest, entrypoint string, log *slog.Logger) ([]tools.Tool, error) {
	cfg := PluginConfig{
		Name:      manifest.Name,
		Path:      entrypoint,
		Args:      manifest.Args,
		Env:       manifest.Env,
	}
	transport := NewStdioTransport(cfg)
	cli := newClient(manifest.Name, transport, log)

	if err := cli.start(ctx); err != nil {
		return nil, fmt.Errorf("start transport: %w", err)
	}

	// Call initialize to get the tool list
	initResp, err := cli.call(ctx, MethodInitialize, initializeParams{})
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if initResp.Error != nil {
		cli.Close()
		return nil, fmt.Errorf("initialize error: %s", initResp.Error.Message)
	}

	// Parse the initialize response
	var initResult initializeResult
	if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
		cli.Close()
		return nil, fmt.Errorf("parse initialize result: %w", err)
	}

	// Wrap each tool
	var pluginTools []tools.Tool
	for _, ti := range initResult.Tools {
		tool := &PluginTool{
			client:    cli,
			plugin:    manifest.Name,
			name:      ti.Name,
			desc:      ti.Description,
			inputJSON: ti.InputSchema,
		}
		pluginTools = append(pluginTools, tool)
	}

	return pluginTools, nil
}

// initializeParams is the params for initialize request.
type initializeParams struct {
	ProtocolVersion string `json:"protocolVersion,omitempty"`
}

// initializeResult is the response from initialize.
type initializeResult struct {
	ProtocolVersion string      `json:"protocolVersion"`
	Capabilities    struct{}    `json:"capabilities"`
	ServerInfo      serverInfo  `json:"serverInfo"`
	Tools           []toolInfo  `json:"tools"`
}

// MethodInitialize is the JSON-RPC method for initialization.
const MethodInitialize = "initialize"

// MethodToolsCall is the JSON-RPC method for tool calls.
const MethodToolsCall = "tools/call"