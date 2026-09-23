package picli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// TestPiCLIRealDurableAckContract is the CertDurableAck P0 proof for Pi:
// a follow-up steered into a busy Pi turn must be confirmable against
// the marker stream (message_end user row), not just the pane, and the
// model must obey it. A second, idle follow-up must be durably acked
// fast. Uses the raw adapter path with no server queue in between.
func TestPiCLIRealDurableAckContract(t *testing.T) {
	requireRealPiCLIContractE2E(t)
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	adapter := newRealPiCLIAdapter(t)
	ownerSessionID := "pi-real-durable-ack-" + piRandomHex(4)
	workDir := piLiveWorkDir(t)
	toolToken := "SLOW_DURABLE_" + piRandomHex(4)
	firstDone := "PI_FIRST_DONE_" + piRandomHex(4)
	liveAck := "PI_DURABLE_ACK_" + piRandomHex(4)
	idleToken := "PI_DURABLE_IDLE_" + piRandomHex(4)

	slowToolMarker := filepath.Join(t.TempDir(), "slow-tool-started")
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, writePiSlowMCPServer(t, "PI_DURABLE_SECRET", slowToolMarker))

	paneDiag := func() string {
		if session, ok := activePiInteractiveSession(ownerSessionID); ok && session != nil {
			p, _ := capturePiPane(context.Background(), session.tmuxSessionName)
			return p
		}
		return "(no active session)"
	}

	parentCtx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	resultCh := make(chan piRealResult, 1)
	startupErrCh := make(chan error, 1)
	go func() {
		resp, err := adapter.GenerateContent(parentCtx, []llmtypes.MessageContent{
			llmtypes.TextPart(llmtypes.ChatMessageTypeSystem, "Use declared MCP tools when asked. Do not answer until slow tools return. If a follow-up user message arrives while you are working, handle it after the current tool call finishes."),
			llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, fmt.Sprintf("Call the api-bridge MCP tool slow_contract with token %s and delay_ms 8000. Do not answer until the tool returns. Then reply exactly %s.", toolToken, firstDone)),
		},
			WithInteractiveSessionID(ownerSessionID),
			WithPersistentInteractiveSession(true),
			WithWorkingDir(workDir),
			WithMCPConfig(mcpConfig),
			WithBridgeOnlyTools(true),
		)
		out := piRealResult{err: err}
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

	waitForPiRealActiveSession(t, ownerSessionID, 45*time.Second, startupErrCh)
	waitForPiRealFile(t, slowToolMarker, "slow MCP tool call start", 90*time.Second, resultCh)

	// Deliver the follow-up WHILE Pi is busy in the slow tool.
	liveMessage := fmt.Sprintf("Follow-up task: after the current answer completes, also reply exactly %s and nothing else.", liveAck)
	sendCtx, sendCancel := context.WithTimeout(context.Background(), 30*time.Second)
	sendErr := SendPiInteractiveInput(sendCtx, ownerSessionID, liveMessage)
	sendCancel()
	if sendErr != nil {
		cancel()
		t.Fatalf("SendPiInteractiveInput error = %v", sendErr)
	}

	// Durability half of the receipt: the marker stream must record this
	// exact send. Pi natively queues mid-turn input and flushes it as a
	// fresh turn, so the user row lands when the queue flushes.
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 90*time.Second)
	ack, awaitErr := AwaitPiInputDurable(awaitCtx, ownerSessionID, liveMessage, 60*time.Second)
	awaitCancel()
	if awaitErr != nil {
		cancel()
		t.Fatalf("AwaitPiInputDurable error = %v\npane:\n%s", awaitErr, paneDiag())
	}
	if ack.Outcome != PiDurableAckConfirmed {
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
			t.Fatalf("live steer did not affect the busy Pi turn; final content=%q\npane:\n%s", got.content, paneDiag())
		}
	case <-parentCtx.Done():
		t.Fatalf("timed out waiting for the busy turn: %v", parentCtx.Err())
	}

	// Idle follow-up: paste into the settled composer and prove the
	// marker stream confirms it fast.
	idleMessage := fmt.Sprintf("Do not use tools. Reply exactly %s.", idleToken)
	idleSendCtx, idleSendCancel := context.WithTimeout(context.Background(), 30*time.Second)
	idleSendErr := SendPiInteractiveInput(idleSendCtx, ownerSessionID, idleMessage)
	idleSendCancel()
	if idleSendErr != nil {
		t.Fatalf("idle SendPiInteractiveInput error = %v", idleSendErr)
	}
	idleAwaitCtx, idleAwaitCancel := context.WithTimeout(context.Background(), 90*time.Second)
	idleAck, idleAwaitErr := AwaitPiInputDurable(idleAwaitCtx, ownerSessionID, idleMessage, 60*time.Second)
	idleAwaitCancel()
	if idleAwaitErr != nil {
		t.Fatalf("idle AwaitPiInputDurable error = %v\npane:\n%s", idleAwaitErr, paneDiag())
	}
	if idleAck.Outcome != PiDurableAckConfirmed {
		t.Fatalf("idle durable ack outcome = %q, want confirmed\npane:\n%s", idleAck.Outcome, paneDiag())
	}
	if idleAck.Latency > 45*time.Second {
		t.Fatalf("idle durable ack latency = %s, want well under the budget\npane:\n%s", idleAck.Latency, paneDiag())
	}
	t.Logf("idle follow-up durably acked in %s", idleAck.Latency.Round(100*time.Millisecond))
}
