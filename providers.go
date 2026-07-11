package llmproviders

import (
	"context"
	"fmt"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	agycli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/agycli"
	claudecodeadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	codexcli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	cursorcli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	geminicli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/geminicli"
	picli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/picli"
)

// ForceCompleteCodingAgentTmuxSession asks a tmux-backed coding adapter to
// finish its current wait loop on the next poll and return the pane's current
// response through the normal generation path.
func ForceCompleteCodingAgentTmuxSession(sessionName string) bool {
	return tmuxcontrol.RequestForceComplete(sessionName)
}

// Provider represents the available LLM providers
type Provider string

const (
	ProviderBedrock           Provider = "bedrock"
	ProviderOpenAI            Provider = "openai"
	ProviderAnthropic         Provider = "anthropic"
	ProviderOpenRouter        Provider = "openrouter"
	ProviderVertex            Provider = "vertex"
	ProviderAzure             Provider = "azure"
	ProviderZAI               Provider = "z-ai"
	ProviderKimi              Provider = "kimi"
	ProviderClaudeCode        Provider = "claude-code"
	ProviderGeminiCLI         Provider = "gemini-cli"
	ProviderCodexCLI          Provider = "codex-cli"
	ProviderCursorCLI         Provider = "cursor-cli"
	ProviderAgyCLI            Provider = "agy-cli"
	ProviderPiCLI             Provider = "pi-cli"
	ProviderMiniMax           Provider = "minimax"
	ProviderMiniMaxCodingPlan Provider = "minimax-coding-plan"
	ProviderElevenLabs        Provider = "elevenlabs"
	ProviderDeepgram          Provider = "deepgram"

	DefaultCodexCLIModel  = "high"
	DefaultCursorCLIModel = "composer-2.5"
	DefaultAgyCLIModel    = "agy-cli"
	DefaultPiCLIModel     = picli.DefaultModelID

	// EnvClaudeCodeTransport selects the Claude Code provider transport.
	// Supported normal value: "tmux" for Claude Code TUI mode.
	EnvClaudeCodeTransport = "CLAUDE_CODE_TRANSPORT"
	// EnvClaudeCodeMode is kept as a compatibility alias for older deployments.
	EnvClaudeCodeMode = "CLAUDE_CODE_MODE"

	ClaudeCodeTransportTmux = "tmux"
	// ClaudeCodeTransportExperimental is kept as a legacy alias.
	// Deprecated: use ClaudeCodeTransportTmux.
	ClaudeCodeTransportExperimental = "experimental"
	// ClaudeCodeTransportPrint is retained only so callers get a clear
	// unsupported-transport error instead of an unknown symbol during migration.
	//
	// Deprecated: Claude Code uses tmux only.
	ClaudeCodeTransportPrint = "print"
)

// SetCodingAgentTmuxSize records the operator's last-known terminal viewport
// (cols × rows) for any newly-launched coding-agent tmux session (Claude
// Code, Codex CLI, Cursor CLI, Gemini CLI, Agy CLI, Pi CLI). Pass <=0 for either
// axis to clear that axis's override and fall back to the env/default value.
// Sizes outside the package's safe band are clamped, not rejected.
func SetCodingAgentTmuxSize(columns, rows int) {
	tmuxsize.SetPreferredSize(columns, rows)
}

// CleanupClaudeCodeTmuxSessions removes Claude Code tmux sessions
// registered by this process. It intentionally does not kill every tmux session
// with the provider prefix, because other backend processes/tests may own live
// coding-agent sessions in the same user tmux server.
func CleanupClaudeCodeTmuxSessions(ctx context.Context) error {
	return claudecodeadapter.CleanupClaudeCodeTmuxSessions(ctx)
}

// CleanupCodexCLIInteractiveSessions removes Codex CLI tmux sessions registered
// by this process.
func CleanupCodexCLIInteractiveSessions(ctx context.Context) error {
	return codexcli.CleanupCodexCLIInteractiveSessions(ctx)
}

// CleanupCursorCLIInteractiveSessions removes Cursor CLI tmux sessions
// registered by this process.
func CleanupCursorCLIInteractiveSessions(ctx context.Context) error {
	return cursorcli.CleanupCursorCLIInteractiveSessions(ctx)
}

// CleanupAgyCLIInteractiveSessions removes Antigravity CLI tmux sessions
// registered by this process.
func CleanupAgyCLIInteractiveSessions(ctx context.Context) error {
	return agycli.CleanupAgyCLIInteractiveSessions(ctx)
}

// CleanupPiCLIInteractiveSessions removes Pi CLI tmux sessions registered by
// this process.
func CleanupPiCLIInteractiveSessions(ctx context.Context) error {
	return picli.CleanupPiCLIInteractiveSessions(ctx)
}

