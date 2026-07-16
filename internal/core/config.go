package core

import "encoding/json"

// PermissionMode is the global permission posture for the session.
// Spec §2.3: Default, AcceptEdits, BypassPermissions, Plan.
type PermissionMode int

const (
	// PermissionDefault auto-allows read-only tools and prompts for
	// everything else. The standard interactive experience.
	PermissionDefault PermissionMode = iota
	// PermissionAcceptEdits auto-allows file edits (Write, Edit, NotebookEdit)
	// but still prompts for shell execution and other side effects.
	PermissionAcceptEdits
	// PermissionBypassPermissions auto-allows every tool. Must be
	// explicitly and loudly opted into — typically via the
	// --dangerously-skip-permissions CLI flag. The TUI also shows an
	// extra-friction confirmation dialog when enabling this mode.
	PermissionBypassPermissions
	// PermissionPlan is read-only planning mode. Any mutating action
	// is denied. Used during /plan workflows.
	PermissionPlan
)

func (m PermissionMode) String() string {
	switch m {
	case PermissionDefault:
		return "default"
	case PermissionAcceptEdits:
		return "accept-edits"
	case PermissionBypassPermissions:
		return "bypass-permissions"
	case PermissionPlan:
		return "plan"
	default:
		return "unknown"
	}
}

// ParsePermissionMode parses a CLI flag value into a PermissionMode.
// Returns PermissionDefault and ok=false on unrecognized input.
func ParsePermissionMode(s string) (PermissionMode, bool) {
	switch s {
	case "default":
		return PermissionDefault, true
	case "accept-edits":
		return PermissionAcceptEdits, true
	case "bypass-permissions":
		return PermissionBypassPermissions, true
	case "plan":
		return PermissionPlan, true
	}
	return PermissionDefault, false
}

// OutputFormat is the CLI's output mode. Spec §2.3.
type OutputFormat int

const (
	// OutputText streams text to stdout. Default for interactive use
	// and for `forge -p "..."` one-shots.
	OutputText OutputFormat = iota
	// OutputJson buffers the entire turn and prints one final JSON
	// object. Useful for programmatic consumption.
	OutputJson
	// OutputStreamJson prints every internal event as one NDJSON line.
	// Spec §7.3: this is the integration point for driving Forge from
	// a parent process without using ACP.
	OutputStreamJson
)

func (f OutputFormat) String() string {
	switch f {
	case OutputText:
		return "text"
	case OutputJson:
		return "json"
	case OutputStreamJson:
		return "stream-json"
	default:
		return "text"
	}
}

// ParseOutputFormat parses a CLI flag value into an OutputFormat.
func ParseOutputFormat(s string) (OutputFormat, bool) {
	switch s {
	case "text":
		return OutputText, true
	case "json":
		return OutputJson, true
	case "stream-json":
		return OutputStreamJson, true
	}
	return OutputText, false
}

// HookEvent enumerates the lifecycle moments at which a hook can fire.
// Spec §2.3.
type HookEvent int

const (
	// HookPreToolUse fires before each tool call. Can block the call.
	HookPreToolUse HookEvent = iota
	// HookPostToolUse fires after each tool call. Observational only —
	// the tool result is already determined.
	HookPostToolUse
	// HookStop fires when the agent loop returns EndTurn.
	HookStop
	// HookUserPromptSubmit fires when the user submits a new prompt
	// (before the query loop sees it).
	HookUserPromptSubmit
	// HookNotification fires for arbitrary notifications the agent
	// wants to surface to the user or the parent process.
	HookNotification
)

func (e HookEvent) String() string {
	switch e {
	case HookPreToolUse:
		return "PreToolUse"
	case HookPostToolUse:
		return "PostToolUse"
	case HookStop:
		return "Stop"
	case HookUserPromptSubmit:
		return "UserPromptSubmit"
	case HookNotification:
		return "Notification"
	default:
		return "unknown"
	}
}

