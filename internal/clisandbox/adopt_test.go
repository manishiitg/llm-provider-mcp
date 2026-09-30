package clisandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func writeSessionFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// Each CLI's resumed session moves into the private home, and only that session.
func TestAdoptResumedSessionPerCLI(t *testing.T) {
	root := t.TempDir()
	account := filepath.Join(root, "account")
	private := filepath.Join(root, "private")
	policy := func(provider string, env map[string]string) *llmtypes.CLISecurityPolicy {
		return &llmtypes.CLISecurityPolicy{Provider: provider, PrivateHome: private, CredentialHome: account, CredentialEnv: env}
	}

	writeSessionFile(t, filepath.Join(account, ".local/share/muse/sessions/2026/09/30/mine/session.jsonl"))
	writeSessionFile(t, filepath.Join(account, ".local/share/muse/sessions/.msp-view-v1/mine/journal.bin"))
	writeSessionFile(t, filepath.Join(account, ".local/share/muse/sessions/2026/09/30/other/session.jsonl"))
	adoptResumedSession(policy("muse-cli", nil), []string{"env", "muse", "resume", "mine", "--trust-workspace"})
	for _, rel := range []string{".local/share/muse/sessions/2026/09/30/mine/session.jsonl", ".local/share/muse/sessions/.msp-view-v1/mine/journal.bin"} {
		if !exists(filepath.Join(private, rel)) {
			t.Fatalf("muse: %s not adopted", rel)
		}
	}
	if exists(filepath.Join(private, ".local/share/muse/sessions/2026/09/30/other")) {
		t.Fatal("muse: another session was copied")
	}

	// Codex keeps sessions under the account's own CODEX_HOME when it sets one.
	codexHome := filepath.Join(root, "codex-home")
	writeSessionFile(t, filepath.Join(codexHome, "sessions/2026/09/30/rollout-2026-09-30T10-00-00-abc123.jsonl"))
	writeSessionFile(t, filepath.Join(codexHome, "sessions/2026/09/30/rollout-2026-09-30T10-00-00-zzz999.jsonl"))
	adoptResumedSession(policy("codex-cli", map[string]string{"CODEX_HOME": codexHome}), []string{"codex", "exec", "resume", "abc123", "--json"})
	if !exists(filepath.Join(private, ".codex/sessions/2026/09/30/rollout-2026-09-30T10-00-00-abc123.jsonl")) {
		t.Fatal("codex: session not adopted")
	}
	if exists(filepath.Join(private, ".codex/sessions/2026/09/30/rollout-2026-09-30T10-00-00-zzz999.jsonl")) {
		t.Fatal("codex: another session was copied")
	}

	writeSessionFile(t, filepath.Join(account, ".cursor/chats/hash1/chat-1/store.db"))
	writeSessionFile(t, filepath.Join(account, ".cursor/chats/hash1/chat-2/store.db"))
	adoptResumedSession(policy("cursor-cli", nil), []string{"cursor-agent", "--resume", "chat-1"})
	if !exists(filepath.Join(private, ".cursor/chats/hash1/chat-1/store.db")) || exists(filepath.Join(private, ".cursor/chats/hash1/chat-2")) {
		t.Fatal("cursor: only chat-1 should be adopted")
	}
}

func TestAdoptResumedSessionKeepsAnExistingPrivateCopy(t *testing.T) {
	root := t.TempDir()
	account, private := filepath.Join(root, "account"), filepath.Join(root, "private")
	writeSessionFile(t, filepath.Join(account, ".cursor/chats/h/c/store.db"))
	if err := os.MkdirAll(filepath.Join(private, ".cursor/chats/h/c"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, ".cursor/chats/h/c/mine.txt"), []byte("newer"), 0o600); err != nil {
		t.Fatal(err)
	}
	adoptResumedSession(&llmtypes.CLISecurityPolicy{Provider: "cursor-cli", PrivateHome: private, CredentialHome: account}, []string{"--resume", "c"})
	if exists(filepath.Join(private, ".cursor/chats/h/c/store.db")) {
		t.Fatal("an existing private session was overwritten")
	}
}
