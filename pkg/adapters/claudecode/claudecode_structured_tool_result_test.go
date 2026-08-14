package claudecode

import (
	"encoding/json"
	"testing"
)

func TestClaudeToolResultTextPreservesNonTextBlocks(t *testing.T) {
	raw := json.RawMessage(`[{"type":"tool_reference","tool_name":"Monitor"}]`)
	if got := claudeToolResultText(raw); got != string(raw) {
		t.Fatalf("result = %q, want structured tool reference %q", got, raw)
	}
}
