package cursorcli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/agentreview"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func writeWorkbenchMCPServer(t *testing.T, outDir, codeWord string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workbench-mcp.js")
	script := fmt.Sprintf(`#!/usr/bin/env node
const fs = require("fs");
const path = require("path");
const readline = require("readline");
const OUT = %q;
const CODEWORD = %q;
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
function send(m){ process.stdout.write(JSON.stringify(m) + "\n"); }
rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg; try { msg = JSON.parse(line); } catch (e) { return; }
  if (msg.method === "initialize") { send({jsonrpc:"2.0",id:msg.id,result:{protocolVersion:"2024-11-05",capabilities:{tools:{}},serverInfo:{name:"workbench",version:"1.0.0"}}}); return; }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    send({jsonrpc:"2.0",id:msg.id,result:{tools:[
      {name:"web_search",description:"Search the web for a query and return the top result.",inputSchema:{type:"object",properties:{query:{type:"string"}},required:["query"]}},
      {name:"write_file",description:"Write text content to a file by name.",inputSchema:{type:"object",properties:{name:{type:"string"},content:{type:"string"}},required:["name","content"]}},
      {name:"read_file",description:"Read a file's text content by name.",inputSchema:{type:"object",properties:{name:{type:"string"}},required:["name"]}}
    ]}}); return;
  }
  if (msg.method === "tools/call") {
    const name = msg.params && msg.params.name;
    const args = (msg.params && msg.params.arguments) || {};
    let text = "";
    try {
      if (name === "web_search") { text = "Top result: the project code word is " + CODEWORD + "."; }
      else if (name === "write_file") { fs.writeFileSync(path.join(OUT, String(args.name||"")), String(args.content||"")); text = "WROTE " + args.name; }
      else if (name === "read_file") { text = fs.readFileSync(path.join(OUT, String(args.name||"")), "utf8"); }
      else { text = "unknown tool"; }
      send({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text}],isError:false}});
    } catch (e) {
      send({jsonrpc:"2.0",id:msg.id,result:{content:[{type:"text",text:"ERR " + e.message}],isError:true}});
    }
    return;
  }
  if (msg.id !== undefined) send({jsonrpc:"2.0",id:msg.id,result:{}});
});
`, outDir, codeWord)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write workbench MCP server: %v", err)
	}
	return path
}

const workbenchRealWorldTask = "You have three tools from the 'workbench' MCP server: web_search, write_file, read_file. " +
	"Do these steps in order, writing one short sentence of narration BEFORE each tool call:\n" +
	"1. Use web_search with the query \"project code word\" to find the code word.\n" +
	"2. Use write_file to save ONLY that code word into a file named exactly result.txt.\n" +
	"3. Use read_file on result.txt to confirm what was saved.\n" +
	"Finally, reply with the code word on its own line."

type cursorStreamCapture struct {
	content       strings.Builder
	contentTexts  []string
	contentChunks int
	toolStarts    int
	toolNames     []string
	order         []string
	terminalOnly  int
}

func collectCursorTranscriptStream(streamChan <-chan llmtypes.StreamChunk) <-chan cursorStreamCapture {
	done := make(chan cursorStreamCapture, 1)
	go func() {
		var cap cursorStreamCapture
		for chunk := range streamChan {
			fromTranscript := chunk.Metadata != nil && chunk.Metadata["cursor_cli_stream_source"] == "transcript"
			switch chunk.Type {
			case llmtypes.StreamChunkTypeContent:
				if fromTranscript {
					cap.content.WriteString(chunk.Content)
					cap.contentTexts = append(cap.contentTexts, chunk.Content)
					cap.contentChunks++
					cap.order = append(cap.order, "text")
				}
			case llmtypes.StreamChunkTypeToolCallStart:
				if fromTranscript {
					cap.toolStarts++
					cap.toolNames = append(cap.toolNames, chunk.ToolName)
					cap.order = append(cap.order, "tool")
				}
			case llmtypes.StreamChunkTypeTerminal:
				cap.terminalOnly++
			}
		}
		done <- cap
	}()
	return done
}

func distinctToolNames(names []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestCursorCLITranscriptStreamingRealWorldLive is the realistic Cursor P0 test:
// a real Cursor tmux turn, MCP-only, driving search -> edit-a-file -> read-back
// over a real MCP server, with CURSOR_CLI_STREAM_TRANSCRIPT=1. Cursor commits its
// store.db asynchronously so streaming is laggier than the JSONL adapters; the
// tailer's final flush catches late commits. Asserts real work (result.txt on
// disk) + that structured chunks streamed, and records output for agent review.
func TestCursorCLITranscriptStreamingRealWorldLive(t *testing.T) {
	requireRealCursorCLIE2E(t)
	t.Setenv(EnvCursorInteractiveStreamTranscript, "1")
	t.Cleanup(func() { _ = CleanupCursorCLIInteractiveSessions(context.Background()) })

	adapter := NewCursorCLIAdapter("", "cursor-cli", &MockLogger{})
	workDir := t.TempDir()
	outDir := t.TempDir()
	codeWord := "ZEBRA_" + cursorRandomHex(4)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"workbench":{"type":"stdio","command":"node","args":[%q]}}}`, writeWorkbenchMCPServer(t, outDir, codeWord))
	preApproveCursorMCP(t, workDir, mcpConfig, "workbench")

	streamChan := make(chan llmtypes.StreamChunk, 2048)
	captureDone := collectCursorTranscriptStream(streamChan)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, workbenchRealWorldTask)},
		WithInteractiveSessionID("cursor-realworld-stream-"+cursorRandomHex(4)),
		WithPersistentInteractiveSession(true),
		WithWorkingDir(workDir),
		WithMCPConfig(mcpConfig),
		WithApproveMCPs(),
		WithForce(),
		llmtypes.WithStreamingChan(streamChan),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	capture := <-captureDone

	final := ""
	if len(resp.Choices) == 1 {
		final = strings.TrimSpace(resp.Choices[0].Content)
	}
	t.Logf("cursor realworld stream: %d content, %d tool start(s) %v, order=%v; streamed=%q final=%q",
		capture.contentChunks, capture.toolStarts, capture.toolNames, capture.order,
		strings.TrimSpace(capture.content.String()), final)

	wrote, readErr := os.ReadFile(filepath.Join(outDir, "result.txt"))
	if readErr != nil {
		t.Fatalf("result.txt was not written to disk (real file edit via bridge did not happen): %v", readErr)
	}
	if !strings.Contains(string(wrote), codeWord) {
		t.Fatalf("result.txt does not contain the searched code word %q; got %q", codeWord, string(wrote))
	}
	// Cursor's async store.db commit is laggy — require SOMETHING structured
	// streamed (content and/or a tool), not strict counts.
	if capture.contentChunks == 0 && capture.toolStarts == 0 {
		t.Fatalf("no transcript-sourced chunks streamed at all; order=%v", capture.order)
	}

	rec := agentreview.Write(t, "TestCursorCLITranscriptStreamingRealWorldLive",
		"Cursor MCP-only: web_search -> write_file (to disk) -> read_file, streamed from store.db",
		map[string]any{
			"content_chunks":   capture.contentTexts,
			"streamed_content": strings.TrimSpace(capture.content.String()),
			"stream_order":     capture.order,
			"tool_names":       capture.toolNames,
			"final":            final,
			"file_on_disk":     string(wrote),
		},
		map[string]any{"distinct_tools": sortedKeys(distinctToolNames(capture.toolNames))},
	)
	agentreview.RequireReviewed(t, rec)
}
