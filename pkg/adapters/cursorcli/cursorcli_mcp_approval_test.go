package cursorcli

import "testing"

// Cursor gates every MCP tool call behind a "Run this MCP tool?" prompt unless
// launched with --force. In an orchestrated bridge session that stalls the turn,
// so the response loop must detect the prompt and auto-allowlist it (Tab).
func TestHasCursorMCPToolApprovalPrompt(t *testing.T) {
	// The exact pane Cursor renders (captured live).
	approval := `┌─────────────────────────────────────────────┐
 │ api-bridge: execute_shell_command           │
 │   { "command": "cat soul.md" }              │
 └─────────────────────────────────────────────┘
  Run this MCP tool?
   → Run (once) (y)
     Allowlist MCP Tool (tab)
     Reject & propose changes (p)
     Skip (esc or n)`
	if !hasCursorMCPToolApprovalPrompt(approval) {
		t.Fatal("hasCursorMCPToolApprovalPrompt = false for the live approval pane; want true")
	}
	// The approval prompt must NOT be treated as a ready prompt (its "→ Run
	// (once)" arrow otherwise trips the ready-marker → bogus completion).
	if hasCursorReadyPrompt(approval) {
		t.Fatal("hasCursorReadyPrompt = true on the MCP approval pane; want false (still gated)")
	}

	notApproval := []string{
		"",
		"> ",
		"here is the final answer about mcp tools",
		"▸ Thought for 3s",
	}
	for _, s := range notApproval {
		if hasCursorMCPToolApprovalPrompt(s) {
			t.Errorf("hasCursorMCPToolApprovalPrompt(%q) = true, want false", s)
		}
	}
}
