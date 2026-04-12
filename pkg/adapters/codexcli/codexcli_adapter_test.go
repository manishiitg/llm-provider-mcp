package codexcli

import (
	"fmt"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type MockLogger struct{}

func (l *MockLogger) Infof(format string, args ...any)  { fmt.Printf("INFO: "+format+"\n", args...) }
func (l *MockLogger) Errorf(format string, args ...any) { fmt.Printf("ERROR: "+format+"\n", args...) }
func (l *MockLogger) Debugf(format string, args ...interface{}) {
	fmt.Printf("DEBUG: "+format+"\n", args...)
}

func TestCodexCLIAdapterImplementsWebSearchModel(t *testing.T) {
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
	if _, ok := interface{}(adapter).(llmtypes.WebSearchModel); !ok {
		t.Fatal("CodexCLIAdapter should implement llmtypes.WebSearchModel")
	}
}

func TestLooksLikeCodexRateLimit(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{line: "error: 429 Too Many Requests", want: true},
		{line: "service unavailable from upstream", want: true},
		{line: "you hit your usage limit, try again later", want: true},
		{line: `WARN codex_core::shell_snapshot: Failed to delete shell snapshot at "/tmp/x": No such file or directory`, want: false},
		{line: "migration 21 was previously applied but is missing in the resolved migrations", want: false},
	}

	for _, tt := range tests {
		if got := looksLikeCodexRateLimit(tt.line); got != tt.want {
			t.Fatalf("looksLikeCodexRateLimit(%q) = %v, want %v", tt.line, got, tt.want)
		}
	}
}
