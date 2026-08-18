package claudecode

import (
	"encoding/json"
	"os"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// claudeStatuslineRateLimitWindows reads the rate-limit windows Claude Code
// last wrote to the statusline sidecar for a tmux session.
//
// PLAT-101 ranks reset-time sources, and this is the top one: the CLI states
// `rate_limits.<window>.resets_at` as an exact epoch, whereas the pane prints
// a bare wall clock ("resets 10am") that has to be reconstructed into an
// instant. Both were already being collected — the structured form only ever
// travelled outward to the UI, so the failure path reconstructed a time it did
// not need to.
//
// One-shot and best-effort by design. Unlike readClaudeStatuslineWithWait this
// never polls: the only caller is already holding a usage-limit wall, and
// blocking a failing turn for fifteen seconds to sharpen a timestamp is a bad
// trade. A missing, empty, or unparseable sidecar yields no windows and the
// caller falls back to the next source.
func claudeStatuslineRateLimitWindows(sessionName string) []llmtypes.RateLimitWindow {
	if sessionName == "" {
		return nil
	}
	raw, err := os.ReadFile(claudeStatuslinePath(sessionName))
	if err != nil {
		return nil
	}
	var rawMap map[string]interface{}
	if err := json.Unmarshal(raw, &rawMap); err != nil {
		return nil
	}
	return claudeRateLimitWindows(rawMap)
}
