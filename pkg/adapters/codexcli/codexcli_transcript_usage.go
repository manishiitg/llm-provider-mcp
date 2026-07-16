package codexcli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
// Selection is scoped by cwd when possible because the codex interactive
// adapter doesn't observe codex's internal UUID from tmux (it lives in the
// rollout filename and session_meta payload). Recency alone is unsafe when
// multiple Codex processes are active, including this desktop Codex session.
// We pick the freshest rollout file whose mtime is >= turnStart-30s and whose
// session_meta.cwd matches expectedWorkingDir. If expectedWorkingDir is set and
// no matching rollout exists, we return empty rather than attaching a resume id
// for the wrong Codex thread.
//
// Returns nil/empty on any error — best-effort. The model string is
// the latest model observed on an in-turn `turn_context` event
// (codex reports the effective model + reasoning effort there). The
// thread ID is the Codex session UUID from session_meta or the rollout
// filename.
func readCodexTranscriptUsage(turnStart time.Time, expectedWorkingDir string) (*llmtypes.GenerationInfo, string, string) {
	root := codexSessionsRoot()
	if root == "" {
		return nil, "", ""
	}

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
		return nil, "", ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })

	for _, candidate := range cands {
		usage, model, threadID, cwd := readCodexTranscriptUsageFile(candidate.path, turnStart)
		if strings.TrimSpace(expectedWorkingDir) != "" && !sameCodexWorkingDir(cwd, expectedWorkingDir) {
			continue
		}
		return usage, model, threadID
	}
	return nil, "", ""
}

func readCodexTranscriptUsageFile(path string, turnStart time.Time) (*llmtypes.GenerationInfo, string, string, string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", "", ""
	}
	defer f.Close()
	threadID := codexThreadIDFromRolloutPath(path)

	type tokenSnapshot struct {
		InputTokens           int `json:"input_tokens"`
		CachedInputTokens     int `json:"cached_input_tokens"`
		OutputTokens          int `json:"output_tokens"`
		ReasoningOutputTokens int `json:"reasoning_output_tokens"`
		TotalTokens           int `json:"total_tokens"`
	}
	type eventPayload struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		CWD   string `json:"cwd"`
		Model string `json:"model"`
		// Effort is the reasoning effort carried on turn_context events (e.g.
		// "xhigh", "high").
		Effort string `json:"effort"`
		Info   struct {
			LastTokenUsage tokenSnapshot `json:"last_token_usage"`
			// ModelContextWindow is the model's max context size, reported on
			// token_count events — the denominator for context-fill percentage.
			ModelContextWindow int `json:"model_context_window"`
		} `json:"info"`
		// Codex attaches plan rate-limit windows alongside info on each
		// token_count event (primary ≈ 5h, secondary ≈ 7d). See codexRateLimits.
		RateLimits *codexRateLimits `json:"rate_limits"`
	}
	type event struct {
		Type      string       `json:"type"`
		Timestamp string       `json:"timestamp"`
		Payload   eventPayload `json:"payload"`
	}

	var latest *tokenSnapshot
	var latestRateLimits *codexRateLimits
	var latestModel string
	var latestEffort string
	var latestContextWindow int
	var sessionCWD string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var ev event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type == "session_meta" && ev.Payload.ID != "" {
			threadID = ev.Payload.ID
		}
		if ev.Type == "session_meta" && ev.Payload.CWD != "" {
			sessionCWD = ev.Payload.CWD
		}
		if !turnStart.IsZero() && ev.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
		}
		switch {
		case ev.Type == "turn_context":
			if ev.Payload.Model != "" {
				latestModel = ev.Payload.Model
			}
			if ev.Payload.Effort != "" {
				latestEffort = ev.Payload.Effort
			}
		case ev.Type == "event_msg" && ev.Payload.Type == "token_count":
			snap := ev.Payload.Info.LastTokenUsage
			latest = &snap
			if ev.Payload.Info.ModelContextWindow > 0 {
				latestContextWindow = ev.Payload.Info.ModelContextWindow
			}
			if ev.Payload.RateLimits != nil {
				latestRateLimits = ev.Payload.RateLimits
			}
		}
	}
	if latest == nil || (latest.InputTokens+latest.OutputTokens+latest.CachedInputTokens) == 0 {
		return nil, latestModel, threadID, sessionCWD
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
		// Mirror the cache hit count under the raw Anthropic-style
		// key the cost ledger's extractCacheTokens looks for. OpenAI's
		// prompt caching is automatic and read-only (no separate
		// "creation" event), so cache_write stays zero — only
		// cache_read_input_tokens needs surfacing.
		gi.Additional = map[string]interface{}{
			"cache_read_input_tokens": latest.CachedInputTokens,
		}
	}
	if latest.ReasoningOutputTokens > 0 {
		gi.ReasoningTokens = intRef(latest.ReasoningOutputTokens)
	}
	// Carry display-ready statusline extras (rate-limit usage, context fill,
	// effort, plan) so buildCodexStatusLine can expose them generically (see
	// llmtypes.StatusExtrasMetaKey).
	if extras := codexStatusExtras(latestRateLimits, latest.InputTokens, latestContextWindow, latestEffort, time.Now()); len(extras) > 0 {
		if gi.Additional == nil {
			gi.Additional = map[string]interface{}{}
		}
		gi.Additional[llmtypes.StatusExtrasMetaKey] = extras
	}
	return gi, latestModel, threadID, sessionCWD
}

