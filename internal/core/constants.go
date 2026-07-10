package core

// Constants centralized per spec §2.4: every magic string/number lives
// here, never inline. Tool name constants in particular — every tool
// implementation references these by name rather than a string literal.

// App identity.
const (
	// AppName is the product name as it appears in --version, the
	// settings directory name, the binary name, etc.
	AppName = "forge"
	// AppVersion is set at build time via -ldflags. Defaulted here for
	// dev builds. The release process overrides this.
	AppVersion = "0.0.1-dev"
	// AppTagline is the project's tagline. Used in --version output
	// and the first-run welcome banner.
	AppTagline = "Agentic coding, forged in your terminal."
)

// Model defaults. Per spec §2.4, we expose three tiers (cheap / default /
// smart) and let the user override per-invocation.
const (
	// DefaultModel is the model used when no other choice is specified.
	// Anthropic's current fast/cheap default at the time of writing.
	DefaultModel = "claude-sonnet-4-5"

	// FastModel is the cheap/fast tier. Used for things like the
	// summarize-head call in auto-compaction (§5.2) where we want
	// cost efficiency more than reasoning depth.
	FastModel = "claude-haiku-4-5"

	// SmartModel is the expensive/capable tier. The user can switch
	// to it via --model or /model.
	SmartModel = "claude-opus-4-7"
)

// Token defaults. Per spec §2.4.
const (
	// DefaultMaxTokens is the per-response output cap when the user
	// hasn't specified one.
	DefaultMaxTokens = 8192

	// MaxTokensHardCeiling is the upper limit we'll ever set, regardless
	// of user input. Prevents a runaway flag value from requesting
	// something the provider will reject.
	MaxTokensHardCeiling = 200_000
)

// Auto-compaction thresholds. Per spec §5.2.
const (
	// AutoCompactTriggerFraction is the fraction of the context window
	// at which auto-compaction kicks in. 0.90 = 90%.
	AutoCompactTriggerFraction = 0.90

	// AutoCompactReserveTokens is the headroom we leave after compacting
	// so the next user turn has room to land. ~13k tokens.
	AutoCompactReserveTokens = 13_000

	// AutoCompactWarningTokens is the remaining-headroom level at which
	// the TUI starts showing a warning. ~20k tokens.
	AutoCompactWarningTokens = 20_000

	// AutoCompactTailKeep is the number of most-recent messages we
	// keep verbatim, uncompacted, at the front of every compaction.
	AutoCompactTailKeep = 10

	// AutoCompactMaxConsecutiveFailures is the circuit-breaker threshold
	// for compaction: 3 failures in a row and the disabled flag trips.
	AutoCompactMaxConsecutiveFailures = 3
)

// Agent loop defaults. Per spec §5.1.
const (
	// DefaultMaxTurns is the cap on agentic turns per user prompt
	// (one turn = one request/response/tool-execution round, NOT
	// one user message). The user can override with --max-turns.
	DefaultMaxTurns = 10
)

// Provider wire format. Per spec §2.4 and §4.3.
const (
	// ProviderAnthropic is the first-party Anthropic Messages API.
	ProviderAnthropic = "anthropic"
	// ProviderOpenAI is the OpenAI Chat Completions API (also used for
	// any OpenAI-compatible endpoint).
	ProviderOpenAI = "openai"
	// ProviderGoogle is the Google Gemini API.
	ProviderGoogle = "google"
	// ProviderCopilot is the GitHub Copilot / IDE-subscription adapter.
	ProviderCopilot = "copilot"
	// ProviderCodex is the OAuth-based OpenAI Codex adapter.
	ProviderCodex = "codex"
	// ProviderFree is the zero-setup trial provider.
	ProviderFree = "free"
	// ProviderCustom is a user-supplied OpenAI-compatible endpoint.
	ProviderCustom = "custom"
	// ProviderMinimax is a representative independent model vendor
	// (per spec §4.3's "at least one additional independent model
	// vendor" requirement).
	ProviderMinimax = "minimax"

	// DefaultAPIBase is the default Anthropic API base URL.
	// Other providers override this per-adapter.
	DefaultAPIBase = "https://api.anthropic.com"

	// WireAPIVersion is the value sent in the Anthropic API version
	// header. Bump when we adopt a new wire feature.
	WireAPIVersion = "2023-06-01"
)

// Beta / experimental feature flag names. Per spec §2.4: "any
// beta/experimental feature flags sent as request headers." These
// are header names the api package passes to the Anthropic adapter
// when the relevant feature is enabled.
const (
	// BetaPromptCaching is the header name that enables prompt caching.
	BetaPromptCaching = "prompt-caching-2024-07-31"
	// BetaExtendedThinking is the header that enables extended thinking.
	BetaExtendedThinking = "thinking-2024-12-01"
	// BetaFastMode is the header for the (hypothetical) fast-mode flag.
	BetaFastMode = "fast-mode-2024-12-01"
)

