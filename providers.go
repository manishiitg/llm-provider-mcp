package llmproviders

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxcontrol"
	"github.com/manishiitg/multi-llm-provider-go/internal/tmuxsize"
	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	agycli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/agycli"
	anthropicadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/anthropic"
	azureadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/azure"
	bedrockadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/bedrock"
	claudecodeadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/claudecode"
	codexcli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/codexcli"
	cursorcli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/cursorcli"
	deepgramadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/deepgram"
	elevenlabsadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/elevenlabs"
	geminicli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/geminicli"
	kimiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/kimi"
	minimaxadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/minimax"
	openaiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/openai"
	picli "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/picli"
	vertexadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/vertex"
	zaiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/zai"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	openaisdk "github.com/openai/openai-go/v3"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/openai/openai-go/v3/option"

	"google.golang.org/genai"
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
	// EnvClaudeCodeAllowLegacyPrint must be explicitly enabled before the legacy
	// `claude -p` stream-json transport can be selected.
	EnvClaudeCodeAllowLegacyPrint = "CLAUDE_CODE_ALLOW_LEGACY_PRINT"

	ClaudeCodeTransportTmux = "tmux"
	// ClaudeCodeTransportExperimental is kept as a legacy alias.
	// Deprecated: use ClaudeCodeTransportTmux.
	ClaudeCodeTransportExperimental = "experimental"
	ClaudeCodeTransportPrint        = "print"
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
	// transport). "print" selects the `claude -p` stream-json transport — an
	// opt-in path a workflow step may request via its transport config.
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
	Bedrock           *BedrockConfig
	Azure             *AzureAPIConfig
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
func InitializeEmbeddingModel(config Config) (llmtypes.EmbeddingModel, error) {
	var embeddingModel llmtypes.EmbeddingModel
	var err error

	switch config.Provider {
	case ProviderOpenAI:
		embeddingModel, err = initializeOpenAIEmbedding(config)
	case ProviderOpenRouter:
		// OpenRouter uses OpenAI-compatible API, so we can use OpenAI adapter
		embeddingModel, err = initializeOpenAIEmbedding(config)
	case ProviderVertex:
		embeddingModel, err = initializeVertexEmbedding(config)
	case ProviderBedrock:
		embeddingModel, err = initializeBedrockEmbedding(config)
	default:
		return nil, fmt.Errorf("embedding generation not supported for provider: %s. Supported providers: openai, openrouter, vertex, bedrock", config.Provider)
	}

	if err != nil {
		return nil, err
	}

	return embeddingModel, nil
}

// InitializeImageGenerationModel creates and initializes an image generation model.
// Supported providers:
//   - "imagen-*" models use the Imagen GenerateImages API
//   - "gemini-*" models use GenerateContent with IMAGE response modality
//   - "minimax-coding-plan" uses MiniMax image generation with image-01
//   - "codex-cli" uses the native Codex CLI image generation flow
//   - "agy-cli" uses the native Antigravity CLI image generation flow
func InitializeImageGenerationModel(config Config) (llmtypes.ImageGenerationModel, error) {
	switch config.Provider {
	case ProviderVertex:
		return initializeVertexImagen(config)
	case ProviderMiniMaxCodingPlan:
		return initializeMiniMaxCodingPlanImagen(config)
	case ProviderCodexCLI:
		return initializeCodexCLIImage(config)
	case ProviderAgyCLI:
		return initializeAgyCLIImage(config)
	default:
		return nil, fmt.Errorf("image generation not supported for provider: %s. Supported providers: vertex, minimax-coding-plan, codex-cli, agy-cli", config.Provider)
	}
}

// InitializeVideoGenerationModel creates and initializes a video generation model.
// Supported providers:
//   - "veo-*" models use Google's GenerateVideos API
func InitializeVideoGenerationModel(config Config) (llmtypes.VideoGenerationModel, error) {
	switch config.Provider {
	case ProviderVertex:
		return initializeVertexVeo(config)
	default:
		return nil, fmt.Errorf("video generation not supported for provider: %s. Supported providers: vertex", config.Provider)
	}
}

// InitializeAudioGenerationModel creates and initializes an audio generation model.
// Supported providers:
//   - "gemini-*" models use GenerateContent with AUDIO response modality
func InitializeAudioGenerationModel(config Config) (llmtypes.AudioGenerationModel, error) {
	switch config.Provider {
	case ProviderVertex:
		return initializeVertexTTS(config)
	case ProviderMiniMax:
		return initializeMiniMaxTTS(config)
	case ProviderElevenLabs:
		return initializeElevenLabsTTS(config)
	case ProviderDeepgram:
		return initializeDeepgramTTS(config)
	default:
		return nil, fmt.Errorf("audio generation not supported for provider: %s. Supported providers: vertex, minimax, elevenlabs, deepgram", config.Provider)
	}
}

// InitializeAudioTranscriptionModel creates and initializes a speech-to-text model.
// Supported providers:
//   - "deepgram" models use Deepgram prerecorded transcription
func InitializeAudioTranscriptionModel(config Config) (llmtypes.AudioTranscriptionModel, error) {
	switch config.Provider {
	case ProviderDeepgram:
		return initializeDeepgramSTT(config)
	default:
		return nil, fmt.Errorf("audio transcription not supported for provider: %s. Supported providers: deepgram", config.Provider)
	}
}

// InitializeMusicGenerationModel creates and initializes a music generation model.
// Supported providers:
//   - "elevenlabs" models use the ElevenLabs Music API
//   - "minimax" models use the MiniMax Music Generation API
func InitializeMusicGenerationModel(config Config) (llmtypes.MusicGenerationModel, error) {
	switch config.Provider {
	case ProviderElevenLabs:
		return initializeElevenLabsMusic(config)
	case ProviderMiniMax:
		return initializeMiniMaxMusic(config)
	default:
		return nil, fmt.Errorf("music generation not supported for provider: %s. Supported providers: elevenlabs, minimax", config.Provider)
	}
}

// initializeMiniMaxCodingPlanImagen creates a MiniMax image generation adapter using the
// coding-plan credential, which is the canonical MiniMax non-text auth path.
func initializeMiniMaxCodingPlanImagen(config Config) (llmtypes.ImageGenerationModel, error) {
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.MiniMaxCodingPlan != nil && *config.APIKeys.MiniMaxCodingPlan != "" {
		apiKey = *config.APIKeys.MiniMaxCodingPlan
	} else {
		apiKey = os.Getenv("MINIMAX_CODING_PLAN_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("MINIMAX_CODING_PLAN_API_KEY is required for MiniMax coding plan image generation")
	}

	modelID := config.ModelID
	if modelID == "" {
		modelID = minimaxadapter.ModelMiniMaxImage01
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	logger.Infof("Initializing MiniMax Coding Plan Image Generation with model: %s", modelID)
	return minimaxadapter.NewMiniMaxImageAdapter(apiKey, modelID, logger), nil
}

func initializeCodexCLIImage(config Config) (llmtypes.ImageGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = "codex-cli"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.CodexCLI != nil && *config.APIKeys.CodexCLI != "" {
		apiKey = *config.APIKeys.CodexCLI
	}
	if apiKey == "" {
		apiKey = os.Getenv("CODEX_API_KEY")
	}

	logger.Infof("Initializing Codex CLI Image Generation with model: %s", modelID)
	if apiKey == "" {
		logger.Infof("Codex CLI image generation: using Codex CLI local auth/session (CODEX_API_KEY not provided)")
	}
	return codexcli.NewCodexCLIImageAdapter(apiKey, modelID, logger), nil
}

func initializeAgyCLIImage(config Config) (llmtypes.ImageGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = "agy-cli"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.AgyCLI != nil && *config.APIKeys.AgyCLI != "" {
		apiKey = *config.APIKeys.AgyCLI
	}
	if apiKey == "" {
		apiKey = os.Getenv("AGY_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}

	logger.Infof("Initializing Agy CLI Image Generation with model: %s", modelID)
	if apiKey == "" {
		logger.Infof("Agy CLI image generation: using Antigravity CLI local sign-in")
	}
	return agycli.NewAgyCLIImageAdapter(apiKey, modelID, logger), nil
}

// initializeVertexImagen creates an image generation adapter using the Gemini API.
// If the model starts with "gemini-", uses GenerateContent (native Gemini image output).
// Otherwise assumes an Imagen model and uses the GenerateImages API.
// Uses GEMINI_API_KEY with the Gemini Developer API backend.
func initializeVertexImagen(config Config) (llmtypes.ImageGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = "gemini-3.1-flash-image-preview"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	// Check config APIKeys first, then fall back to environment variables
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Vertex != nil && *config.APIKeys.Vertex != "" {
		apiKey = *config.APIKeys.Vertex
	}
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("VERTEX_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY environment variable is required for Imagen image generation (or provide api_key in config)")
	}

	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create GenAI client for Imagen: %w", err)
	}

	logger.Infof("Initialized Gemini image model - model_id: %s", modelID)
	return vertexadapter.NewGeminiImageAdapter(client, modelID, logger), nil
}

const defaultGeminiTTSModelID = "gemini-3.1-flash-tts-preview"

// initializeVertexTTS creates an audio generation adapter using the Gemini API.
func initializeVertexTTS(config Config) (llmtypes.AudioGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = defaultGeminiTTSModelID
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Vertex != nil && *config.APIKeys.Vertex != "" {
		apiKey = *config.APIKeys.Vertex
	}
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("VERTEX_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY environment variable is required for Gemini TTS audio generation (or provide api_key in config)")
	}

	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create GenAI client for Gemini TTS: %w", err)
	}

	logger.Infof("Initialized Gemini TTS audio model - model_id: %s", modelID)
	return vertexadapter.NewGeminiTTSAdapter(client, modelID, logger), nil
}

// initializeElevenLabsTTS creates an audio generation adapter using ElevenLabs TTS.
func initializeElevenLabsTTS(config Config) (llmtypes.AudioGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = elevenlabsadapter.DefaultModelID
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.ElevenLabs != nil && *config.APIKeys.ElevenLabs != "" {
		apiKey = *config.APIKeys.ElevenLabs
	}
	if apiKey == "" {
		apiKey = os.Getenv("ELEVENLABS_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ELEVENLABS_API_KEY environment variable is required for ElevenLabs audio generation (or provide api_key in config)")
	}

	logger.Infof("Initialized ElevenLabs TTS audio model - model_id: %s", modelID)
	return elevenlabsadapter.NewElevenLabsTTSAdapter(apiKey, modelID, elevenlabsadapter.DefaultVoiceID, elevenlabsadapter.DefaultOutputFormat, logger), nil
}

// initializeMiniMaxTTS creates an audio generation adapter using MiniMax T2A.
func initializeMiniMaxTTS(config Config) (llmtypes.AudioGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = minimaxadapter.DefaultTTSModelID
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.MiniMax != nil && *config.APIKeys.MiniMax != "" {
		apiKey = *config.APIKeys.MiniMax
	}
	if apiKey == "" {
		apiKey = os.Getenv("MINIMAX_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("MINIMAX_API_KEY environment variable is required for MiniMax audio generation (or provide api_key in config)")
	}

	logger.Infof("Initialized MiniMax TTS audio model - model_id: %s", modelID)
	return minimaxadapter.NewMiniMaxTTSAdapter(apiKey, modelID, minimaxadapter.DefaultTTSVoiceID, logger), nil
}

// initializeDeepgramTTS creates an audio generation adapter using Deepgram Speak.
func initializeDeepgramTTS(config Config) (llmtypes.AudioGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = deepgramadapter.DefaultModelID
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Deepgram != nil && *config.APIKeys.Deepgram != "" {
		apiKey = *config.APIKeys.Deepgram
	}
	if apiKey == "" {
		apiKey = os.Getenv("DEEPGRAM_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("DEEPGRAM_API_KEY environment variable is required for Deepgram audio generation (or provide api_key in config)")
	}

	logger.Infof("Initialized Deepgram TTS audio model - model_id: %s", modelID)
	return deepgramadapter.NewDeepgramTTSAdapter(apiKey, modelID, logger), nil
}

// initializeDeepgramSTT creates a speech-to-text adapter using Deepgram Listen.
func initializeDeepgramSTT(config Config) (llmtypes.AudioTranscriptionModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = deepgramadapter.DefaultTranscriptionModelID
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Deepgram != nil && *config.APIKeys.Deepgram != "" {
		apiKey = *config.APIKeys.Deepgram
	}
	if apiKey == "" {
		apiKey = os.Getenv("DEEPGRAM_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("DEEPGRAM_API_KEY environment variable is required for Deepgram audio transcription (or provide api_key in config)")
	}

	logger.Infof("Initialized Deepgram STT audio model - model_id: %s", modelID)
	return deepgramadapter.NewDeepgramTTSAdapter(apiKey, modelID, logger), nil
}

// initializeElevenLabsMusic creates a music generation adapter using ElevenLabs Music.
func initializeElevenLabsMusic(config Config) (llmtypes.MusicGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = elevenlabsadapter.DefaultMusicModelID
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.ElevenLabs != nil && *config.APIKeys.ElevenLabs != "" {
		apiKey = *config.APIKeys.ElevenLabs
	}
	if apiKey == "" {
		apiKey = os.Getenv("ELEVENLABS_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ELEVENLABS_API_KEY environment variable is required for ElevenLabs music generation (or provide api_key in config)")
	}

	logger.Infof("Initialized ElevenLabs music model - model_id: %s", modelID)
	return elevenlabsadapter.NewElevenLabsMusicAdapter(apiKey, modelID, elevenlabsadapter.DefaultMusicOutputFormat, logger), nil
}

// initializeMiniMaxMusic creates a music generation adapter using MiniMax Music Generation.
func initializeMiniMaxMusic(config Config) (llmtypes.MusicGenerationModel, error) {
	modelID := config.ModelID
	if modelID == "" {
		modelID = minimaxadapter.DefaultMusicModelID
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.MiniMax != nil && *config.APIKeys.MiniMax != "" {
		apiKey = *config.APIKeys.MiniMax
	}
	if apiKey == "" {
		apiKey = os.Getenv("MINIMAX_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("MINIMAX_API_KEY environment variable is required for MiniMax music generation (or provide api_key in config)")
	}

	logger.Infof("Initialized MiniMax music model - model_id: %s", modelID)
	return minimaxadapter.NewMiniMaxMusicAdapter(apiKey, modelID, logger), nil
}

const (
	defaultGeminiVeoModelID = "veo-3.1-generate-preview"
	defaultVertexVeoModelID = "veo-3.1-generate-001"
	defaultVertexLocation   = "us-central1"
)

var vertexOnlyVeoModels = map[string]struct{}{
	"veo-3.1-generate-001":      {},
	"veo-3.1-fast-generate-001": {},
	"veo-3.1-lite-generate-001": {},
}

// initializeVertexVeo creates a video generation adapter using Google's GenerateVideos API.
// It supports both:
//   - Gemini Developer API with API-key auth for preview Veo models
//   - Vertex AI with ADC/OAuth for GA Vertex Veo models such as veo-3.1-generate-001
func initializeVertexVeo(config Config) (llmtypes.VideoGenerationModel, error) {
	modelID := config.ModelID

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Vertex != nil && *config.APIKeys.Vertex != "" {
		apiKey = *config.APIKeys.Vertex
	}
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("VERTEX_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}

	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}

	projectID := firstNonEmpty(
		os.Getenv("GOOGLE_CLOUD_PROJECT"),
		os.Getenv("VERTEX_PROJECT_ID"),
	)
	locationID := firstNonEmpty(
		os.Getenv("GOOGLE_CLOUD_LOCATION"),
		os.Getenv("GOOGLE_CLOUD_REGION"),
		os.Getenv("VERTEX_LOCATION_ID"),
	)
	if locationID == "" {
		locationID = defaultVertexLocation
	}

	if modelID == "" {
		if apiKey != "" {
			modelID = defaultGeminiVeoModelID
		} else if projectID != "" {
			modelID = defaultVertexVeoModelID
		} else {
			modelID = defaultGeminiVeoModelID
		}
	}

	clientConfig := &genai.ClientConfig{}
	backendLabel := "Gemini API"

	if requiresVertexVeoBackend(modelID) {
		if projectID == "" {
			return nil, fmt.Errorf(
				"model %q requires the Vertex AI backend. Set GOOGLE_CLOUD_PROJECT or VERTEX_PROJECT_ID, optionally GOOGLE_CLOUD_LOCATION or VERTEX_LOCATION_ID, and authenticate with Application Default Credentials. For API-key auth, use %q or %q instead",
				modelID,
				defaultGeminiVeoModelID,
				"veo-3.1-fast-generate-preview",
			)
		}
		clientConfig.Backend = genai.BackendVertexAI
		clientConfig.Project = projectID
		clientConfig.Location = locationID
		backendLabel = "Vertex AI"
	} else if apiKey != "" {
		clientConfig.APIKey = apiKey
		clientConfig.Backend = genai.BackendGeminiAPI
	} else if projectID != "" {
		clientConfig.Backend = genai.BackendVertexAI
		clientConfig.Project = projectID
		clientConfig.Location = locationID
		backendLabel = "Vertex AI"
	} else {
		return nil, fmt.Errorf("Veo video generation requires either GEMINI_API_KEY / VERTEX_API_KEY / GOOGLE_API_KEY for Gemini API preview models, or GOOGLE_CLOUD_PROJECT / VERTEX_PROJECT_ID plus ADC for Vertex AI models")
	}

	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create GenAI client for Veo: %w", err)
	}

	logger.Infof("Initialized Veo video model - backend: %s, model_id: %s", backendLabel, modelID)
	return vertexadapter.NewVertexVeoAdapter(client, modelID, logger), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func requiresVertexVeoBackend(modelID string) bool {
	_, ok := vertexOnlyVeoModels[strings.TrimSpace(modelID)]
	return ok
}

// initializeOpenAIEmbedding creates and configures an OpenAI embedding model instance
func initializeOpenAIEmbedding(config Config) (llmtypes.EmbeddingModel, error) {
	// Check for API key
	if os.Getenv("OPENAI_API_KEY") == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable is required for OpenAI embedding provider")
	}

	// Set default embedding model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "text-embedding-3-small"
	}

	// Create OpenAI client using official SDK
	client := openaisdk.NewClient(
		option.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
	)

	// Create OpenAI adapter (it implements both Model and EmbeddingModel interfaces)
	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	embeddingModel := openaiadapter.NewOpenAIAdapter(&client, modelID, logger)

	logger.Infof("Initialized OpenAI Embedding Model - model_id: %s", modelID)
	return embeddingModel, nil
}

// initializeVertexEmbedding creates and configures a Vertex AI embedding model instance
func initializeVertexEmbedding(config Config) (llmtypes.EmbeddingModel, error) {
	// Set default embedding model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "text-embedding-004" // Latest Vertex AI embedding model
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	logger.Infof("Initializing Vertex AI Embedding Model - model_id: %s", modelID)

	// Check for API key from environment
	apiKey := os.Getenv("VERTEX_API_KEY")
	if apiKey == "" {
		// Try alternative environment variable names
		apiKey = os.Getenv("GOOGLE_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("VERTEX_API_KEY or GOOGLE_API_KEY environment variable is required for Vertex AI embedding models")
	}

	// Use provided context or use background context
	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// Create Google GenAI client with API key authentication
	// Using BackendGeminiAPI for Gemini Developer API
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create GenAI client: %w", err)
	}

	// Create Vertex adapter (it implements both Model and EmbeddingModel interfaces)
	embeddingModel := vertexadapter.NewGoogleGenAIAdapter(client, modelID, logger)

	logger.Infof("Initialized Vertex AI Embedding Model - model_id: %s", modelID)
	return embeddingModel, nil
}

// initializeBedrockEmbedding creates and configures a Bedrock embedding model instance
func initializeBedrockEmbedding(config Config) (llmtypes.EmbeddingModel, error) {
	// Set default embedding model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "amazon.titan-embed-text-v1" // Default Bedrock embedding model
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	logger.Infof("Initializing Bedrock Embedding Model - model_id: %s", modelID)

	// Create AWS config
	cfg, err := awsconfig.LoadDefaultConfig(config.Context)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create Bedrock runtime client
	client := bedrockruntime.NewFromConfig(cfg)

	// Create Bedrock adapter (it implements both Model and EmbeddingModel interfaces)
	embeddingModel := bedrockadapter.NewBedrockAdapter(client, modelID, logger)

	logger.Infof("Initialized Bedrock Embedding Model - model_id: %s", modelID)
	return embeddingModel, nil
}

// initializeBedrockWithFallback creates a Bedrock LLM with fallback models for rate limiting
func initializeBedrockWithFallback(config Config) (llmtypes.Model, error) {
	// Try primary model first
	llm, err := initializeBedrock(config)
	if err == nil {
		return llm, nil
	}

	// If primary fails and we have fallback models, try them
	if len(config.FallbackModels) > 0 {
		logger := config.Logger
		if logger == nil {
			logger = &noopLoggerImpl{}
		}
		logger.Infof("Primary Bedrock model failed, trying fallback models - primary_model: %s, fallback_models: %v, error: %s", config.ModelID, config.FallbackModels, err.Error())

		for _, fallbackModel := range config.FallbackModels {
			fallbackConfig := config
			fallbackConfig.ModelID = fallbackModel

			llm, err := initializeBedrock(fallbackConfig)
			if err == nil {
				logger.Infof("Successfully initialized fallback Bedrock model - fallback_model: %s", fallbackModel)
				return llm, nil
			}

			logger.Infof("Fallback Bedrock model failed - fallback_model: %s, error: %s", fallbackModel, err.Error())
		}
	}

	// If all models fail, return the original error
	return nil, fmt.Errorf("all Bedrock models failed: %w", err)
}

// initializeOpenAIWithFallback creates an OpenAI LLM with fallback models for rate limiting
func initializeOpenAIWithFallback(config Config) (llmtypes.Model, error) {
	// Try primary model first
	llm, err := initializeOpenAI(config)
	if err == nil {
		return llm, nil
	}

	// If primary fails and we have fallback models, try them
	if len(config.FallbackModels) > 0 {
		logger := config.Logger
		if logger == nil {
			logger = &noopLoggerImpl{}
		}
		logger.Infof("Primary OpenAI model failed, trying fallback models - primary_model: %s, fallback_models: %v, error: %s", config.ModelID, config.FallbackModels, err.Error())

		for _, fallbackModel := range config.FallbackModels {
			fallbackConfig := config
			fallbackConfig.ModelID = fallbackModel

			llm, err := initializeOpenAI(fallbackConfig)
			if err == nil {
				logger.Infof("Successfully initialized fallback OpenAI model - fallback_model: %s", fallbackModel)
				return llm, nil
			}

			logger.Infof("Fallback OpenAI model failed - fallback_model: %s, error: %s", fallbackModel, err.Error())
		}
	}

	// If all models fail, return the original error
	return nil, fmt.Errorf("all OpenAI models failed: %w", err)
}

// initializeZAIWithFallback creates a Z.AI LLM with fallback models for rate limiting.
func initializeZAIWithFallback(config Config) (llmtypes.Model, error) {
	llm, err := initializeZAI(config)
	if err == nil {
		return llm, nil
	}

	if len(config.FallbackModels) > 0 {
		logger := config.Logger
		if logger == nil {
			logger = &noopLoggerImpl{}
		}
		logger.Infof("Primary Z.AI model failed, trying fallback models - primary_model: %s, fallback_models: %v, error: %s", config.ModelID, config.FallbackModels, err.Error())

		for _, fallbackModel := range config.FallbackModels {
			fallbackConfig := config
			fallbackConfig.ModelID = fallbackModel

			llm, err := initializeZAI(fallbackConfig)
			if err == nil {
				logger.Infof("Successfully initialized fallback Z.AI model - fallback_model: %s", fallbackModel)
				return llm, nil
			}

			logger.Infof("Fallback Z.AI model failed - fallback_model: %s, error: %s", fallbackModel, err.Error())
		}
	}

	return nil, fmt.Errorf("all Z.AI models failed: %w", err)
}

// initializeOpenRouterWithFallback creates an OpenRouter LLM with fallback models for rate limiting
func initializeOpenRouterWithFallback(config Config) (llmtypes.Model, error) {
	// Try primary model first
	llm, err := initializeOpenRouter(config)
	if err == nil {
		return llm, nil
	}

	// If primary fails and we have fallback models, try them
	if len(config.FallbackModels) > 0 {
		logger := config.Logger
		if logger == nil {
			logger = &noopLoggerImpl{}
		}
		logger.Infof("Primary OpenRouter model failed, trying fallback models - primary_model: %s, fallback_models: %v, error: %s", config.ModelID, config.FallbackModels, err.Error())

		for _, fallbackModel := range config.FallbackModels {
			fallbackConfig := config
			fallbackConfig.ModelID = fallbackModel

			llm, err := initializeOpenRouter(fallbackConfig)
			if err == nil {
				logger.Infof("Successfully initialized fallback OpenRouter model - fallback_model: %s", fallbackModel)
				return llm, nil
			}

			logger.Infof("Fallback OpenRouter model failed - fallback_model: %s, error: %s", fallbackModel, err.Error())
		}
	}

	// If all models fail, return the original error
	return nil, fmt.Errorf("all OpenRouter models failed: %w", err)
}

// initializeVertexWithFallback creates a Vertex AI LLM with fallback models for rate limiting
func initializeVertexWithFallback(config Config) (llmtypes.Model, error) {
	// Try primary model first
	llm, err := initializeVertex(config)
	if err == nil {
		return llm, nil
	}

	// If primary fails and we have fallback models, try them
	if len(config.FallbackModels) > 0 {
		logger := config.Logger
		if logger == nil {
			logger = &noopLoggerImpl{}
		}
		logger.Infof("Primary Vertex model failed, trying fallback models - primary_model: %s, fallback_models: %v, error: %s", config.ModelID, config.FallbackModels, err.Error())

		for _, fallbackModel := range config.FallbackModels {
			fallbackConfig := config
			fallbackConfig.ModelID = fallbackModel

			llm, err := initializeVertex(fallbackConfig)
			if err == nil {
				logger.Infof("Successfully initialized fallback Vertex model - fallback_model: %s", fallbackModel)
				return llm, nil
			}

			logger.Infof("Fallback Vertex model failed - fallback_model: %s, error: %s", fallbackModel, err.Error())
		}
	}

	// If all models fail, return the original error
	return nil, fmt.Errorf("all Vertex models failed: %w", err)
}

// initializeBedrock creates and configures a Bedrock LLM instance
func initializeBedrock(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data - use typed structure directly
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    40000, // Will be set at call time
		TopP:         config.Temperature,
		User:         "bedrock_user",
		CustomFields: map[string]string{
			"provider":  "bedrock",
			"operation": "llm_initialization",
		},
	}

	var logger = config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Debug: Log AWS environment variables
	logger.Infof("Initializing Bedrock LLM with model: %s", config.ModelID)

	// Get region from config first, then environment (default to us-east-1)
	region := ""
	if config.APIKeys != nil && config.APIKeys.Bedrock != nil && config.APIKeys.Bedrock.Region != "" {
		region = config.APIKeys.Bedrock.Region
		logger.Infof("Using region from config: %s", region)
	} else {
		region = os.Getenv("AWS_REGION")
		if region == "" {
			region = "us-east-1"
			logger.Infof("AWS_REGION not set, using default: %s", region)
		} else {
			logger.Infof("Using region from environment: %s", region)
		}
	}

	logger.Infof("AWS_REGION: %s", region)
	logger.Infof("AWS_ACCESS_KEY_ID: %s", os.Getenv("AWS_ACCESS_KEY_ID"))
	logger.Infof("AWS_SECRET_ACCESS_KEY: %s", os.Getenv("AWS_SECRET_ACCESS_KEY"))

	// Load AWS SDK configuration
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		logger.Errorf("Failed to load AWS config: %w", err)

		// Emit LLM initialization error event - use typed structure directly
		errorMetadata := LLMMetadata{
			ModelVersion: config.ModelID,
			User:         "bedrock_user",
			CustomFields: map[string]string{
				"provider":  "bedrock",
				"operation": OperationLLMInitialization,
				"error":     err.Error(),
				"status":    StatusLLMFailed,
			},
		}
		emitLLMInitializationError(config.EventEmitter, string(config.Provider), config.ModelID, OperationLLMInitialization, err, config.TraceID, errorMetadata)

		return nil, fmt.Errorf("load aws config: %w", err)
	}

	// Create Bedrock runtime client
	client := bedrockruntime.NewFromConfig(cfg)

	// Set default model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "us.anthropic.claude-3-sonnet-20240229-v1:0"
	}

	// Create Bedrock adapter
	llm := bedrockadapter.NewBedrockAdapter(client, modelID, logger)

	// Emit LLM initialization success event - use typed structure directly
	successMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		User:         "bedrock_user",
		CustomFields: map[string]string{
			"provider":     "bedrock",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), config.ModelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Bedrock LLM - model_id: %s", config.ModelID)
	return llm, nil
}

