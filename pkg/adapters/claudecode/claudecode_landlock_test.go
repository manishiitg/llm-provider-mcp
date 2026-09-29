package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Claude rejects a settings file whose additionalDirectories is not an array
// ("Expected array, but received undefined") and then stops on a Settings
// Error screen, so a chat with no extra grants must still write [].
func TestClaudeMirrorLandlockReadsWritesArrays(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	policy := &llmtypes.CLISecurityPolicy{PrivateHome: home, WorkspaceWritePaths: []string{work}}
	if err := claudeMirrorLandlockReads(policy, work); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Permissions struct {
			Block bool            `json:"blockReadsOutsideWorkingDirectories"`
			Dirs  json.RawMessage `json:"additionalDirectories"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Permissions.Block || string(settings.Permissions.Dirs) != "[]" {
		t.Fatalf("settings = %s", data)
	}
}
