package musecli

import (
	"encoding/json"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Muse usage reaches the host in two ways: the bounded Run reads the usage of its own final run id from the session
// log, and every other run (a follow-up typed into the running terminal, or a later turn) was never read by anyone.
// This registry remembers which run ids a session log's usage was already handed out for, from either path, so each
// run is reported once.
var museReportedUsage = struct {
	sync.Mutex
	runs map[string]map[string]bool
}{runs: map[string]map[string]bool{}}

func museMarkUsageReported(logPath, runID string) bool {
	if logPath == "" || runID == "" {
		return false
	}
	museReportedUsage.Lock()
	defer museReportedUsage.Unlock()
	set := museReportedUsage.runs[logPath]
	if set == nil {
		set = map[string]bool{}
		museReportedUsage.runs[logPath] = set
	}
	if set[runID] {
		return false
	}
	set[runID] = true
	return true
}

// ReadRetainedTurnUsage returns the usage Muse reported for runs of this owner's terminal that nobody has reported
// yet (one entry per run id, oldest first), priced for modelID. Only runs after the floor taken when the transcript
// was bound are considered, so a restart never re-reports runs from before it. Read-only; it marks what it returns.
func ReadRetainedTurnUsage(ownerSessionID, modelID string) []llmtypes.RetainedTurnUsage {
	key, err := musePersistentKey(ownerSessionID)
	if err != nil {
		return nil
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	if entry == nil {
		musePersistentPool.Unlock()
		log.Printf("[RETAINED_USAGE] muse owner=%s skipped: no terminal registered for this owner", ownerSessionID)
		return nil
	}
	path, floor := entry.logPath, entry.usageFloorSequence
	if path == "" && entry.nativeSessionID != "" {
		path = museSessionLogPath(entry.nativeSessionID, entry.accountDataHome)
	}
	musePersistentPool.Unlock()
	raw, err := os.ReadFile(path) //nolint:gosec // adapter-owned transcript path
	if err != nil {
		log.Printf("[RETAINED_USAGE] muse owner=%s skipped: transcript unreadable", ownerSessionID)
		return nil
	}
	type runUsage struct {
		first int64
		usage llmtypes.Usage
	}
	byRun := map[string]*runUsage{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) != nil {
			continue
		}
		sequence, _ := rec["sequence"].(float64)
		if int64(sequence) <= floor {
			continue
		}
		payload, _ := rec["payload"].(map[string]any)
		if payload == nil || museLogString(payload, "kind") != "run" {
			continue
		}
		event, _ := payload["event"].(map[string]any)
		if event == nil || museLogString(event, "kind") != "goal_usage_attribution" {
			continue
		}
		record, _ := event["record"].(map[string]any)
		quantity, _ := record["quantity"].(map[string]any)
		if quantity == nil || !museLogBool(quantity, "reported") {
			continue
		}
		runID := museLogString(event, "record", "owner", "run_id")
		if runID == "" {
			runID = museLogString(payload, "run_id")
		}
		if runID == "" {
			continue
		}
		one := byRun[runID]
		if one == nil {
			one = &runUsage{first: int64(sequence)}
			byRun[runID] = one
		}
		one.usage.InputTokens += museLogInt(quantity, "input_tokens")
		one.usage.OutputTokens += museLogInt(quantity, "output_tokens")
		if cached := museLogInt(quantity, "cached_tokens"); cached > 0 {
			sum := cached
			if one.usage.CacheTokens != nil {
				sum += *one.usage.CacheTokens
			}
			one.usage.CacheTokens = &sum
		}
		if reasoning := museLogInt(quantity, "reasoning_tokens"); reasoning > 0 {
			sum := reasoning
			if one.usage.ReasoningTokens != nil {
				sum += *one.usage.ReasoningTokens
			}
			one.usage.ReasoningTokens = &sum
		}
	}
	ids := make([]string, 0, len(byRun))
	for id := range byRun {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return byRun[ids[i]].first < byRun[ids[j]].first })
	var out []llmtypes.RetainedTurnUsage
	for _, id := range ids {
		one := byRun[id]
		one.usage.TotalTokens = one.usage.InputTokens + one.usage.OutputTokens
		if !museMarkUsageReported(path, id) {
			continue // already reported by the Run or an earlier read
		}
		item := llmtypes.RetainedTurnUsage{ID: id, Usage: one.usage}
		museAttachTurnCost(&item.Info, modelID, &item.Usage)
		log.Printf("[RETAINED_USAGE] muse owner=%s run=%s input=%d output=%d cached=%d reported", ownerSessionID, id, one.usage.InputTokens, one.usage.OutputTokens, museCacheValue(one.usage))
		out = append(out, item)
	}
	if len(out) == 0 {
		log.Printf("[RETAINED_USAGE] muse owner=%s no new reported run usage (floor=%d runs_seen=%d)", ownerSessionID, floor, len(byRun))
	}
	return out
}

func museCacheValue(u llmtypes.Usage) int {
	if u.CacheTokens == nil {
		return 0
	}
	return *u.CacheTokens
}