// CleanupGeminiCLIInteractiveSessions removes Gemini CLI tmux sessions
// registered by this process.
func CleanupGeminiCLIInteractiveSessions(ctx context.Context) error {
	return geminicli.CleanupGeminiCLIInteractiveSessions(ctx)
}

// interactiveSessionPrefixes are the tmux session-name prefixes used by every
// coding-agent CLI transport. Kept in sync with each adapter's
// <provider>InteractiveSessionPrefix() default.
var interactiveSessionPrefixes = []string{
	"mlp-gemini-cli-int",
	"mlp-agy-cli-int",
	"mlp-pi-cli-int",
	"mlp-codex-cli-int",
	"mlp-cursor-cli-int",
	"mlp-claude-code",
}

// SweepOrphanedInteractiveTmuxSessions reaps and kills every coding-agent tmux
// session (and its orphaned process tree) left over from a PREVIOUS backend run.
// It finds sessions by name prefix rather than the in-process registries, which
// are empty on a fresh boot — so this is the recovery path for sessions stranded
// by an ungraceful exit (crash, SIGKILL, sleep). Returns the count swept.
//
// CAUTION: prefix matching also catches sessions owned by a concurrent backend
// or test sharing the same tmux server. Call this ONLY at single-instance
// startup (e.g. the desktop app), never from a context where another owner may
// hold live sessions.
func SweepOrphanedInteractiveTmuxSessions(ctx context.Context) int {
	return tmuxcontrol.SweepInteractiveSessions(ctx, interactiveSessionPrefixes)
}

// Close*InteractiveSessionForOwner force-closes the persistent CLI
// session for the given owner. Callers reach for these when something
// about the session must change in a way the running CLI process can't
// adopt mid-flight — most concretely a workshop-mode switch that
// rewrites the agent's system prompt. The CLI process loaded its
// prompt at launch time and won't re-read the rule file, so the
// orchestrator tears down the persistent session here and the next
// turn relaunches with the new content.
//
// Calls are no-ops when no session is registered for the owner.

func CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	agycli.CloseAgyCLIInteractiveSessionForOwner(ownerSessionID, reason)
}

func ClosePiCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	picli.ClosePiCLIInteractiveSessionForOwner(ownerSessionID, reason)
}

func CloseCursorCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	cursorcli.CloseCursorCLIInteractiveSessionForOwner(ownerSessionID, reason)
}

func CloseGeminiCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	geminicli.CloseGeminiCLIInteractiveSessionForOwner(ownerSessionID, reason)
}

func CloseCodexCLIInteractiveSessionForOwner(ownerSessionID, reason string) {
	codexcli.CloseCodexCLIInteractiveSessionForOwner(ownerSessionID, reason)
}

func CloseClaudeCodeInteractiveSessionForOwner(ownerSessionID, reason string) {
	claudecodeadapter.CloseClaudeCodeInteractiveSessionForOwner(ownerSessionID, reason)
}

// CloseXxxCLIInteractiveSessionByTmux variants tear down a tmux-backed coding
// CLI session by its tmux session name rather than by owner key. They run the
// same provider-specific graceful exit + cleanup as the owner-keyed closes,
// and are used as a teardown backstop when the owning session ID is unknown or
// has drifted (e.g. workflow sub-agents registered under a step-execution
// owner). All are no-ops when no live session matches the tmux name.

func CloseAgyCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	agycli.CloseAgyCLIInteractiveSessionByTmux(tmuxSessionName, reason)
}

func ClosePiCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	picli.ClosePiCLIInteractiveSessionByTmux(tmuxSessionName, reason)
}

func CloseCursorCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	cursorcli.CloseCursorCLIInteractiveSessionByTmux(tmuxSessionName, reason)
}

func CloseGeminiCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	geminicli.CloseGeminiCLIInteractiveSessionByTmux(tmuxSessionName, reason)
}

func CloseCodexCLIInteractiveSessionByTmux(tmuxSessionName, reason string) {
	codexcli.CloseCodexCLIInteractiveSessionByTmux(tmuxSessionName, reason)
}

func CloseClaudeCodeInteractiveSessionByTmux(tmuxSessionName, reason string) {
	claudecodeadapter.CloseClaudeCodeInteractiveSessionByTmux(tmuxSessionName, reason)
}

// SendClaudeCodeInput sends user input to a live Claude Code tmux session
// registered for the owning application session.
func SendClaudeCodeInput(ctx context.Context, sessionID, message string) error {
	return claudecodeadapter.SendClaudeCodeInput(ctx, sessionID, message)
}