// HookEntry is a single hook configuration. Spec §2.3.
type HookEntry struct {
	// Command is the shell command to run when the hook fires.
	// The hook context is JSON on stdin (see §2.9).
	Command string `json:"command"`
	// ToolNameFilter, if non-empty, is a glob that must match the
	// current tool name for the hook to fire (only used for
	// PreToolUse / PostToolUse).
	ToolNameFilter string `json:"tool_name_filter,omitempty"`
	// Blocking: if true, a non-zero exit code is treated as a
	// Blocked outcome (PreToolUse only) or an error (other events).
	Blocking bool `json:"blocking,omitempty"`
}

// McpServerConfig describes a Model Context Protocol server to connect
// to at startup. Spec §2.3, fully exercised in §13.4.
type McpServerConfig struct {
	// Name is the server's local name. Used to namespace its tools
	// (e.g. "github__create_issue") and to address its resources.
	Name string `json:"name"`
	// Command is the executable to spawn (stdio transport).
	Command string `json:"command,omitempty"`
	// Args are the command-line arguments.
	Args []string `json:"args,omitempty"`
	// Env is extra environment variables to set for the child process.
	Env map[string]string `json:"env,omitempty"`
	// URL is set for SSE/HTTP transports instead of Command.
	URL string `json:"url,omitempty"`
	// ServerType is "stdio", "sse", or "http".
	ServerType string `json:"server_type,omitempty"`
}

// Config is the effective, resolved runtime configuration. It is
// assembled by the CLI from (in order of increasing priority):
//   1. Compiled-in defaults
//   2. Managed/enterprise settings (read-only, injected by admin)
//   3. Global settings.json (~/.forge/settings.json)
//   4. Project settings.json (.forge/settings.json)
//   5. Project-local settings.json (.forge/settings.local.json)
//   6. CLI flag overrides
//
// Per spec §2.3, Config is "runtime, layered." The loader in
// internal/cli (Phase 1) builds it; later phases add the layer-merging
// logic in core.
type Config struct {
	// Provider is the active provider id, e.g. "anthropic", "openai",
	// "ollama". Empty means "use the default provider."
	Provider string `json:"provider,omitempty"`

	// APIKey is the explicit key. If empty, the provider's environment
	// variable is consulted (resolve_api_key, spec §2.3).
	APIKey string `json:"api_key,omitempty"`

	// APIBase is the API base URL override. If empty, the active
	// provider's default is used. Spec §2.3: env var overrides this
	// only for some providers.
	APIBase string `json:"api_base,omitempty"`

	// Model is the model id to send in every request. Empty means
	// "use the provider's default model" (see constants.go).
	Model string `json:"model,omitempty"`

	// MaxTokens is the per-response output token cap. 0 means "use
	// the provider/model default."
	MaxTokens int `json:"max_tokens,omitempty"`

	// PermissionMode is the active permission posture.
	PermissionMode PermissionMode `json:"permission_mode"`

	// Verbose enables debug-level logging. CLI: -v / --verbose.
	Verbose bool `json:"verbose,omitempty"`

	// OutputFormat is the CLI's output mode. CLI: --output-format.
	OutputFormat OutputFormat `json:"output_format"`

	// MaxTurns is the cap on agentic turns. CLI: --max-turns.
	// Spec §5.1 default is 10.
	MaxTurns int `json:"max_turns,omitempty"`

	// SystemPrompt, if non-empty, REPLACES the default system prompt.
	// CLI: -s / --system-prompt.
	SystemPrompt string `json:"system_prompt,omitempty"`

	// AppendSystemPrompt is appended to the default system prompt.
	// CLI: --append-system-prompt.
	AppendSystemPrompt string `json:"append_system_prompt,omitempty"`

	// SkipProjectMemoryFile disables §2.5's FORGE.md discovery.
	// CLI: --no-project-memory.
	SkipProjectMemoryFile bool `json:"skip_project_memory_file,omitempty"`

	// AutoCompact enables §5.2's auto-compaction. CLI: --no-auto-compact
	// flips it off (default is on).
	AutoCompact bool `json:"auto_compact"`

	// ThinkingBudget is the extended-thinking token budget, 0 = off.
	// Not directly a CLI flag in §7.1 — driven by /effort and /thinking.
	ThinkingBudget int `json:"thinking_budget,omitempty"`

	// McpServers is the list of MCP server configs to connect at
	// startup. CLI: --mcp-config (path to a JSON file with this list).
	McpServers []McpServerConfig `json:"mcp_servers,omitempty"`

	// Hooks maps each event to its list of hook entries.
	Hooks map[HookEvent][]HookEntry `json:"hooks,omitempty"`

	// WorkingDir is the effective working directory. CLI: --cwd.
	WorkingDir string `json:"working_dir,omitempty"`
}