// IsO3O4Model detects o3/o4 models (OpenAI) for conditional logic in agent
func IsO3O4Model(modelID string) bool {
	// Covers gpt-4o, gpt-4.0, gpt-4.1, gpt-4, gpt-3.5, etc
	return strings.HasPrefix(modelID, "o3") ||
		strings.HasPrefix(modelID, "o4")
}

// Helper functions for event emission
func emitLLMInitializationStart(emitter interfaces.EventEmitter, provider string, modelID string, temperature float64, traceID interfaces.TraceID, metadata LLMMetadata) {
	if emitter != nil {
		emitter.EmitLLMInitializationStart(provider, modelID, temperature, traceID, metadata)
	}
}

func emitLLMInitializationSuccess(emitter interfaces.EventEmitter, provider string, modelID string, capabilities string, traceID interfaces.TraceID, metadata LLMMetadata) {
	if emitter != nil {
		emitter.EmitLLMInitializationSuccess(provider, modelID, capabilities, traceID, metadata)
	}
}

func emitLLMInitializationError(emitter interfaces.EventEmitter, provider string, modelID string, operation string, err error, traceID interfaces.TraceID, metadata LLMMetadata) {
	if emitter != nil {
		emitter.EmitLLMInitializationError(provider, modelID, operation, err, traceID, metadata)
	}
}

func emitLLMGenerationSuccess(emitter interfaces.EventEmitter, provider string, modelID string, operation string, messages int, temperature float64, messageContent string, responseLength int, choicesCount int, traceID interfaces.TraceID, metadata LLMMetadata) {
	if emitter != nil {
		emitter.EmitLLMGenerationSuccess(provider, modelID, operation, messages, temperature, messageContent, responseLength, choicesCount, traceID, metadata)
	}
}

func emitLLMGenerationError(emitter interfaces.EventEmitter, provider string, modelID string, operation string, messages int, temperature float64, messageContent string, err error, traceID interfaces.TraceID, metadata LLMMetadata) {
	if emitter != nil {
		emitter.EmitLLMGenerationError(provider, modelID, operation, messages, temperature, messageContent, err, traceID, metadata)
	}
}

func emitToolCallDetected(emitter interfaces.EventEmitter, provider string, modelID string, toolCallID string, toolName string, arguments string, traceID interfaces.TraceID, metadata LLMMetadata) {
	if emitter != nil {
		emitter.EmitToolCallDetected(provider, modelID, toolCallID, toolName, arguments, traceID, metadata)
	}
}

