package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// Claude stores children beside the exact parent's transcript, under that
// session's subagents directory. Never search unrelated projects for children.
func readClaudeNativeSubagents(sessionID, workingDir, accountHome string) []llmtypes.NativeSubagentTranscript {
	parentPath, err := resolveClaudeTranscriptPath(sessionID, workingDir, false, accountHome)
	if err != nil || parentPath == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(parentPath, ".jsonl"), "subagents", "agent-*.jsonl"))
	var out []llmtypes.NativeSubagentTranscript
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		messages := parseClaudeTranscriptMessages(file, time.Time{}, true)
		_ = file.Close()
		file, err = os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 16*1024*1024)
		started := time.Time{}
		status := "running"
		for scanner.Scan() {
			var row struct {
				Type      string    `json:"type"`
				Timestamp time.Time `json:"timestamp"`
				Message   struct {
					StopReason string `json:"stop_reason"`
				} `json:"message"`
			}
			if json.Unmarshal(scanner.Bytes(), &row) != nil {
				continue
			}
			if started.IsZero() {
				started = row.Timestamp
			}
			if row.Type == "assistant" {
				if row.Message.StopReason == "end_turn" {
					status = "completed"
				} else {
					status = "running"
				}
			}
		}
		info, statErr := file.Stat()
		_ = file.Close()
		if statErr != nil {
			continue
		}
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		out = append(out, llmtypes.NativeSubagentTranscript{ID: id, ParentID: sessionID, Name: "Native agent " + strings.TrimPrefix(id, "agent-"), Provider: "claude-code", Status: status, StartedAt: started, UpdatedAt: info.ModTime(), Messages: messages})
	}
	return out
}
