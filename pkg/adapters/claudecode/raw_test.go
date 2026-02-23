package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestRawClaude(t *testing.T) {
	args := []string{"-p", "--output-format", "stream-json", "--input-format", "stream-json", "--verbose", "--include-partial-messages", "--dangerously-skip-permissions"}
	cmd := exec.CommandContext(context.Background(), "claude", args...)

	var inputStream bytes.Buffer
	encoder := json.NewEncoder(&inputStream)
	msg := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role": "user",
			"content": []map[string]interface{}{{"type": "text", "text": "use bash to run echo hello"}},
		},
	}
	encoder.Encode(msg)
	cmd.Stdin = &inputStream
	
	stdoutPipe, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr

	cmd.Start()
	
	b, _ := io.ReadAll(stdoutPipe)
	fmt.Printf("STDOUT: %s\n", string(b))
	
	cmd.Wait()
}
