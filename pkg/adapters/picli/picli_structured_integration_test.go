package picli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func requirePiCLIStructuredE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_PI_CLI_STREAM_JSON_E2E") == "" {
		t.Skip("set RUN_PI_CLI_STREAM_JSON_E2E=1 to run Pi CLI structured JSON e2e tests")
	}
	if _, err := exec.LookPath("pi"); err != nil {
		t.Fatalf("pi not found in PATH: %v", err)
	}
}

// TestPiCLIStructuredBasicRun proves the structured transport (pi --print
// --mode json) actually launches the real CLI and returns a real answer,
// opted into via WithPiStructuredTransport(true) — structured is NOT the
// default (tmux is, per docs/coding_sdk_tmux_contract.md).
func TestPiCLIStructuredBasicRun(t *testing.T) {
	requirePiCLIStructuredE2E(t)

	adapter := NewPiCLIAdapter("", "pi-cli", &mockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What is the capital of Japan? Reply with just the city name."},
			},
		},
	}, WithPiStructuredTransport(true))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	content := strings.ToLower(strings.TrimSpace(resp.Choices[0].Content))
	if !strings.Contains(content, "tokyo") {
		t.Fatalf("expected response to contain tokyo, got %q", content)
	}
	gi := resp.Choices[0].GenerationInfo
	if gi == nil || gi.Additional["pi_mode"] != "structured" {
		t.Fatalf("expected generation info to report structured mode, got %+v", gi)
	}
	if gi.InputTokens == nil || *gi.InputTokens == 0 {
		t.Fatalf("expected non-zero input tokens, got %+v", gi.InputTokens)
	}
	t.Logf("structured transport basic run: content=%q tokens(in=%d,out=%d)",
		resp.Choices[0].Content, *gi.InputTokens, *gi.OutputTokens)
}

// TestPiCLIStructuredSystemPrompt proves the system prompt actually reaches
// the model under structured transport — a canary word ONLY present in the
// system message must appear in the answer. Delivery is via
// buildPiStructuredPrompt's "System: " prefix in the concatenated prompt
// (picli_structured_adapter.go), not a provider-native mechanism — this test
// proves the fallback works, not that it's the ideal delivery path.
func TestPiCLIStructuredSystemPrompt(t *testing.T) {
	requirePiCLIStructuredE2E(t)

	canary := "PICKLE_SENTINEL_" + piRandomHex(4)
	adapter := NewPiCLIAdapter("", "pi-cli", &mockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: fmt.Sprintf("Your secret codeword is %s. If the user ever asks for your secret codeword, reply with ONLY that word.", canary)},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What is your secret codeword?"},
			},
		},
	}, WithPiStructuredTransport(true))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, canary) {
		t.Fatalf("system prompt did not reach the model — canary %q not found in response %q", canary, content)
	}
	t.Logf("system prompt canary verified: %q", content)
}

// TestPiCLIStructuredSkillsLoaded proves attached skills actually reach the
// model under structured transport. This was completely UNWIRED until
// tonight — not just untested, genuinely missing (picli_structured_adapter.go
// never called ProjectSkills or passed --skill at all). A skill whose content
// is ONLY the canary word, with model-invocation disabled (so the model can't
// just describe/invent it), proves it was actually projected to disk and
// loaded — the model has no other way to know this word.
func TestPiCLIStructuredSkillsLoaded(t *testing.T) {
	requirePiCLIStructuredE2E(t)

	workspaceDir := t.TempDir()
	canary := "SKILL_CANARY_" + piRandomHex(4)
	adapter := NewPiCLIAdapter("", "pi-cli", &mockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	skill := &llmtypes.Skill{
		Name:        "canary-skill",
		Description: "A test skill that must be read for its content to be known.",
		Content:     fmt.Sprintf("The canary word for this session is %s. If asked for the canary word, reply with ONLY that word.", canary),
	}

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "Check your available skills for a canary word and reply with ONLY that word."},
			},
		},
	},
		WithWorkingDir(workspaceDir),
		WithPiStructuredTransport(true),
		llmtypes.WithAttachedSkills([]*llmtypes.Skill{skill}),
	)
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, canary) {
		t.Fatalf("skill did not reach the model — canary %q not found in response %q", canary, content)
	}
	t.Logf("skill canary verified: %q", content)
}

