package agycli

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAgyMountFingerprint(t *testing.T) {
	if got := agyMountFingerprint(""); got != "unmounted" {
		t.Fatalf("empty = %q, want unmounted", got)
	}
	if got := agyMountFingerprint("  \n"); got != "unmounted" {
		t.Fatalf("blank = %q, want unmounted", got)
	}
	a := agyMountFingerprint(`{"mcpServers":{"x":{"command":"node"}}}`)
	b := agyMountFingerprint("  " + `{"mcpServers":{"x":{"command":"node"}}}` + "\n")
	c := agyMountFingerprint(`{"mcpServers":{"y":{"command":"node"}}}`)
	if a == "unmounted" || a != b {
		t.Fatalf("same config fingerprints differ: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("different configs share fingerprint %q", a)
	}
}

func TestAgyTurnUsageDecodesGoldenPayload(t *testing.T) {
	raw, err := os.ReadFile("testdata/assistant_step_usage.bin")
	if err != nil {
		t.Fatalf("read golden payload: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Captured live 2026-09-20: field 5.9 holds input 8523, output 313,
	// thinking 309 — the same turn shape exec JSON reports. Pins the tag
	// mapping against real CLI bytes, not hand-built fixtures.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "golden.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE steps (idx INTEGER PRIMARY KEY, step_type INTEGER, step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO steps (idx, step_type, step_payload) VALUES (0, 14, ?)`, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO steps (idx, step_type, step_payload) VALUES (1, 15, ?)`, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got := agyTurnUsageSince("golden", -1)
	if got.InputTokens != 8523 || got.OutputTokens != 313 {
		t.Fatalf("usage = %+v, want input 8523 output 313", got)
	}
	if got.TotalTokens != 8836 {
		t.Fatalf("total = %d, want 8836", got.TotalTokens)
	}
	if got.ThoughtsTokens == nil || *got.ThoughtsTokens != 309 {
		t.Fatalf("thinking = %+v, want 309", got.ThoughtsTokens)
	}
	if since := agyTurnUsageSince("golden", 1); since.TotalTokens != 0 {
		t.Fatalf("usage since idx 1 = %+v, want zero (no new steps)", since)
	}
	if missing := agyTurnUsageSince("nope", -1); missing.TotalTokens != 0 {
		t.Fatalf("usage for missing conversation = %+v, want zero", missing)
	}
}

func TestAgyPermissionAllowRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"modelProvider":"gemini","permissions":{"allow":["mcp(user-srv/tool)"]},"trustedWorkspaces":["/tmp"]}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agyAllowMountedTools([]string{"agentworks-a-1", "agentworks-b-2"}); err != nil {
		t.Fatalf("allow: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	got := string(raw)
	for _, want := range []string{"mcp(agentworks-a-1/*)", "mcp(agentworks-b-2/*)", "mcp(user-srv/tool)", "trustedWorkspaces", "modelProvider"} {
		if !strings.Contains(got, want) {
			t.Fatalf("settings after allow missing %q:\n%s", want, got)
		}
	}
	// Idempotent: second add changes nothing.
	if err := agyAllowMountedTools([]string{"agentworks-a-1"}); err != nil {
		t.Fatalf("re-allow: %v", err)
	}
	again, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if string(again) != got {
		t.Fatalf("re-allow changed settings:\n%s\nvs\n%s", again, got)
	}
	// Removal drops only our entries.
	if err := agyRemoveAllowedTools([]string{"agentworks-a-1", "agentworks-b-2"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	final := string(after)
	for _, gone := range []string{"agentworks-a-1", "agentworks-b-2"} {
		if strings.Contains(final, gone) {
			t.Fatalf("settings after remove still has %q:\n%s", gone, final)
		}
	}
	if !strings.Contains(final, "mcp(user-srv/tool)") {
		t.Fatalf("remove dropped foreign entry:\n%s", final)
	}
}
