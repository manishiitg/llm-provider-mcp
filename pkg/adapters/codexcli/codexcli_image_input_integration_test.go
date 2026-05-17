package codexcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestCodexCLIRealImageInput(t *testing.T) {
	if os.Getenv("RUN_CODEX_CLI_IMAGE_E2E") == "" {
		t.Skip("set RUN_CODEX_CLI_IMAGE_E2E=1 to run real Codex CLI image input E2E")
	}

	adapter := NewCodexCLIAdapter("", "gpt-5.4-mini", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Do not use tools. Answer with one color word."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What is the dominant color of this image? Reply with only the color word."},
				llmtypes.ImageContent{
					SourceType: "base64",
					MediaType:  "image/png",
					Data:       base64.StdEncoding.EncodeToString(testRedPNG(t)),
				},
			},
		},
	}, WithDisableShellTool(), WithReasoningEffort("low"))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	if got := strings.ToLower(resp.Choices[0].Content); !strings.Contains(got, "red") {
		t.Fatalf("content = %q, want red", resp.Choices[0].Content)
	}
}

func testRedPNG(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}