// SendCodexCLIInteractiveInput sends user input to a live Codex CLI interactive
// tmux session registered for the owning application session.
func SendCodexCLIInteractiveInput(ctx context.Context, sessionID, message string) error {
	return codexcli.SendCodexInteractiveInput(ctx, sessionID, message)
}

// SendCursorCLIInteractiveInput sends user input to a live Cursor CLI
// interactive tmux session registered for the owning application session.
func SendCursorCLIInteractiveInput(ctx context.Context, sessionID, message string) error {
	return cursorcli.SendCursorInteractiveInput(ctx, sessionID, message)
}

// SendAgyCLIInteractiveInput sends user input to a live Antigravity CLI
// interactive tmux session registered for the owning application session.
func SendAgyCLIInteractiveInput(ctx context.Context, sessionID, message string) error {
	return agycli.SendAgyInteractiveInput(ctx, sessionID, message)
}

// SendPiCLIInteractiveInput sends user input to a live Pi CLI interactive tmux
// session registered for the owning application session.
func SendPiCLIInteractiveInput(ctx context.Context, sessionID, message string) error {
	return picli.SendPiInteractiveInput(ctx, sessionID, message)
}

// SendGeminiCLIInteractiveInput sends user input to a live Gemini CLI
// interactive tmux session registered for the owning application session.
func SendGeminiCLIInteractiveInput(ctx context.Context, sessionID, message string) error {
	return geminicli.SendGeminiInteractiveInput(ctx, sessionID, message)
}

// SendClaudeCodeControlKey injects a tmux control key (e.g. "Escape", "C-c")
// into a registered Claude Code tmux session.
func SendClaudeCodeControlKey(ctx context.Context, sessionID, key string) error {
	return claudecodeadapter.SendClaudeCodeControlKey(ctx, sessionID, key)
}

// SendCodexCLIInteractiveControlKey injects a tmux control key into a
// registered Codex CLI interactive session.
func SendCodexCLIInteractiveControlKey(ctx context.Context, sessionID, key string) error {
	return codexcli.SendCodexInteractiveControlKey(ctx, sessionID, key)
}

// SendCursorCLIInteractiveControlKey injects a tmux control key into a
// registered Cursor CLI interactive session.
func SendCursorCLIInteractiveControlKey(ctx context.Context, sessionID, key string) error {
	return cursorcli.SendCursorInteractiveControlKey(ctx, sessionID, key)
}

// SendAgyCLIInteractiveControlKey injects a tmux control key into a registered
// Antigravity CLI interactive session.
func SendAgyCLIInteractiveControlKey(ctx context.Context, sessionID, key string) error {
	return agycli.SendAgyInteractiveControlKey(ctx, sessionID, key)
}

// SendPiCLIInteractiveControlKey injects a tmux control key into a registered
// Pi CLI interactive session.
func SendPiCLIInteractiveControlKey(ctx context.Context, sessionID, key string) error {
	return picli.SendPiInteractiveControlKey(ctx, sessionID, key)
}

// SendGeminiCLIInteractiveControlKey injects a tmux control key into a
// registered Gemini CLI interactive session.
func SendGeminiCLIInteractiveControlKey(ctx context.Context, sessionID, key string) error {
	return geminicli.SendGeminiInteractiveControlKey(ctx, sessionID, key)
}

// Config holds configuration for LLM initialization
type Config struct {
	Provider    Provider
	ModelID     string
	Temperature float64
	// EventEmitter for emitting LLM events (replaces Tracers)
	EventEmitter interfaces.EventEmitter
	TraceID      interfaces.TraceID
	// Fallback configuration for rate limiting
	FallbackModels []string
	MaxRetries     int
	// Logger for structured logging
	Logger interfaces.Logger
	// Context for LLM initialization (optional, uses background with timeout if not provided)
	Context context.Context
	// API keys for providers (optional, falls back to environment variables if not provided)
	APIKeys *ProviderAPIKeys
	// ClaudeCodeTransport optionally overrides CLAUDE_CODE_TRANSPORT for this
	// initialized Claude Code model. Default is "tmux" (the interactive TUI
	// transport). The old "print" / `claude -p` stream-json transport is no
	// longer supported.
	ClaudeCodeTransport string
}

// ProviderAPIKeys holds API keys for different providers
type ProviderAPIKeys struct {
	OpenRouter        *string
	OpenAI            *string
	Anthropic         *string
	Vertex            *string
	GeminiCLI         *string
	CodexCLI          *string
	CursorCLI         *string
	AgyCLI            *string
	PiCLI             *string
	MiniMax           *string
	MiniMaxCodingPlan *string
	ElevenLabs        *string
	Deepgram          *string
	ZAI               *string
	Kimi              *string
	// PiProviderKeys stores Pi sub-provider API keys keyed by Pi provider id
	// (for example "google", "zai", "zai-coding-cn", "deepseek").
	PiProviderKeys map[string]string
	Bedrock        *BedrockConfig
	Azure          *AzureAPIConfig
}

