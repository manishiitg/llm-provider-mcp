package opencodecli

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestOpenCodeCLIRealImagePathAnalysis(t *testing.T) {
	requireRealOpenCodeCLIE2E(t)

	// Vision check: needs a model that actually looks at PNG bytes.
	// The default free-tier model (deepseek-v4-flash-free) doesn't
	// have vision and reliably says "black" for any image. Operators
	// who want to run this test must point at a vision-capable model
	// (e.g. a paid Claude/Gemini/GPT-4o tile) via the env override.
	model := freeTierTestModel()
	if model == "opencode/deepseek-v4-flash-free" {
		t.Skip("set OPENCODE_CLI_REAL_E2E_MODEL=<vision-capable model> to run the image-path analysis test; the default free-tier model lacks vision")
	}

	workspaceDir := t.TempDir()
	if out, err := exec.CommandContext(context.Background(), "git", "init", workspaceDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	imagePath := filepath.Join(workspaceDir, "sample.png")
	writeSolidOpenCodeTestPNG(t, imagePath, color.RGBA{R: 255, A: 255})

	adapter := NewOpenCodeCLIAdapter("", model, &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	prompt := fmt.Sprintf("Inspect the local image file at this absolute path:\n%s\n\nQuestion: What is the dominant color? Reply with one lowercase English color word.", imagePath)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: prompt},
			},
		},
	},
		WithWorkingDir(workspaceDir),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	content := strings.ToLower(strings.TrimSpace(resp.Choices[0].Content))
	if !strings.Contains(content, "red") {
		t.Fatalf("expected image analysis to mention red, got %q", content)
	}
}

func writeSolidOpenCodeTestPNG(t *testing.T, path string, pixel color.RGBA) {
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
