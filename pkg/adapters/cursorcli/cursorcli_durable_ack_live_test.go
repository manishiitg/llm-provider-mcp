package cursorcli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestCursorCLIRealDurableAckContract is the CertDurableAck P0 proof: a
// follow-up steered into a busy Cursor turn must be confirmable against
// the session store.db (not just the pane), and the model must obey it.
// A second, idle follow-up must be durably acked fast. Uses the raw
// adapter path with no server queue in between.
//
// NOTE (2026-09-19): written while login quota was exhausted, so the
// busy leg is unverified live. Pre-quota probes saw the auto model
// twice echo the slow-tool token instead of calling the tool (turn
// completed before the marker); if that recurs, strengthen the tool
// mandate in the turn prompt rather than weakening the assertions.
func TestCursorCLIRealDurableAckContract(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	ownerSessionID := "cursor-real-durable-ack-" + cursorRandomHex(4)
	workDir := t.TempDir()
	toolToken := "SLOW_DURABLE_" + cursorRandomHex(4)
	liveAck := "CURSOR_DURABLE_ACK_" + cursorRandomHex(4)
	idleToken := "CURSOR_DURABLE_IDLE_" + cursorRandomHex(4)

	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpServerPath := writeCursorSlowContractMCPServer(t, slowToolMarker)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	preApproveCursorMCP(t, workDir, mcpConfig, "api-bridge")

	paneDiag := func() string {
		if sn, ok := activeCursorInteractiveSession(ownerSessionID); ok {
			p, _ := captureCursorPaneForDisplay(context.Background(), sn)
			return p
		}
		return "(no active session)"
	}

	parentCtx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	resultCh := make(chan cursorRealResult, 1)
	startupErrCh := make(chan error, 1)
	streamChan := make(chan llmtypes.StreamChunk, 128)

	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeSystem, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Use only declared MCP tools. If a follow-up user message arrives while you are working, handle it after the current tool call finishes. Keep answers concise."}}},
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge MCP tool named contract_echo_token with token %s and delay_ms 8000. Do not answer until the tool returns.", toolToken)}}},
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithForce(),
			WithMCPConfig(mcpConfig),
			WithApproveMCPs(),
			llmtypes.WithStreamingChan(streamChan),
		)
		out := cursorRealResult{err: err}
		if err == nil && resp != nil && len(resp.Choices) > 0 && resp.Choices[0] != nil {
			out.content = resp.Choices[0].Content
		}
		if err != nil {
			select {
			case startupErrCh <- err:
			default:
			}
		}
		resultCh <- out
	}()

	waitForCursorRealActiveSession(t, ownerSessionID, 60*time.Second, startupErrCh)
	waitForCursorRealFile(t, slowToolMarker, "slow MCP tool call start", 120*time.Second, resultCh)

	// Deliver the follow-up WHILE Cursor is busy in the slow tool.
	liveMessage := fmt.Sprintf("Follow-up task: after the current answer completes, also reply exactly %s and nothing else.", liveAck)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
	sendErr := SendCursorInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if sendErr != nil {
		cancel()
		t.Fatalf("SendCursorInteractiveInput error = %v", sendErr)
	}

	// Durability half of the receipt: the store must record this exact
	// send. The pane already accepted it (fast ack above); the file
	// proves the CLI durably holds it even across a pane misread. Zero
	// timeout exercises the default budget (busy path unverified).
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 200*time.Second)
	ack, awaitErr := AwaitCursorInputDurable(awaitCtx, ownerSessionID, liveMessage, 0)
	awaitCancel()
	if awaitErr != nil {
		cancel()
		t.Fatalf("AwaitCursorInputDurable error = %v\npane:\n%s", awaitErr, paneDiag())
	}
	if ack.Outcome != CursorDurableAckConfirmed {
		cancel()
		t.Fatalf("durable ack outcome = %q, want confirmed (latency %s)\n pane:\n%s", ack.Outcome, ack.Latency, paneDiag())
	}
	t.Logf("busy steer durably acked in %s via %s", ack.Latency.Round(100*time.Millisecond), ack.ProofPath)

	select {
	case got := <-resultCh:
		if got.err != nil {
			t.Fatalf("GenerateContent error = %v (content=%q)", got.err, got.content)
		}
		if !strings.Contains(got.content, liveAck) {
			t.Fatalf("live steer did not affect the busy Cursor turn; final content=%q\npane:\n%s", got.content, paneDiag())
		}
	case <-parentCtx.Done():
		t.Fatalf("timed out waiting for the busy turn: %v", parentCtx.Err())
	}

	// Idle follow-up: paste into the settled composer and prove the
	// durable record confirms it fast — this is the single-tick to
	// double-tick path with no queue in between.
	idleMessage := fmt.Sprintf("Do not use tools. Reply exactly %s.", idleToken)
	idleSendCtx, idleSendCancel := context.WithTimeout(context.Background(), 30*time.Second)
	idleSendErr := SendCursorInteractiveInput(idleSendCtx, ownerSessionID, idleMessage)
	idleSendCancel()
	if idleSendErr != nil {
		t.Fatalf("idle SendCursorInteractiveInput error = %v", idleSendErr)
	}
	idleAwaitCtx, idleAwaitCancel := context.WithTimeout(context.Background(), 200*time.Second)
	idleAck, idleAwaitErr := AwaitCursorInputDurable(idleAwaitCtx, ownerSessionID, idleMessage, 0)
	idleAwaitCancel()
	if idleAwaitErr != nil {
		t.Fatalf("idle AwaitCursorInputDurable error = %v\npane:\n%s", idleAwaitErr, paneDiag())
	}
	if idleAck.Outcome != CursorDurableAckConfirmed {
		t.Fatalf("idle durable ack outcome = %q, want confirmed\npane:\n%s", idleAck.Outcome, paneDiag())
	}
	if idleAck.Latency > 90*time.Second {
		t.Fatalf("idle durable ack latency = %s, want well under the budget\npane:\n%s", idleAck.Latency, paneDiag())
	}
	t.Logf("idle follow-up durably acked in %s", idleAck.Latency.Round(100*time.Millisecond))
}
