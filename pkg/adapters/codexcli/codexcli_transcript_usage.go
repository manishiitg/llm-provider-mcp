package codexcli

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// readCodexTranscriptUsage looks up the codex-cli session rollout
// JSONL written by the local codex CLI and extracts the latest
// per-turn token snapshot.
//
// codex writes rollout files at
//
//	~/.codex/sessions/YYYY/MM/DD/rollout-<timestamp>-<session-uuid>.jsonl
//
// Each token-accounting line is an `event_msg` with payload
// `type: token_count` carrying both `total_token_usage` (cumulative)
// and `last_token_usage` (delta for the most recent turn). We take
// the LAST token_count event with timestamp >= turnStart — that one
// reflects the just-completed turn.
//
// Selection by recency rather than by session-id because the codex
// interactive adapter doesn't observe codex's internal UUID from
// tmux (it lives in the rollout filename and session_meta payload).
// We pick the freshest rollout file whose mtime is >= turnStart-30s,
// which is robust as long as we're not racing parallel codex
// instances.
//
// Returns nil on any error — best-effort.
func readCodexTranscriptUsage(turnStart time.Time) *llmtypes.GenerationInfo {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	root := filepath.Join(home, ".codex", "sessions")

	// Find the freshest rollout-*.jsonl whose mtime is at-or-after turnStart-30s.
	cutoff := turnStart.Add(-30 * time.Second)
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Ext(p) != ".jsonl" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			return nil
		}
		cands = append(cands, cand{path: p, mod: info.ModTime()})
		return nil
	})
	if len(cands) == 0 {
		return nil
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })

	f, err := os.Open(cands[0].path)
	if err != nil {
		return nil
	}
	defer f.Close()

	type tokenSnapshot struct {
		InputTokens           int `json:"input_tokens"`
		CachedInputTokens     int `json:"cached_input_tokens"`
		OutputTokens          int `json:"output_tokens"`
		ReasoningOutputTokens int `json:"reasoning_output_tokens"`
		TotalTokens           int `json:"total_tokens"`
	}
	type eventPayload struct {
		Type string `json:"type"`
		Info struct {
			LastTokenUsage tokenSnapshot `json:"last_token_usage"`
		} `json:"info"`
	}
	type event struct {
		Type      string       `json:"type"`
		Timestamp string       `json:"timestamp"`
		Payload   eventPayload `json:"payload"`
	}

	var latest *tokenSnapshot
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type != "event_msg" || ev.Payload.Type != "token_count" {
			continue
		}
		if !turnStart.IsZero() && ev.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
		}
		snap := ev.Payload.Info.LastTokenUsage
		latest = &snap
	}
	if latest == nil || (latest.InputTokens+latest.OutputTokens+latest.CachedInputTokens) == 0 {
		return nil
	}

	// codex reports `input_tokens` as the total prompt-side count
	// (uncached + cached). Surface uncached + cached separately so cost
	// math downstream can apply the cache discount.
	uncachedPrompt := latest.InputTokens - latest.CachedInputTokens
	if uncachedPrompt < 0 {
		uncachedPrompt = latest.InputTokens
	}
	gi := &llmtypes.GenerationInfo{
		PromptTokens:     intRef(uncachedPrompt),
		CompletionTokens: intRef(latest.OutputTokens),
		TotalTokens:      intRef(latest.TotalTokens),
	}
	if latest.CachedInputTokens > 0 {
		gi.CachedContentTokens = intRef(latest.CachedInputTokens)
	}
	if latest.ReasoningOutputTokens > 0 {
		gi.ReasoningTokens = intRef(latest.ReasoningOutputTokens)
	}
	return gi
}

func intRef(v int) *int { return &v }