// initializeOpenAI creates and configures an OpenAI LLM instance
func initializeOpenAI(config Config) (llmtypes.Model, error) {
	// Check for API key from config first, then environment
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.OpenAI != nil && *config.APIKeys.OpenAI != "" {
		apiKey = *config.APIKeys.OpenAI
	} else {
		// Try environment variable
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required for OpenAI provider (not found in config or environment)")
	}

	// LLM Initialization event data - use typed structure directly
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0, // Will be set at call time
		TopP:         config.Temperature,
		User:         "openai_user",
		CustomFields: map[string]string{
			"provider":  "openai",
			"operation": "llm_initialization",
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Set default model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "gpt-4.1"
	}

	// Create OpenAI client using official SDK
	client := openaisdk.NewClient(
		option.WithAPIKey(apiKey),
	)

	// Create OpenAI adapter
	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	llm := openaiadapter.NewOpenAIAdapter(&client, modelID, logger)

	// Emit LLM initialization success event - use typed structure directly
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "openai_user",
		CustomFields: map[string]string{
			"provider":     "openai",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized OpenAI LLM - model_id: %s", modelID)
	return llm, nil
}

// initializeZAI creates and configures a Z.AI LLM instance using the OpenAI-compatible
// Chat Completions API surface exposed at api.z.ai.
func initializeZAI(config Config) (llmtypes.Model, error) {
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.ZAI != nil && *config.APIKeys.ZAI != "" {
		apiKey = *config.APIKeys.ZAI
	} else {
		apiKey = os.Getenv("ZAI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ZAI_API_KEY is required for Z.AI provider (not found in config or environment)")
	}

	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0,
		TopP:         config.Temperature,
		User:         "zai_user",
		CustomFields: map[string]string{
			"provider":  "z-ai",
			"operation": "llm_initialization",
		},
	}
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	modelID := config.ModelID
	if modelID == "" {
		modelID = zaiadapter.ModelGLM51
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	baseURL := os.Getenv("ZAI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.z.ai/api/coding/paas/v4"
	}

	client := openaisdk.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
	)

	llm := openaiadapter.NewCompatibleOpenAIAdapter(&client, modelID, logger, openaiadapter.OpenAICompatibilityConfig{
		ProviderName:   "z-ai",
		MetadataLookup: zaiadapter.GetZAIModelMetadata,
		RequestExtraFields: func(modelID string, opts *llmtypes.CallOptions) map[string]interface{} {
			extra := map[string]interface{}{}

			thinkingType := "enabled"
			if strings.EqualFold(opts.ReasoningEffort, "none") {
				thinkingType = "disabled"
			}
			extra["thinking"] = map[string]interface{}{
				"type":           thinkingType,
				"clear_thinking": true,
			}

			if opts.StreamChan != nil && len(opts.Tools) > 0 {
				extra["tool_stream"] = true
			}

			return extra
		},
	})
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "zai_user",
		CustomFields: map[string]string{
			"provider":     "z-ai",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Z.AI LLM - model_id: %s, base_url: %s", modelID, baseURL)
	return llm, nil
}

// initializeAnthropic creates and configures an Anthropic LLM instance
func initializeAnthropic(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data - use typed structure directly
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0, // Will be set at call time
		TopP:         config.Temperature,
		User:         "anthropic_user",
		CustomFields: map[string]string{
			"provider":  "anthropic",
			"operation": "llm_initialization",
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Check for API key from config first, then environment
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Anthropic != nil && *config.APIKeys.Anthropic != "" {
		apiKey = *config.APIKeys.Anthropic
	} else {
		// Try environment variable
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is required (not found in config or environment)")
	}

	// Use provided model or default. We default to the cheapest current
	// model on the active model line; the previous default
	// (claude-3-5-sonnet-20241022) is no longer accepted by the API and
	// any caller that did not explicitly set ModelID would 404.
	modelID := config.ModelID
	if modelID == "" {
		modelID = "claude-haiku-4-5"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing Anthropic LLM with model: %s", modelID)

	// Create Anthropic SDK client
	// NewClient reads from environment by default, but we can explicitly set API key
	// Note: Beta header for prompt caching must be added per-request, not at client level
	client := anthropic.NewClient(
		anthropicoption.WithAPIKey(apiKey),
	)

	// Create Anthropic adapter
	llm := anthropicadapter.NewAnthropicAdapter(client, modelID, logger)

	// Emit LLM initialization success event - use typed structure directly
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "anthropic_user",
		CustomFields: map[string]string{
			"provider":     "anthropic",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Anthropic LLM - model_id: %s", modelID)
	return llm, nil
}

// initializeMiniMax creates and configures a MiniMax LLM instance using the Anthropic-compatible API
func initializeMiniMax(config Config) (llmtypes.Model, error) {
	// Check for API key from config first, then environment
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.MiniMax != nil && *config.APIKeys.MiniMax != "" {
		apiKey = *config.APIKeys.MiniMax
	} else {
		apiKey = os.Getenv("MINIMAX_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("MINIMAX_API_KEY is required for MiniMax provider (not found in config or environment)")
	}

	modelID := config.ModelID
	if modelID == "" {
		modelID = minimaxadapter.ModelMiniMaxM27
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing MiniMax LLM with model: %s", modelID)

	llmMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "minimax_user",
		CustomFields: map[string]string{
			"provider":  "minimax",
			"operation": "llm_initialization",
		},
	}
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), modelID, config.Temperature, config.TraceID, llmMetadata)

	llm := minimaxadapter.NewMiniMaxAdapter(apiKey, modelID, logger)

	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "minimax_user",
		CustomFields: map[string]string{
			"provider":     "minimax",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized MiniMax LLM - model_id: %s", modelID)
	return llm, nil
}

// initializeMiniMaxCodingPlan creates a MiniMax coding plan adapter using Anthropic model names.
// The coding plan uses the same Anthropic-compatible endpoint but authenticates with a
// coding-plan-specific API key (MINIMAX_CODING_PLAN_API_KEY) and accepts Anthropic model names
// (e.g. claude-sonnet-4-5) which MiniMax maps to their equivalent models.
func initializeMiniMaxCodingPlan(config Config) (llmtypes.Model, error) {
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.MiniMaxCodingPlan != nil && *config.APIKeys.MiniMaxCodingPlan != "" {
		apiKey = *config.APIKeys.MiniMaxCodingPlan
	} else {
		apiKey = os.Getenv("MINIMAX_CODING_PLAN_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("MINIMAX_CODING_PLAN_API_KEY is required for MiniMax coding plan provider")
	}

	modelID := config.ModelID
	if modelID == "" {
		modelID = "claude-sonnet-4-5"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing MiniMax Coding Plan LLM with model: %s", modelID)

	llm := minimaxadapter.NewMiniMaxCodingPlanAdapter(apiKey, modelID, logger)

	logger.Infof("Initialized MiniMax Coding Plan LLM - model_id: %s", modelID)
	return llm, nil
}

// initializeAzureWithFallback creates an Azure LLM with fallback models for rate limiting
func initializeAzureWithFallback(config Config) (llmtypes.Model, error) {
	// Try primary model first
	llm, err := initializeAzure(config)
	if err == nil {
		return llm, nil
	}

	// If primary fails and we have fallback models, try them
	if len(config.FallbackModels) > 0 {
		logger := config.Logger
		if logger == nil {
			logger = &noopLoggerImpl{}
		}
		logger.Infof("Primary Azure model failed, trying fallback models - primary_model: %s, fallback_models: %v, error: %s", config.ModelID, config.FallbackModels, err.Error())

		for _, fallbackModel := range config.FallbackModels {
			fallbackConfig := config
			fallbackConfig.ModelID = fallbackModel

			llm, err := initializeAzure(fallbackConfig)
			if err == nil {
				logger.Infof("Successfully initialized fallback Azure model - fallback_model: %s", fallbackModel)
				return llm, nil
			}

			logger.Infof("Fallback Azure model failed - fallback_model: %s, error: %s", fallbackModel, err.Error())
		}
	}

	// If all models fail, return the original error
	return nil, fmt.Errorf("all Azure models failed: %w", err)
}

// initializeAzure creates and configures an Azure AI LLM instance
func initializeAzure(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0, // Will be set at call time
		TopP:         config.Temperature,
		User:         "azure_user",
		CustomFields: map[string]string{
			"provider":  "azure",
			"operation": OperationLLMInitialization,
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Check for Azure config from APIKeys or environment
	var endpoint, apiKey, apiVersion, region string

	if config.APIKeys != nil && config.APIKeys.Azure != nil {
		endpoint = config.APIKeys.Azure.Endpoint
		apiKey = config.APIKeys.Azure.APIKey
		apiVersion = config.APIKeys.Azure.APIVersion
		region = config.APIKeys.Azure.Region
	}

	// Fallback to environment variables
	if endpoint == "" {
		endpoint = os.Getenv("AZURE_AI_ENDPOINT")
	}
	if apiKey == "" {
		apiKey = os.Getenv("AZURE_AI_API_KEY")
	}
	if apiVersion == "" {
		apiVersion = os.Getenv("AZURE_AI_API_VERSION")
	}
	if region == "" {
		region = os.Getenv("AZURE_AI_REGION")
	}

	// Validate required fields
	if endpoint == "" {
		return nil, fmt.Errorf("AZURE_AI_ENDPOINT is required for Azure provider (not found in config or environment)")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("AZURE_AI_API_KEY is required for Azure provider (not found in config or environment)")
	}

	// Set default model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "gpt-4o"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing Azure AI LLM - model_id: %s, endpoint: %s, region: %s", modelID, endpoint, region)

	// Create Azure adapter config
	azureConfig := azureadapter.AzureConfig{
		Endpoint:   endpoint,
		APIKey:     apiKey,
		APIVersion: apiVersion,
		Region:     region,
	}

	// Create Azure adapter
	llm := azureadapter.NewAzureAdapter(azureConfig, modelID, logger)

	// Emit LLM initialization success event
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "azure_user",
		CustomFields: map[string]string{
			"provider":     "azure",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
			"endpoint":     endpoint,
			"region":       region,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Azure AI LLM - model_id: %s", modelID)
	return llm, nil
}

// initializeOpenRouter creates and configures an OpenRouter LLM instance
func initializeOpenRouter(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data - use typed structure directly
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0, // Will be set at call time
		TopP:         config.Temperature,
		User:         "openrouter_user",
		CustomFields: map[string]string{
			"provider":  "openrouter",
			"operation": OperationLLMInitialization,
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Check for API key from config first, then environment
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.OpenRouter != nil && *config.APIKeys.OpenRouter != "" {
		apiKey = *config.APIKeys.OpenRouter
	} else {
		// Try environment variables (check both naming conventions)
		apiKey = os.Getenv("OPENROUTER_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("OPEN_ROUTER_API_KEY")
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY or OPEN_ROUTER_API_KEY is required for OpenRouter provider (not found in config or environment)")
	}

	// Set default model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "moonshotai/kimi-k2"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("🔧 Initializing OpenRouter LLM - model_id: %s, base_url: https://openrouter.ai/api/v1", modelID)

	// 🆕 DETAILED OPENROUTER INITIALIZATION LOGGING
	logger.Infof("🔧 [DEBUG] Creating OpenRouter LLM with OpenAI client...")
	logger.Infof("🔧 [DEBUG] Model: %s", modelID)
	logger.Infof("🔧 [DEBUG] Base URL: https://openrouter.ai/api/v1")
	logger.Infof("🔧 [DEBUG] API Key present: %v", apiKey != "")

	// Create OpenAI SDK client with OpenRouter base URL
	clientOptions := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithBaseURL("https://openrouter.ai/api/v1"),
	}

	// Add optional OpenRouter headers if provided
	if httpReferer := os.Getenv("OPENROUTER_HTTP_REFERER"); httpReferer != "" {
		clientOptions = append(clientOptions, option.WithHeader("HTTP-Referer", httpReferer))
		logger.Infof("🔧 [DEBUG] Added HTTP-Referer header: %s", httpReferer)
	}
	if xTitle := os.Getenv("OPENROUTER_X_TITLE"); xTitle != "" {
		clientOptions = append(clientOptions, option.WithHeader("X-Title", xTitle))
		logger.Infof("🔧 [DEBUG] Added X-Title header: %s", xTitle)
	}

	client := openaisdk.NewClient(clientOptions...)

	// Create OpenAI adapter with OpenRouter configuration
	llm := openaiadapter.NewOpenAIAdapter(&client, modelID, logger)

	// 🆕 POST-INITIALIZATION LOGGING
	logger.Infof("🔧 [DEBUG] OpenRouter LLM creation completed - LLM: %v", llm != nil)

	// Emit LLM initialization success event - use typed structure directly
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "openrouter_user",
		CustomFields: map[string]string{
			"provider":     "openrouter",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("✅ Successfully initialized OpenRouter LLM - model_id: %s", modelID)
	return llm, nil
}

// initializeVertex creates and configures a Vertex AI LLM instance
// Supports both Gemini (via API key) and Anthropic (via OAuth2) models
func initializeVertex(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data - use typed structure directly
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0, // Will be set at call time
		TopP:         config.Temperature,
		User:         "vertex_user",
		CustomFields: map[string]string{
			"provider":  "vertex",
			"operation": "llm_initialization",
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Set default model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = vertexadapter.ModelGemini35Flash
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	// Detect if this is an Anthropic model (starts with "claude-\n")
	isAnthropicModel := strings.HasPrefix(modelID, "claude-")

	if isAnthropicModel {
		// Initialize Vertex AI Anthropic adapter
		return initializeVertexAnthropic(config, modelID, logger)
	}

	// Initialize Gemini adapter (existing implementation)
	return initializeVertexGemini(config, modelID, logger)
}

// initializeVertexAnthropic creates and configures a Vertex AI Anthropic LLM instance
func initializeVertexAnthropic(config Config, modelID string, logger interfaces.Logger) (llmtypes.Model, error) {
	logger.Infof("Initializing Vertex AI Anthropic LLM - model_id: %s", modelID)

	// Get required configuration
	projectID := os.Getenv("VERTEX_PROJECT_ID")
	if projectID == "" {
		return nil, fmt.Errorf("VERTEX_PROJECT_ID environment variable is required for Anthropic models")
	}

	locationID := os.Getenv("VERTEX_LOCATION_ID")
	if locationID == "" {
		locationID = "global" // Default location
		logger.Infof("VERTEX_LOCATION_ID not set, using default: %s", locationID)
	}

	// Create Vertex Anthropic adapter
	llm := vertexadapter.NewVertexAnthropicAdapter(projectID, locationID, modelID, logger)

	// Emit LLM initialization success event
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "vertex_user",
		CustomFields: map[string]string{
			"provider":     "vertex",
			"model_type":   "anthropic",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Vertex AI Anthropic LLM - model_id: %s, project: %s, location: %s", modelID, projectID, locationID)
	return llm, nil
}

// initializeVertexGemini creates and configures a Vertex AI Gemini LLM instance
func initializeVertexGemini(config Config, modelID string, logger interfaces.Logger) (llmtypes.Model, error) {
	logger.Infof("Initializing Vertex AI (Gemini) LLM with API key - model_id: %s", modelID)

	// Check for API key from config first, then environment
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Vertex != nil && *config.APIKeys.Vertex != "" {
		apiKey = *config.APIKeys.Vertex
		logger.Infof("🔑 [VERTEX AUTH] Using API key from config")
	} else {
		// Try environment variables (AI Studio key works under any of these names)
		apiKey = firstNonEmpty(
			os.Getenv("VERTEX_API_KEY"),
			os.Getenv("GEMINI_API_KEY"),
			os.Getenv("GOOGLE_API_KEY"),
		)
	}
	if apiKey == "" {
		return nil, fmt.Errorf("an AI Studio API key is required for Gemini models — set VERTEX_API_KEY, GEMINI_API_KEY, or GOOGLE_API_KEY (or pass via config.APIKeys.Vertex)")
	}

	// Use provided context or use background context
	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// Create Google GenAI client with API key authentication
	// Using BackendGeminiAPI for Gemini Developer API
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		logger.Errorf("Failed to create GenAI client: %w", err)

		// Emit LLM initialization error event
		errorMetadata := LLMMetadata{
			ModelVersion: modelID,
			User:         "vertex_user",
			CustomFields: map[string]string{
				"provider":   "vertex",
				"model_type": "gemini",
				"operation":  OperationLLMInitialization,
				"error":      err.Error(),
				"status":     StatusLLMFailed,
			},
		}
		emitLLMInitializationError(config.EventEmitter, string(config.Provider), modelID, OperationLLMInitialization, err, config.TraceID, errorMetadata)

		return nil, fmt.Errorf("create genai client: %w", err)
	}

	// Create adapter wrapper that implements llmtypes.Model interface
	llm := vertexadapter.NewGoogleGenAIAdapter(client, modelID, logger)

	// Emit LLM initialization success event - use typed structure directly
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "vertex_user",
		CustomFields: map[string]string{
			"provider":     "vertex",
			"model_type":   "gemini",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Vertex AI Gemini LLM - model_id: %s", modelID)
	return llm, nil
}

// initializeClaudeCode creates and configures a Claude Code CLI adapter instance
func initializeClaudeCode(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0, // Will be set at call time or by CLI
		TopP:         config.Temperature,
		User:         "claude_code_user",
		CustomFields: map[string]string{
			"provider":  "claude-code",
			"operation": OperationLLMInitialization,
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Set default model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = "claude-code" // Default ID representing the CLI
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	transport, err := resolveClaudeCodeTransport(config.ClaudeCodeTransport)
	if err != nil {
		errorMetadata := LLMMetadata{
			ModelVersion: modelID,
			CustomFields: map[string]string{
				"provider":  "claude-code",
				"operation": OperationLLMInitialization,
			},
		}
		emitLLMInitializationError(config.EventEmitter, string(config.Provider), modelID, OperationLLMInitialization, err, config.TraceID, errorMetadata)
		return nil, err
	}

	logger.Infof("Initializing Claude Code %s adapter - model_id: %s", transport, modelID)

	// claude-code provider always uses the claude CLI's OAuth session (via `claude login`).
	// We intentionally ignore any Anthropic API key from config or env: forwarding one would
	// make the CLI prefer that key over its OAuth credentials, silently switching billing to
	// a key that often has low/no credits. Users who want API-key billing should select the
	// `anthropic` provider instead, which is a separate direct-API adapter.
	if transport == ClaudeCodeTransportPrint {
		logger.Infof("Claude Code: using legacy print transport with CLI OAuth credentials (`claude -p` stream-json)")

		llm := claudecodeadapter.NewClaudeCodeAdapter("", modelID, logger)

		successMetadata := LLMMetadata{
			ModelVersion: modelID,
			User:         "claude_code_user",
			CustomFields: map[string]string{
				"provider":     "claude-code",
				"status":       StatusLLMInitialized,
				"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
				"mode":         ClaudeCodeTransportPrint,
				"transport":    ClaudeCodeTransportPrint,
			},
		}
		emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

		logger.Infof("Initialized Claude Code print adapter - model_id: %s", modelID)
		return llm, nil
	}

	logger.Infof("Claude Code: using tmux mode with CLI OAuth credentials (no `claude -p` invocation)")

	// Create Claude Code tmux adapter.
	llm := claudecodeadapter.NewClaudeCodeInteractiveAdapter(modelID, logger)

	// Emit LLM initialization success event
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "claude_code_user",
		CustomFields: map[string]string{
			"provider":     "claude-code",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
			"mode":         ClaudeCodeTransportTmux,
			"transport":    ClaudeCodeTransportTmux,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Claude Code tmux adapter - model_id: %s", modelID)
	return llm, nil
}

func resolveClaudeCodeTransport(configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return normalizeClaudeCodeTransport(configured)
	}
	raw := strings.TrimSpace(os.Getenv(EnvClaudeCodeTransport))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv(EnvClaudeCodeMode))
	}
	return normalizeClaudeCodeTransport(raw)
}

func normalizeClaudeCodeTransport(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ClaudeCodeTransportTmux, ClaudeCodeTransportExperimental, "interactive":
		return ClaudeCodeTransportTmux, nil
	case ClaudeCodeTransportPrint, "-p", "p", "legacy", "agent-sdk", "agentsdk", "sdk":
		// Print / stream-json transport. Opt-in and rarely used — the default is
		// tmux; a workflow step selects this explicitly via its transport config
		// (step-level "structured"/"json" is mapped to this "print" value before
		// it reaches here). Fully supported (not legacy-gated): the `claude -p`
		// path passes the structured contract tests against the current CLI.
		return ClaudeCodeTransportPrint, nil
	default:
		return "", fmt.Errorf("unsupported Claude Code transport %q; use %s=%q (default) or %q", raw, EnvClaudeCodeTransport, ClaudeCodeTransportTmux, ClaudeCodeTransportPrint)
	}
}

// initializeKimi creates and configures the Kimi API provider.
func initializeKimi(config Config) (llmtypes.Model, error) {
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0,
		TopP:         config.Temperature,
		User:         "kimi_user",
		CustomFields: map[string]string{
			"provider":  "kimi",
			"operation": OperationLLMInitialization,
		},
	}

	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	modelID := strings.TrimSpace(config.ModelID)
	if modelID == "" {
		modelID = kimiadapter.ModelKimiK26
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}

	if modelID == kimiadapter.ModelKimiCode {
		err := fmt.Errorf("kimi-code is no longer supported as a Kimi provider model; use kimi-k2.7-code for Kimi coding API workloads or Pi CLI for multi-model coding-agent plans")
		errorMetadata := LLMMetadata{
			ModelVersion: modelID,
			User:         "kimi_user",
			CustomFields: map[string]string{
				"provider":  "kimi",
				"operation": OperationLLMInitialization,
				"error":     err.Error(),
				"status":    StatusLLMFailed,
			},
		}
		emitLLMInitializationError(config.EventEmitter, string(config.Provider), modelID, OperationLLMInitialization, err, config.TraceID, errorMetadata)
		return nil, err
	}

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.Kimi != nil {
		apiKey = strings.TrimSpace(*config.APIKeys.Kimi)
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("KIMI_API_KEY"))
	}
	if apiKey == "" {
		err := fmt.Errorf("KIMI_API_KEY is required for Kimi provider (not found in config or environment)")
		errorMetadata := LLMMetadata{
			ModelVersion: modelID,
			User:         "kimi_user",
			CustomFields: map[string]string{
				"provider":  "kimi",
				"operation": OperationLLMInitialization,
				"error":     err.Error(),
				"status":    StatusLLMFailed,
			},
		}
		emitLLMInitializationError(config.EventEmitter, string(config.Provider), modelID, OperationLLMInitialization, err, config.TraceID, errorMetadata)
		return nil, err
	}

	baseURL := os.Getenv("KIMI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.moonshot.ai/v1"
	}

	client := openaisdk.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(baseURL),
	)

	llm := openaiadapter.NewCompatibleOpenAIAdapter(&client, modelID, logger, openaiadapter.OpenAICompatibilityConfig{
		ProviderName:   "kimi",
		MetadataLookup: kimiadapter.GetKimiModelMetadata,
	})

	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "kimi_user",
		CustomFields: map[string]string{
			"provider":     "kimi",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Kimi API provider - model_id: %s, base_url: %s", modelID, baseURL)
	return llm, nil
}

// initializeGeminiCLI creates and configures a Gemini CLI adapter instance
func initializeGeminiCLI(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0,
		TopP:         config.Temperature,
		User:         "gemini_cli_user",
		CustomFields: map[string]string{
			"provider":  "gemini-cli",
			"operation": OperationLLMInitialization,
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Set default model if not specified
	// Gemini CLI supports aliases: "auto" (default), "pro", "flash", "flash-lite"
	modelID := config.ModelID
	if modelID == "" {
		modelID = "auto"
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing Gemini CLI adapter - model_id: %s", modelID)

	apiKey, apiKeySource := resolveGeminiCLIAPIKey(config)
	if apiKey != "" {
		logger.Infof("Gemini CLI: using API key from %s (length=%d)", apiKeySource, len(apiKey))
	} else {
		logger.Infof("Gemini CLI: no API key found in config or environment")
	}

	// Create Gemini CLI adapter — pass API key so it can set GEMINI_API_KEY on the subprocess
	llm := geminicli.NewGeminiCLIAdapter(apiKey, modelID, logger)

	// Emit LLM initialization success event
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "gemini_cli_user",
		CustomFields: map[string]string{
			"provider":     "gemini-cli",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Gemini CLI adapter - model_id: %s", modelID)
	return llm, nil
}

func resolveGeminiCLIAPIKey(config Config) (string, string) {
	if config.APIKeys != nil {
		if config.APIKeys.GeminiCLI != nil && strings.TrimSpace(*config.APIKeys.GeminiCLI) != "" {
			return strings.TrimSpace(*config.APIKeys.GeminiCLI), "gemini-cli config"
		}
		if config.APIKeys.Vertex != nil && strings.TrimSpace(*config.APIKeys.Vertex) != "" {
			return strings.TrimSpace(*config.APIKeys.Vertex), "vertex config"
		}
	}
	if envKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY")); envKey != "" {
		return envKey, "GEMINI_API_KEY env var"
	}
	if envKey := strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")); envKey != "" {
		return envKey, "GOOGLE_API_KEY env var"
	}
	return "", ""
}

// initializeCodexCLI creates and configures an OpenAI Codex CLI adapter instance
func initializeCodexCLI(config Config) (llmtypes.Model, error) {
	// LLM Initialization event data
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0,
		TopP:         config.Temperature,
		User:         "codex_cli_user",
		CustomFields: map[string]string{
			"provider":  "codex-cli",
			"operation": OperationLLMInitialization,
		},
	}

	// Emit LLM initialization start event
	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	// Set default model if not specified
	modelID := config.ModelID
	if modelID == "" {
		modelID = DefaultCodexCLIModel
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing Codex CLI adapter - model_id: %s", modelID)

	// Resolve API key: explicit config > CODEX_API_KEY only.
	// Do NOT fall back to OPENAI_API_KEY — Codex CLI has its own auth
	// (via `codex login` stored in ~/.codex/auth.json) which should be preferred.
	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.CodexCLI != nil {
		apiKey = *config.APIKeys.CodexCLI
		logger.Infof("Codex CLI: using API key from config (length=%d)", len(apiKey))
	} else if envKey := os.Getenv("CODEX_API_KEY"); envKey != "" {
		apiKey = envKey
		logger.Infof("Codex CLI: using API key from CODEX_API_KEY env var (length=%d)", len(apiKey))
	} else {
		logger.Infof("Codex CLI: no explicit API key — will use Codex CLI's own stored auth")
	}

	// Create Codex CLI adapter
	llm := codexcli.NewCodexCLIAdapter(apiKey, modelID, logger)

	// Emit LLM initialization success event
	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "codex_cli_user",
		CustomFields: map[string]string{
			"provider":     "codex-cli",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Codex CLI adapter - model_id: %s", modelID)
	return llm, nil
}

// initializeCursorCLI creates and configures a Cursor Agent CLI adapter instance.
func initializeCursorCLI(config Config) (llmtypes.Model, error) {
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0,
		TopP:         config.Temperature,
		User:         "cursor_cli_user",
		CustomFields: map[string]string{
			"provider":  "cursor-cli",
			"operation": OperationLLMInitialization,
		},
	}

	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	modelID := config.ModelID
	if modelID == "" {
		modelID = DefaultCursorCLIModel
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing Cursor CLI adapter - model_id: %s", modelID)

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.CursorCLI != nil {
		apiKey = *config.APIKeys.CursorCLI
		logger.Infof("Cursor CLI: using API key from config (length=%d)", len(apiKey))
	} else if envKey := os.Getenv("CURSOR_API_KEY"); envKey != "" {
		apiKey = envKey
		logger.Infof("Cursor CLI: using API key from CURSOR_API_KEY env var (length=%d)", len(apiKey))
	} else {
		logger.Infof("Cursor CLI: no explicit API key — will use Cursor Agent CLI's own stored auth")
	}

	llm := cursorcli.NewCursorCLIAdapter(apiKey, modelID, logger)

	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "cursor_cli_user",
		CustomFields: map[string]string{
			"provider":     "cursor-cli",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Cursor CLI adapter - model_id: %s", modelID)
	return llm, nil
}

// initializeAgyCLI creates and configures an Antigravity CLI adapter instance.
func initializeAgyCLI(config Config) (llmtypes.Model, error) {
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0,
		TopP:         config.Temperature,
		User:         "agy_cli_user",
		CustomFields: map[string]string{
			"provider":  "agy-cli",
			"operation": OperationLLMInitialization,
		},
	}

	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	modelID := config.ModelID
	if modelID == "" {
		modelID = DefaultAgyCLIModel
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing Antigravity CLI adapter - model_id: %s", modelID)

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.AgyCLI != nil {
		apiKey = *config.APIKeys.AgyCLI
		logger.Infof("Antigravity CLI: using API key from config (length=%d)", len(apiKey))
	} else if envKey := os.Getenv("AGY_API_KEY"); envKey != "" {
		apiKey = envKey
		logger.Infof("Antigravity CLI: using API key from AGY_API_KEY env var (length=%d)", len(apiKey))
	} else {
		logger.Infof("Antigravity CLI: no explicit API key — will use agy's own stored auth")
	}

	llm := agycli.NewAgyCLIAdapter(apiKey, modelID, logger)

	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "agy_cli_user",
		CustomFields: map[string]string{
			"provider":     "agy-cli",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Antigravity CLI adapter - model_id: %s", modelID)
	return llm, nil
}

// initializePiCLI creates and configures a Pi Coding Agent CLI adapter
// instance.
func initializePiCLI(config Config) (llmtypes.Model, error) {
	llmMetadata := LLMMetadata{
		ModelVersion: config.ModelID,
		MaxTokens:    0,
		TopP:         config.Temperature,
		User:         "pi_cli_user",
		CustomFields: map[string]string{
			"provider":  "pi-cli",
			"operation": OperationLLMInitialization,
		},
	}

	emitLLMInitializationStart(config.EventEmitter, string(config.Provider), config.ModelID, config.Temperature, config.TraceID, llmMetadata)

	modelID := config.ModelID
	if modelID == "" {
		modelID = DefaultPiCLIModel
	}

	logger := config.Logger
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	logger.Infof("Initializing Pi CLI adapter - model_id: %s", modelID)

	apiKey := ""
	if config.APIKeys != nil && config.APIKeys.PiCLI != nil {
		apiKey = *config.APIKeys.PiCLI
		logger.Infof("Pi CLI: using API key from config (length=%d)", len(apiKey))
	} else if envKey := os.Getenv("PI_API_KEY"); envKey != "" {
		apiKey = envKey
		logger.Infof("Pi CLI: using API key from PI_API_KEY env var (length=%d)", len(apiKey))
	} else if envKey := os.Getenv("GEMINI_API_KEY"); envKey != "" {
		apiKey = envKey
		logger.Infof("Pi CLI: using API key from GEMINI_API_KEY env var (length=%d)", len(apiKey))
	} else if envKey := os.Getenv("GOOGLE_API_KEY"); envKey != "" {
		apiKey = envKey
		logger.Infof("Pi CLI: using API key from GOOGLE_API_KEY env var (length=%d)", len(apiKey))
	} else {
		logger.Infof("Pi CLI: no explicit API key — will use Pi CLI/provider local auth if available")
	}

	llm := picli.NewPiCLIAdapter(apiKey, modelID, logger)

	successMetadata := LLMMetadata{
		ModelVersion: modelID,
		User:         "pi_cli_user",
		CustomFields: map[string]string{
			"provider":     "pi-cli",
			"status":       StatusLLMInitialized,
			"capabilities": CapabilityTextGeneration + "," + CapabilityToolCalling,
		},
	}
	emitLLMInitializationSuccess(config.EventEmitter, string(config.Provider), modelID, CapabilityTextGeneration+","+CapabilityToolCalling, config.TraceID, successMetadata)

	logger.Infof("Initialized Pi CLI adapter - model_id: %s", modelID)
	return llm, nil
}

// GetDefaultModel returns the default model for each provider from environment variables
func GetDefaultModel(provider Provider) string {
	switch provider {
	case ProviderBedrock:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("BEDROCK_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return "us.anthropic.claude-sonnet-4-20250514-v1:0"
	case ProviderOpenAI:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("OPENAI_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return "gpt-4.1-mini"
	case ProviderAnthropic:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("ANTHROPIC_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return "claude-sonnet-4-6"
	case ProviderOpenRouter:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("OPENROUTER_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return "moonshotai/kimi-k2"
	case ProviderVertex:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("VERTEX_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return vertexadapter.ModelGemini35Flash
	case ProviderAzure:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("AZURE_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return "gpt-4o"
	case ProviderZAI:
		if primaryModel := os.Getenv("ZAI_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return zaiadapter.ModelGLM51
	case ProviderKimi:
		if primaryModel := os.Getenv("KIMI_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return kimiadapter.ModelKimiK26
	case ProviderClaudeCode:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("CLAUDE_CODE_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return "claude-code"
	case ProviderGeminiCLI:
		// Get primary model from environment variable
		// Supports aliases: "auto" (default), "pro", "flash", "flash-lite"
		// or full names: "gemini-3.5-flash", "gemini-3-pro-preview", etc.
		if primaryModel := os.Getenv("GEMINI_CLI_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return "auto"
	case ProviderCodexCLI:
		// Get primary model from environment variable
		if primaryModel := os.Getenv("CODEX_CLI_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return DefaultCodexCLIModel
	case ProviderCursorCLI:
		if primaryModel := os.Getenv("CURSOR_CLI_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return DefaultCursorCLIModel
	case ProviderAgyCLI:
		if primaryModel := os.Getenv("AGY_CLI_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		if primaryModel := os.Getenv("AGY_PRIMARY_MODEL"); primaryModel != "" {
			return primaryModel
		}
		return DefaultAgyCLIModel
	default:
		return ""
	}
}

func parseFallbackModelsEnv(modelsEnv string) []string {
	if modelsEnv == "" {
		return []string{}
	}

	models := strings.Split(modelsEnv, ",")
	parsed := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		parsed = append(parsed, model)
	}
	return parsed
}

func prefixModelsWithProvider(models []string, provider string) []string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return models
	}

	// Ignore invalid provider values and return models as-is.
	if _, err := ValidateProvider(provider); err != nil {
		return models
	}

	prefixed := make([]string, len(models))
	for i, model := range models {
		// Preserve already provider-qualified references (provider/model).
		if strings.Contains(model, "/") {
			prefixed[i] = model
			continue
		}
		prefixed[i] = provider + "/" + model
	}
	return prefixed
}

// GetDefaultFallbackModels returns fallback models for each provider from environment variables
func GetDefaultFallbackModels(provider Provider) []string {
	switch provider {
	case ProviderBedrock:
		// Get Bedrock fallback models from environment variable
		fallbackModelsEnv := os.Getenv("BEDROCK_FALLBACK_MODELS")
		if fallbackModelsEnv != "" {
			// Split by comma and trim whitespace
			models := strings.Split(fallbackModelsEnv, ",")
			for i, model := range models {
				models[i] = strings.TrimSpace(model)
			}
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderOpenAI:
		// Get fallback models from environment variable
		fallbackModelsEnv := os.Getenv("OPENAI_FALLBACK_MODELS")
		if fallbackModelsEnv != "" {
			// Split by comma and trim whitespace
			models := strings.Split(fallbackModelsEnv, ",")
			for i, model := range models {
				models[i] = strings.TrimSpace(model)
			}
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderOpenRouter:
		// Get fallback models from environment variable
		fallbackModelsEnv := os.Getenv("OPENROUTER_FALLBACK_MODELS")
		if fallbackModelsEnv != "" {
			// Split by comma and trim whitespace
			models := strings.Split(fallbackModelsEnv, ",")
			for i, model := range models {
				models[i] = strings.TrimSpace(model)
			}
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderVertex:
		// Get fallback models from environment variable
		fallbackModelsEnv := os.Getenv("VERTEX_FALLBACK_MODELS")
		if fallbackModelsEnv != "" {
			// Split by comma and trim whitespace
			models := strings.Split(fallbackModelsEnv, ",")
			for i, model := range models {
				models[i] = strings.TrimSpace(model)
			}
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderAzure:
		// Get fallback models from environment variable
		fallbackModelsEnv := os.Getenv("AZURE_FALLBACK_MODELS")
		models := parseFallbackModelsEnv(fallbackModelsEnv)
		if len(models) > 0 {
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderZAI:
		fallbackModelsEnv := os.Getenv("ZAI_FALLBACK_MODELS")
		models := parseFallbackModelsEnv(fallbackModelsEnv)
		if len(models) > 0 {
			return models
		}
		return []string{}
	case ProviderKimi:
		fallbackModelsEnv := os.Getenv("KIMI_FALLBACK_MODELS")
		models := parseFallbackModelsEnv(fallbackModelsEnv)
		if len(models) > 0 {
			return models
		}
		return []string{}
	case ProviderClaudeCode:
		// Get fallback models from environment variable
		fallbackModelsEnv := os.Getenv("CLAUDE_CODE_FALLBACK_MODELS")
		if fallbackModelsEnv == "" {
			fallbackModelsEnv = os.Getenv("CLAUDECODE_FALLBACK_MODELS") // Legacy naming
		}
		models := parseFallbackModelsEnv(fallbackModelsEnv)
		if len(models) > 0 {
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderGeminiCLI:
		// Get fallback models from environment variable
		fallbackModelsEnv := os.Getenv("GEMINI_CLI_FALLBACK_MODELS")
		if fallbackModelsEnv == "" {
			fallbackModelsEnv = os.Getenv("GEMINICLI_FALLBACK_MODELS") // Legacy naming
		}
		models := parseFallbackModelsEnv(fallbackModelsEnv)
		if len(models) > 0 {
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderCodexCLI:
		// Get fallback models from environment variable
		fallbackModelsEnv := os.Getenv("CODEX_CLI_FALLBACK_MODELS")
		if fallbackModelsEnv == "" {
			fallbackModelsEnv = os.Getenv("CODEXCLI_FALLBACK_MODELS") // Legacy naming
		}
		models := parseFallbackModelsEnv(fallbackModelsEnv)
		if len(models) > 0 {
			return models
		}
		// No fallback models if environment variable is not set
		return []string{}
	case ProviderCursorCLI:
		fallbackModelsEnv := os.Getenv("CURSOR_CLI_FALLBACK_MODELS")
		if fallbackModelsEnv == "" {
			fallbackModelsEnv = os.Getenv("CURSORCLI_FALLBACK_MODELS")
		}
		models := parseFallbackModelsEnv(fallbackModelsEnv)
		if len(models) > 0 {
			return models
		}
		return []string{}
	default:
		return []string{}
	}
}

// GetDefaultFallbackModelsForModel returns fallback models for a provider, optionally
// taking the current primary model into account when provider-specific defaults need
// to preserve capabilities. Environment overrides still take precedence.
func GetDefaultFallbackModelsForModel(provider Provider, primaryModel string) []string {
	models := GetDefaultFallbackModels(provider)
	if len(models) > 0 {
		return models
	}

	switch provider {
	case ProviderZAI:
		modelID := strings.TrimSpace(primaryModel)
		if modelID == "" {
			modelID = GetDefaultModel(provider)
		}

		switch modelID {
		case zaiadapter.ModelGLM51:
			return []string{zaiadapter.ModelGLM47}
		case zaiadapter.ModelGLM47:
			return []string{zaiadapter.ModelGLM51}
		default:
			// Avoid defaulting vision or niche models to a text-only fallback.
			return []string{}
		}
	default:
		return models
	}
}

// GetCrossProviderFallbackModels returns cross-provider fallback models (e.g., OpenAI for Bedrock)
func GetCrossProviderFallbackModels(provider Provider) []string {
	switch provider {
	case ProviderBedrock:
		// Get OpenAI cross-provider fallback models
		openaiFallbackEnv := os.Getenv("BEDROCK_OPENAI_FALLBACK_MODELS")
		if openaiFallbackEnv != "" {
			// Split by comma and trim whitespace
			models := strings.Split(openaiFallbackEnv, ",")
			for i, model := range models {
				models[i] = strings.TrimSpace(model)
			}
			return models
		}
		// No cross-provider fallbacks if environment variable is not set
		return []string{}
	case ProviderOpenAI:
		// For OpenAI provider, no cross-provider fallbacks by default
		return []string{}
	case ProviderOpenRouter:
		// Get cross-provider fallback models for OpenRouter
		crossFallbackEnv := os.Getenv("OPENROUTER_CROSS_FALLBACK_MODELS")
		if crossFallbackEnv != "" {
			// Split by comma and trim whitespace
			models := strings.Split(crossFallbackEnv, ",")
			for i, model := range models {
				models[i] = strings.TrimSpace(model)
			}
			return models
		}
		// No cross-provider fallbacks if environment variable is not set
		return []string{}
	case ProviderVertex:
		// Get Anthropic cross-provider fallback models for Vertex
		anthropicFallbackEnv := os.Getenv("VERTEX_ANTHROPIC_FALLBACK_MODELS")
		models := parseFallbackModelsEnv(anthropicFallbackEnv)
		if len(models) > 0 {
			return models
		}
		// No cross-provider fallbacks if environment variable is not set
		return []string{}
	case ProviderClaudeCode:
		// Get cross-provider fallback models for Claude Code
		crossFallbackEnv := os.Getenv("CLAUDE_CODE_CROSS_FALLBACK_MODELS")
		if crossFallbackEnv == "" {
			crossFallbackEnv = os.Getenv("CLAUDECODE_CROSS_FALLBACK_MODELS") // Legacy naming
		}
		models := parseFallbackModelsEnv(crossFallbackEnv)
		if len(models) == 0 {
			return []string{}
		}
		crossProvider := os.Getenv("CLAUDE_CODE_CROSS_FALLBACK_PROVIDER")
		if crossProvider == "" {
			crossProvider = os.Getenv("CLAUDECODE_CROSS_FALLBACK_PROVIDER") // Legacy naming
		}
		return prefixModelsWithProvider(models, crossProvider)
	case ProviderKimi:
		crossFallbackEnv := os.Getenv("KIMI_CROSS_FALLBACK_MODELS")
		models := parseFallbackModelsEnv(crossFallbackEnv)
		if len(models) == 0 {
			return []string{}
		}
		crossProvider := os.Getenv("KIMI_CROSS_FALLBACK_PROVIDER")
		return prefixModelsWithProvider(models, crossProvider)
	case ProviderGeminiCLI:
		// Get cross-provider fallback models for Gemini CLI
		crossFallbackEnv := os.Getenv("GEMINI_CLI_CROSS_FALLBACK_MODELS")
		if crossFallbackEnv == "" {
			crossFallbackEnv = os.Getenv("GEMINICLI_CROSS_FALLBACK_MODELS") // Legacy naming
		}
		models := parseFallbackModelsEnv(crossFallbackEnv)
		if len(models) == 0 {
			return []string{}
		}
		crossProvider := os.Getenv("GEMINI_CLI_CROSS_FALLBACK_PROVIDER")
		if crossProvider == "" {
			crossProvider = os.Getenv("GEMINICLI_CROSS_FALLBACK_PROVIDER") // Legacy naming
		}
		return prefixModelsWithProvider(models, crossProvider)
	case ProviderCodexCLI:
		// Get cross-provider fallback models for Codex CLI
		crossFallbackEnv := os.Getenv("CODEX_CLI_CROSS_FALLBACK_MODELS")
		if crossFallbackEnv == "" {
			crossFallbackEnv = os.Getenv("CODEXCLI_CROSS_FALLBACK_MODELS") // Legacy naming
		}
		models := parseFallbackModelsEnv(crossFallbackEnv)
		if len(models) == 0 {
			return []string{}
		}
		crossProvider := os.Getenv("CODEX_CLI_CROSS_FALLBACK_PROVIDER")
		if crossProvider == "" {
			crossProvider = os.Getenv("CODEXCLI_CROSS_FALLBACK_PROVIDER") // Legacy naming
		}
		return prefixModelsWithProvider(models, crossProvider)
	case ProviderCursorCLI:
		crossFallbackEnv := os.Getenv("CURSOR_CLI_CROSS_FALLBACK_MODELS")
		if crossFallbackEnv == "" {
			crossFallbackEnv = os.Getenv("CURSORCLI_CROSS_FALLBACK_MODELS")
		}
		models := parseFallbackModelsEnv(crossFallbackEnv)
		if len(models) == 0 {
			return []string{}
		}
		crossProvider := os.Getenv("CURSOR_CLI_CROSS_FALLBACK_PROVIDER")
		if crossProvider == "" {
			crossProvider = os.Getenv("CURSORCLI_CROSS_FALLBACK_PROVIDER")
		}
		return prefixModelsWithProvider(models, crossProvider)
	case ProviderAgyCLI:
		crossFallbackEnv := os.Getenv("AGY_CLI_CROSS_FALLBACK_MODELS")
		if crossFallbackEnv == "" {
			crossFallbackEnv = os.Getenv("AGY_CROSS_FALLBACK_MODELS")
		}
		models := parseFallbackModelsEnv(crossFallbackEnv)
		if len(models) == 0 {
			return []string{}
		}
		crossProvider := os.Getenv("AGY_CLI_CROSS_FALLBACK_PROVIDER")
		if crossProvider == "" {
			crossProvider = os.Getenv("AGY_CROSS_FALLBACK_PROVIDER")
		}
		return prefixModelsWithProvider(models, crossProvider)
	default:
		return []string{}
	}
}

// ValidateProvider checks if the provider is supported
func ValidateProvider(provider string) (Provider, error) {
	switch Provider(provider) {
	case ProviderBedrock, ProviderOpenAI, ProviderAnthropic, ProviderOpenRouter, ProviderVertex, ProviderAzure, ProviderZAI, ProviderKimi, ProviderClaudeCode, ProviderGeminiCLI, ProviderCodexCLI, ProviderCursorCLI, ProviderAgyCLI, ProviderPiCLI, ProviderMiniMax, ProviderMiniMaxCodingPlan:
		return Provider(provider), nil
	default:
		return "", fmt.Errorf("unsupported provider: %s. Supported providers: bedrock, openai, anthropic, openrouter, vertex, azure, z-ai, kimi, claude-code, gemini-cli, codex-cli, cursor-cli, agy-cli, pi-cli, minimax, minimax-coding-plan", provider)
	}
}

// ProviderAwareLLM is a wrapper around LLM that preserves provider information
// and automatically captures token usage in LLM events
type ProviderAwareLLM struct {
	llmtypes.Model
	provider     Provider
	modelID      string
	eventEmitter interfaces.EventEmitter
	traceID      interfaces.TraceID
	logger       interfaces.Logger
}

const (
	providerAwareMessageLogMaxChars    = 500
	providerAwarePromptLogMaxChars     = 2000
	providerAwareToolNamesLogMaxChars  = 4000
	providerAwareToolSchemaLogMaxChars = 2000
)

// NewProviderAwareLLM creates a new provider-aware LLM wrapper
func NewProviderAwareLLM(llm llmtypes.Model, provider Provider, modelID string, eventEmitter interfaces.EventEmitter, traceID interfaces.TraceID, logger interfaces.Logger) *ProviderAwareLLM {
	// Use no-op logger if nil is provided
	if logger == nil {
		logger = &noopLoggerImpl{}
	}
	return &ProviderAwareLLM{
		Model:        llm,
		provider:     provider,
		modelID:      modelID,
		eventEmitter: eventEmitter,
		traceID:      traceID,
		logger:       logger,
	}
}

// GetProvider returns the provider of this LLM
func (p *ProviderAwareLLM) GetProvider() Provider {
	return p.provider
}

// GetModelID returns the model ID of this LLM
func (p *ProviderAwareLLM) GetModelID() string {
	return p.modelID
}

// GenerateContent wraps the underlying LLM's GenerateContent method to automatically capture token usage
// extractTextFromParts extracts text content from message parts
func extractTextFromParts(parts []llmtypes.ContentPart) string {
	var textParts []string
	for _, part := range parts {
		if textPart, ok := part.(llmtypes.TextContent); ok {
			textParts = append(textParts, textPart.Text)
		}
	}
	return strings.Join(textParts, " ")
}

func truncateProviderAwareLogText(text string, maxChars int) string {
	if maxChars <= 0 || len(text) == 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars]) + fmt.Sprintf("... [truncated, total length: %d chars]", len(runes))
}

func providerAwareVerboseRequestLogging() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTI_LLM_VERBOSE_REQUEST_LOGS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func providerAwareRequestPayloadLoggingEnabled() bool {
	if providerAwareVerboseRequestLogging() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MULTI_LLM_REQUEST_LOGS"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (p *ProviderAwareLLM) logRequestPayload(messages []llmtypes.MessageContent, options ...llmtypes.CallOption) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	// Extract and log system prompts
	var systemPrompts []string
	for _, msg := range messages {
		if msg.Role == llmtypes.ChatMessageTypeSystem {
			text := extractTextFromParts(msg.Parts)
			if text != "" {
				systemPrompts = append(systemPrompts, text)
			}
		}
	}
	if len(systemPrompts) > 0 {
		p.logger.Infof("📋 SYSTEM PROMPTS (%d):", len(systemPrompts))
		for i, prompt := range systemPrompts {
			p.logger.Infof("   [%d] length=%d preview=%s", i+1, len([]rune(prompt)), truncateProviderAwareLogText(prompt, providerAwarePromptLogMaxChars))
		}
	} else {
		p.logger.Infof("📋 SYSTEM PROMPTS: None")
	}

	// Log all messages
	p.logger.Infof("💬 MESSAGES (%d):", len(messages))
	for i, msg := range messages {
		text := extractTextFromParts(msg.Parts)
		displayText := truncateProviderAwareLogText(text, providerAwareMessageLogMaxChars)
		p.logger.Infof("   [%d] Role: %s, Content: %s", i+1, msg.Role, displayText)
	}

	// Log tools if provided
	if len(opts.Tools) > 0 {
		p.logger.Infof("🔧 TOOLS (%d):", len(opts.Tools))
		toolNames := make([]string, 0, len(opts.Tools))
		for i, tool := range opts.Tools {
			if tool.Function != nil {
				toolNames = append(toolNames, tool.Function.Name)
			} else {
				toolNames = append(toolNames, fmt.Sprintf("tool[%d]=<nil function>", i+1))
			}
		}
		p.logger.Infof("   Names: %s", truncateProviderAwareLogText(strings.Join(toolNames, ", "), providerAwareToolNamesLogMaxChars))

		if providerAwareVerboseRequestLogging() {
			for i, tool := range opts.Tools {
				if tool.Function != nil {
					toolJSON, err := json.MarshalIndent(tool, "      ", "  ")
					if err != nil {
						p.logger.Infof("   [%d] %s (error marshaling: %v)", i+1, tool.Function.Name, err)
					} else {
						p.logger.Infof("   [%d] %s schema preview:\n%s", i+1, tool.Function.Name, truncateProviderAwareLogText(string(toolJSON), providerAwareToolSchemaLogMaxChars))
					}
				} else {
					p.logger.Infof("   [%d] Tool with nil Function", i+1)
				}
			}
		}
	} else {
		p.logger.Infof("🔧 TOOLS: None")
	}
}

func (p *ProviderAwareLLM) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	// Note: LLM generation start event is now emitted at the agent level to avoid duplication

	// Automatically add usage parameter for OpenRouter requests to get cache token information
	if p.provider == ProviderOpenRouter {
		options = append(options, WithOpenRouterUsage())
	}

	if providerAwareRequestPayloadLoggingEnabled() {
		p.logRequestPayload(messages, options...)
	}

	// Log request timing
	requestStartTime := time.Now()
	p.logger.Infof("⏱️  LLM REQUEST START - Time: %s", requestStartTime.Format(time.RFC3339))

	// Call the underlying LLM
	resp, err := p.Model.GenerateContent(ctx, messages, options...)

	// Log response timing
	requestEndTime := time.Now()
	duration := requestEndTime.Sub(requestStartTime)
	p.logger.Infof("⏱️  LLM RESPONSE RECEIVED - Time: %s, Duration: %v", requestEndTime.Format(time.RFC3339), duration)

	// Check if we have a valid response
	if err != nil {
		// Classify so consumers can branch on cause (rate limit vs auth vs
		// outage) via llmerrors.KindOf instead of string-matching. The
		// original error stays in the chain (Unwrap), so existing
		// string-based handling keeps working.
		err = llmerrors.Classify(string(p.provider), p.modelID, err)
		p.logger.Infof("❌ LLM generation failed - provider: %s, model: %s, kind: %s, error: %v", string(p.provider), p.modelID, llmerrors.KindOf(err), err)

		// Emit LLM generation error event with rich debugging information
		errorMetadata := LLMMetadata{
			User: "llm_generation_user",
			CustomFields: map[string]string{
				"provider":        string(p.provider),
				"model_id":        p.modelID,
				"messages":        fmt.Sprintf("%d", len(messages)),
				"temperature":     fmt.Sprintf("%f", getTemperatureFromOptions(options)),
				"message_content": extractMessageContentAsString(messages),
				"error":           err.Error(),
				"error_type":      fmt.Sprintf("%T", err),
				"error_kind":      string(llmerrors.KindOf(err)),
				"debug_note":      "Enhanced error logging for turn 2 debugging",
			},
		}
		emitLLMGenerationError(p.eventEmitter, string(p.provider), p.modelID, OperationLLMGeneration, len(messages), getTemperatureFromOptions(options), extractMessageContentAsString(messages), err, p.traceID, errorMetadata)

		return nil, err
	}

	// Validate response structure
	if resp == nil {
		p.logger.Infof("❌ Response is nil")

		// Emit LLM generation error event for nil response
		errorMetadata := LLMMetadata{
			User: "llm_generation_user",
			CustomFields: map[string]string{
				"debug_note": "Response validation failed - nil response",
			},
		}
		emitLLMGenerationError(p.eventEmitter, string(p.provider), p.modelID, OperationLLMGeneration, len(messages), getTemperatureFromOptions(options), extractMessageContentAsString(messages), fmt.Errorf("response validation failed - nil response"), p.traceID, errorMetadata)

		return nil, fmt.Errorf("response is nil")
	}

	if resp.Choices == nil {
		p.logger.Infof("❌ Response.Choices is nil")

		// Emit LLM generation error event for nil choices
		errorMetadata := LLMMetadata{
			User: "llm_generation_user",
			CustomFields: map[string]string{
				"provider":        string(p.provider),
				"model_id":        p.modelID,
				"messages":        fmt.Sprintf("%d", len(messages)),
				"temperature":     fmt.Sprintf("%f", getTemperatureFromOptions(options)),
				"message_content": extractMessageContentAsString(messages),
				"error":           "Response.Choices is nil",
				"debug_note":      "Response validation failed - nil choices",
			},
		}
		emitLLMGenerationError(p.eventEmitter, string(p.provider), p.modelID, OperationLLMGeneration, len(messages), getTemperatureFromOptions(options), extractMessageContentAsString(messages), fmt.Errorf("response.Choices is nil"), p.traceID, errorMetadata)

		return nil, fmt.Errorf("response.Choices is nil")
	}

	if len(resp.Choices) == 0 {
		p.logger.Infof("❌ Response.Choices is empty array - this will cause 'no results' error")

		// Enhanced logging for ALL providers when choices array is empty
		p.logger.Errorf("🔍 Empty Choices Array Debug Information for %s:", string(p.provider))
		p.logger.Errorf("   Model ID: %s", p.modelID)
		p.logger.Errorf("   Provider: %s", string(p.provider))
		p.logger.Errorf("   Response Type: %T", resp)
		p.logger.Errorf("   Response Pointer: %p", resp)
		p.logger.Errorf("   Choices Array Length: %d", len(resp.Choices))
		p.logger.Errorf("   Choices Array Nil: %v", resp.Choices == nil)
		p.logger.Errorf("   Choices Array Cap: %d", cap(resp.Choices))

		// Log the ENTIRE response structure for comprehensive debugging
		p.logger.Errorf("🔍 COMPLETE LLM RESPONSE STRUCTURE:")
		p.logger.Errorf("   Full Response: %+v", resp)

		// Log the options that were passed to the LLM
		p.logger.Errorf("🔍 LLM CALL OPTIONS:")
		for i, opt := range options {
			p.logger.Errorf("   Option %d: %T = %+v", i+1, opt, opt)
		}

		// Log the messages that were sent to the LLM
		p.logger.Errorf("🔍 MESSAGES SENT TO LLM:")
		for i, msg := range messages {
			p.logger.Errorf("   Message %d - Role: %s, Parts: %d", i+1, msg.Role, len(msg.Parts))
			for j, part := range msg.Parts {
				p.logger.Errorf("     Part %d - Type: %T, Content: %+v", j+1, part, part)
			}
		}

		// Emit LLM generation error event for empty choices
		errorMetadata := LLMMetadata{
			User: "llm_generation_user",
			CustomFields: map[string]string{
				"provider":        string(p.provider),
				"model_id":        p.modelID,
				"messages":        fmt.Sprintf("%d", len(messages)),
				"temperature":     fmt.Sprintf("%f", getTemperatureFromOptions(options)),
				"message_content": extractMessageContentAsString(messages),
				"error":           "Response.Choices is empty",
				"debug_note":      "Response validation failed - empty choices array",
			},
		}
		emitLLMGenerationError(p.eventEmitter, string(p.provider), p.modelID, OperationLLMGeneration, len(messages), getTemperatureFromOptions(options), extractMessageContentAsString(messages), fmt.Errorf("response.Choices is empty"), p.traceID, errorMetadata)

		return nil, fmt.Errorf("response.Choices is empty")
	}

	// Validate first choice has content
	firstChoice := resp.Choices[0]
	if firstChoice.Content == "" {
		// Check if this is a valid tool call response
		if len(firstChoice.ToolCalls) > 0 {
			p.logger.Infof("✅ Valid tool call response detected - Content is empty but ToolCalls present")
			p.logger.Infof("   Tool Calls: %d", len(firstChoice.ToolCalls))
			for i, toolCall := range firstChoice.ToolCalls {
				functionName := "N/A"
				arguments := "{}"
				if toolCall.FunctionCall != nil {
					functionName = toolCall.FunctionCall.Name
					if toolCall.FunctionCall.Arguments != "" {
						arguments = toolCall.FunctionCall.Arguments
					}
				}
				p.logger.Infof("   Tool Call %d: ID=%s, Type=%s, Function=%s, Arguments=%s",
					i+1, toolCall.ID, toolCall.Type, functionName, arguments)
			}
			// Note: Tool call events are emitted later in the function (line ~1594) to avoid duplication
			// This is a valid response, continue processing
		} else if firstChoice.FuncCall != nil { // Legacy function call handling
			p.logger.Infof("✅ Valid function call response detected - Content is empty but FuncCall present")
			p.logger.Infof("   Function Call: Name=%s", firstChoice.FuncCall.Name)
			// This is a valid response, continue processing
		} else if handle, ok := llmtypes.ExtractCodingProviderSessionHandle(firstChoice.GenerationInfo); ok && !handle.Empty() {
			// LaunchOnly response: the adapter successfully started/reacquired the tmux transport
			// and embedded the session handle in GenerationInfo. Empty Content is intentional here.
			p.logger.Infof("✅ Valid coding-agent launch-only response - Content is empty but session handle present (provider=%s, tmux=%s)", handle.Provider, handle.TmuxSession)
			// This is a valid response, continue processing
		} else {
			// This is actually an empty content error
			p.logger.Infof("❌ Choice.Content is empty - this will cause 'no results' error")

			// Enhanced logging for ALL providers when choice content is empty
			p.logger.Errorf("🔍 Empty Choice Content Debug Information for %s:", string(p.provider))
			p.logger.Errorf("   Model ID: %s", p.modelID)
			p.logger.Errorf("   Provider: %s", string(p.provider))
			p.logger.Errorf("   Response Type: %T", resp)
			p.logger.Errorf("   Response Pointer: %p", resp)
			p.logger.Errorf("   Choices Count: %d", len(resp.Choices))
			p.logger.Errorf("   First Choice Type: %T", firstChoice)
			p.logger.Errorf("   First Choice Content Empty: %v", firstChoice.Content == "")

			p.logger.Errorf("   First Choice Content Length: %d", len(firstChoice.Content))

			// Detailed choice structure logging
			p.logger.Errorf("🔍 DETAILED CHOICE STRUCTURE:")
			p.logger.Errorf("   Choice.StopReason: %v", firstChoice.StopReason)
			toolCallsCount := 0
			if firstChoice.ToolCalls != nil {
				toolCallsCount = len(firstChoice.ToolCalls)
			}
			p.logger.Errorf("   Choice.ToolCalls: %v (nil: %v, count: %d)", firstChoice.ToolCalls != nil, firstChoice.ToolCalls == nil, toolCallsCount)
			if len(firstChoice.ToolCalls) > 0 {
				for i, tc := range firstChoice.ToolCalls {
					p.logger.Errorf("     ToolCall %d: ID=%s, Type=%s, FunctionName=%s, Arguments=%s",
						i+1, tc.ID, tc.Type, tc.FunctionCall.Name, truncateString(tc.FunctionCall.Arguments, 200))
				}
			}
			p.logger.Errorf("   Choice.FuncCall: %v", firstChoice.FuncCall != nil)
			if firstChoice.FuncCall != nil {
				p.logger.Errorf("     FuncCall Name: %s, Arguments: %s",
					firstChoice.FuncCall.Name, truncateString(firstChoice.FuncCall.Arguments, 200))
			}
			p.logger.Errorf("   Choice.GenerationInfo: %v (nil: %v)", firstChoice.GenerationInfo != nil, firstChoice.GenerationInfo == nil)
			if firstChoice.GenerationInfo != nil {
				info := firstChoice.GenerationInfo
				p.logger.Errorf("     GenerationInfo: InputTokens=%v, OutputTokens=%v, TotalTokens=%v",
					info.InputTokens, info.OutputTokens, info.TotalTokens)
				// Log additional fields if present
				if info.Additional != nil {
					for key, value := range info.Additional {
						valueStr := fmt.Sprintf("%v", value)
						if len(valueStr) > 200 {
							valueStr = truncateString(valueStr, 200)
						}
						p.logger.Errorf("       %s: %s (type: %T)", key, valueStr, value)
					}
				}
			}

			// Log the ENTIRE response structure for comprehensive debugging
			p.logger.Errorf("🔍 COMPLETE LLM RESPONSE STRUCTURE:")
			p.logger.Errorf("   Full Response: %+v", resp)

			// Serialize response to JSON for raw-like representation
			// Note: This is the processed response from langchaingo, not the raw HTTP response
			// but it gives us a JSON representation of what we received
			if respJSON, err := json.MarshalIndent(resp, "   ", "  "); err == nil {
				jsonStr := string(respJSON)
				// Truncate if too long to avoid massive log files
				if len(jsonStr) > 5000 {
					jsonStr = jsonStr[:5000] + "\n   ... (truncated, total length: " + fmt.Sprintf("%d", len(jsonStr)) + " bytes)"
				}
				p.logger.Errorf("🔍 RAW RESPONSE AS JSON (processed by langchaingo):")
				p.logger.Errorf("%s", jsonStr)
			} else {
				p.logger.Errorf("   ⚠️ Failed to serialize response to JSON: %w", err)
			}

			// Log the options that were passed to the LLM
			p.logger.Errorf("🔍 LLM CALL OPTIONS:")
			for i, opt := range options {
				p.logger.Errorf("   Option %d: %T = %+v", i+1, opt, opt)
			}

			// Log the messages that were sent to the LLM
			p.logger.Errorf("🔍 MESSAGES SENT TO LLM:")
			for i, msg := range messages {
				p.logger.Errorf("   Message %d - Role: %s, Parts: %d", i+1, msg.Role, len(msg.Parts))
				for j, part := range msg.Parts {
					p.logger.Errorf("     Part %d - Type: %T, Content: %+v", j+1, part, part)
				}
			}

			// Emit LLM generation error event for empty choice content
			errorMetadata := LLMMetadata{
				User: "llm_generation_user",
				CustomFields: map[string]string{
					"provider":        string(p.provider),
					"model_id":        p.modelID,
					"messages":        fmt.Sprintf("%d", len(messages)),
					"temperature":     fmt.Sprintf("%f", getTemperatureFromOptions(options)),
					"message_content": extractMessageContentAsString(messages),
					"error":           "Choice.Content is empty",
					"debug_note":      "Response validation failed - empty content",
				},
			}
			emitLLMGenerationError(p.eventEmitter, string(p.provider), p.modelID, OperationLLMGeneration, len(messages), getTemperatureFromOptions(options), extractMessageContentAsString(messages), fmt.Errorf("choice.Content is empty"), p.traceID, errorMetadata)

			// Include provider-specific API error if available (e.g. gemini_api_error from gemini-cli)
			emptyContentErr := "choice.Content is empty"
			if firstChoice.GenerationInfo != nil && firstChoice.GenerationInfo.Additional != nil {
				if apiErr, ok := firstChoice.GenerationInfo.Additional["gemini_api_error"].(string); ok && apiErr != "" {
					emptyContentErr = fmt.Sprintf("choice.Content is empty: %s", apiErr)
				}
			}
			return nil, fmt.Errorf("%s", emptyContentErr)
		}
	}

	// 🆕 ENHANCED SUCCESS LOGGING
	p.logger.Infof("✅ LLM generation validation passed - provider: %s, model: %s", string(p.provider), p.modelID)
	p.logger.Infof("✅ Response structure - Choices: %v, Choices count: %d", resp.Choices != nil, len(resp.Choices))
	if len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		p.logger.Infof("✅ First choice - Content: %v, Content length: %d, GenerationInfo: %v",
			choice.Content != "", len(choice.Content), choice.GenerationInfo != nil)
		if choice.GenerationInfo != nil {
			p.logger.Infof("✅ GenerationInfo available: InputTokens=%v, OutputTokens=%v, TotalTokens=%v",
				choice.GenerationInfo.InputTokens, choice.GenerationInfo.OutputTokens, choice.GenerationInfo.TotalTokens)
		}

		// Log tool calls if present (even when content is also present)
		if len(choice.ToolCalls) > 0 {
			p.logger.Infof("🔧 TOOL CALLS IN RESPONSE (%d):", len(choice.ToolCalls))
			for i, toolCall := range choice.ToolCalls {
				functionName := "N/A"
				arguments := "{}"
				if toolCall.FunctionCall != nil {
					functionName = toolCall.FunctionCall.Name
					if toolCall.FunctionCall.Arguments != "" {
						arguments = toolCall.FunctionCall.Arguments
					}
				}
				p.logger.Infof("   Tool Call %d: ID=%s, Type=%s, Function=%s, Arguments=%s",
					i+1, toolCall.ID, toolCall.Type, functionName, arguments)
			}
		}

		// Emit tool call events for all tool calls (even when content is present)
		if len(choice.ToolCalls) > 0 {
			for _, toolCall := range choice.ToolCalls {
				toolName := ""
				arguments := "{}"
				if toolCall.FunctionCall != nil {
					toolName = toolCall.FunctionCall.Name
					if toolCall.FunctionCall.Arguments != "" {
						arguments = toolCall.FunctionCall.Arguments
					}
				}

				toolCallMetadata := LLMMetadata{
					User: "tool_call_user",
					CustomFields: map[string]string{
						"provider":     string(p.provider),
						"model_id":     p.modelID,
						"tool_call_id": toolCall.ID,
						"tool_type":    toolCall.Type,
						"tool_name":    toolName,
					},
				}
				emitToolCallDetected(p.eventEmitter, string(p.provider), p.modelID, toolCall.ID, toolName, arguments, p.traceID, toolCallMetadata)
			}
		}
	}

	// Extract token usage using unified Usage struct (comprehensive extraction)
	var usage *llmtypes.Usage
	if resp.Usage != nil {
		// Use unified Usage field (already populated by adapters with all token types)
		usage = resp.Usage
	} else if len(resp.Choices) > 0 && resp.Choices[0].GenerationInfo != nil {
		// Fallback: Extract from GenerationInfo using comprehensive extraction
		usage = llmtypes.ExtractUsageFromGenerationInfo(resp.Choices[0].GenerationInfo)
	}

	if usage != nil {
		// Calculate total tokens if not provided by the provider
		if usage.TotalTokens == 0 && usage.InputTokens > 0 && usage.OutputTokens > 0 {
			usage.TotalTokens = usage.InputTokens + usage.OutputTokens
		}

		// Build comprehensive log message with all token types
		logMsg := fmt.Sprintf("Token usage extracted: Input=%d, Output=%d, Total=%d", usage.InputTokens, usage.OutputTokens, usage.TotalTokens)
		if usage.CacheTokens != nil && *usage.CacheTokens > 0 {
			logMsg += fmt.Sprintf(", Cached=%d", *usage.CacheTokens)
		}
		if usage.ThoughtsTokens != nil && *usage.ThoughtsTokens > 0 {
			logMsg += fmt.Sprintf(", Thoughts=%d", *usage.ThoughtsTokens)
		}
		if usage.ReasoningTokens != nil && *usage.ReasoningTokens > 0 {
			logMsg += fmt.Sprintf(", Reasoning=%d", *usage.ReasoningTokens)
		}
		p.logger.Infof(logMsg)

		// Emit LLM generation success event with comprehensive token usage
		successMetadata := LLMMetadata{
			User: "llm_generation_user",
			CustomFields: map[string]string{
				"provider":        string(p.provider),
				"model_id":        p.modelID,
				"messages":        fmt.Sprintf("%d", len(messages)),
				"temperature":     fmt.Sprintf("%f", getTemperatureFromOptions(options)),
				"message_content": extractMessageContentAsString(messages),
				"response_length": fmt.Sprintf("%d", len(resp.Choices[0].Content)),
				"choices_count":   fmt.Sprintf("%d", len(resp.Choices)),
				"input_tokens":    fmt.Sprintf("%d", usage.InputTokens),
				"output_tokens":   fmt.Sprintf("%d", usage.OutputTokens),
				"total_tokens":    fmt.Sprintf("%d", usage.TotalTokens),
			},
		}

		// Add optional token types to metadata if present
		if usage.CacheTokens != nil && *usage.CacheTokens > 0 {
			successMetadata.CustomFields["cache_tokens"] = fmt.Sprintf("%d", *usage.CacheTokens)
		}
		if usage.ThoughtsTokens != nil && *usage.ThoughtsTokens > 0 {
			successMetadata.CustomFields["thoughts_tokens"] = fmt.Sprintf("%d", *usage.ThoughtsTokens)
		}
		if usage.ReasoningTokens != nil && *usage.ReasoningTokens > 0 {
			successMetadata.CustomFields["reasoning_tokens"] = fmt.Sprintf("%d", *usage.ReasoningTokens)
		}

		successMetadata.CustomFields["note"] = "Token usage extracted from unified Usage struct"
		emitLLMGenerationSuccess(p.eventEmitter, string(p.provider), p.modelID, OperationLLMGeneration, len(messages), getTemperatureFromOptions(options), extractMessageContentAsString(messages), len(resp.Choices[0].Content), len(resp.Choices), p.traceID, successMetadata)
	} else {
		// No token usage available, emit success event without usage
		p.logger.Infof("No token usage available (neither resp.Usage nor GenerationInfo)")

		// Emit LLM generation success event without token usage
		successMetadata := LLMMetadata{
			User: "llm_generation_user",
			CustomFields: map[string]string{
				"provider":        string(p.provider),
				"model_id":        p.modelID,
				"messages":        fmt.Sprintf("%d", len(messages)),
				"temperature":     fmt.Sprintf("%f", getTemperatureFromOptions(options)),
				"message_content": extractMessageContentAsString(messages),
				"response_length": fmt.Sprintf("%d", len(resp.Choices[0].Content)),
				"choices_count":   fmt.Sprintf("%d", len(resp.Choices)),
				"note":            "No GenerationInfo available for token usage",
			},
		}
		emitLLMGenerationSuccess(p.eventEmitter, string(p.provider), p.modelID, OperationLLMGeneration, len(messages), getTemperatureFromOptions(options), extractMessageContentAsString(messages), len(resp.Choices[0].Content), len(resp.Choices), p.traceID, successMetadata)
	}

	return resp, nil
}

// extractMessageContentAsString converts message content to a readable string
func extractMessageContentAsString(messages []llmtypes.MessageContent) string {
	if len(messages) == 0 {
		return "no messages"
	}

	var result strings.Builder
	for i, msg := range messages {
		if i > 0 {
			result.WriteString(" | ")
		}
		result.WriteString(fmt.Sprintf("Role:%s", msg.Role))

		for j, part := range msg.Parts {
			if j > 0 {
				result.WriteString(",")
			}
			if textPart, ok := part.(llmtypes.TextContent); ok {
				content := textPart.Text
				if len(content) > 100 {
					content = content[:100] + "..."
				}
				result.WriteString(fmt.Sprintf("Text:%s", content))
			} else {
				result.WriteString(fmt.Sprintf("Part:%T", part))
			}
		}
	}
	return result.String()
}

// getTemperatureFromOptions extracts temperature from call options
func getTemperatureFromOptions(options []llmtypes.CallOption) float64 {
	// For now, return default temperature since CallOption is a function type
	// and we can't easily extract the temperature value
	return 0.7 // default temperature
}

// truncateString truncates a string to a specified length
func truncateString(s string, length int) string {
	if len(s) <= length {
		return s
	}
	return s[:length] + "..."
}

// WithOpenRouterUsage enables usage parameter for OpenRouter requests to get cache token information
func WithOpenRouterUsage() CallOption {
	return func(opts *CallOptions) {
		// Set the usage parameter in the request metadata (not CallOptions metadata)
		// This will be passed to the actual HTTP request body
		if opts.Metadata == nil {
			opts.Metadata = &llmtypes.Metadata{
				Usage: &llmtypes.UsageMetadata{Include: true},
			}
		} else {
			if opts.Metadata.Usage == nil {
				opts.Metadata.Usage = &llmtypes.UsageMetadata{Include: true}
			} else {
				opts.Metadata.Usage.Include = true
			}
		}
	}
}

// WithMCPConfig sets the MCP configuration JSON string for the Claude Code adapter session.
func WithMCPConfig(config string) llmtypes.CallOption {
	return claudecodeadapter.WithMCPConfig(config)
}

// WithDangerouslySkipPermissions enables the --dangerously-skip-permissions flag for the Claude Code CLI.
// CAUTION: This allows the agent to execute any tool without user confirmation.
func WithDangerouslySkipPermissions() llmtypes.CallOption {
	return claudecodeadapter.WithDangerouslySkipPermissions()
}

// WithClaudeCodeSettings sets the --settings flag for the Claude Code CLI.
// It accepts either a JSON string or a file path.
func WithClaudeCodeSettings(settings string) llmtypes.CallOption {
	return claudecodeadapter.WithClaudeCodeSettings(settings)
}

// WithClaudeCodeTools sets the --tools flag for the Claude Code CLI.
// Use "" to disable all built-in tools.
func WithClaudeCodeTools(tools string) llmtypes.CallOption {
	return claudecodeadapter.WithClaudeCodeTools(tools)
}

// WithAllowedTools sets the --allowed-tools flag for the Claude Code CLI.
// Example: "mcp__api-bridge__*" to allow all tools from the bridge.
func WithAllowedTools(tools string) llmtypes.CallOption {
	return claudecodeadapter.WithAllowedTools(tools)
}

// WithMaxTurns sets the --max-turns flag for the Claude Code CLI.
// Limits the number of agentic turns. Claude Code exits with an error when the limit is reached.
func WithMaxTurns(maxTurns int) llmtypes.CallOption {
	return claudecodeadapter.WithMaxTurns(maxTurns)
}

// WithResumeSessionID sets the --resume flag so the Claude Code CLI resumes
// an existing session instead of starting a new one.
func WithResumeSessionID(id string) llmtypes.CallOption {
	return claudecodeadapter.WithResumeSessionID(id)
}

// WithClaudeCodeInteractiveSessionID links a Claude Code tmux run to
// the owning application session so live follow-up input can be sent to it.
func WithClaudeCodeInteractiveSessionID(id string) llmtypes.CallOption {
	return claudecodeadapter.WithInteractiveSessionID(id)
}

// WithClaudeCodePersistentInteractiveSession keeps an interactive Claude Code
// tmux session alive across completed turns for normal chat. Workflow runs
// should keep the default per-turn lifecycle.
func WithClaudeCodePersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return claudecodeadapter.WithPersistentInteractiveSession(enabled)
}

// WithClaudeCodeWorkingDir sets the process working directory for Claude Code.
func WithClaudeCodeWorkingDir(dir string) llmtypes.CallOption {
	return claudecodeadapter.WithWorkingDir(dir)
}

// WithClaudeCodeWriteProjectInstructionFile controls whether the adapter
// ALSO projects the per-session system prompt into <workingDir>/CLAUDE.md
// (Claude Code's project-instructions convention), in addition to the
// --system-prompt-file injection. ON by default; pass false to opt out
// for repos where you want to preserve an operator-authored CLAUDE.md
// even across crash windows. Any pre-existing CLAUDE.md is byte-restored
// on session cleanup; a process crash between write and cleanup
// destroys the operator's prior content.
func WithClaudeCodeWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return claudecodeadapter.WithWriteProjectInstructionFile(enabled)
}

// WithClaudeCodeProjectInstructionOnly makes the adapter inject the
// per-session system prompt SOLELY via <workingDir>/CLAUDE.md and skip the
// --system-prompt-file flag. OFF by default. Claude Code auto-loads CLAUDE.md
// as project instructions, so the prompt is still applied — but only once,
// avoiding the doubled system prompt that otherwise results from passing the
// same bytes through both --system-prompt-file and CLAUDE.md. Requires the
// CLAUDE.md projection (on by default) and a working dir; if the projection is
// disabled or its write fails, the adapter falls back to --system-prompt-file.
func WithClaudeCodeProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return claudecodeadapter.WithProjectInstructionOnly(enabled)
}

// WithClaudeCodeEffort sets the --effort flag for the Claude Code CLI.
// Values: "low", "medium", "high", "max"
func WithClaudeCodeEffort(level string) llmtypes.CallOption {
	return claudecodeadapter.WithEffort(level)
}

// --- Gemini CLI Wrapper Functions ---

// WithGeminiModel sets the --model flag for the Gemini CLI.
func WithGeminiModel(model string) llmtypes.CallOption {
	return geminicli.WithGeminiModel(model)
}

// WithGeminiResumeSessionID sets the --resume flag so the Gemini CLI resumes
// an existing session instead of starting a new one.
func WithGeminiResumeSessionID(id string) llmtypes.CallOption {
	return geminicli.WithResumeSessionID(id)
}

// WithGeminiApprovalMode sets the --approval-mode flag for the Gemini CLI.
func WithGeminiApprovalMode(mode string) llmtypes.CallOption {
	return geminicli.WithApprovalMode(mode)
}

// WithGeminiSystemPromptFile sets the GEMINI_SYSTEM_MD environment variable path.
func WithGeminiSystemPromptFile(path string) llmtypes.CallOption {
	return geminicli.WithSystemPromptFile(path)
}

// WithGeminiProjectSettings writes a .gemini/settings.json in a temp directory
// and runs the Gemini CLI from there. This controls tool restrictions (tools.core),
// MCP server configuration (mcpServers), and other project-level settings.
func WithGeminiProjectSettings(settingsJSON string) llmtypes.CallOption {
	return geminicli.WithProjectSettings(settingsJSON)
}

// WithGeminiPolicyPath passes --policy to the Gemini CLI.
func WithGeminiPolicyPath(path string) llmtypes.CallOption {
	return geminicli.WithPolicyPath(path)
}

// WithGeminiAdminPolicyPath passes --admin-policy to the Gemini CLI.
func WithGeminiAdminPolicyPath(path string) llmtypes.CallOption {
	return geminicli.WithAdminPolicyPath(path)
}

// WithGeminiWorkingDir sets the Gemini CLI process working directory.
func WithGeminiWorkingDir(dir string) llmtypes.CallOption {
	return geminicli.WithWorkingDir(dir)
}

// WithGeminiWriteProjectInstructionFile controls whether the gemini
// adapter ALSO drops the per-session system prompt at <workingDir>/GEMINI.md
// (gemini-cli's project-context convention), in addition to the
// GEMINI_SYSTEM_MD env-var injection. ON by default; pass false to opt
// out. Cleanup byte-restores any pre-existing operator GEMINI.md; a
// process crash between write and cleanup destroys the operator's prior
// content.
func WithGeminiWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return geminicli.WithWriteProjectInstructionFile(enabled)
}

// WithGeminiProjectInstructionOnly carries the per-session system prompt solely
// via the projected GEMINI.md (auto-loaded as project context) and skips the
// GEMINI_SYSTEM_MD env injection, so the prompt is applied once instead of
// doubled. OFF by default. Falls back to GEMINI_SYSTEM_MD if the projection is
// disabled or its write fails.
func WithGeminiProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return geminicli.WithProjectInstructionOnly(enabled)
}

// WithGeminiAllowedTools sets the deprecated --allowed-tools flag for the Gemini CLI.
// Prefer WithGeminiProjectSettings plus Policy Engine rules instead.
func WithGeminiAllowedTools(tools string) llmtypes.CallOption {
	return geminicli.WithAllowedTools(tools)
}

// WithGeminiProjectDirID sets an explicit project directory ID for the Gemini CLI.
// This ensures resume calls use the same isolated project directory as the original invocation.
func WithGeminiProjectDirID(id string) llmtypes.CallOption {
	return geminicli.WithProjectDirID(id)
}

// WithGeminiProjectDirAbsolute overrides the default /tmp project directory with
// an absolute path. Used for workflow main_agent so GEMINI_PROJECT_DIR points at
// a workflow-rooted location (e.g. <workflow>/.gemini-main) instead of /tmp.
// Sub-step agents should leave this unset to keep /tmp isolation.
func WithGeminiProjectDirAbsolute(absPath string) llmtypes.CallOption {
	return geminicli.WithProjectDirAbsolute(absPath)
}

// WithGeminiInteractiveSessionID links a Gemini CLI interactive run to the
// owning application session so live follow-up input can be sent to it.
func WithGeminiInteractiveSessionID(id string) llmtypes.CallOption {
	return geminicli.WithInteractiveSessionID(id)
}

// WithGeminiPersistentInteractiveSession keeps a Gemini CLI tmux TUI alive
// across completed interactive chat turns.
func WithGeminiPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return geminicli.WithPersistentInteractiveSession(enabled)
}

// --- Codex CLI Wrapper Functions ---

// WithCodexResumeSessionID sets the session ID to resume via `codex exec resume`.
func WithCodexResumeSessionID(id string) llmtypes.CallOption {
	return codexcli.WithResumeSessionID(id)
}

// WithCursorResumeSessionID sets the --resume flag so cursor-agent resumes
// the chat by session id (the value cursor emits in its stream-json init
// event, also the directory name under ~/.cursor/chats/<md5(cwd)>/<id>).
// Mirrors the claude-code / gemini / codex equivalents.
func WithCursorResumeSessionID(id string) llmtypes.CallOption {
	return cursorcli.WithResumeSessionID(id)
}

// WithAgyResumeSessionID resumes an Antigravity CLI conversation by id.
func WithAgyResumeSessionID(id string) llmtypes.CallOption {
	return agycli.WithResumeSessionID(id)
}

// WithCodexInteractiveSessionID links a Codex CLI interactive run to the
// owning application session so live follow-up input can be sent to it.
func WithCodexInteractiveSessionID(id string) llmtypes.CallOption {
	return codexcli.WithInteractiveSessionID(id)
}

// WithCodexPersistentInteractiveSession keeps a Codex CLI tmux TUI alive across
// completed interactive chat turns.
func WithCodexPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return codexcli.WithPersistentInteractiveSession(enabled)
}

// WithCodexWriteProjectInstructionFile controls whether the codex
// adapter ALSO projects the per-session system prompt into
// <workingDir>/AGENTS.md (codex's project-instructions convention), in
// addition to the -c model_instructions_file injection. ON by default;
// pass false to opt out. Cleanup byte-restores any pre-existing
// operator AGENTS.md; a process crash between write and cleanup
// destroys the operator's prior content.
func WithCodexWriteProjectInstructionFile(enabled bool) llmtypes.CallOption {
	return codexcli.WithWriteProjectInstructionFile(enabled)
}

// WithCodexProjectInstructionOnly carries the per-session system prompt solely
// via the projected AGENTS.md and skips the codex developer_instructions /
// model_instructions_file CLI override, so the prompt is applied once instead
// of doubled. OFF by default. Falls back to the CLI override if the projection
// is disabled or its write fails.
func WithCodexProjectInstructionOnly(enabled bool) llmtypes.CallOption {
	return codexcli.WithProjectInstructionOnly(enabled)
}

// WithCodexApprovalPolicy sets the approval_policy config override for the Codex CLI.
// Values: "never" (auto-approve all), "on-request" (model decides), "untrusted" (most restrictive)
func WithCodexApprovalPolicy(policy string) llmtypes.CallOption {
	return codexcli.WithApprovalPolicy(policy)
}

// WithCodexReasoningEffort sets the model_reasoning_effort for the Codex CLI.
// Values: "none", "minimal", "low", "medium", "high", "xhigh"
func WithCodexReasoningEffort(effort string) llmtypes.CallOption {
	return codexcli.WithReasoningEffort(effort)
}

// WithCodexDisableShellTool disables the built-in shell tool in Codex CLI.
func WithCodexDisableShellTool() llmtypes.CallOption {
	return codexcli.WithDisableShellTool()
}

// WithCodexFullAuto enables --full-auto mode for the Codex CLI.
func WithCodexFullAuto() llmtypes.CallOption {
	return codexcli.WithFullAuto()
}

// WithCodexSandbox sets the --sandbox flag for the Codex CLI.
// Values: "read-only", "workspace-write", "danger-full-access"
func WithCodexSandbox(sandbox string) llmtypes.CallOption {
	return codexcli.WithSandbox(sandbox)
}

// WithCodexConfigOverrides passes arbitrary -c key=value overrides to the Codex CLI.
func WithCodexConfigOverrides(overrides []string) llmtypes.CallOption {
	return codexcli.WithConfigOverrides(overrides)
}

// WithCodexProjectDirID sets the --cd flag for the Codex CLI working directory.
func WithCodexProjectDirID(dir string) llmtypes.CallOption {
	return codexcli.WithProjectDirID(dir)
}

// WithCodexEnableFeatures enables one or more Codex CLI features (comma-separated).
func WithCodexEnableFeatures(features string) llmtypes.CallOption {
	return codexcli.WithEnableFeatures(features)
}

// WithCursorWorkingDir sets the Cursor Agent CLI workspace/cwd for tmux launch.
func WithCursorWorkingDir(dir string) llmtypes.CallOption {
	return cursorcli.WithWorkingDir(dir)
}

// WithCursorInteractiveSessionID links a Cursor Agent CLI tmux run to the
// owning application session for live follow-up input.
func WithCursorInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return cursorcli.WithInteractiveSessionID(sessionID)
}

// WithCursorPersistentInteractiveSession keeps the Cursor Agent CLI tmux
// session alive across turns.
func WithCursorPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return cursorcli.WithPersistentInteractiveSession(enabled)
}

// WithCursorMCPConfig writes a temporary/restored .cursor/mcp.json before
// launching Cursor Agent CLI.
func WithCursorMCPConfig(config string) llmtypes.CallOption {
	return cursorcli.WithMCPConfig(config)
}

// WithCursorProjectConfig writes a temporary/restored .cursor/cli.json before
// launching Cursor Agent CLI.
func WithCursorProjectConfig(config string) llmtypes.CallOption {
	return cursorcli.WithProjectConfig(config)
}

// WithCursorForce enables Cursor Agent CLI's --force flag.
func WithCursorForce() llmtypes.CallOption {
	return cursorcli.WithForce()
}

// WithCursorApproveMCPs enables Cursor Agent CLI's --approve-mcps flag, which
// auto-accepts the "approve this MCP server?" TUI consent dialog so bridge
// tool calls do not stall waiting for a human operator. Only useful when an
// MCP config is also provided (see WithCursorMCPConfig).
func WithCursorApproveMCPs() llmtypes.CallOption {
	return cursorcli.WithApproveMCPs()
}

// WithCursorDenyBuiltinTools installs a per-session .cursor/hooks.json
// that denies cursor's built-in Shell and Read tools via the
// beforeShellExecution + beforeReadFile hook events. The agent must then
// route those actions through the MCP bridge instead — pair with
// WithCursorMCPConfig so api-bridge.execute_shell_command and
// api-bridge.read_file are available. Cleanup at session teardown
// restores any pre-existing hooks.json the operator had in their
// workspace. This is the "hard lever" for bridge-only tool usage that
// the soft system-prompt coaching can't enforce reliably.
func WithCursorDenyBuiltinTools(enabled bool) llmtypes.CallOption {
	return cursorcli.WithDenyBuiltinTools(enabled)
}

// WithCursorMode sets Cursor Agent CLI's --mode flag. "ask" and "plan" are
// both read-only at the CLI level. Leave empty for normal agent mode.
//
// DEPRECATED FOR "ask" — prefer WithCursorDenyBuiltinTools(true) instead.
// Ask mode is a conversational stance that hard-refuses natural-language
// write requests with "Switch to Agent mode and ask…"; the orchestrator
// no longer uses it. To force the agent through the MCP bridge instead
// of cursor's built-in Read/Shell, install cursor hooks via
// WithCursorDenyBuiltinTools(true). "plan" mode remains a valid use of
// this option for read-only planning sessions.
func WithCursorMode(mode string) llmtypes.CallOption {
	return cursorcli.WithMode(mode)
}

// WithCursorSandbox sets Cursor Agent CLI's --sandbox flag. Supported values
// are "enabled" and "disabled".
func WithCursorSandbox(mode string) llmtypes.CallOption {
	return cursorcli.WithSandbox(mode)
}

// WithAgyWorkingDir sets the Antigravity CLI workspace/cwd for tmux launch.
func WithAgyWorkingDir(dir string) llmtypes.CallOption {
	return agycli.WithWorkingDir(dir)
}

// WithAgyInteractiveSessionID links an Antigravity CLI tmux run to the owning
// application session for live follow-up input.
func WithAgyInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return agycli.WithInteractiveSessionID(sessionID)
}

// WithAgyPersistentInteractiveSession keeps the Antigravity CLI tmux session
// alive across turns.
func WithAgyPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return agycli.WithPersistentInteractiveSession(enabled)
}

// WithAgyMCPConfig writes an Antigravity workspace MCP config candidate into
// .agents/mcp_config.json for the adapter-owned working directory.
func WithAgyMCPConfig(config string) llmtypes.CallOption {
	return agycli.WithMCPConfig(config)
}

// WithAgyBridgeOnlyTools writes an Antigravity workspace hook that denies
// built-in tools while leaving configured MCP bridge tools available.
func WithAgyBridgeOnlyTools(enabled bool) llmtypes.CallOption {
	return agycli.WithBridgeOnlyTools(enabled)
}

// WithAgyDangerouslySkipPermissions controls agy's
// --dangerously-skip-permissions launch flag.
func WithAgyDangerouslySkipPermissions(enabled bool) llmtypes.CallOption {
	return agycli.WithDangerouslySkipPermissions(enabled)
}

// WithAgySandbox sets Antigravity CLI's --sandbox flag.
func WithAgySandbox(mode string) llmtypes.CallOption {
	return agycli.WithSandbox(mode)
}

// WithPiWorkingDir sets the Pi CLI workspace/cwd for tmux launch.
func WithPiWorkingDir(dir string) llmtypes.CallOption {
	return picli.WithWorkingDir(dir)
}

// WithPiInteractiveSessionID links a Pi CLI tmux run to the owning application
// session for live follow-up input.
func WithPiInteractiveSessionID(sessionID string) llmtypes.CallOption {
	return picli.WithInteractiveSessionID(sessionID)
}

// WithPiPersistentInteractiveSession keeps the Pi CLI tmux session alive
// across turns.
func WithPiPersistentInteractiveSession(enabled bool) llmtypes.CallOption {
	return picli.WithPersistentInteractiveSession(enabled)
}

// WithPiResumeSessionID resumes a Pi native session created with --session-id.
func WithPiResumeSessionID(sessionID string) llmtypes.CallOption {
	return picli.WithResumeSessionID(sessionID)
}

// WithPiProvider overrides Pi's provider routing while keeping model selection
// separate. Model IDs can also be provider-qualified, e.g.
// google/gemini-3.5-flash.
func WithPiProvider(provider string) llmtypes.CallOption {
	return picli.WithProvider(provider)
}

// WithPiMCPConfig writes a Pi project MCP config override into .pi/mcp.json
// for the adapter-owned working directory.
func WithPiMCPConfig(config string) llmtypes.CallOption {
	return picli.WithMCPConfig(config)
}

// WithPiBridgeOnlyTools disables Pi's built-in tools while leaving explicit
// extension/custom tools, including the MCP adapter, enabled.
func WithPiBridgeOnlyTools(enabled bool) llmtypes.CallOption {
	return picli.WithBridgeOnlyTools(enabled)
}

// WithPiMCPExtension overrides the Pi extension source used for MCP support.
// The default is npm:pi-mcp-adapter.
func WithPiMCPExtension(source string) llmtypes.CallOption {
	return picli.WithMCPExtension(source)
}

// WithPiStatuslineExtension overrides the Pi statusline extension source.
// The default is npm:@narumitw/pi-statusline@0.8.0. Pass "off", "false",
// "0", or "none" to disable the adapter-managed statusline extension.
func WithPiStatuslineExtension(source string) llmtypes.CallOption {
	return picli.WithStatuslineExtension(source)
}

// LLM Configuration Management Functions

// LLMDefaultsResponse represents the response structure for LLM defaults
type LLMDefaultsResponse struct {
	PrimaryConfig           map[string]interface{} `json:"primary_config"`
	OpenrouterConfig        map[string]interface{} `json:"openrouter_config"`
	BedrockConfig           map[string]interface{} `json:"bedrock_config"`
	OpenaiConfig            map[string]interface{} `json:"openai_config"`
	AnthropicConfig         map[string]interface{} `json:"anthropic_config"`
	AzureConfig             map[string]interface{} `json:"azure_config"`
	ZAIConfig               map[string]interface{} `json:"zai_config"`
	KimiConfig              map[string]interface{} `json:"kimi_config"`
	MinimaxConfig           map[string]interface{} `json:"minimax_config"`
	MinimaxCodingPlanConfig map[string]interface{} `json:"minimax_coding_plan_config"`
	ElevenLabsConfig        map[string]interface{} `json:"elevenlabs_config"`
	DeepgramConfig          map[string]interface{} `json:"deepgram_config"`
	AvailableModels         map[string][]string    `json:"available_models"`
}

// APIKeyValidationRequest represents a request to validate an API key
type APIKeyValidationRequest struct {
	Provider string                 `json:"provider"`
	APIKey   string                 `json:"api_key"`
	ModelID  string                 `json:"model_id,omitempty"` // Optional model ID for Bedrock validation
	Options  map[string]interface{} `json:"options,omitempty"`
}

// APIKeyValidationResponse represents the response for API key validation
type APIKeyValidationResponse struct {
	Valid            bool                   `json:"valid"`
	Message          string                 `json:"message,omitempty"`
	Error            string                 `json:"error,omitempty"`
	CorrectedOptions map[string]interface{} `json:"corrected_options,omitempty"`
}

// GetLLMDefaults returns default LLM configurations from environment variables
func GetLLMDefaults() LLMDefaultsResponse {
	// Get primary configuration from environment
	defaultProvider := os.Getenv("AGENT_PROVIDER")
	if defaultProvider == "" {
		defaultProvider = "openrouter" // fallback default
	}

	defaultModel := os.Getenv("AGENT_MODEL")
	if defaultModel == "" {
		defaultModel = "x-ai/grok-code-fast-1" // fallback default
	}

	// Parse fallback models
	fallbackStr := os.Getenv("OPENROUTER_FALLBACK_MODELS")
	var fallbackModels []string
	if fallbackStr != "" {
		fallbackModels = strings.Split(fallbackStr, ",")
		for i, model := range fallbackModels {
			fallbackModels[i] = strings.TrimSpace(model)
		}
	} else {
		fallbackModels = []string{} // No fallback defaults
	}

	// Parse cross-provider fallback
	crossProvider := os.Getenv("OPENROUTER_CROSS_FALLBACK_PROVIDER")
	if crossProvider == "" {
		crossProvider = "openai" // Default fallback provider
	}
	crossModelsStr := os.Getenv("OPENROUTER_CROSS_FALLBACK_MODELS")
	if crossModelsStr == "" {
		crossModelsStr = os.Getenv("OPEN_ROUTER_CROSS_FALLBACK_MODELS") // Fallback to old naming
	}
	var crossModels []string
	if crossModelsStr != "" {
		crossModels = strings.Split(crossModelsStr, ",")
		for i, model := range crossModels {
			crossModels[i] = strings.TrimSpace(model)
		}
	} else {
		crossModels = []string{} // No cross-provider fallback defaults
	}

	var crossProviderFallback *map[string]interface{}
	if crossProvider != "" && len(crossModels) > 0 {
		crossProviderFallback = &map[string]interface{}{
			"provider": crossProvider,
			"models":   crossModels,
		}
	}

	// Get API keys from environment for prefilling
	openrouterAPIKey := os.Getenv("OPENROUTER_API_KEY")
	if openrouterAPIKey == "" {
		openrouterAPIKey = os.Getenv("OPEN_ROUTER_API_KEY") // Fallback to old naming
	}
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")

	// Bedrock configuration
	bedrockModel := os.Getenv("BEDROCK_MODEL")
	if bedrockModel == "" {
		bedrockModel = os.Getenv("BEDROCK_PRIMARY_MODEL") // Fallback to old naming
	}
	if bedrockModel == "" {
		bedrockModel = "us.anthropic.claude-sonnet-4-20250514-v1:0" // fallback default
	}

	bedrockFallbackStr := os.Getenv("BEDROCK_FALLBACK_MODELS")
	var bedrockFallbacks []string
	if bedrockFallbackStr != "" {
		bedrockFallbacks = strings.Split(bedrockFallbackStr, ",")
		for i, model := range bedrockFallbacks {
			bedrockFallbacks[i] = strings.TrimSpace(model)
		}
	} else {
		bedrockFallbacks = []string{} // No fallback defaults
	}

	bedrockRegion := os.Getenv("BEDROCK_REGION")
	if bedrockRegion == "" {
		bedrockRegion = "us-east-1" // fallback default
	}

	bedrockCrossProvider := os.Getenv("BEDROCK_CROSS_FALLBACK_PROVIDER")
	if bedrockCrossProvider == "" {
		bedrockCrossProvider = "openai" // Default fallback provider
	}
	bedrockCrossModelsStr := os.Getenv("BEDROCK_CROSS_FALLBACK_MODELS")
	if bedrockCrossModelsStr == "" {
		bedrockCrossModelsStr = os.Getenv("BEDROCK_OPENAI_FALLBACK_MODELS") // Fallback to old naming
	}
	var bedrockCrossModels []string
	if bedrockCrossModelsStr != "" {
		bedrockCrossModels = strings.Split(bedrockCrossModelsStr, ",")
		for i, model := range bedrockCrossModels {
			bedrockCrossModels[i] = strings.TrimSpace(model)
		}
	} else {
		bedrockCrossModels = []string{} // No cross-provider fallback defaults
	}

	var bedrockCrossProviderFallback *map[string]interface{}
	if bedrockCrossProvider != "" && len(bedrockCrossModels) > 0 {
		bedrockCrossProviderFallback = &map[string]interface{}{
			"provider": bedrockCrossProvider,
			"models":   bedrockCrossModels,
		}
	}

	// OpenAI configuration
	openaiModel := os.Getenv("OPENAI_MODEL")
	if openaiModel == "" {
		openaiModel = os.Getenv("OPENAI_PRIMARY_MODEL") // Fallback to old naming
	}
	if openaiModel == "" {
		openaiModel = "gpt-4o" // fallback default
	}

	openaiFallbackStr := os.Getenv("OPENAI_FALLBACK_MODELS")
	var openaiFallbacks []string
	if openaiFallbackStr != "" {
		openaiFallbacks = strings.Split(openaiFallbackStr, ",")
		for i, model := range openaiFallbacks {
			openaiFallbacks[i] = strings.TrimSpace(model)
		}
	} else {
		openaiFallbacks = []string{} // No fallback defaults
	}

	openaiCrossProvider := os.Getenv("OPENAI_CROSS_FALLBACK_PROVIDER")
	if openaiCrossProvider == "" {
		openaiCrossProvider = "bedrock" // Default fallback provider
	}
	openaiCrossModelsStr := os.Getenv("OPENAI_CROSS_FALLBACK_MODELS")
	if openaiCrossModelsStr == "" {
		openaiCrossModelsStr = os.Getenv("OPENAI_BEDROCK_FALLBACK_MODELS") // Fallback to old naming
	}
	var openaiCrossModels []string
	if openaiCrossModelsStr != "" {
		openaiCrossModels = strings.Split(openaiCrossModelsStr, ",")
		for i, model := range openaiCrossModels {
			openaiCrossModels[i] = strings.TrimSpace(model)
		}
	} else {
		openaiCrossModels = []string{} // No cross-provider fallback defaults
	}

	var openaiCrossProviderFallback *map[string]interface{}
	if openaiCrossProvider != "" && len(openaiCrossModels) > 0 {
		openaiCrossProviderFallback = &map[string]interface{}{
			"provider": openaiCrossProvider,
			"models":   openaiCrossModels,
		}
	}

	// Anthropic configuration
	anthropicModel := os.Getenv("ANTHROPIC_PRIMARY_MODEL")
	if anthropicModel == "" {
		anthropicModel = "claude-sonnet-4-20250514"
	}
	anthropicAPIKey := os.Getenv("ANTHROPIC_API_KEY")

	// Azure configuration
	azureModel := os.Getenv("AZURE_PRIMARY_MODEL")
	azureAPIKey := os.Getenv("AZURE_AI_API_KEY")
	azureEndpoint := os.Getenv("AZURE_AI_ENDPOINT")

	// Z.AI configuration
	zaiModel := os.Getenv("ZAI_PRIMARY_MODEL")
	if zaiModel == "" {
		zaiModel = zaiadapter.ModelGLM51
	}
	zaiAPIKey := os.Getenv("ZAI_API_KEY")

	// Kimi configuration
	kimiModel := os.Getenv("KIMI_PRIMARY_MODEL")
	if kimiModel == "" {
		kimiModel = kimiadapter.ModelKimiK26
	}
	kimiAPIKey := os.Getenv("KIMI_API_KEY")

	// MiniMax configuration
	minimaxModel := os.Getenv("MINIMAX_PRIMARY_MODEL")
	if minimaxModel == "" {
		minimaxModel = "MiniMax-M2.7"
	}
	minimaxAPIKey := os.Getenv("MINIMAX_API_KEY")

	// MiniMax Coding Plan configuration (uses Anthropic model names)
	minimaxCodingPlanModel := os.Getenv("MINIMAX_CODING_PLAN_PRIMARY_MODEL")
	if minimaxCodingPlanModel == "" {
		minimaxCodingPlanModel = "claude-sonnet-4-5"
	}
	minimaxCodingPlanAPIKey := os.Getenv("MINIMAX_CODING_PLAN_API_KEY")

	// ElevenLabs configuration for media tools
	elevenLabsModel := os.Getenv("ELEVENLABS_PRIMARY_MODEL")
	if elevenLabsModel == "" {
		elevenLabsModel = elevenlabsadapter.DefaultModelID
	}
	elevenLabsAPIKey := os.Getenv("ELEVENLABS_API_KEY")

	// Deepgram configuration for media tools
	deepgramModel := os.Getenv("DEEPGRAM_PRIMARY_MODEL")
	if deepgramModel == "" {
		deepgramModel = deepgramadapter.DefaultTranscriptionModelID
	}
	deepgramAPIKey := os.Getenv("DEEPGRAM_API_KEY")

	// Build response
	return LLMDefaultsResponse{
		PrimaryConfig: map[string]interface{}{
			"provider":                defaultProvider,
			"model_id":                defaultModel,
			"fallback_models":         fallbackModels,
			"cross_provider_fallback": crossProviderFallback,
		},
		OpenrouterConfig: map[string]interface{}{
			"provider":                "openrouter",
			"model_id":                defaultModel,
			"fallback_models":         fallbackModels,
			"cross_provider_fallback": crossProviderFallback,
			"api_key":                 openrouterAPIKey,
		},
		BedrockConfig: map[string]interface{}{
			"provider":                "bedrock",
			"model_id":                bedrockModel,
			"fallback_models":         bedrockFallbacks,
			"cross_provider_fallback": bedrockCrossProviderFallback,
			"region":                  bedrockRegion,
		},
		OpenaiConfig: map[string]interface{}{
			"provider":                "openai",
			"model_id":                openaiModel,
			"fallback_models":         openaiFallbacks,
			"cross_provider_fallback": openaiCrossProviderFallback,
			"api_key":                 openaiAPIKey,
		},
		AnthropicConfig: map[string]interface{}{
			"provider":        "anthropic",
			"model_id":        anthropicModel,
			"fallback_models": []string{},
			"api_key":         anthropicAPIKey,
		},
		AzureConfig: map[string]interface{}{
			"provider":        "azure",
			"model_id":        azureModel,
			"fallback_models": []string{},
			"api_key":         azureAPIKey,
			"endpoint":        azureEndpoint,
		},
		ZAIConfig: map[string]interface{}{
			"provider":        "z-ai",
			"model_id":        zaiModel,
			"fallback_models": []string{},
			"api_key":         zaiAPIKey,
		},
		KimiConfig: map[string]interface{}{
			"provider":        "kimi",
			"model_id":        kimiModel,
			"fallback_models": []string{},
			"api_key":         kimiAPIKey,
		},
		MinimaxConfig: map[string]interface{}{
			"provider":        "minimax",
			"model_id":        minimaxModel,
			"fallback_models": []string{},
			"api_key":         minimaxAPIKey,
		},
		MinimaxCodingPlanConfig: map[string]interface{}{
			"provider":        "minimax-coding-plan",
			"model_id":        minimaxCodingPlanModel,
			"fallback_models": []string{},
			"api_key":         minimaxCodingPlanAPIKey,
		},
		ElevenLabsConfig: map[string]interface{}{
			"provider":        "elevenlabs",
			"model_id":        elevenLabsModel,
			"fallback_models": []string{},
			"api_key":         elevenLabsAPIKey,
		},
		DeepgramConfig: map[string]interface{}{
			"provider":        "deepgram",
			"model_id":        deepgramModel,
			"fallback_models": []string{},
			"api_key":         deepgramAPIKey,
		},
		AvailableModels: map[string][]string{
			"bedrock":             getBedrockAvailableModels(),
			"openrouter":          getOpenRouterAvailableModels(),
			"openai":              getOpenAIAvailableModels(),
			"anthropic":           getAnthropicAvailableModels(),
			"azure":               getAzureAvailableModels(),
			"z-ai":                getZAIAvailableModels(),
			"kimi":                getKimiAvailableModels(),
			"minimax":             getMiniMaxAvailableModels(),
			"minimax-coding-plan": getMiniMaxCodingPlanAvailableModels(),
			"elevenlabs":          getElevenLabsAvailableModels(),
			"deepgram":            getDeepgramAvailableModels(),
		},
	}
}

func getZAIAvailableModels() []string {
	modelsStr := os.Getenv("ZAI_AVAILABLE_MODELS")
	if modelsStr != "" {
		var models []string
		for _, m := range strings.Split(modelsStr, ",") {
			if t := strings.TrimSpace(m); t != "" {
				models = append(models, t)
			}
		}
		if len(models) > 0 {
			return models
		}
	}

	return zaiadapter.GetDefaultVisibleZAIModelIDs()
}

func getKimiAvailableModels() []string {
	modelsStr := os.Getenv("KIMI_AVAILABLE_MODELS")
	if modelsStr != "" {
		var models []string
		for _, m := range strings.Split(modelsStr, ",") {
			if t := strings.TrimSpace(m); t != "" {
				models = append(models, t)
			}
		}
		if len(models) > 0 {
			return models
		}
	}

	return kimiadapter.GetDefaultVisibleKimiModelIDs()
}

// getMiniMaxCodingPlanAvailableModels returns Anthropic model names available via MiniMax coding plan
func getMiniMaxCodingPlanAvailableModels() []string {
	modelsStr := os.Getenv("MINIMAX_CODING_PLAN_AVAILABLE_MODELS")
	if modelsStr != "" {
		var models []string
		for _, m := range strings.Split(modelsStr, ",") {
			if t := strings.TrimSpace(m); t != "" {
				models = append(models, t)
			}
		}
		if len(models) > 0 {
			return models
		}
	}
	// Default: Anthropic model names that MiniMax coding plan supports
	return []string{
		"claude-sonnet-4-5",
		"claude-opus-4-6",
		"claude-haiku-4-5-20251001",
	}
}

func getElevenLabsAvailableModels() []string {
	modelsStr := os.Getenv("ELEVENLABS_AVAILABLE_MODELS")
	if modelsStr != "" {
		var models []string
		for _, m := range strings.Split(modelsStr, ",") {
			if t := strings.TrimSpace(m); t != "" {
				models = append(models, t)
			}
		}
		if len(models) > 0 {
			return models
		}
	}
	return []string{
		elevenlabsadapter.DefaultModelID,
		"eleven_turbo_v2_5",
		"eleven_flash_v2_5",
		"eleven_v3",
		elevenlabsadapter.DefaultMusicModelID,
	}
}

func getDeepgramAvailableModels() []string {
	modelsStr := os.Getenv("DEEPGRAM_AVAILABLE_MODELS")
	if modelsStr != "" {
		var models []string
		for _, m := range strings.Split(modelsStr, ",") {
			if t := strings.TrimSpace(m); t != "" {
				models = append(models, t)
			}
		}
		if len(models) > 0 {
			return models
		}
	}
	return []string{
		deepgramadapter.DefaultTranscriptionModelID,
		"nova-3-multilingual",
		"nova-2",
		"base",
		deepgramadapter.DefaultModelID,
		"aura-2-luna-en",
		"aura-2-asteria-en",
		"aura-2-apollo-en",
	}
}

// ValidateAPIKey validates API keys for OpenRouter, OpenAI, Bedrock, and Vertex
func ValidateAPIKey(req APIKeyValidationRequest) APIKeyValidationResponse {
	// Use fmt.Printf for logging in validation functions
	fmt.Printf("[API KEY VALIDATION] Request received for provider: %s\n", req.Provider)

	var isValid bool
	var message string
	var err error
	var correctedOptions map[string]interface{}

	fmt.Printf("[API KEY VALIDATION] Validating %s API key\n", req.Provider)
	switch req.Provider {
	case "openrouter":
		isValid, message, err = validateOpenRouterAPIKey(req.APIKey, req.ModelID, req.Options)
	case "openai":
		isValid, message, err = validateOpenAIAPIKey(req.APIKey, req.ModelID, req.Options)
	case "bedrock":
		// Bedrock uses AWS credentials, test them instead of API key
		fmt.Printf("[API KEY VALIDATION] Testing AWS Bedrock credentials\n")
		isValid, message, err = validateBedrockCredentials(req.ModelID, req.Options)
	case "vertex":
		// Vertex supports both API key and OAuth authentication
		if req.APIKey == "" {
			// Test OAuth authentication (gcloud/service account/ADC)
			fmt.Printf("[API KEY VALIDATION] Testing Vertex AI OAuth credentials\n")
			isValid, message, err = validateVertexCredentials(req.ModelID, req.Options)
		} else {
			// Test API key authentication
			fmt.Printf("[API KEY VALIDATION] Testing Vertex AI API key\n")
			isValid, message, err = validateVertexAPIKey(req.APIKey, req.ModelID, req.Options)
		}
	case "anthropic":
		// Anthropic validation with real GenerateContent call
		fmt.Printf("[API KEY VALIDATION] Testing Anthropic API key\n")
		isValid, message, err = validateAnthropicAPIKey(req.APIKey, req.ModelID, req.Options)
	case "minimax":
		// MiniMax validation with real GenerateContent call
		fmt.Printf("[API KEY VALIDATION] Testing MiniMax API key\n")
		isValid, message, err = validateMinimaxAPIKey(req.APIKey, req.ModelID, req.Options)
	case "minimax-coding-plan":
		// MiniMax Coding Plan validation — uses Anthropic model names
		fmt.Printf("[API KEY VALIDATION] Testing MiniMax Coding Plan API key\n")
		isValid, message, err = validateMinimaxCodingPlanAPIKey(req.APIKey, req.ModelID, req.Options)
	case "elevenlabs":
		fmt.Printf("[API KEY VALIDATION] Testing ElevenLabs API key\n")
		isValid, message, err = validateElevenLabsAPIKey(req.APIKey)
	case "deepgram":
		fmt.Printf("[API KEY VALIDATION] Testing Deepgram API key\n")
		isValid, message, err = validateDeepgramAPIKey(req.APIKey)
	case "azure":
		// Azure AI validation with real GenerateContent call
		fmt.Printf("[API KEY VALIDATION] Testing Azure AI API key\n")
		isValid, message, correctedOptions, err = validateAzureAPIKey(req.APIKey, req.ModelID, req.Options)
	case "z-ai":
		fmt.Printf("[API KEY VALIDATION] Testing Z.AI API key\n")
		isValid, message, err = validateZAIAPIKey(req.APIKey, req.ModelID, req.Options)
	case "kimi":
		fmt.Printf("[API KEY VALIDATION] Testing Kimi API key\n")
		isValid, message, err = validateKimiAPIKey(req.APIKey, req.ModelID, req.Options)
	default:
		fmt.Printf("[API KEY VALIDATION WARN] Unsupported provider: %s\n", req.Provider)
		return APIKeyValidationResponse{
			Valid: false,
			Error: "Unsupported provider",
		}
	}

	// Handle validation errors
	if err != nil {
		fmt.Printf("[API KEY VALIDATION ERROR] %s validation failed: %v\n", req.Provider, err)
		return APIKeyValidationResponse{
			Valid: false,
			Error: fmt.Sprintf("Validation failed: %v", err),
		}
	}

	// Return validation result
	if isValid {
		fmt.Printf("[API KEY VALIDATION SUCCESS] %s: %s\n", req.Provider, message)
	} else {
		fmt.Printf("[API KEY VALIDATION FAILED] %s: %s\n", req.Provider, message)
	}

	return APIKeyValidationResponse{
		Valid:            isValid,
		Message:          message,
		CorrectedOptions: correctedOptions,
	}
}

func validateZAIAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[ZAI VALIDATION] Starting API key validation\n")

	if apiKey == "" {
		return false, "Z.AI API key is required", nil
	}

	if modelID == "" {
		modelID = zaiadapter.ModelGLM51
		fmt.Printf("[ZAI VALIDATION] Using default model: %s\n", modelID)
	}

	originalKey := os.Getenv("ZAI_API_KEY")
	os.Setenv("ZAI_API_KEY", apiKey)
	defer func() {
		if originalKey != "" {
			os.Setenv("ZAI_API_KEY", originalKey)
		} else {
			os.Unsetenv("ZAI_API_KEY")
		}
	}()

	noopLog := &noopLoggerImpl{}
	temperature := extractTemperatureFromOptions(options)

	config := Config{
		Provider:    ProviderZAI,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
	}

	llm, err := initializeZAI(config)
	if err != nil {
		fmt.Printf("[ZAI VALIDATION ERROR] Failed to create LLM instance: %v\n", err)
		return false, fmt.Sprintf("Failed to create Z.AI LLM instance: %v", err), nil
	}

	callOptions := createCallOptionsFromMap(options)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say hello in one word."}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[ZAI VALIDATION ERROR] %v\n", err)
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") {
			return false, "Invalid Z.AI API key", nil
		}
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			return false, "Z.AI API rate limit exceeded", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "Z.AI service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("Z.AI test generation failed: %v", err), nil
	}

	fmt.Printf("[ZAI VALIDATION SUCCESS] Z.AI API key is valid\n")
	return true, fmt.Sprintf("Z.AI API key is valid for model %s", modelID), nil
}

func validateKimiAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[KIMI VALIDATION] Starting API key validation\n")

	if apiKey == "" {
		return false, "Kimi API key is required", nil
	}
	if !strings.HasPrefix(apiKey, "sk-kimi-") && !strings.HasPrefix(apiKey, "sk-") {
		return false, "Invalid Kimi API key format", nil
	}

	if modelID == "" {
		modelID = kimiadapter.ModelKimiK26
		fmt.Printf("[KIMI VALIDATION] Using default model: %s\n", modelID)
	}

	noopLog := &noopLoggerImpl{}
	temperature := extractTemperatureFromOptions(options)

	config := Config{
		Provider:    ProviderKimi,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
		APIKeys: &ProviderAPIKeys{
			Kimi: &apiKey,
		},
	}

	llm, err := initializeKimi(config)
	if err != nil {
		fmt.Printf("[KIMI VALIDATION ERROR] Failed to create LLM instance: %v\n", err)
		return false, fmt.Sprintf("Failed to create Kimi LLM instance: %v", err), nil
	}

	callOptions := createCallOptionsFromMap(options)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply with exactly: KIMI_OK"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[KIMI VALIDATION ERROR] %v\n", err)
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") {
			return false, "Invalid Kimi API key", nil
		}
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			return false, "Kimi API rate limit exceeded", nil
		}
		if strings.Contains(err.Error(), "claude cli not found") {
			return false, "Claude Code CLI not found. Install it with: npm install -g @anthropic-ai/claude-code", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "Kimi service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("Kimi test generation failed: %v", err), nil
	}

	fmt.Printf("[KIMI VALIDATION SUCCESS] Kimi API key is valid\n")
	return true, fmt.Sprintf("Kimi API key is valid for model %s", modelID), nil
}

// validateOpenRouterAPIKey validates an OpenRouter API key by making a real GenerateContent call
func validateOpenRouterAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[OPENROUTER VALIDATION] Starting API key validation\n")

	// Basic format validation
	if !strings.HasPrefix(apiKey, "sk-or-") {
		fmt.Printf("[OPENROUTER VALIDATION WARN] Format validation failed - missing sk-or- prefix\n")
		return false, "Invalid OpenRouter API key format", nil
	}
	fmt.Printf("[OPENROUTER VALIDATION] Format validation passed\n")

	// Use a default model if none provided
	if modelID == "" {
		modelID = "moonshotai/kimi-k2"
		fmt.Printf("[OPENROUTER VALIDATION] Using default model: %s\n", modelID)
	}

	// Set API key in environment temporarily for initialization
	originalKey := os.Getenv("OPEN_ROUTER_API_KEY")
	os.Setenv("OPEN_ROUTER_API_KEY", apiKey)
	defer func() {
		if originalKey != "" {
			os.Setenv("OPEN_ROUTER_API_KEY", originalKey)
		} else {
			os.Unsetenv("OPEN_ROUTER_API_KEY")
		}
	}()

	// Create a no-op logger for validation
	noopLog := &noopLoggerImpl{}

	// Extract temperature from options (no default - let the model use its own default)
	temperature := extractTemperatureFromOptions(options)

	// Create OpenRouter LLM instance
	fmt.Printf("[OPENROUTER VALIDATION] Creating OpenRouter LLM instance (temperature: %v)\n", temperature)
	config := Config{
		Provider:    ProviderOpenRouter,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
	}

	llm, err := initializeOpenRouter(config)
	if err != nil {
		fmt.Printf("[OPENROUTER VALIDATION ERROR] Failed to create LLM instance: %v\n", err)
		return false, fmt.Sprintf("Failed to create OpenRouter LLM instance: %v", err), nil
	}

	// Create call options from map
	callOptions := createCallOptionsFromMap(options)

	// Test the LLM with a simple generation call
	fmt.Printf("[OPENROUTER VALIDATION] Making test generation call to OpenRouter\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hi"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[OPENROUTER VALIDATION ERROR] OpenRouter test generation failed: %v\n", err)
		// Check for specific error types
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") {
			return false, "Invalid OpenRouter API key", nil
		}
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			return false, "OpenRouter API rate limit exceeded", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "OpenRouter service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("OpenRouter test generation failed: %v", err), nil
	}

	fmt.Printf("[OPENROUTER VALIDATION SUCCESS] OpenRouter API key is valid\n")
	return true, fmt.Sprintf("OpenRouter API key is valid for model %s", modelID), nil
}

// validateOpenAIAPIKey validates an OpenAI API key by making a real GenerateContent call
func validateOpenAIAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[OPENAI VALIDATION] Starting API key validation\n")
	// Basic format validation
	if !strings.HasPrefix(apiKey, "sk-") {
		fmt.Printf("[OPENAI VALIDATION WARN] Format validation failed - missing sk- prefix\n")
		return false, "Invalid OpenAI API key format", nil
	}
	fmt.Printf("[OPENAI VALIDATION] Format validation passed\n")

	// Use a default model if none provided
	if modelID == "" {
		modelID = "gpt-4o-mini"
		fmt.Printf("[OPENAI VALIDATION] Using default model: %s\n", modelID)
	}

	// Set API key in environment temporarily for initialization
	originalKey := os.Getenv("OPENAI_API_KEY")
	os.Setenv("OPENAI_API_KEY", apiKey)
	defer func() {
		if originalKey != "" {
			os.Setenv("OPENAI_API_KEY", originalKey)
		} else {
			os.Unsetenv("OPENAI_API_KEY")
		}
	}()

	// Create a no-op logger for validation
	noopLog := &noopLoggerImpl{}

	// Extract temperature from options (no default - let the model use its own default)
	temperature := extractTemperatureFromOptions(options)

	// Create OpenAI LLM instance
	fmt.Printf("[OPENAI VALIDATION] Creating OpenAI LLM instance (temperature: %v)\n", temperature)
	config := Config{
		Provider:    ProviderOpenAI,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
	}

	llm, err := initializeOpenAI(config)
	if err != nil {
		fmt.Printf("[OPENAI VALIDATION ERROR] Failed to create LLM instance: %v\n", err)
		return false, fmt.Sprintf("Failed to create OpenAI LLM instance: %v", err), nil
	}

	// Create call options from map
	callOptions := createCallOptionsFromMap(options)

	// Test the LLM with a simple generation call
	fmt.Printf("[OPENAI VALIDATION] Making test generation call to OpenAI\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hi"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[OPENAI VALIDATION ERROR] OpenAI test generation failed: %v\n", err)
		// Check for specific error types
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") {
			return false, "Invalid OpenAI API key", nil
		}
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			return false, "OpenAI API rate limit exceeded", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "OpenAI service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("OpenAI test generation failed: %v", err), nil
	}

	fmt.Printf("[OPENAI VALIDATION SUCCESS] OpenAI API key is valid\n")
	return true, fmt.Sprintf("OpenAI API key is valid for model %s", modelID), nil
}

// validateAnthropicAPIKey validates an Anthropic API key by making a real GenerateContent call
func validateAnthropicAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[ANTHROPIC VALIDATION] Starting API key validation\n")

	// Basic format validation - Anthropic API keys start with "sk-ant-"
	if !strings.HasPrefix(apiKey, "sk-ant-") {
		fmt.Printf("[ANTHROPIC VALIDATION WARN] Format validation failed - missing sk-ant- prefix\n")
		return false, "Invalid Anthropic API key format", nil
	}
	fmt.Printf("[ANTHROPIC VALIDATION] Format validation passed\n")

	// Use a default model if none provided. Haiku 4.5 is the cheapest
	// current model — perfect for a low-cost auth-check call.
	if modelID == "" {
		modelID = "claude-haiku-4-5"
		fmt.Printf("[ANTHROPIC VALIDATION] Using default model: %s\n", modelID)
	}

	// Set API key in environment temporarily for initialization
	originalKey := os.Getenv("ANTHROPIC_API_KEY")
	os.Setenv("ANTHROPIC_API_KEY", apiKey)
	defer func() {
		if originalKey != "" {
			os.Setenv("ANTHROPIC_API_KEY", originalKey)
		} else {
			os.Unsetenv("ANTHROPIC_API_KEY")
		}
	}()

	// Create a no-op logger for validation
	noopLog := &noopLoggerImpl{}

	// Extract temperature from options (no default - let the model use its own default)
	temperature := extractTemperatureFromOptions(options)

	// Create Anthropic LLM instance
	fmt.Printf("[ANTHROPIC VALIDATION] Creating Anthropic LLM instance (temperature: %v)\n", temperature)
	config := Config{
		Provider:    ProviderAnthropic,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
	}

	llm, err := initializeAnthropic(config)
	if err != nil {
		fmt.Printf("[ANTHROPIC VALIDATION ERROR] Failed to create LLM instance: %v\n", err)
		return false, fmt.Sprintf("Failed to create Anthropic LLM instance: %v", err), nil
	}

	// Create call options from map
	callOptions := createCallOptionsFromMap(options)

	// Test the LLM with a simple generation call
	fmt.Printf("[ANTHROPIC VALIDATION] Making test generation call to Anthropic\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hi"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[ANTHROPIC VALIDATION ERROR] Anthropic test generation failed: %v\n", err)
		// Check for specific error types
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") {
			return false, "Invalid Anthropic API key", nil
		}
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			return false, "Anthropic API rate limit exceeded", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "Anthropic service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("Anthropic test generation failed: %v", err), nil
	}

	fmt.Printf("[ANTHROPIC VALIDATION SUCCESS] Anthropic API key is valid\n")
	return true, fmt.Sprintf("Anthropic API key is valid for model %s", modelID), nil
}

