package picli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type piTranscriptSummary struct {
	Path                string
	Messages            []llmtypes.MessageContent
	Provider            string
	Model               string
	API                 string
	ResponseID          string
	StopReason          string
	InputTokens         int
	OutputTokens        int
	TotalTokens         int
	CacheReadTokens     int
	CacheWriteTokens    int
	InputCostUSD        float64
	OutputCostUSD       float64
	TotalCostUSD        float64
	CacheReadCostUSD    float64
	CacheWriteCostUSD   float64
	AssistantUsageCount int
}

func (s *piTranscriptSummary) hasUsage() bool {
	return s != nil && s.AssistantUsageCount > 0 && s.InputTokens+s.OutputTokens+s.TotalTokens+s.CacheReadTokens+s.CacheWriteTokens > 0
}

func readPiTranscriptSummary(sessionID string, turnStart time.Time) *piTranscriptSummary {
	transcriptPath := latestPiTranscriptPath(sessionID)
	if transcriptPath == "" {
		return nil
	}
	summary := readPiTranscriptSummaryFile(transcriptPath, turnStart)
	if summary == nil {
		return &piTranscriptSummary{Path: transcriptPath}
	}
	summary.Path = transcriptPath
	return summary
}

func latestPiTranscriptPath(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	sessionDir := piTranscriptSessionDir()
	if sessionDir == "" {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	cands := []candidate{}
	suffix := "_" + sessionID + ".jsonl"
	if err := filepath.WalkDir(sessionDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry == nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), suffix) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		cands = append(cands, candidate{path: path, mod: info.ModTime()})
		return nil
	}); err != nil {
		return ""
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	return cands[0].path
}

func piTranscriptSessionDir() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR")); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return filepath.Join(dir, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", "sessions")
}

func piConfiguredTranscriptSessionDir() string {
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_SESSION_DIR")); dir != "" {
		return dir
	}
	if dir := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); dir != "" {
		return filepath.Join(dir, "sessions")
	}
	return ""
}

func readPiTranscriptSummaryFile(path string, turnStart time.Time) *piTranscriptSummary {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	summary := &piTranscriptSummary{Path: path}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev piTranscriptRecord
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil || ev.Type != "message" || ev.Message == nil {
			continue
		}
		if !piTranscriptRecordInTurn(ev, turnStart) {
			continue
		}
		role, ok := piTranscriptRole(ev.Message.Role)
		if !ok {
			continue
		}
		if text := strings.TrimSpace(piTranscriptText(ev.Message.Content)); text != "" {
			summary.Messages = append(summary.Messages, llmtypes.TextPart(role, text))
		}
		if role != llmtypes.ChatMessageTypeAI || ev.Message.Usage == nil {
			continue
		}
		summary.AssistantUsageCount++
		usage := ev.Message.Usage
		summary.InputTokens += usage.Input
		summary.OutputTokens += usage.Output
		summary.CacheReadTokens += usage.CacheRead
		summary.CacheWriteTokens += usage.CacheWrite
		if usage.TotalTokens > 0 {
			summary.TotalTokens += usage.TotalTokens
		}
		summary.InputCostUSD += usage.Cost.Input
		summary.OutputCostUSD += usage.Cost.Output
		summary.TotalCostUSD += usage.Cost.Total
		summary.CacheReadCostUSD += usage.Cost.CacheRead
		summary.CacheWriteCostUSD += usage.Cost.CacheWrite
		if ev.Message.Provider != "" {
			summary.Provider = ev.Message.Provider
		}
		if ev.Message.Model != "" {
			summary.Model = ev.Message.Model
		}
		if ev.Message.API != "" {
			summary.API = ev.Message.API
		}
		if ev.Message.ResponseID != "" {
			summary.ResponseID = ev.Message.ResponseID
		}
		if ev.Message.StopReason != "" {
			summary.StopReason = ev.Message.StopReason
		}
	}
	if summary.TotalTokens == 0 {
		summary.TotalTokens = summary.InputTokens + summary.OutputTokens
	}
	return summary
}

type piTranscriptRecord struct {
	Type      string               `json:"type"`
	Timestamp string               `json:"timestamp"`
	Message   *piTranscriptMessage `json:"message"`
}

type piTranscriptMessage struct {
	Role       string                `json:"role"`
	Content    []piTranscriptContent `json:"content"`
	API        string                `json:"api"`
	Provider   string                `json:"provider"`
	Model      string                `json:"model"`
	Usage      *piTranscriptUsage    `json:"usage"`
	StopReason string                `json:"stopReason"`
	ResponseID string                `json:"responseId"`
}

type piTranscriptContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type piTranscriptUsage struct {
	Input       int              `json:"input"`
	Output      int              `json:"output"`
	TotalTokens int              `json:"totalTokens"`
	CacheRead   int              `json:"cacheRead"`
	CacheWrite  int              `json:"cacheWrite"`
	Cost        piTranscriptCost `json:"cost"`
}

type piTranscriptCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	Total      float64 `json:"total"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

func piTranscriptRecordInTurn(ev piTranscriptRecord, turnStart time.Time) bool {
	if turnStart.IsZero() || ev.Timestamp == "" {
		return true
	}
	ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
	if err != nil {
		return true
	}
	return !ts.Before(turnStart)
}

func piTranscriptRole(role string) (llmtypes.ChatMessageType, bool) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		return llmtypes.ChatMessageTypeHuman, true
	case "assistant":
		return llmtypes.ChatMessageTypeAI, true
	case "system":
		return llmtypes.ChatMessageTypeSystem, true
	default:
		return "", false
	}
}

func piTranscriptText(parts []piTranscriptContent) string {
	texts := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Type) != "" && part.Type != "text" {
			continue
		}
		if strings.TrimSpace(part.Text) != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}