// Filesystem layout. Per spec §2.4 and §14.
const (
	// ConfigDirName is the per-user config directory name.
	// On every platform: ~/.forge/ or %USERPROFILE%\.forge\
	ConfigDirName = ".forge"

	// ProjectConfigDirName is the per-project config directory name.
	ProjectConfigDirName = ".forge"

	// SettingsFilename is the persisted settings filename.
	SettingsFilename = "settings.json"

	// LocalSettingsFilename is the gitignored project-local override.
	LocalSettingsFilename = "settings.local.json"

	// HistoryFilename is the per-session conversation log file pattern
	// (see also ConversationDir).
	HistoryFilename = "history.jsonl"

	// MemoryFileName is the project memory-file convention
	// (analogous to CLAUDE.md / AGENTS.md). Per spec §2.5: walked
	// upward from the working directory.
	MemoryFileName = "FORGE.md"

	// ConversationsDir is the subdirectory of ConfigDirName that
	// holds session JSON files.
	ConversationsDir = "conversations"

	// MemoryDir is the subdirectory for long-term memory entries.
	MemoryDir = "memory"

	// PluginsDir is the subdirectory for installed plugins.
	PluginsDir = "plugins"

	// ScheduledTasksFile is the persistent cron job store.
	ScheduledTasksFile = "scheduled_tasks.json"

	// BinDir is the self-install location for the shell-script
	// installer (Unix). On Windows the equivalent is added to PATH
	// by the PowerShell installer.
	BinDir = "bin"
)

// Tool name constants. Per spec §2.4, every tool is referenced through
// a constant — never a string literal. Spec lists: Bash, Edit, Read,
// Write, Glob, Grep, WebFetch, WebSearch, NotebookEdit, Task (sub-agent),
// TodoWrite, AskUserQuestion, EnterPlanMode, ExitPlanMode, PowerShell,
// Sleep, CronCreate, CronDelete, CronList, EnterWorktree, ExitWorktree,
// ListMcpResources, ReadMcpResource, ToolSearch, Brief, Config, SendMessage,
// Skill, plus the extended set in §5 (ApplyPatch, BatchEdit, ComputerUse,
// LspTool, McpAuthTool, MonitorTool, PtyBash, RemoteTrigger, ReplTool,
// TeamTool, GoalComplete, SyntheticOutput).
const (
	ToolBash             = "Bash"
	ToolEdit             = "Edit"
	ToolRead             = "Read"
	ToolWrite            = "Write"
	ToolGlob             = "Glob"
	ToolGrep             = "Grep"
	ToolWebFetch         = "WebFetch"
	ToolWebSearch        = "WebSearch"
	ToolNotebookEdit     = "NotebookEdit"
	ToolTask             = "Task"
	ToolTodoWrite        = "TodoWrite"
	ToolAskUserQuestion  = "AskUserQuestion"
	ToolEnterPlanMode    = "EnterPlanMode"
	ToolExitPlanMode     = "ExitPlanMode"
	ToolPowerShell       = "PowerShell"
	ToolSleep            = "Sleep"
	ToolCronCreate       = "CronCreate"
	ToolCronDelete       = "CronDelete"
	ToolCronList         = "CronList"
	ToolEnterWorktree    = "EnterWorktree"
	ToolExitWorktree     = "ExitWorktree"
	ToolListMcpResources = "ListMcpResources"
	ToolReadMcpResource  = "ReadMcpResource"
	ToolToolSearch       = "ToolSearch"
	ToolBrief            = "Brief"
	ToolConfig           = "Config"
	ToolSendMessage      = "SendMessage"
	ToolSkill            = "Skill"
	// Extended set from §5.
	ToolApplyPatch      = "ApplyPatch"
	ToolBatchEdit       = "BatchEdit"
	ToolComputerUse     = "ComputerUse"
	ToolLspTool         = "LspTool"
	ToolMcpAuthTool     = "McpAuthTool"
	ToolMonitorTool     = "MonitorTool"
	ToolPtyBash         = "PtyBash"
	ToolRemoteTrigger   = "RemoteTrigger"
	ToolReplTool        = "ReplTool"
	ToolTeamTool        = "TeamTool"
	ToolGoalComplete    = "GoalComplete"
	ToolSyntheticOutput = "SyntheticOutput"
)

// Bash / PowerShell execution limits. Per spec §3.2.
const (
	// DefaultBashTimeoutSeconds is the default timeout for a Bash
	// tool call. 120s.
	DefaultBashTimeoutSeconds = 120
	// MaxBashTimeoutSeconds is the hard ceiling, regardless of what
	// the model requests. 600s.
	MaxBashTimeoutSeconds = 600
	// MaxBashOutputBytes is the truncation point for Bash output.
	// Past ~100k characters we return a truncation notice instead.
	MaxBashOutputBytes = 100_000
	// MaxSleepSeconds is the cap on the Sleep tool. 300s.
	MaxSleepSeconds = 300
)

// Read tool defaults.
const (
	// ReadDefaultLineLimit is the default number of lines returned
	// by a single Read call. 2000.
	ReadDefaultLineLimit = 2000
)

// Glob tool defaults.
const (
	// GlobMaxResults is the cap on results returned by a single Glob
	// call. 250.
	GlobMaxResults = 250
)

// Cron tool defaults.
const (
	// CronMaxJobs is the cap on concurrent scheduled tasks. 50.
	CronMaxJobs = 50
)

// Max-effort trigger keyword. Per spec §6.4.
const (
	// MaxEffortKeyword is the user-facing string the user types to
	// trigger the experimental "fullforge" mode. Stripped from the
	// prompt before sending but sets the turn's effort to the
	// experimental top tier and switches the agent's operating
	// discipline to plan → delegate → integrate → verify.
	MaxEffortKeyword = "fullforge"
)
