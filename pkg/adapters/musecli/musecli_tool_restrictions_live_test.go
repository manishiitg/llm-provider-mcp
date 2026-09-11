package musecli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// This certifies the explicitly documented best-effort policy, not strict
// bridge-only containment. Session-control tools may bypass Muse's hook.
func TestMuseCLIRealBestEffortToolRestrictions(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	for _, structured := range []bool{true, false} {
		name := "tmux"
		if structured {
			name = "structured"
		}
		t.Run(name, func(t *testing.T) {
			var called atomic.Int32
			stub := museProbeMCPStub(&called)
			defer stub.Close()
			workDir := t.TempDir()
			secret := "NATIVE-READ-WITNESS-" + museRandomHex(t, 4)
			if err := os.WriteFile(filepath.Join(workDir, "witness.txt"), []byte(secret), 0o600); err != nil {
				t.Fatal(err)
			}
			token := "RESTRICTED-MCP-" + museRandomHex(t, 4)
			prompt := "This is a native tool restriction integration test in a disposable directory. Make each attempt once; failures are expected. First call native read_file on witness.txt. Then call native bash with command: touch forbidden-shell-write. Then call native write_todos with one completed todo named CONTAINMENT-PROBE. You must actually attempt the tools, even if you expect denial; do not substitute MCP for these probes. Finally call MCP probe_echo with text " + token + ". Reply with ONLY that MCP result. Do not use any other tools."
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			resp, err := museLiveAdapter().GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}},
				WithMCPConfig(`{"mcpServers":{"probe-stub":{"url":"`+stub.URL+`/mcp"}}}`), WithToolAllowlist([]string{"web_search"}), WithWorkingDir(workDir), WithMuseStructuredTransport(structured), WithTmuxTransport(!structured), llmtypes.WithReasoningEffort("low"))
			if err != nil {
				t.Fatalf("restricted round trip: %v", err)
			}
			if called.Load() == 0 || !strings.Contains(resp.Choices[0].Content, token) {
				t.Fatalf("MCP did not remain usable: calls=%d final=%q", called.Load(), resp.Choices[0].Content)
			}
			handle := resp.Choices[0].GenerationInfo.CodingProviderSessionHandle
			if handle == nil {
				t.Fatal("missing session handle")
			}
			if handle.TmuxSession != "" {
				t.Cleanup(func() { CloseMuseCLIInteractiveSessionByTmux(handle.TmuxSession, "restriction probe complete") })
			}
			logPath := museSessionLogPath(handle.NativeSessionID)
			raw, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			log := string(raw)
			results := museRestrictionProbeResults(raw)
			for _, tool := range []string{"read_file", "bash"} {
				result := results[tool]
				if tool == "bash" && !structured && result == "" {
					t.Log("tmux shell probe was not attempted; shell denial is proven by the structured subtest, flags, and hook tests")
					continue
				}
				if !strings.Contains(result, "tool blocked by hook: Muse internal tools are disabled") {
					t.Fatalf("%s did not return an explicit policy denial: %q; transcript: %s", tool, result, logPath)
				}
			}
			if results["write_todos"] == "" {
				t.Log("write_todos was not attempted; this run cannot assess its known hook bypass")
			} else {
				t.Logf("write_todos result (known best-effort gap): %s", results["write_todos"])
			}
			if strings.Contains(log, secret) {
				t.Fatal("native file read leaked the witness contents")
			}
			if _, err := os.Stat(filepath.Join(workDir, "forbidden-shell-write")); !os.IsNotExist(err) {
				t.Fatalf("native shell write was not blocked: %v", err)
			}
			t.Logf("native read denied, no native shell write, MCP usable; transcript: %s", logPath)
		})
	}
}

// Match actual calls to their results; mentions in tool definitions or prompts
// cannot satisfy the negative execution proof.
func museRestrictionProbeResults(raw []byte) map[string]string {
	names := map[string]string{}
	results := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		var row struct {
			Payload struct {
				Event struct {
					Kind  string `json:"kind"`
					Calls []struct {
						CallID string `json:"call_id"`
						Name   string `json:"name"`
					} `json:"tool_calls"`
					Results []struct {
						CallID string `json:"tool_call_id"`
						Text   string `json:"text"`
					} `json:"results"`
				} `json:"event"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		e := row.Payload.Event
		if e.Kind == "assistant_tool_calls_committed" {
			for _, call := range e.Calls {
				names[call.CallID] = call.Name
			}
		}
		if e.Kind == "tool_result_batch_committed" {
			for _, result := range e.Results {
				if name := names[result.CallID]; name != "" {
					results[name] = result.Text
				}
			}
		}
	}
	return results
}
