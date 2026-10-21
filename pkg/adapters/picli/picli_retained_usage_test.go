package picli

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPiRetainedUsagePreservesPricingRouteAndReportsEachMessageOnce(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", dir)
	owner, native := t.Name(), "pi-pricing-native"
	piInteractiveRegistry.Lock()
	piInteractiveRegistry.sessions[owner] = &piInteractiveSession{ownerSessionID: owner, nativeSessionID: native, tmuxSessionName: "pi-pricing-test"}
	piInteractiveRegistry.Unlock()
	t.Cleanup(func() {
		piInteractiveRegistry.Lock()
		delete(piInteractiveRegistry.sessions, owner)
		piInteractiveRegistry.Unlock()
	})
	data := `{"type":"message","id":"google-call","timestamp":"2026-10-21T02:38:49Z","message":{"role":"assistant","provider":"google","model":"gemini-3.7-flash","usage":{"input":2522,"output":1730,"cacheRead":154264,"cost":{"total":0}}}}
{"type":"message","id":"router-call","timestamp":"2026-10-21T02:42:10Z","message":{"role":"assistant","provider":"openrouter","model":"google/gemini-3.7-flash","usage":{"input":2954,"output":1007,"cacheRead":79943,"cost":{"total":0.0123}}}}
`
	if err := os.WriteFile(filepath.Join(dir, "2026-10-21_"+native+".jsonl"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	items := ReadRetainedTurnUsage(owner, start)
	if len(items) != 2 {
		t.Fatalf("usage messages = %d, want 2", len(items))
	}
	if items[0].Info.Additional["pi_model"] != "google/gemini-3.7-flash" || items[1].Info.Additional["pi_model"] != "openrouter/google/gemini-3.7-flash" {
		t.Fatal("native routing was lost")
	}
	if items[0].Info.Additional["prompt_tokens_include_cache"] != false || items[1].Info.Additional["cost_usd"] != 0.0123 {
		t.Fatal("native usage/cost was changed")
	}
	if again := ReadRetainedTurnUsage(owner, start); len(again) != 0 {
		t.Fatal("native messages were billed twice")
	}
}