// validateMinimaxAPIKey validates a MiniMax API key by making a real GenerateContent call
func validateMinimaxAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[MINIMAX VALIDATION] Starting API key validation\n")

	if apiKey == "" {
		return false, "MiniMax API key is required", nil
	}

	// Use a default model if none provided
	if modelID == "" {
		modelID = "MiniMax-M2.7"
		fmt.Printf("[MINIMAX VALIDATION] Using default model: %s\n", modelID)
	}

	// Set API key in environment temporarily for initialization
	originalKey := os.Getenv("MINIMAX_API_KEY")
	os.Setenv("MINIMAX_API_KEY", apiKey)
	defer func() {
		if originalKey != "" {
			os.Setenv("MINIMAX_API_KEY", originalKey)
		} else {
			os.Unsetenv("MINIMAX_API_KEY")
		}
	}()

	noopLog := &noopLoggerImpl{}
	temperature := extractTemperatureFromOptions(options)

	config := Config{
		Provider:    ProviderMiniMax,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
	}

	llm, err := initializeMiniMax(config)
	if err != nil {
		fmt.Printf("[MINIMAX VALIDATION ERROR] Failed to create LLM instance: %v\n", err)
		return false, fmt.Sprintf("Failed to create MiniMax LLM instance: %v", err), nil
	}

	callOptions := createCallOptionsFromMap(options)

	fmt.Printf("[MINIMAX VALIDATION] Making test generation call\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hi"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[MINIMAX VALIDATION ERROR] MiniMax test generation failed: %v\n", err)
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "1004") {
			return false, "Invalid MiniMax API key", nil
		}
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			return false, "MiniMax API rate limit exceeded", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "MiniMax service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("MiniMax test generation failed: %v", err), nil
	}

	fmt.Printf("[MINIMAX VALIDATION SUCCESS] MiniMax API key is valid\n")
	return true, fmt.Sprintf("MiniMax API key is valid for model %s", modelID), nil
}