// AzureAPIConfig holds Azure-specific configuration
type AzureAPIConfig struct {
	Endpoint   string // Azure AI endpoint URL
	APIKey     string // Azure API key
	APIVersion string // API version (optional, defaults to 2024-10-21)
	Region     string // Azure region (optional, for logging)
}

// BedrockConfig holds Bedrock-specific configuration
type BedrockConfig struct {
	Region string
}

// SetKeyForProvider sets the API key for a given provider.
// Use this instead of switch statements to avoid missing new providers.
func (k *ProviderAPIKeys) SetKeyForProvider(provider Provider, key *string) {
	switch provider {
	case ProviderOpenRouter:
		k.OpenRouter = key
	case ProviderOpenAI:
		k.OpenAI = key
	case ProviderAnthropic:
		k.Anthropic = key
	case ProviderVertex:
		k.Vertex = key
	case ProviderGeminiCLI:
		k.GeminiCLI = key
	case ProviderCodexCLI:
		k.CodexCLI = key
	case ProviderCursorCLI:
		k.CursorCLI = key
	case ProviderAgyCLI:
		k.AgyCLI = key
	case ProviderPiCLI:
		k.PiCLI = key
	case ProviderMiniMax:
		k.MiniMax = key
	case ProviderMiniMaxCodingPlan:
		k.MiniMaxCodingPlan = key
	case ProviderElevenLabs:
		k.ElevenLabs = key
	case ProviderDeepgram:
		k.Deepgram = key
	case ProviderZAI:
		k.ZAI = key
	case ProviderKimi:
		k.Kimi = key
	}
}

// Clone returns a deep copy of ProviderAPIKeys.
// Use this instead of field-by-field copies to avoid missing new fields.
func (k *ProviderAPIKeys) Clone() *ProviderAPIKeys {
	if k == nil {
		return nil
	}
	out := *k // shallow copy — all *string fields are immutable, so sharing is safe
	if k.Bedrock != nil {
		b := *k.Bedrock
		out.Bedrock = &b
	}
	if k.Azure != nil {
		a := *k.Azure
		out.Azure = &a
	}
	if k.PiProviderKeys != nil {
		out.PiProviderKeys = make(map[string]string, len(k.PiProviderKeys))
		for provider, key := range k.PiProviderKeys {
			out.PiProviderKeys[provider] = key
		}
	}
	return &out
}

// InitializeLLM creates and initializes an LLM based on the provider configuration
func InitializeLLM(config Config) (llmtypes.Model, error) {
	var llm llmtypes.Model
	var err error

	switch config.Provider {
	case ProviderBedrock:
		llm, err = initializeBedrockWithFallback(config)
	case ProviderOpenAI:
		llm, err = initializeOpenAIWithFallback(config)
	case ProviderAnthropic:
		llm, err = initializeAnthropic(config)
	case ProviderOpenRouter:
		llm, err = initializeOpenRouterWithFallback(config)
	case ProviderVertex:
		llm, err = initializeVertexWithFallback(config)
	case ProviderAzure:
		llm, err = initializeAzureWithFallback(config)
	case ProviderZAI:
		llm, err = initializeZAIWithFallback(config)
	case ProviderKimi:
		llm, err = initializeKimi(config)
	case ProviderClaudeCode:
		llm, err = initializeClaudeCode(config)
	case ProviderGeminiCLI:
		llm, err = initializeGeminiCLI(config)
	case ProviderCodexCLI:
		llm, err = initializeCodexCLI(config)
	case ProviderCursorCLI:
		llm, err = initializeCursorCLI(config)
	case ProviderAgyCLI:
		llm, err = initializeAgyCLI(config)
	case ProviderPiCLI:
		llm, err = initializePiCLI(config)
	case ProviderMiniMax:
		llm, err = initializeMiniMax(config)
	case ProviderMiniMaxCodingPlan:
		llm, err = initializeMiniMaxCodingPlan(config)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", config.Provider)
	}

	if err != nil {
		return nil, err
	}

	// Wrap the LLM with provider information and tracing
	return NewProviderAwareLLM(llm, config.Provider, config.ModelID, config.EventEmitter, config.TraceID, config.Logger), nil
}

// InitializeEmbeddingModel creates and initializes an embedding model based on the provider configuration
// Supported providers: OpenAI, OpenRouter, Vertex AI, Bedrock
