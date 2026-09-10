// Package musecli is the Muse (Muse Code, `muse` CLI) adapter stub.
//
// Recon: multi-llm-provider-go/docs/MUSE_CLI_CODING_AGENT_CONTRACT.md.
// GenerateContent runs the exec --json lane; the tmux lane arrives with
// the interactive adapter (later step).
package musecli

import (
	"context"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// MuseCLIAdapter implements llmtypes.Model for the Muse CLI.
type MuseCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewMuseCLIAdapter creates a new MuseCLIAdapter. apiKey is META_API_KEY
// (may be empty; the CLI falls back to its stored `muse login`).
func NewMuseCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *MuseCLIAdapter {
	return &MuseCLIAdapter{apiKey: apiKey, modelID: modelID, logger: logger}
}

// GetModelID returns the configured model id.
func (a *MuseCLIAdapter) GetModelID() string { return a.modelID }

// GetModelMetadata returns metadata for a Muse model id.
func (a *MuseCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	return GetMuseModelMetadata(modelID)
}

// GenerateContent runs one headless turn through the exec --json lane.
// The tmux lane arrives with the interactive adapter (later step).
func (a *MuseCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	return a.generateContentExec(ctx, messages, options...)
}