func validateElevenLabsAPIKey(apiKey string) (bool, string, error) {
	fmt.Printf("[ELEVENLABS VALIDATION] Starting API key validation\n")
	if strings.TrimSpace(apiKey) == "" {
		return false, "ElevenLabs API key is required", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.elevenlabs.io/v1/user", nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("xi-api-key", strings.TrimSpace(apiKey))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "ElevenLabs service timeout - check network connectivity", nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, "ElevenLabs API key is valid", nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, "Invalid ElevenLabs API key", nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return false, "ElevenLabs API rate limit exceeded", nil
	}
	return false, fmt.Sprintf("ElevenLabs validation failed with status %d: %s", resp.StatusCode, truncateValidationBody(data)), nil
}

func validateDeepgramAPIKey(apiKey string) (bool, string, error) {
	fmt.Printf("[DEEPGRAM VALIDATION] Starting API key validation\n")
	if strings.TrimSpace(apiKey) == "" {
		return false, "Deepgram API key is required", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deepgram.com/v1/projects", nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Token "+strings.TrimSpace(apiKey))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "Deepgram service timeout - check network connectivity", nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, "Deepgram API key is valid", nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return false, "Invalid Deepgram API key", nil
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return false, "Deepgram API rate limit exceeded", nil
	}
	return false, fmt.Sprintf("Deepgram validation failed with status %d: %s", resp.StatusCode, truncateValidationBody(data)), nil
}

