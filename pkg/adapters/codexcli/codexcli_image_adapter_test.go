package codexcli

import (
	"os"
	"path/filepath"
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
