package geminicli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestRawGemini(t *testing.T) {
	args := []string{"--output-format", "stream-json", "--approval-mode", "yolo", "say hello in one short sentence"}
	cmd := exec.CommandContext(context.Background(), "gemini", args...)

	cmd.Stderr = os.Stderr

	stdoutPipe, _ := cmd.StdoutPipe()

	cmd.Start()

	b, _ := io.ReadAll(stdoutPipe)
	fmt.Printf("STDOUT: %s\n", string(b))

	cmd.Wait()
}