func truncateValidationBody(data []byte) string {
	msg := strings.TrimSpace(string(data))
	if len(msg) > 300 {
		return msg[:300] + "..."
	}
	return msg
}

// validateMinimaxCodingPlanAPIKey validates a MiniMax coding plan API key using an Anthropic model name.
func validateMinimaxCodingPlanAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[MINIMAX-CP VALIDATION] Starting coding plan API key validation\n")

	if apiKey == "" {
		return false, "MiniMax Coding Plan API key is required", nil
	}

	if modelID == "" {
		modelID = "claude-sonnet-4-5"
		fmt.Printf("[MINIMAX-CP VALIDATION] Using default model: %s\n", modelID)
	}

	noopLog := &noopLoggerImpl{}
	temperature := extractTemperatureFromOptions(options)

	config := Config{
		Provider:    ProviderMiniMaxCodingPlan,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
	}

	os.Setenv("MINIMAX_CODING_PLAN_API_KEY", apiKey)
	llm, err := initializeMiniMaxCodingPlan(config)
	if err != nil {
		return false, fmt.Sprintf("Failed to create MiniMax Coding Plan LLM instance: %v", err), nil
	}

	callOptions := createCallOptionsFromMap(options)
	_, err = llm.GenerateContent(context.Background(), []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Say hello in one word."}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[MINIMAX-CP VALIDATION ERROR] %v\n", err)
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") {
			return false, "Invalid MiniMax Coding Plan API key", nil
		}
		return false, fmt.Sprintf("MiniMax Coding Plan test generation failed: %v", err), nil
	}

	fmt.Printf("[MINIMAX-CP VALIDATION SUCCESS] MiniMax Coding Plan API key is valid\n")
	return true, fmt.Sprintf("MiniMax Coding Plan API key is valid for model %s", modelID), nil
}

