package vertex

import (
	"context"
	"fmt"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"

	"google.golang.org/genai"
)

// VertexImagenAdapter implements llmtypes.ImageGenerationModel using Vertex AI Imagen.
//
// Deprecated: Imagen endpoints are no longer exposed by provider initialization.
// Use GeminiImageAdapter with gemini-3.1-flash-image instead.
type VertexImagenAdapter struct {
	client  *genai.Client
	modelID string
	logger  interfaces.Logger
}

// NewVertexImagenAdapter creates a new Imagen adapter instance.
// The client must be initialized with genai.BackendVertexAI.
func NewVertexImagenAdapter(client *genai.Client, modelID string, logger interfaces.Logger) *VertexImagenAdapter {
	return &VertexImagenAdapter{
		client:  client,
		modelID: modelID,
		logger:  logger,
	}
}

// imagenModels is intentionally empty; Imagen model IDs are no longer advertised.
var imagenModels = map[string]llmtypes.ModelMetadata{}

// GetModelMetadata returns pricing metadata for the given Imagen model ID.
func (a *VertexImagenAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	if modelID == "" {
		modelID = a.modelID
	}
	if meta, ok := imagenModels[modelID]; ok {
		return &meta, nil
	}
	return nil, fmt.Errorf("no metadata found for Imagen model: %s", modelID)
}

// GenerateImages implements llmtypes.ImageGenerationModel
func (a *VertexImagenAdapter) GenerateImages(ctx context.Context, prompt string, options ...llmtypes.ImageGenerationOption) (*llmtypes.ImageGenerationResponse, error) {
	opts := &llmtypes.ImageGenerationOptions{
		NumberOfImages: 1,
		AspectRatio:    "1:1",
	}
	for _, opt := range options {
		opt(opts)
	}

	cfg := &genai.GenerateImagesConfig{
		NumberOfImages: int32(opts.NumberOfImages),
		AspectRatio:    opts.AspectRatio,
	}
	if opts.NegativePrompt != "" {
		cfg.NegativePrompt = opts.NegativePrompt
	}

	a.logger.Infof("Generating %d image(s) with model %s, prompt: %q", opts.NumberOfImages, a.modelID, prompt)

	resp, err := a.client.Models.GenerateImages(ctx, a.modelID, prompt, cfg)
	if err != nil {
		return nil, fmt.Errorf("imagen GenerateImages failed: %w", err)
	}

	result := &llmtypes.ImageGenerationResponse{
		Images: make([]llmtypes.GeneratedImage, 0, len(resp.GeneratedImages)),
	}

	for _, img := range resp.GeneratedImages {
		if img.Image == nil {
			continue
		}
		mimeType := img.Image.MIMEType
		if mimeType == "" {
			mimeType = "image/png"
		}
		result.Images = append(result.Images, llmtypes.GeneratedImage{
			Data:     img.Image.ImageBytes,
			MimeType: mimeType,
		})
	}

	a.logger.Infof("Generated %d image(s) successfully", len(result.Images))
	return result, nil
}
