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

func TestHasCursorMCPServerApprovalPrompt(t *testing.T) {
	approval := `Cursor Agent

MCP server approval required
api-bridge wants to run from .cursor/mcp.json.

[y] Approve MCP server
[n] Reject
→ Plan, search, build anything`

	if !hasCursorMCPServerApprovalPrompt(approval) {
		t.Fatal("hasCursorMCPServerApprovalPrompt = false for startup MCP approval; want true")
	}
	if hasCursorReadyPrompt(approval) {
		t.Fatal("hasCursorReadyPrompt = true on startup MCP approval pane; want false")
	}
	if got := cursorMCPServerApprovalResponse(approval); got != "y" {
		t.Fatalf("cursorMCPServerApprovalResponse = %q, want y", got)
	}

	trustAll := `MCP servers configured for this workspace
[a] Enable all MCP servers
[w] Continue without MCP servers`
	if !hasCursorMCPServerApprovalPrompt(trustAll) {
		t.Fatal("hasCursorMCPServerApprovalPrompt = false for enable-all MCP pane; want true")
	}
	if got := cursorMCPServerApprovalResponse(trustAll); got != "a" {
		t.Fatalf("cursorMCPServerApprovalResponse = %q, want a", got)
	}

	toolApproval := `api-bridge: execute_shell_command
Run this MCP tool?
 → Run (once) (y)
   Allowlist MCP Tool (tab)`
	if hasCursorMCPServerApprovalPrompt(toolApproval) {
		t.Fatal("startup MCP detector must not match per-tool approval prompts")
	}

	completedPane := `Cursor Agent
Tip: Hit shift+tab to enable Plan Mode for large changes.
Explored available MCP tools api-bridge · get_api_spec
api-bridge execute_shell_command
RUN_WORKFLOW_TOOL_STARTED
→ Add a follow-up`
	if hasCursorMCPServerApprovalPrompt(completedPane) {
		t.Fatal("completed pane must not combine unrelated MCP/api-bridge and enable-tip text into a startup approval")
	}
	if !hasCursorReadyPrompt(completedPane) {
		t.Fatal("completed pane with final answer and follow-up prompt must be ready")
	}
}