// validateAzureAPIKey validates an Azure AI API key by making a real GenerateContent call
// Returns: isValid, message, correctedOptions, error
func validateAzureAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, map[string]interface{}, error) {
	fmt.Printf("[AZURE VALIDATION] Starting API key validation for model: %s\n", modelID)

	// Check for required endpoint in options
	var endpoint string
	if options != nil {
		if e, ok := options["endpoint"].(string); ok {
			endpoint = e
		}
	}
	if endpoint == "" {
		// Fallback to environment variable
		endpoint = os.Getenv("AZURE_AI_ENDPOINT")
	}
	if endpoint == "" {
		fmt.Printf("[AZURE VALIDATION WARN] No endpoint provided\n")
		return false, "Azure endpoint URL is required", nil, nil
	}
	fmt.Printf("[AZURE VALIDATION] Endpoint: %s\n", endpoint)

	// Basic validation - API key should not be empty
	if apiKey == "" {
		fmt.Printf("[AZURE VALIDATION WARN] API key is empty\n")
		return false, "Azure API key is required", nil, nil
	}
	fmt.Printf("[AZURE VALIDATION] API key format validation passed\n")

	// Use a default model if none provided
	if modelID == "" {
		modelID = "gpt-4o"
		fmt.Printf("[AZURE VALIDATION] Using default model: %s\n", modelID)
	}

	// Helper to try validation with a specific endpoint and modelID
	tryValidation := func(testEndpoint string, testModelID string) (bool, string, error) {
		// Extract optional fields from options
		var apiVersion, region string
		if options != nil {
			if v, ok := options["api_version"].(string); ok {
				apiVersion = v
			}
			if r, ok := options["region"].(string); ok {
				region = r
			}
		}

		// Set environment variables temporarily for initialization
		originalEndpoint := os.Getenv("AZURE_AI_ENDPOINT")
		originalKey := os.Getenv("AZURE_AI_API_KEY")
		originalVersion := os.Getenv("AZURE_AI_API_VERSION")
		originalRegion := os.Getenv("AZURE_AI_REGION")

		os.Setenv("AZURE_AI_ENDPOINT", testEndpoint)
		os.Setenv("AZURE_AI_API_KEY", apiKey)
		if apiVersion != "" {
			os.Setenv("AZURE_AI_API_VERSION", apiVersion)
		}
		if region != "" {
			os.Setenv("AZURE_AI_REGION", region)
		}
		defer func() {
			// Restore original environment variables
			if originalEndpoint != "" {
				os.Setenv("AZURE_AI_ENDPOINT", originalEndpoint)
			} else {
				os.Unsetenv("AZURE_AI_ENDPOINT")
			}
			if originalKey != "" {
				os.Setenv("AZURE_AI_API_KEY", originalKey)
			} else {
				os.Unsetenv("AZURE_AI_API_KEY")
			}
			if originalVersion != "" {
				os.Setenv("AZURE_AI_API_VERSION", originalVersion)
			} else {
				os.Unsetenv("AZURE_AI_API_VERSION")
			}
			if originalRegion != "" {
				os.Setenv("AZURE_AI_REGION", originalRegion)
			} else {
				os.Unsetenv("AZURE_AI_REGION")
			}
		}()

		// Create a no-op logger for validation
		noopLog := &noopLoggerImpl{}

		// Extract temperature from options
		temperature := extractTemperatureFromOptions(options)

		// Create Azure LLM instance
		fmt.Printf("[AZURE VALIDATION] Creating Azure LLM instance (endpoint: %s, model: %s)\n", testEndpoint, testModelID)
		config := Config{
			Provider:    ProviderAzure,
			ModelID:     testModelID,
			Temperature: temperature,
			Logger:      noopLog,
			Context:     context.Background(),
		}

		llm, err := initializeAzure(config)
		if err != nil {
			return false, fmt.Sprintf("Failed to create Azure LLM instance: %v", err), nil
		}

		// Create call options from map
		callOptions := createCallOptionsFromMap(options)

		// Test the LLM with a simple generation call
		fmt.Printf("[AZURE VALIDATION] Making test generation call to Azure AI\n")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
			{
				Role:  llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hi"}},
			},
		}, callOptions...)
		if err != nil {
			// Check for specific error types
			if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "Unauthorized") {
				return false, "Invalid Azure API key", nil
			}
			if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
				return false, "Azure API rate limit exceeded", nil
			}
			if strings.Contains(err.Error(), "timeout") {
				return false, "Azure service timeout - check network connectivity", nil
			}
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "unknown_model") || strings.Contains(err.Error(), "Unknown model") || strings.Contains(err.Error(), "DeploymentNotFound") {
				return false, fmt.Sprintf("Model '%s' not found or endpoint incorrect", testModelID), nil
			}
			return false, fmt.Sprintf("Azure test generation failed: %v", err), nil
		}

		return true, fmt.Sprintf("Azure API key is valid for model %s", testModelID), nil
	}

	// Clean Model ID: strip date suffix like -2026-01-14
	cleanModelID := modelID
	if parts := strings.Split(modelID, "-20"); len(parts) > 1 {
		cleanModelID = parts[0]
	}

	// Try combinations
	endpoints := []string{endpoint}

	// Case 1: services.ai.azure.com -> cognitiveservices.azure.com
	if strings.Contains(endpoint, "services.ai.azure.com") {
		parts := strings.Split(endpoint, "services.ai.azure.com")
		if len(parts) > 0 {
			prefix := parts[0]
			if strings.HasPrefix(prefix, "https://") {
				resourceName := strings.TrimPrefix(prefix, "https://")
				resourceName = strings.TrimSuffix(resourceName, ".")
				if resourceName != "" {
					derivedEndpoint := fmt.Sprintf("https://%s.cognitiveservices.azure.com/", resourceName)
					endpoints = append(endpoints, derivedEndpoint)
				}
			}
		}
	}

	models := []string{modelID}
	if cleanModelID != modelID {
		models = append(models, cleanModelID)
	}

	var lastMessage string
	var lastErr error

	for _, testEndpoint := range endpoints {
		for _, testModel := range models {
			isValid, message, err := tryValidation(testEndpoint, testModel)
			if isValid {
				// Success! Check if we need to return corrected options
				correctedOptions := make(map[string]interface{})
				if options != nil {
					for k, v := range options {
						correctedOptions[k] = v
					}
				}

				isCorrected := false
				if testEndpoint != endpoint {
					correctedOptions["endpoint"] = testEndpoint
					isCorrected = true
				}
				if testModel != modelID {
					correctedOptions["model_id"] = testModel
					isCorrected = true
				}

				if isCorrected {
					msg := fmt.Sprintf("%s (Note: We automatically optimized your configuration to endpoint: %s, model: %s)", message, testEndpoint, testModel)
					return true, msg, correctedOptions, nil
				}
				return true, message, nil, nil
			}
			lastMessage = message
			lastErr = err
		}
	}

	return false, lastMessage, nil, lastErr
}

