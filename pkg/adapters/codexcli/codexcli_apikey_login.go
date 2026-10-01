package codexcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/internal/slotfs"
)

// seedCodexAPIKeyLogin saves an API key as Codex's own login before the terminal UI starts. The UI does
// not use CODEX_API_KEY / OPENAI_API_KEY from the environment: with no saved login it opens its "paste
// your API key" screen and waits there for someone to press Enter, so a user who configured a key never
// got a working session. The file is the one `codex login --with-api-key` writes.
func seedCodexAPIKeyLogin(env []string, accountRoot, hint string) error {
	key, home := "", strings.TrimSpace(accountRoot)
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, "CODEX_API_KEY="); ok && strings.TrimSpace(v) != "" {
			key = strings.TrimSpace(v)
		}
		if v, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok && strings.TrimSpace(v) != "" {
			home = strings.TrimSpace(v)
		}
	}
	if home == "" {
		return nil
	}
	path := filepath.Join(home, "auth.json")
	marker := path + ".agentworks"
	if key == "" {
		// No key is configured any more: a key login saved here earlier must not stay valid. Only a login
		// this function wrote (it leaves a marker beside it) is removed; a key the user pasted into
		// Codex themselves, or a browser login, is theirs and is left alone.
		if _, err := os.Stat(marker); err == nil {
			_ = os.Remove(marker)
			return os.Remove(path)
		}
		return nil
	}
	if raw, err := os.ReadFile(path); err == nil {
		var current struct {
			Mode string `json:"auth_mode"`
			Key  string `json:"OPENAI_API_KEY"`
		}
		if json.Unmarshal(raw, &current) == nil && current.Key == key && current.Mode == "apikey" {
			return nil
		}
	}
	body, err := json.MarshalIndent(map[string]string{"auth_mode": "apikey", "OPENAI_API_KEY": key}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, slotfs.Mode(hint, 0o700)); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(body, '\n'), slotfs.Mode(hint, 0o600)); err != nil {
		return err
	}
	return os.WriteFile(marker, nil, slotfs.Mode(hint, 0o600))
}
