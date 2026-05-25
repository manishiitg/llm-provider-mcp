package agycli

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestAgyCLIRealImagePathAnalysisContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	workspaceDir := t.TempDir()
	imagePath := filepath.Join(workspaceDir, "sample.png")
	writeSolidAgyTestPNG(t, imagePath, color.RGBA{R: 255, A: 255})

	adapter := NewAgyCLIAdapter(agyRealE2EAPIKeyFromEnv(), "agy-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	prompt := fmt.Sprintf("Inspect the local image file at this workspace path:\n%s\n\nQuestion: What is the dominant color? Reply with one lowercase English color word.", imagePath)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: prompt},
			},
		},
	},
		WithInteractiveSessionID("agy-e2e-image-"+agyRandomHex(4)),
		WithWorkingDir(workspaceDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(resp); ok && handle.TmuxSession != "" {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = killAgyTmuxSession(cleanupCtx, handle.TmuxSession)
		})
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	content := strings.ToLower(strings.TrimSpace(resp.Choices[0].Content))
	if !strings.Contains(content, "red") {
		t.Fatalf("expected image analysis to mention red, got %q", content)
	}
}

func TestAgyCLIRealImageGenerationPathContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	workspaceDir := t.TempDir()
	imagePath := filepath.Join(workspaceDir, "generated.png")

	adapter := NewAgyCLIAdapter(agyRealE2EAPIKeyFromEnv(), "agy-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	token := "AGY_IMAGE_GENERATED_" + agyRandomHex(5)
	prompt := fmt.Sprintf(`Use your native image generation capability to create an actual PNG image file at this exact absolute path:
%s

Image requirements:
- simple blue square
- no text in the image
- save the file as a real image, not a text description

After the file has been saved, reply exactly: %s`, imagePath, token)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: prompt},
			},
		},
	},
		WithInteractiveSessionID("agy-e2e-image-gen-"+agyRandomHex(4)),
		WithWorkingDir(workspaceDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if handle, ok := llmtypes.ExtractCodingProviderSessionHandleFromResponse(resp); ok && handle.TmuxSession != "" {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			_ = killAgyTmuxSession(cleanupCtx, handle.TmuxSession)
		})
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, token) {
		t.Fatalf("content = %q, want token %s", content, token)
	}

	file, err := os.Open(imagePath)
	if err != nil {
		t.Fatalf("generated image %s was not created: %v; response=%q", imagePath, err, content)
	}
	defer file.Close()

	config, format, err := image.DecodeConfig(file)
	if err != nil {
		t.Fatalf("generated image is not decodable: %v; response=%q", err, content)
	}
	if format != "png" {
		t.Fatalf("generated image format = %q, want png", format)
	}
	if config.Width < 16 || config.Height < 16 {
		t.Fatalf("generated image dimensions = %dx%d, want at least 16x16", config.Width, config.Height)
	}
}

func TestAgyCLIRealImageGenerationModelContract(t *testing.T) {
	requireRealAgyCLIE2E(t)
	t.Cleanup(func() { _ = CleanupAgyCLIInteractiveSessions(context.Background()) })

	imageModel := NewAgyCLIImageAdapter(agyRealE2EAPIKeyFromEnv(), "agy-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	resp, err := imageModel.GenerateImages(ctx, "A simple blue square icon on a plain background, no text",
		llmtypes.WithAspectRatio("1:1"),
		llmtypes.WithResolution("1K"),
	)
	if err != nil {
		t.Fatalf("GenerateImages() error = %v", err)
	}
	if resp == nil || len(resp.Images) != 1 {
		t.Fatalf("GenerateImages() returned %#v, want one image", resp)
	}
	generated := resp.Images[0]
	if generated.MimeType != "image/png" {
		t.Fatalf("MimeType = %q, want image/png", generated.MimeType)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(generated.Data))
	if err != nil {
		t.Fatalf("generated image is not decodable: %v", err)
	}
	if format != "png" {
		t.Fatalf("generated image format = %q, want png", format)
	}
	if config.Width < 16 || config.Height < 16 {
		t.Fatalf("generated image dimensions = %dx%d, want at least 16x16", config.Width, config.Height)
	}
}

func writeSolidAgyTestPNG(t *testing.T, path string, pixel color.RGBA) {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 48, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 48; x++ {
			img.SetRGBA(x, y, pixel)
		}
	}

	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create test image: %v", err)
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		t.Fatalf("encode test image: %v", err)
	}
}

func agyRealE2EAPIKeyFromEnv() string {
	for _, key := range []string{"AGY_CLI_REAL_E2E_API_KEY", "AGY_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
