package agycli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// AgyCLIImageAdapter implements ImageGenerationModel through Agy's native
// image tool in the interactive CLI, then reads the saved file bytes back into
// the standard ImageGenerationResponse shape.
type AgyCLIImageAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewAgyCLIImageAdapter creates a new Agy CLI image generation adapter.
func NewAgyCLIImageAdapter(apiKey, modelID string, logger interfaces.Logger) *AgyCLIImageAdapter {
	if modelID == "" {
		modelID = "agy-cli"
	}
	return &AgyCLIImageAdapter{
		apiKey:  apiKey,
		modelID: modelID,
		logger:  logger,
	}
}

// GenerateImages implements llmtypes.ImageGenerationModel.
func (a *AgyCLIImageAdapter) GenerateImages(ctx context.Context, prompt string, options ...llmtypes.ImageGenerationOption) (*llmtypes.ImageGenerationResponse, error) {
	if _, err := exec.LookPath("agy"); err != nil {
		return nil, fmt.Errorf("agy cli not found in PATH. Please install Antigravity CLI first")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found in PATH; agy-cli image generation requires tmux")
	}

	opts := &llmtypes.ImageGenerationOptions{
		NumberOfImages: 1,
		AspectRatio:    "1:1",
		Resolution:     "1K",
	}
	for _, opt := range options {
		opt(opts)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if opts.NumberOfImages <= 0 {
		opts.NumberOfImages = 1
	}
	if opts.InputImageURL != "" {
		return nil, fmt.Errorf("agy cli image generation does not support input_image_url; pass image bytes with WithInputImage instead")
	}

	workdir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve current working directory: %w", err)
	}
	tempDir, err := os.MkdirTemp(workdir, ".agy-image-*")
	if err != nil {
		return nil, fmt.Errorf("create agy image temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	var inputImagePath string
	if len(opts.InputImage) > 0 {
		inputImagePath = filepath.Join(tempDir, "input"+extensionForAgyImageMIMEType(opts.InputImageMIMEType))
		if err := os.WriteFile(inputImagePath, opts.InputImage, 0o600); err != nil {
			return nil, fmt.Errorf("write agy input image: %w", err)
		}
	}

	result := &llmtypes.ImageGenerationResponse{
		Images: make([]llmtypes.GeneratedImage, 0, opts.NumberOfImages),
	}
	adapter := NewAgyCLIAdapter(a.apiKey, a.modelID, a.logger)
	for i := 0; i < opts.NumberOfImages; i++ {
		outputPath := filepath.Join(tempDir, fmt.Sprintf("generated-%02d.png", i+1))
		imagePrompt := buildAgyImagePrompt(prompt, outputPath, opts, i, inputImagePath)
		if a.logger != nil {
			a.logger.Infof("[AGY IMAGE] Running agy image generation model=%s output=%s", a.modelID, outputPath)
		}
		if _, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
			{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: imagePrompt},
				},
			},
		},
			WithWorkingDir(tempDir),
			WithInteractiveSessionID("agy-image-"+agyRandomHex(6)),
			WithDangerouslySkipPermissions(true),
		); err != nil {
			return nil, fmt.Errorf("agy cli image generation failed: %w", err)
		}

		data, err := os.ReadFile(outputPath)
		if err != nil {
			return nil, fmt.Errorf("read generated agy image %q: %w", outputPath, err)
		}
		result.Images = append(result.Images, llmtypes.GeneratedImage{
			Data:     data,
			MimeType: mimeTypeForAgyImageFile(outputPath, data),
		})
	}

	return result, nil
}

func buildAgyImagePrompt(prompt, outputPath string, opts *llmtypes.ImageGenerationOptions, index int, inputImagePath string) string {
	var sb strings.Builder
	if inputImagePath != "" {
		sb.WriteString("Use your native image generation or editing capability to create exactly one edited image based on this local reference image file:\n")
		sb.WriteString(inputImagePath)
		sb.WriteString("\n")
	} else {
		sb.WriteString("Use your native image generation capability to create exactly one image.\n")
	}
	sb.WriteString("Do not write code. Do not create a text-only description.\n")
	sb.WriteString(fmt.Sprintf("Save the final image as a real PNG file at this exact absolute path:\n%s\n", outputPath))
	sb.WriteString("If Agy creates the image in a temporary brain/cache path first, copy that image file to the exact path above.\n")
	sb.WriteString("After the file has been saved, reply with exactly the saved file path only.\n")
	sb.WriteString("\nPrompt:\n")
	sb.WriteString(strings.TrimSpace(prompt))
	sb.WriteString("\n")
	if strings.TrimSpace(opts.AspectRatio) != "" {
		sb.WriteString(fmt.Sprintf("Aspect ratio: %s\n", opts.AspectRatio))
	}
	if strings.TrimSpace(opts.Resolution) != "" {
		sb.WriteString(fmt.Sprintf("Resolution target: %s\n", opts.Resolution))
	}
	if strings.TrimSpace(opts.NegativePrompt) != "" {
		sb.WriteString(fmt.Sprintf("Avoid: %s\n", opts.NegativePrompt))
	}
	if opts.NumberOfImages > 1 {
		sb.WriteString(fmt.Sprintf("Variant number: %d of %d. Make it distinct while staying faithful to the same brief.\n", index+1, opts.NumberOfImages))
	}
	return sb.String()
}

func extensionForAgyImageMIMEType(mimeType string) string {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func mimeTypeForAgyImageFile(path string, data []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".png":
		return "image/png"
	}

	detected := http.DetectContentType(data)
	if strings.HasPrefix(detected, "image/") {
		return detected
	}
	return "image/png"
}