// validateVertexCredentials validates Vertex AI OAuth credentials (gcloud/service account/ADC)
func validateVertexCredentials(modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[VERTEX VALIDATION] Starting OAuth credentials validation\n")

	// Check for required environment variables
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = os.Getenv("VERTEX_PROJECT_ID")
	}
	if projectID == "" {
		fmt.Printf("[VERTEX VALIDATION WARN] GOOGLE_CLOUD_PROJECT or VERTEX_PROJECT_ID not set\n")
		return false, "GOOGLE_CLOUD_PROJECT or VERTEX_PROJECT_ID environment variable is required for OAuth authentication", nil
	}

	location := os.Getenv("GOOGLE_CLOUD_LOCATION")
	if location == "" {
		location = os.Getenv("VERTEX_LOCATION_ID")
	}
	if location == "" {
		location = "us-central1"
		fmt.Printf("[VERTEX VALIDATION] Using default location: %s\n", location)
	}
	// Vertex AI doesn't support "global" location
	if location == "global" {
		location = "us-central1"
		fmt.Printf("[VERTEX VALIDATION] Location 'global' is not valid for Vertex AI, using: %s\n", location)
	}

	fmt.Printf("[VERTEX VALIDATION] Testing OAuth with project: %s, location: %s\n", projectID, location)

	// Use a default model if none provided
	if modelID == "" {
		modelID = vertexadapter.ModelGemini31FlashLitePreview
		fmt.Printf("[VERTEX VALIDATION] Using default model: %s\n", modelID)
	}

	// Test OAuth by creating an LLM instance and making a real API call
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a no-op logger for validation
	noopLog := &noopLoggerImpl{}

	// Detect if this is an Anthropic model (starts with "claude-")
	isAnthropicModel := strings.HasPrefix(modelID, "claude-")

	var llm llmtypes.Model
	var err error

	if isAnthropicModel {
		// Create Vertex Anthropic adapter for OAuth
		fmt.Printf("[VERTEX VALIDATION] Creating Vertex Anthropic adapter for model: %s\n", modelID)
		llm = vertexadapter.NewVertexAnthropicAdapter(projectID, location, modelID, noopLog)
	} else {
		// For Gemini models with OAuth, use Vertex AI backend (not Gemini Developer API)
		fmt.Printf("[VERTEX VALIDATION] Creating Vertex AI Gemini adapter for model: %s with OAuth\n", modelID)

		// Set environment variables for Vertex AI OAuth (genai library reads these)
		originalProject := os.Getenv("GOOGLE_CLOUD_PROJECT")
		originalLocation := os.Getenv("GOOGLE_CLOUD_LOCATION")
		originalUseVertex := os.Getenv("GOOGLE_GENAI_USE_VERTEXAI")

		os.Setenv("GOOGLE_CLOUD_PROJECT", projectID)
		os.Setenv("GOOGLE_CLOUD_LOCATION", location)
		os.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")

		defer func() {
			if originalProject != "" {
				os.Setenv("GOOGLE_CLOUD_PROJECT", originalProject)
			} else {
				os.Unsetenv("GOOGLE_CLOUD_PROJECT")
			}
			if originalLocation != "" {
				os.Setenv("GOOGLE_CLOUD_LOCATION", originalLocation)
			} else {
				os.Unsetenv("GOOGLE_CLOUD_LOCATION")
			}
			if originalUseVertex != "" {
				os.Setenv("GOOGLE_GENAI_USE_VERTEXAI", originalUseVertex)
			} else {
				os.Unsetenv("GOOGLE_GENAI_USE_VERTEXAI")
			}
		}()

		// Create Google GenAI client with OAuth (no API key, uses BackendVertexAI via env vars)
		client, err := genai.NewClient(ctx, &genai.ClientConfig{
			Backend: genai.BackendVertexAI,
		})
		if err != nil {
			fmt.Printf("[VERTEX VALIDATION ERROR] Failed to create Vertex AI client: %v\n", err)
			return false, fmt.Sprintf("Failed to create Vertex AI client for Gemini model '%s': %v", modelID, err), nil
		}

		// Create Gemini adapter with Vertex AI backend
		llm = vertexadapter.NewGoogleGenAIAdapter(client, modelID, noopLog)
	}

	// Create call options from map
	callOptions := createCallOptionsFromMap(options)

	// Test the LLM with a simple generation call
	fmt.Printf("[VERTEX VALIDATION] Making test generation call to Vertex AI\n")
	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hi"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[VERTEX VALIDATION ERROR] Vertex AI test generation failed: %v\n", err)
		// Check for specific error types
		if strings.Contains(err.Error(), "authentication") || strings.Contains(err.Error(), "unauthorized") {
			return false, "OAuth authentication failed. Make sure you have run 'gcloud auth application-default login' or set up service account credentials.", nil
		}
		if strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), "forbidden") {
			return false, "OAuth credentials do not have permission to access Vertex AI", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "Vertex AI service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("Vertex AI test generation failed: %v", err), nil
	}

	fmt.Printf("[VERTEX VALIDATION SUCCESS] Vertex AI OAuth credentials are valid\n")
	return true, fmt.Sprintf("Vertex AI OAuth authentication successful (project: %s, location: %s)", projectID, location), nil
}

// validateVertexAPIKey validates a Vertex AI (Google Gemini) API key by making a real GenerateContent call
func validateVertexAPIKey(apiKey string, modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[VERTEX VALIDATION] Starting API key validation\n")
	// Basic validation - Google API keys don't have a specific prefix
	if apiKey == "" {
		fmt.Printf("[VERTEX VALIDATION WARN] API key is empty\n")
		return false, "API key is empty", nil
	}
	fmt.Printf("[VERTEX VALIDATION] API key format check passed\n")

	// Use a default model if none provided
	if modelID == "" {
		modelID = vertexadapter.ModelGemini31FlashLitePreview
		fmt.Printf("[VERTEX VALIDATION] Using default model: %s\n", modelID)
	}

	// Set API key in environment temporarily for initialization
	originalKey := os.Getenv("VERTEX_API_KEY")
	originalGoogleKey := os.Getenv("GOOGLE_API_KEY")
	os.Setenv("VERTEX_API_KEY", apiKey)
	defer func() {
		if originalKey != "" {
			os.Setenv("VERTEX_API_KEY", originalKey)
		} else {
			os.Unsetenv("VERTEX_API_KEY")
		}
		if originalGoogleKey != "" {
			os.Setenv("GOOGLE_API_KEY", originalGoogleKey)
		}
	}()

	// Create a no-op logger for validation
	noopLog := &noopLoggerImpl{}

	// Extract temperature from options (no default - let the model use its own default)
	temperature := extractTemperatureFromOptions(options)

	// Create Vertex LLM instance (for Gemini models with API key)
	fmt.Printf("[VERTEX VALIDATION] Creating Vertex Gemini LLM instance (temperature: %v)\n", temperature)
	config := Config{
		Provider:    ProviderVertex,
		ModelID:     modelID,
		Temperature: temperature,
		Logger:      noopLog,
		Context:     context.Background(),
		APIKeys: &ProviderAPIKeys{
			Vertex: &apiKey,
		},
	}

	llm, err := initializeVertexGemini(config, modelID, noopLog)
	if err != nil {
		fmt.Printf("[VERTEX VALIDATION ERROR] Failed to create LLM instance: %v\n", err)
		return false, fmt.Sprintf("Failed to create Vertex LLM instance: %v", err), nil
	}

	// Create call options from map
	callOptions := createCallOptionsFromMap(options)

	// Test the LLM with a simple generation call
	fmt.Printf("[VERTEX VALIDATION] Making test generation call to Vertex AI\n")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Hi"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[VERTEX VALIDATION ERROR] Vertex AI test generation failed: %v\n", err)
		// Check for specific error types
		if strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "401") {
			return false, "Invalid Vertex AI API key", nil
		}
		if strings.Contains(err.Error(), "permission") || strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "403") {
			return false, "API key lacks required permissions", nil
		}
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "404") {
			return false, fmt.Sprintf("Model %s not found", modelID), nil
		}
		if strings.Contains(err.Error(), "rate limit") || strings.Contains(err.Error(), "429") {
			return false, "Vertex AI API rate limit exceeded", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "Vertex AI service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("Vertex AI test generation failed: %v", err), nil
	}

	fmt.Printf("[VERTEX VALIDATION SUCCESS] Vertex AI API key is valid\n")
	return true, fmt.Sprintf("Vertex AI API key is valid for model %s", modelID), nil
}

// noopLoggerImpl is a no-op logger implementation for validation functions
type noopLoggerImpl struct{}

func (n *noopLoggerImpl) Infof(format string, v ...any)             {}
func (n *noopLoggerImpl) Errorf(format string, v ...any)            {}
func (n *noopLoggerImpl) Debugf(format string, args ...interface{}) {}

// DefaultLogger is a simple logger implementation that writes to stdout or a file
type DefaultLogger struct {
	output *os.File
	level  string
}

// NewDefaultLogger creates a new default logger instance
// If logFile is empty, logs to stdout. If logFile is provided, logs to that file.
// level can be "info" or "debug" - debug level enables Debugf output
func NewDefaultLogger(logFile string, level string) (interfaces.Logger, error) {
	var output *os.File
	var err error

	if logFile == "" {
		// Use stdout
		output = os.Stdout
	} else {
		// Create log directory if it doesn't exist
		logDir := filepath.Dir(logFile)
		if logDir != "." && logDir != "" {
			if err := os.MkdirAll(logDir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create log directory: %w", err)
			}
		}

		// Open log file
		output, err = os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
	}

	// Validate level
	if level != "info" && level != "debug" {
		level = "info" // Default to info if invalid
	}

	return &DefaultLogger{
		output: output,
		level:  level,
	}, nil
}

// Infof logs an info message
func (l *DefaultLogger) Infof(format string, v ...any) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(l.output, "[%s] [INFO] %s\n", timestamp, fmt.Sprintf(format, v...))
}

// Errorf logs an error message
func (l *DefaultLogger) Errorf(format string, v ...any) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(l.output, "[%s] [ERROR] %s\n", timestamp, fmt.Sprintf(format, v...))
}

// Debugf logs a debug message (only if level is "debug")
func (l *DefaultLogger) Debugf(format string, args ...interface{}) {
	if l.level == "debug" {
		timestamp := time.Now().Format("2006-01-02 15:04:05")
		fmt.Fprintf(l.output, "[%s] [DEBUG] %s\n", timestamp, fmt.Sprintf(format, args...))
	}
}

// validateBedrockCredentials validates AWS Bedrock credentials and region
func validateBedrockCredentials(modelID string, options map[string]interface{}) (bool, string, error) {
	fmt.Printf("[BEDROCK VALIDATION] Starting AWS Bedrock credentials validation\n")
	// Check if AWS region is configured
	region := os.Getenv("AWS_REGION")
	if region == "" {
		fmt.Printf("[BEDROCK VALIDATION WARN] AWS_REGION environment variable not set\n")
		return false, "AWS_REGION environment variable not set", nil
	}
	fmt.Printf("[BEDROCK VALIDATION] AWS region: %s", region)

	// Check if AWS credentials are configured
	accessKey := os.Getenv("AWS_ACCESS_KEY_ID")
	secretKey := os.Getenv("AWS_SECRET_ACCESS_KEY")

	if accessKey == "" || secretKey == "" {
		fmt.Printf("[BEDROCK VALIDATION WARN] AWS credentials not configured\n")
		return false, "AWS credentials not configured (AWS_ACCESS_KEY_ID or AWS_SECRET_ACCESS_KEY missing)", nil
	}
	fmt.Printf("[BEDROCK VALIDATION] AWS credentials configured\n")
	// Use provided model ID or fallback to default
	if modelID == "" {
		modelID = "us.anthropic.claude-3-haiku-20240307-v1:0" // fallback default
		fmt.Printf("[BEDROCK VALIDATION] Using fallback model ID: %s\n", modelID)
	} else {
		fmt.Printf("[BEDROCK VALIDATION] Using provided model ID: %s\n", modelID)
	}

	// Test Bedrock access by creating a Bedrock LLM instance
	fmt.Printf("[BEDROCK VALIDATION] Testing Bedrock access by creating LLM instance\n")
	// Load AWS SDK configuration
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(region))
	if err != nil {
		fmt.Printf("[BEDROCK VALIDATION ERROR] Failed to load AWS config: %v\n", err)
		return false, "Failed to load AWS configuration", err
	}

	// Create Bedrock runtime client
	client := bedrockruntime.NewFromConfig(cfg)

	// Create a simple no-op logger for validation
	noopLog := &noopLoggerImpl{}
	// Create Bedrock adapter instance
	llm := bedrockadapter.NewBedrockAdapter(client, modelID, noopLog)

	// Create call options from map
	callOptions := createCallOptionsFromMap(options)

	// Test the LLM with a simple generation call
	fmt.Printf("[BEDROCK VALIDATION] Making test generation call to Bedrock\n")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err = llm.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "test"}},
		},
	}, callOptions...)
	if err != nil {
		fmt.Printf("[BEDROCK VALIDATION ERROR] Bedrock test generation failed: %v\n", err)
		// Check for specific error types
		if strings.Contains(err.Error(), "AccessDenied") {
			return false, "AWS credentials do not have permission to access Bedrock", nil
		}
		if strings.Contains(err.Error(), "InvalidUserID.NotFound") {
			return false, "AWS credentials are invalid", nil
		}
		if strings.Contains(err.Error(), "timeout") {
			return false, "Bedrock service timeout - check network connectivity", nil
		}
		return false, fmt.Sprintf("Bedrock test generation failed: %v", err), nil
	}

	fmt.Printf("[BEDROCK VALIDATION SUCCESS] AWS Bedrock credentials are valid\n")
	return true, "AWS Bedrock credentials are valid", nil
}

// Helper functions to get available models from environment variables

// getBedrockAvailableModels returns available Bedrock models from environment variables
func getBedrockAvailableModels() []string {
	// Get from environment variable
	modelsStr := os.Getenv("BEDROCK_AVAILABLE_MODELS")
	if modelsStr == "" {
		// Fallback to old naming
		modelsStr = os.Getenv("BEDROCK_MODELS")
	}
	if modelsStr == "" {
		// Return empty array if no environment variable is set
		return []string{}
	}

	// Parse comma-separated models
	models := strings.Split(modelsStr, ",")
	for i, model := range models {
		models[i] = strings.TrimSpace(model)
	}
	return models
}

// getOpenRouterAvailableModels returns available OpenRouter models from environment variables
func getOpenRouterAvailableModels() []string {
	// Get from environment variable
	modelsStr := os.Getenv("OPENROUTER_AVAILABLE_MODELS")
	if modelsStr == "" {
		// Fallback to old naming
		modelsStr = os.Getenv("OPEN_ROUTER_MODELS")
	}
	if modelsStr == "" {
		// Return empty array if no environment variable is set
		return []string{}
	}

	// Parse comma-separated models
	models := strings.Split(modelsStr, ",")
	for i, model := range models {
		models[i] = strings.TrimSpace(model)
	}
	return models
}

// getOpenAIAvailableModels returns available OpenAI models from environment variables
func getOpenAIAvailableModels() []string {
	// Get from environment variable
	modelsStr := os.Getenv("OPENAI_AVAILABLE_MODELS")
	if modelsStr == "" {
		// Fallback to old naming
		modelsStr = os.Getenv("OPENAI_MODELS")
	}
	if modelsStr == "" {
		// Return empty array if no environment variable is set
		return []string{}
	}

	// Parse comma-separated models
	models := strings.Split(modelsStr, ",")
	for i, model := range models {
		models[i] = strings.TrimSpace(model)
	}
	return models
}

// getAnthropicAvailableModels returns available Anthropic models from environment variables
func getAnthropicAvailableModels() []string {
	// Get from environment variable
	modelsStr := os.Getenv("ANTHROPIC_AVAILABLE_MODELS")
	if modelsStr == "" {
		modelsStr = os.Getenv("ANTHROPIC_MODELS")
	}
	if modelsStr == "" {
		return []string{}
	}

	// Parse comma-separated models
	models := strings.Split(modelsStr, ",")
	for i, model := range models {
		models[i] = strings.TrimSpace(model)
	}
	return models
}

// getAzureAvailableModels returns available Azure models from environment variables
func getAzureAvailableModels() []string {
	// Get from environment variable
	modelsStr := os.Getenv("AZURE_AVAILABLE_MODELS")
	if modelsStr == "" {
		modelsStr = os.Getenv("AZURE_MODELS")
	}
	if modelsStr == "" {
		return []string{}
	}

	// Parse comma-separated models
	models := strings.Split(modelsStr, ",")
	for i, model := range models {
		models[i] = strings.TrimSpace(model)
	}
	return models
}

// getMiniMaxAvailableModels returns available MiniMax models from environment variables
func getMiniMaxAvailableModels() []string {
	modelsStr := os.Getenv("MINIMAX_AVAILABLE_MODELS")
	if modelsStr == "" {
		modelsStr = os.Getenv("MINIMAX_MODELS")
	}
	if modelsStr == "" {
		return []string{}
	}
	models := strings.Split(modelsStr, ",")
	for i, model := range models {
		models[i] = strings.TrimSpace(model)
	}
	return models
}

// Helper to create call options from map
// extractTemperatureFromOptions extracts temperature from options map
// Returns 0 if not found (model will use its own default)
func extractTemperatureFromOptions(options map[string]interface{}) float64 {
	if options == nil {
		return 0
	}
	if temp, ok := options["temperature"].(float64); ok {
		return temp
	}
	if temp, ok := options["temperature"].(int); ok {
		return float64(temp)
	}
	return 0
}

func createCallOptionsFromMap(options map[string]interface{}) []llmtypes.CallOption {
	var callOptions []llmtypes.CallOption
	if options == nil {
		return callOptions
	}

	// Handle temperature
	if temp, ok := options["temperature"].(float64); ok && temp >= 0 {
		callOptions = append(callOptions, llmtypes.WithTemperature(temp))
	} else if temp, ok := options["temperature"].(int); ok && temp >= 0 {
		callOptions = append(callOptions, llmtypes.WithTemperature(float64(temp)))
	}

	if effort, ok := options["reasoning_effort"].(string); ok && effort != "" {
		callOptions = append(callOptions, llmtypes.WithReasoningEffort(effort))
	}
	if verbosity, ok := options["verbosity"].(string); ok && verbosity != "" {
		callOptions = append(callOptions, llmtypes.WithVerbosity(verbosity))
	}
	if thinkingLevel, ok := options["thinking_level"].(string); ok && thinkingLevel != "" {
		callOptions = append(callOptions, llmtypes.WithThinkingLevel(thinkingLevel))
	}
	if thinkingBudget, ok := options["thinking_budget"].(float64); ok && thinkingBudget > 0 {
		callOptions = append(callOptions, llmtypes.WithThinkingBudget(int(thinkingBudget)))
	} else if thinkingBudget, ok := options["thinking_budget"].(int); ok && thinkingBudget > 0 {
		callOptions = append(callOptions, llmtypes.WithThinkingBudget(thinkingBudget))
	}
	return callOptions
}
