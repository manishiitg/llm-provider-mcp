package geminicli

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

// readGeminiTranscriptUsage looks up the gemini-cli session transcript
// written by the local gemini CLI and aggregates token usage for the
// current turn.
//
// The CLI writes a JSONL file at
//
//	~/.gemini/tmp/<projectDirID>/chats/session-<timestamp>-<sid6>.jsonl
//
// where <projectDirID> is the adapter-managed project-dir identifier
// (we already track it on the interactive session). We pick the
// most-recently-modified file under that chats dir and sum the
// `tokens` blocks on every `type=gemini` event whose timestamp is at
// or after turnStart.
//
// Returns nil/empty on any error — best-effort, never surfaces IO
// failures. The model string is the latest model observed on an
// in-turn `gemini` event (gemini-cli auto-routes between models, so
// the value here is the model that ACTUALLY served the request).
func readGeminiTranscriptUsage(projectDirID string, turnStart time.Time) (*llmtypes.GenerationInfo, string) {
	transcriptPath := latestGeminiTranscriptPath(projectDirID)
	if transcriptPath == "" {
		return nil, ""
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil, ""
	}
	defer f.Close()

	var input, output, cached, thoughts, tool int
	var latestModel string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Model     string `json:"model"`
			Tokens    struct {
				Input    int `json:"input"`
				Output   int `json:"output"`
				Cached   int `json:"cached"`
				Thoughts int `json:"thoughts"`
				Tool     int `json:"tool"`
				Total    int `json:"total"`
			} `json:"tokens"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil || ev.Type != "gemini" {
			continue
		}
		if !turnStart.IsZero() && ev.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil && ts.Before(turnStart) {
				continue
			}
		}
		input += ev.Tokens.Input
		output += ev.Tokens.Output
		cached += ev.Tokens.Cached
		thoughts += ev.Tokens.Thoughts
		tool += ev.Tokens.Tool
		if ev.Model != "" {
			latestModel = ev.Model
		}
	}
	if input+output+cached+thoughts+tool == 0 {
		return nil, latestModel
	}

	prompt := input + tool
	total := prompt + output + cached + thoughts
	gi := &llmtypes.GenerationInfo{
		PromptTokens:     intRef(prompt),
		CompletionTokens: intRef(output),
		TotalTokens:      intRef(total),
	}
	if cached > 0 {
		gi.CachedContentTokens = intRef(cached)
	}
	if thoughts > 0 {
		gi.ThoughtsTokens = intRef(thoughts)
	}
	return gi, latestModel
}

func readGeminiTranscriptSessionID(projectDirID string) string {
	transcriptPath := latestGeminiTranscriptPath(projectDirID)
	if transcriptPath == "" {
		return ""
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var ev struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if sessionID := strings.TrimSpace(ev.SessionID); sessionID != "" {
			return sessionID
		}
	}
	return ""
}

func latestGeminiTranscriptPath(projectDirID string) string {
	if projectDirID == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	chatsDir := filepath.Join(home, ".gemini", "tmp", "gemini-cli-project-"+projectDirID, "chats")
	entries, err := os.ReadDir(chatsDir)
	if err != nil || len(entries) == 0 {
		return ""
	}

	type candidate struct {
		path string
		mod  time.Time
	}
	cands := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, candidate{path: filepath.Join(chatsDir, name), mod: info.ModTime()})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	return cands[0].path
}

func intRef(v int) *int { return &v }
