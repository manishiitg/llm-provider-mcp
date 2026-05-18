package opencodecli

import (
	"testing"
)

type MockLogger struct{}

func (m *MockLogger) Infof(format string, args ...interface{})  {}
func (m *MockLogger) Errorf(format string, args ...interface{}) {}
func (m *MockLogger) Debugf(format string, args ...interface{}) {}

func TestResolveOpenCodeCLIModelID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default", input: "", want: ""},
		{name: "provider default", input: "opencode-cli", want: ""},
		{name: "auto", input: "auto", want: ""},
		{name: "provider model", input: "anthropic/claude-sonnet-4-5", want: "anthropic/claude-sonnet-4-5"},
		{name: "high alias", input: "high", want: "openai/gpt-5.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveOpenCodeCLIModelID(tt.input); got != tt.want {
				t.Fatalf("resolveOpenCodeCLIModelID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestOpenCodeCLIModelMetadata(t *testing.T) {
	adapter := NewOpenCodeCLIAdapter("", "opencode-cli", &MockLogger{})
	meta, err := adapter.GetModelMetadata("")
	if err != nil {
		t.Fatalf("GetModelMetadata() error = %v", err)
	}
	if meta.Provider != "opencode-cli" {
		t.Fatalf("Provider = %q, want opencode-cli", meta.Provider)
	}
	if meta.ModelID != "opencode-cli" {
		t.Fatalf("ModelID = %q, want opencode-cli", meta.ModelID)
	}
}
