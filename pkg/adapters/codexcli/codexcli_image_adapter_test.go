package codexcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestCodexImageCommandUsesCurrentBypassFlag(t *testing.T) {
	tmpDir := t.TempDir()
	fakeCodexPath := filepath.Join(tmpDir, "codex")
	argsPath := filepath.Join(tmpDir, "args.txt")
	fakeCodex := `#!/bin/sh
printf '%s\n' "$@" > "` + argsPath + `"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    printf '%s\n' "` + filepath.Join(tmpDir, "generated.png") + `" > "$1"
    break
  fi
  shift
done
exit 0
`
	if err := os.WriteFile(fakeCodexPath, []byte(fakeCodex), 0755); err != nil {
		t.Fatalf("write fake codex binary: %v", err)
	}
	generatedPath := filepath.Join(tmpDir, "generated.png")
	if err := os.WriteFile(generatedPath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0600); err != nil {
		t.Fatalf("write generated image: %v", err)
	}

	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	adapter := NewCodexCLIImageAdapter("", "codex-cli", nil)
	_, err := adapter.GenerateImages(
		t.Context(),
		"make an image",
	)
	if err != nil {
		t.Fatalf("GenerateImages returned error: %v", err)
	}

	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake codex args: %v", err)
	}
	args := string(argsBytes)
	if !strings.Contains(args, "--dangerously-bypass-approvals-and-sandbox\n") {
		t.Fatalf("args = %q, want current Codex bypass flag", args)
	}
	if strings.Contains(args, "--full-auto") {
		t.Fatalf("args = %q, deprecated --full-auto should not be used", args)
	}
}
