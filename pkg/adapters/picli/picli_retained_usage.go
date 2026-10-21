package picli

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// The bounded Run sums the usage of the assistant messages in its window (piMarkUsageReported records each). A
// follow-up typed into the running terminal has the same per-message usage in the transcript and nobody reads it; this
// registry lets the retained-turn reader report each message once.
var piReportedUsage = struct {
	sync.Mutex
	keys map[string]map[string]bool
}{keys: map[string]map[string]bool{}}

func piMarkUsageReported(path, key string) bool {
	if path == "" || key == "" {
		return false
	}
	piReportedUsage.Lock()
	defer piReportedUsage.Unlock()
	set := piReportedUsage.keys[path]
	if set == nil {
		set = map[string]bool{}
		piReportedUsage.keys[path] = set
	}
	if set[key] {
		return false
	}
	set[key] = true
	return true
}

// piUsageKey identifies one assistant message: Pi's response id when it has one, else the record timestamp.
func piUsageKey(ev piTranscriptRecord) string {
	if ev.Message != nil && ev.Message.ResponseID != "" {
		return ev.Message.ResponseID
	}
	return "ts:" + ev.Timestamp
}

// ReadRetainedTurnUsage returns the usage of each assistant message at or after turnStart that has not been reported
// yet (oldest first), with Pi's own cost figures. Read-only; it marks what it returns.
func ReadRetainedTurnUsage(ownerSessionID string, turnStart time.Time) []llmtypes.RetainedTurnUsage {
	ownerSessionID = strings.TrimSpace(ownerSessionID)
	session, ok := activePiInteractiveSession(ownerSessionID)
	if !ok || session == nil {
		log.Printf("[RETAINED_USAGE] pi-cli owner=%s skipped: no terminal registered for this owner", ownerSessionID)
		return nil
	}
	session.mu.Lock()
	nativeSessionID, sessionDir := session.nativeSessionID, session.sessionDir
	session.mu.Unlock()
	if strings.TrimSpace(sessionDir) == "" {
		sessionDir = piTranscriptSessionDir()
	}
	path := latestPiTranscriptPathInDir(sessionDir, nativeSessionID)
	if path == "" {
		log.Printf("[RETAINED_USAGE] pi-cli owner=%s skipped: no transcript resolved", ownerSessionID)
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // adapter-resolved transcript path
	if err != nil {
		log.Printf("[RETAINED_USAGE] pi-cli owner=%s skipped: transcript unreadable", ownerSessionID)
		return nil
	}
	defer f.Close()
	var out []llmtypes.RetainedTurnUsage
	seen := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev piTranscriptRecord
		if json.Unmarshal(scanner.Bytes(), &ev) != nil || ev.Type != "message" || ev.Message == nil || ev.Message.Usage == nil {
			continue
		}
		if role, ok := piTranscriptRole(ev.Message.Role); !ok || role != llmtypes.ChatMessageTypeAI || !piTranscriptRecordInTurn(ev, turnStart) {
			continue
		}
		seen++
		u := ev.Message.Usage
		if u.Input+u.Output+u.CacheRead+u.CacheWrite == 0 {
			continue
		}
		key := piUsageKey(ev)
		if !piMarkUsageReported(path, key) {
			continue
		}
		total := u.TotalTokens
		if total == 0 {
			total = u.Input + u.Output + u.CacheRead + u.CacheWrite
		}
		usage := llmtypes.Usage{InputTokens: u.Input, OutputTokens: u.Output, TotalTokens: total}
		model := ev.Message.Model
		if ev.Message.Provider != "" && model != "" {
			model = ev.Message.Provider + "/" + model
		}
		info := llmtypes.GenerationInfo{InputTokens: intPtr(u.Input), OutputTokens: intPtr(u.Output), TotalTokens: intPtr(total),
			Additional: map[string]interface{}{"provider": "pi-cli", "pi_token_usage_source": "transcript-file", "pi_model": model}}
		if u.CacheRead > 0 {
			info.CachedContentTokens = intPtr(u.CacheRead)
			info.Additional["cache_read_input_tokens"] = u.CacheRead
		}
		if u.CacheWrite > 0 {
			info.Additional["cache_creation_input_tokens"] = u.CacheWrite
		}
		if u.CacheRead > 0 || u.CacheWrite > 0 {
			info.Additional["prompt_tokens_include_cache"] = false // Pi keeps fresh input separate from cache usage
		}
		if u.Cost.Total > 0 {
			info.Additional["cost_usd"] = u.Cost.Total
			info.Additional["input_cost_usd"] = u.Cost.Input
			info.Additional["output_cost_usd"] = u.Cost.Output
			info.Additional["cache_cost_usd"] = u.Cost.CacheRead + u.Cost.CacheWrite
		}
		log.Printf("[RETAINED_USAGE] pi-cli owner=%s message=%s input=%d output=%d reported", ownerSessionID, key, u.Input, u.Output)
		out = append(out, llmtypes.RetainedTurnUsage{ID: key, Usage: usage, Info: info})
	}
	if len(out) == 0 {
		log.Printf("[RETAINED_USAGE] pi-cli owner=%s no new assistant-message usage since %s (messages_seen=%d)", ownerSessionID, turnStart.UTC().Format(time.RFC3339), seen)
	}
	return out
}
