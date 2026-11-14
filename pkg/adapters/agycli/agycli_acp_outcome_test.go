package agycli

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// PLAT-746-987: ACP refusal must not turn streamed text into a finished reply.
func TestAgyACPRefusalAndContextUsageAtRPCBoundary(t *testing.T) {
	for _, used := range []int{321, 0} {
		rpc := &agyACP{pending: map[string]chan agyACPMessage{}, done: make(chan struct{})}
		s := &agyACPSession{rpc: rpc, sessionID: "quota-test", model: "example-model", transcriptHome: t.TempDir()}
		rpc.stdin = agyConfigWireWriter(func(raw []byte) (int, error) {
			var req struct {
				ID json.RawMessage `json:"id"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Error(err)
				return 0, err
			}
			for _, update := range []map[string]any{
				{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "partial answer"}},
				{"sessionUpdate": "usage_update", "used": used, "size": 1000},
			} {
				params, _ := json.Marshal(map[string]any{"sessionId": s.sessionID, "update": update})
				s.mu.Lock()
				listener := s.listener
				s.mu.Unlock()
				listener <- agyACPMessage{Method: "session/update", Params: params}
			}
			rpc.mu.Lock()
			reply := rpc.pending[string(req.ID)]
			rpc.mu.Unlock()
			reply <- agyACPMessage{Result: json.RawMessage(`{"stopReason":"refusal"}`)}
			return len(raw), nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var status *llmtypes.StatusLine
		resp, err := (&AgyCLIAdapter{}).consumeAgyACPTurn(ctx, s, "hello", func(c llmtypes.StreamChunk) {
			if c.StatusLine != nil {
				status = c.StatusLine
			}
		})
		cancel()
		if resp != nil || llmerrors.KindOf(err) != llmerrors.KindQuotaExhausted {
			t.Fatalf("refusal became success: response=%v err=%v", resp, err)
		}
		if status == nil || status.Metadata[llmtypes.ContextUsedTokensMetaKey] != used || status.Metadata[llmtypes.ContextWindowTokensMetaKey] != 1000 {
			t.Fatalf("lost native context: %+v", status)
		}
	}
}

// Only the current session's newest post-prompt step is an outcome carrier.
func TestAgyACPOutcomeScopesNativeErrorAndReset(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "native.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE steps(idx INTEGER PRIMARY KEY,step_type INTEGER,step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	insert := func(idx, typ int, payload string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO steps VALUES(?,?,?)`, idx, typ, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	insert(0, 17, "reason:QUOTA_EXHAUSTED quotaResetTimeStamp:2030-01-01T01:00:00Z")
	watch := beginAgyACPOutcome(home, "native")
	if !watch.enabled || watch.floor != 0 {
		t.Fatal("failed to establish native floor")
	}
	if err := watch.failure("example-model", "end_turn"); err != nil {
		t.Fatalf("old quota leaked: %v", err)
	}
	insert(1, 17, "Error 429, Message: exhausted, Status: RESOURCE_EXHAUSTED reason:QUOTA_EXHAUSTED quotaResetTimeStamp:2030-01-02T01:00:00Z")
	failure := watch.failure("example-model", "end_turn")
	if llmerrors.KindOf(failure) != llmerrors.KindQuotaExhausted || llmerrors.RetryAtOrZero(failure).UTC().Format(time.RFC3339) != "2030-01-02T01:00:00Z" {
		t.Fatalf("lost error or reset: %v", failure)
	}
	insert(2, 1, "normal final answer")
	if err := watch.failure("example-model", "end_turn"); err != nil {
		t.Fatalf("non-final error was treated as outcome: %v", err)
	}
	if _, _, err := agyACPConversationFile(home, "../native"); err == nil {
		t.Fatal("native session escaped directory")
	}
	if err := os.Symlink(path, filepath.Join(dir, "linked.db")); err != nil {
		t.Fatal(err)
	}
	if beginAgyACPOutcome(home, "linked").enabled {
		t.Fatal("linked native log accepted")
	}
	if err := os.Symlink(path, path+"-wal"); err != nil {
		t.Fatal(err)
	}
	if beginAgyACPOutcome(home, "native").enabled {
		t.Fatal("linked WAL accepted")
	}
}

func TestAgyACPTypedErrorsClassifyQuotaRetryAndUnavailableModel(t *testing.T) {
	now := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		carrier string
		kind    llmerrors.Kind
		retry   bool
	}{
		{"Error 429, Message: wait, Status: RESOURCE_EXHAUSTED", llmerrors.KindRateLimit, true},
		{"Error 503, Message: busy, Status: UNAVAILABLE", llmerrors.KindServerError, true},
		{"native type-17 error", llmerrors.KindUnknown, false},
		{"reason:QUOTA_EXHAUSTED quotaResetDelay:1h2m", llmerrors.KindQuotaExhausted, false},
	} {
		err := agyACPOutcomeError("example-model", "", tc.carrier, now)
		if llmerrors.KindOf(err) != tc.kind || llmerrors.IsRetryable(err) != tc.retry {
			t.Fatalf("wrong native outcome %q: %v", tc.carrier, err)
		}
		if tc.kind == llmerrors.KindQuotaExhausted && !llmerrors.RetryAtOrZero(err).Equal(now.Add(62*time.Minute)) {
			t.Fatal("lost relative reset")
		}
	}
	err := agyACPRPCFailure("example-model", "native", &agyACPError{Code: -32602, Message: "Model not available"})
	if llmerrors.KindOf(err) != llmerrors.KindModelNotFound {
		t.Fatalf("raw unknown model: %v", err)
	}
	quota := agyACPRPCFailure("example-model", "native", &agyACPError{Code: 429, Message: "request rejected", Data: json.RawMessage(`{"reason":"QUOTA_EXHAUSTED","detail":"quotaResetDelay:1h"}`)})
	if llmerrors.KindOf(quota) != llmerrors.KindQuotaExhausted || llmerrors.IsRetryable(quota) || llmerrors.RetryAtOrZero(quota).IsZero() {
		t.Fatalf("429 lost explicit quota/reset: %v", quota)
	}
	for _, code := range []int{429, 503} {
		if !llmerrors.IsRetryable(agyACPRPCFailure("example-model", "native", &agyACPError{Code: code, Message: "request failed"})) {
			t.Fatalf("RPC %d not retryable", code)
		}
	}
}
