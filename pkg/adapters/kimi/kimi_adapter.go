// Package kimi provides an adapter for Kimi's Anthropic-compatible coding API.
// Kimi exposes an Anthropic-compatible endpoint at https://api.kimi.com/coding,
// so we use the Anthropic Go SDK with a custom base URL and reuse the existing
// AnthropicAdapter for all message conversion, tool calling, and streaming logic.
package kimi

import (
	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	anthropicadapter "github.com/manishiitg/multi-llm-provider-go/pkg/adapters/anthropic"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
)

// KimiAnthropicBaseURL is the base URL for Kimi's Anthropic-compatible coding API.
// The Anthropic SDK appends /v1/messages, resulting in https://api.kimi.com/coding/v1/messages.
const KimiAnthropicBaseURL = "https://api.kimi.com/coding"

// KimiAdapter implements llmtypes.Model by delegating to AnthropicAdapter against
// Kimi's coding endpoint. Only GetModelMetadata is overridden so callers see
// Kimi model metadata instead of Anthropic's.
type KimiAdapter struct {
	*anthropicadapter.AnthropicAdapter
}

// NewKimiAdapter creates a new KimiAdapter that talks directly to api.kimi.com/coding
// over HTTP using the Anthropic Go SDK. No Claude Code CLI required.
func NewKimiAdapter(apiKey, modelID string, logger interfaces.Logger) *KimiAdapter {
	client := anthropic.NewClient(
		anthropicoption.WithAPIKey(apiKey),
		anthropicoption.WithBaseURL(KimiAnthropicBaseURL),
	)
	return &KimiAdapter{
		AnthropicAdapter: anthropicadapter.NewAnthropicAdapter(client, modelID, logger),
	}
}

// GetModelMetadata returns Kimi metadata for the given model ID, overriding the
// embedded AnthropicAdapter's lookup so this provider reports as "kimi" rather
// than treating the model as a Claude variant.
func (k *KimiAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	return GetKimiModelMetadata(modelID)
}
