package llmproviders

import (
	"fmt"
	"os"
	"strings"

	kimiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/kimi"
	vertexadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/vertex"
	zaiadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/zai"
)

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
	case ProviderBedrock, ProviderOpenAI, ProviderAnthropic, ProviderOpenRouter, ProviderVertex, ProviderAzure, ProviderZAI, ProviderKimi, ProviderClaudeCode, ProviderCodexCLI, ProviderCursorCLI, ProviderAgyCLI, ProviderPiCLI, ProviderMiniMax, ProviderMiniMaxCodingPlan:
		return Provider(provider), nil
	default:
		return "", fmt.Errorf("unsupported provider: %s. Supported providers: bedrock, openai, anthropic, openrouter, vertex, azure, z-ai, kimi, claude-code, codex-cli, cursor-cli, agy-cli, pi-cli, minimax, minimax-coding-plan", provider)
	}
}

// ProviderAwareLLM is a wrapper around LLM that preserves provider information
// and automatically captures token usage in LLM events