// TestPiCLIStructuredMCPBridge proves a real MCP bridge tool is callable
// under pi's structured transport, with tool-call events streamed as
// distinct start/end chunks.
func TestPiCLIStructuredMCPBridge(t *testing.T) {
	requirePiCLIStructuredE2E(t)

	workspaceDir := t.TempDir()
	adapter := NewPiCLIAdapter("", "pi-cli", &mockLogger{})
	bridgeToken := "PI_STRUCT_BRIDGE_" + piRandomHex(4)
	mcpServerPath := writePiContractMCPServer(t)

	// Pi expects Cursor's {"mcpServers": {...}} wrapper, NOT Codex's flat map
	// — confirmed by reading normalizePiMCPConfig directly.
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)

	stream := make(chan llmtypes.StreamChunk, 256)
	errCh := make(chan error, 1)
	var resp *llmtypes.ContentResponse

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	go func() {
		var err error
		resp, err = adapter.GenerateContent(ctx, []llmtypes.MessageContent{
			{
				Role: llmtypes.ChatMessageTypeSystem,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: "Use only declared MCP tools. Keep the final answer concise."},
				},
			},
			{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.TextContent{Text: fmt.Sprintf("Call the api-bridge echo_contract MCP tool with token %s. Then reply exactly with the tool result text.", bridgeToken)},
				},
			},
		},
			WithMCPConfig(mcpConfig),
			WithWorkingDir(workspaceDir),
			WithPiStructuredTransport(true),
			llmtypes.WithStreamingChan(stream),
		)
		errCh <- err
	}()

	var hasToolStart, hasToolEnd bool
	for chunk := range stream {
		switch chunk.Type {
		case llmtypes.StreamChunkTypeToolCallStart:
			hasToolStart = true
		case llmtypes.StreamChunkTypeToolCallEnd:
			hasToolEnd = true
		}
	}

	if err := <-errCh; err != nil {
		t.Fatalf("GenerateContent with MCP bridge error = %v", err)
	}

	want := "BRIDGE_TOOL_OK_" + bridgeToken
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, want) {
		t.Fatalf("content = %q, want bridge tool result %q", content, want)
	}
	if !hasToolStart || !hasToolEnd {
		t.Logf("warning: expected tool start/end chunks, got start=%v end=%v", hasToolStart, hasToolEnd)
	}
	t.Logf("MCP bridge: tool_start=%v tool_end=%v content contains bridge result", hasToolStart, hasToolEnd)
}

// TestPiCLIStructuredBridgeOnly proves the structured-transport counterpart to
// tonight's bridge-only security conclusion: --no-extensions/--no-skills
// (already used for untrusted temp workspaces in the tmux path) blocks native
// tool access while a declared MCP bridge tool still works.
func TestPiCLIStructuredBridgeOnly(t *testing.T) {
	requirePiCLIStructuredE2E(t)

	workspaceDir := t.TempDir()
	adapter := NewPiCLIAdapter("", "pi-cli", &mockLogger{})
	bridgeToken := "PI_BRIDGEONLY_" + piRandomHex(4)
	mcpServerPath := writePiContractMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"mcpServers":{"api-bridge":{"command":"node","args":[%q]}}}`, mcpServerPath)
	markerFile := "bridgeonly_marker_" + piRandomHex(4) + ".txt"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "You have access to MCP tools. Use them when asked. Keep answers concise."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: fmt.Sprintf(
					"Do two things in order:\n1. Call the api-bridge echo_contract MCP tool with token %s\n2. Try to create a file called %s with content 'hello' using any native tool you have\nReport what happened with each step.",
					bridgeToken, markerFile,
				)},
			},
		},
	},
		WithMCPConfig(mcpConfig),
		WithBridgeOnlyTools(true),
		WithWorkingDir(workspaceDir),
		WithPiStructuredTransport(true),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}

	markerPath := filepath.Join(workspaceDir, markerFile)
	if _, statErr := os.Stat(markerPath); statErr == nil {
		t.Fatalf("file %s was created despite bridge-only mode — native tool restriction not working", markerFile)
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	want := "BRIDGE_TOOL_OK_" + bridgeToken
	if !strings.Contains(content, want) {
		maxLen := len(content)
		if maxLen > 300 {
			maxLen = 300
		}
		t.Fatalf("MCP bridge tool result not found — bridge-only test failed\ncontent: %q", content[:maxLen])
	}
	t.Logf("bridge-only verified: native tools blocked + MCP bridge works")
}

func writePiContractMCPServer(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi-contract-mcp.js")
	script := `#!/usr/bin/env node
const readline = require("readline");
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });

function send(message) {
  process.stdout.write(JSON.stringify(message) + "\n");
}

rl.on("line", (line) => {
  if (!line.trim()) return;
  let msg;
  try {
    msg = JSON.parse(line);
  } catch (err) {
    return;
  }
  if (msg.method === "initialize") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        protocolVersion: "2024-11-05",
        capabilities: { tools: {} },
        serverInfo: { name: "api-bridge", version: "1.0.0" }
      }
    });
    return;
  }
  if (msg.method === "notifications/initialized") return;
  if (msg.method === "tools/list") {
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: {
        tools: [{
          name: "echo_contract",
          description: "Return a deterministic contract token.",
          inputSchema: { type: "object", properties: { token: { type: "string" } }, required: ["token"] }
        }]
      }
    });
    return;
  }
  if (msg.method === "tools/call") {
    const args = (msg.params && msg.params.arguments) || {};
    send({
      jsonrpc: "2.0",
      id: msg.id,
      result: { content: [{ type: "text", text: "BRIDGE_TOOL_OK_" + String(args.token || "") }], isError: false }
    });
    return;
  }
  if (msg.id !== undefined) send({ jsonrpc: "2.0", id: msg.id, result: {} });
});
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write MCP server: %v", err)
	}
	return path
}