// EffectiveModel returns the model to use, falling back to the default
// model constant if Config.Model is empty.
func (c *Config) EffectiveModel() string {
	if c.Model != "" {
		return c.Model
	}
	return DefaultModel
}

// EffectiveMaxTokens returns the per-response output token cap,
// falling back to DefaultMaxTokens if Config.MaxTokens is zero.
// Caps at MaxTokensHardCeiling.
func (c *Config) EffectiveMaxTokens() int {
	if c.MaxTokens <= 0 {
		return DefaultMaxTokens
	}
	if c.MaxTokens > MaxTokensHardCeiling {
		return MaxTokensHardCeiling
	}
	return c.MaxTokens
}

// EffectiveMaxTurns returns the per-session cap on agentic turns,
// falling back to DefaultMaxTurns if Config.MaxTurns is zero.
// Spec §5.1 default is 10.
func (c *Config) EffectiveMaxTurns() int {
	if c.MaxTurns > 0 {
		return c.MaxTurns
	}
	return DefaultMaxTurns
}

// Settings is the persisted, user-facing preferences file.
// Spec §2.3: load() returns defaults if the file is missing; save()
// creates parent directories as needed.
//
// In Phase 1 we don't load or save anything — this is just the shape.
// The persistence layer is wired up in a later phase.
type Settings struct {
	// Model is the user's preferred model id.
	Model string `json:"model,omitempty"`
	// MaxTokens overrides the default.
	MaxTokens int `json:"max_tokens,omitempty"`
	// PermissionMode is the user's preferred default.
	PermissionMode PermissionMode `json:"permission_mode,omitempty"`
	// Verbose default.
	Verbose bool `json:"verbose,omitempty"`
	// AutoCompact default.
	AutoCompact *bool `json:"auto_compact,omitempty"`
	// Theme name (referenced by tui, not used in Phase 1).
	Theme string `json:"theme,omitempty"`
	// McpServers is the list of MCP servers the user wants connected
	// by default (separate from per-session Config.McpServers which
	// can override per-invocation).
	McpServers []McpServerConfig `json:"mcp_servers,omitempty"`
	// PermissionRules is the persisted record of "always allow" / "always deny"
	// decisions from the TUI permission dialog.
	PermissionRules []PermissionRule `json:"permission_rules,omitempty"`
}

// PermissionRule is a persisted "always allow/deny for this tool" rule.
// Keyed by tool name and optional argument pattern (glob).
type PermissionRule struct {
	// Tool is the tool name (e.g. "Bash", "Edit").
	Tool string `json:"tool"`
	// ArgPattern is an optional glob to match against the tool input.
	// Empty means "match this tool regardless of arguments."
	ArgPattern string `json:"arg_pattern,omitempty"`
	// Decision is Allow or Deny.
	Decision PermissionDecision `json:"decision"`
}

// DefaultSettings returns a Settings populated with safe defaults.
func DefaultSettings() Settings {
	autoCompact := true
	return Settings{
		Model:        DefaultModel,
		MaxTokens:    DefaultMaxTokens,
		AutoCompact:  &autoCompact,
		PermissionMode: PermissionDefault,
	}
}

// ToJSON renders the settings as indented JSON for display in /config.
func (s Settings) ToJSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}