// codexRateLimitWindow is one of Codex's rolling rate-limit windows.
type codexRateLimitWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

// codexRateLimits mirrors the rate_limits block Codex attaches to token_count
// events. primary is the short (≈5h) window, secondary the long (≈7d) window.
type codexRateLimits struct {
	Primary   *codexRateLimitWindow `json:"primary"`
	Secondary *codexRateLimitWindow `json:"secondary"`
	PlanType  string                `json:"plan_type"`
}

// codexStatusExtras renders Codex's per-turn statusline extras into display-ready
// segments, in footer order: plan rate-limit usage ("5h N%", "7d N%"),
// context-window fill ("ctx N%"), reasoning effort ("xhigh"), and plan ("pro").
// Each input is optional — absent ones are skipped so the footer shows only what
// codex actually reported. Window labels derive from window_minutes (300→"5h").
func codexStatusExtras(rl *codexRateLimits, promptTokens, contextWindow int, effort string, now time.Time) []string {
	var extras []string
	if rl != nil {
		if w := rl.Primary; w != nil {
			extras = append(extras, llmtypes.FormatUsageExtraWithReset(codexWindowLabel(w.WindowMinutes, "5h"), w.UsedPercent, w.ResetsAt, now))
		}
		if w := rl.Secondary; w != nil {
			extras = append(extras, llmtypes.FormatUsageExtraWithReset(codexWindowLabel(w.WindowMinutes, "7d"), w.UsedPercent, w.ResetsAt, now))
		}
	}
	if contextWindow > 0 && promptTokens > 0 {
		extras = append(extras, llmtypes.FormatUsageExtra("ctx", float64(promptTokens)/float64(contextWindow)*100))
	}
	if effort != "" {
		extras = append(extras, effort)
	}
	if rl != nil && rl.PlanType != "" {
		extras = append(extras, rl.PlanType)
	}
	return extras
}

// codexWindowLabel renders a window length in minutes as a compact label
// (e.g. 300 → "5h", 10080 → "7d"), using fallback when minutes is unset.
func codexWindowLabel(minutes int, fallback string) string {
	switch {
	case minutes <= 0:
		return fallback
	case minutes%1440 == 0:
		return fmt.Sprintf("%dd", minutes/1440)
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

func codexThreadIDFromRolloutPath(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	const prefix = "rollout-"
	if !strings.HasPrefix(name, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(name, prefix)
	if len(rest) < 36 {
		return ""
	}
	candidate := rest[len(rest)-36:]
	if candidate[8] != '-' || candidate[13] != '-' || candidate[18] != '-' || candidate[23] != '-' {
		return ""
	}
	return candidate
}

func intRef(v int) *int { return &v }

func sameCodexWorkingDir(a, b string) bool {
	a = canonicalCodexWorkingDir(a)
	b = canonicalCodexWorkingDir(b)
	return a != "" && b != "" && a == b
}

func canonicalCodexWorkingDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}
