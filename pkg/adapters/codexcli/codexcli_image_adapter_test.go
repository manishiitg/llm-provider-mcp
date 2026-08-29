package codexcli

import (
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

// TestCodexCLIRealImagePathAnalysis proves codex's own native vision --
// pointed at a local workspace file path, the same mechanism
// wrapReadImageWithLLM (agent_go) uses for read_image(provider="codex-cli")
// -- actually works, including under the same bridge-lockdown constraint
// (WithDisableShellTool) a real workflow-step session runs under. No prior
// test in this package verified image *understanding* at all; the existing
// codexcli_image_adapter.go/_test.go pair is for image *generation*
// (image_gen/image_edit), a different capability entirely.
func TestCodexCLIRealImagePathAnalysis(t *testing.T) {
	requireRealCodexCLISearchWebE2E(t)

	workspaceDir := t.TempDir()
	imagePath := filepath.Join(workspaceDir, "sample.png")
	writeSolidCodexTestPNG(t, imagePath, color.RGBA{R: 255, A: 255})

	adapter := NewCodexCLIAdapter("", codexCLIRealContractModel, quietCodexStreamLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	prompt := fmt.Sprintf("Inspect the local image file at this workspace path:\n%s\n\nQuestion: What is the dominant color? Reply with one lowercase English color word.", imagePath)
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: prompt},
			},
		},
	}, WithProjectDirID(workspaceDir), WithDisableShellTool(), WithReasoningEffort("low"))
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

func writeSolidCodexTestPNG(t *testing.T, path string, pixel color.RGBA) {
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

func TestExtractImagePathFromLastMessage(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "plain path",
			content: "/tmp/generated.png\n",
			want:    "/tmp/generated.png",
		},
		{
			name:    "quoted path",
			content: "\"/tmp/generated.png\"\n",
			want:    "/tmp/generated.png",
		},
		{
			name:    "json path",
			content: `{"saved_path":"/tmp/generated.png"}`,
			want:    "/tmp/generated.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastMessagePath := filepath.Join(tmpDir, tt.name+".txt")
			if err := os.WriteFile(lastMessagePath, []byte(tt.content), 0600); err != nil {
				t.Fatalf("write last message file: %v", err)
			}

			got, err := extractImagePathFromLastMessage(lastMessagePath)
			if err != nil {
				t.Fatalf("extractImagePathFromLastMessage returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("extractImagePathFromLastMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMimeTypeForImageFile(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/tmp/test.png", want: "image/png"},
		{path: "/tmp/test.jpg", want: "image/jpeg"},
		{path: "/tmp/test.jpeg", want: "image/jpeg"},
		{path: "/tmp/test.webp", want: "image/webp"},
	}

	for _, tt := range tests {
		if got := mimeTypeForImageFile(tt.path, nil); got != tt.want {
			t.Fatalf("mimeTypeForImageFile(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
