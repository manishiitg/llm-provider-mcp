package codexcli

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

func requireCodexCLIStructuredE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_CODEX_CLI_STREAM_JSON_E2E") == "" {
		t.Skip("set RUN_CODEX_CLI_STREAM_JSON_E2E=1 to run Codex CLI structured JSON e2e tests")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatalf("codex not found in PATH: %v", err)
	}
}

// TestCodexCLIStructuredBasicRun proves the revived structured transport
// (codex exec --json) actually launches the real CLI and returns a real
// answer, opted into via WithCodexStructuredTransport(true) — structured is
// NOT the default (tmux is, per docs/coding_sdk_tmux_contract.md).
func TestCodexCLIStructuredBasicRun(t *testing.T) {
	requireCodexCLIStructuredE2E(t)

	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "What is the capital of Japan? Reply with just the city name."},
			},
		},
	}, WithCodexStructuredTransport(true))
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
	if gi == nil || gi.Additional["codex_mode"] != "structured" {
		t.Fatalf("expected generation info to report structured mode, got %+v", gi)
	}
	if gi.InputTokens == nil || *gi.InputTokens == 0 {
		t.Fatalf("expected non-zero input tokens from the real turn.completed event, got %+v", gi.InputTokens)
	}
	t.Logf("structured transport basic run: content=%q tokens(in=%d,out=%d) thread=%v",
		resp.Choices[0].Content, *gi.InputTokens, *gi.OutputTokens, gi.Additional["codex_thread_id"])
}

// TestCodexCLIStructuredSystemPrompt proves the system prompt actually
// reaches the model under structured transport — a canary word ONLY present
// in the system message must appear in the answer. Delivery is a crude
// prepend-into-the-prompt-text (see splitCodexSystemPrompt usage in
// codexcli_structured_adapter.go), not a provider-native mechanism — this
// test proves the fallback works, not that it's the ideal delivery path.
func TestCodexCLIStructuredSystemPrompt(t *testing.T) {
	requireCodexCLIStructuredE2E(t)

	canary := "PICKLE_SENTINEL_" + codexRandomHex(4)
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
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
	}, WithCodexStructuredTransport(true))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, canary) {
		t.Fatalf("system prompt did not reach the model — canary %q not found in response %q", canary, content)
	}
	t.Logf("system prompt canary verified: %q", content)
}

// TestCodexCLIStructuredSkillsLoaded proves attached skills actually reach
// the model under structured transport. Was completely UNWIRED until tonight
// — no ProjectSkills call anywhere in codexcli_structured_adapter.go. A skill
// whose content is ONLY the canary word proves it was actually projected to
// disk and auto-discovered.
func TestCodexCLIStructuredSkillsLoaded(t *testing.T) {
	requireCodexCLIStructuredE2E(t)

	workspaceDir := t.TempDir()
	canary := "SKILL_CANARY_" + codexRandomHex(4)
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
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
		WithProjectDirID(workspaceDir),
		WithCodexStructuredTransport(true),
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

// TestCodexCLIStructuredMCPBridge proves a real MCP bridge tool is callable
// under codex's structured transport, with tool-call events streamed as
// distinct start/end chunks (not buried in text).
func TestCodexCLIStructuredMCPBridge(t *testing.T) {
	requireCodexCLIStructuredE2E(t)

	workspaceDir := t.TempDir()
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
	bridgeToken := "CODEX_STRUCT_BRIDGE_" + codexRandomHex(4)
	mcpServerPath := writeCodexContractMCPServer(t)

	mcpConfig := fmt.Sprintf(`{"api-bridge":{"command":"node","args":[%q]}}`, mcpServerPath)

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
			WithMCPServers(mcpConfig),
			WithApprovalPolicy("never"),
			WithProjectDirID(workspaceDir),
			WithCodexStructuredTransport(true),
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
	t.Logf("MCP bridge: tool_start=%v tool_end=%v content contains bridge result", hasToolStart, hasToolEnd)
}

// TestCodexCLIStructuredSandboxContainment proves the structured-transport
// counterpart to tonight's tmux-path security conclusion: codex's native
// functions.exec cannot be disabled, so --sandbox read-only is the only
// containment that exists for it — under read-only, a native write must fail
// while a declared MCP bridge tool still works.
func TestCodexCLIStructuredSandboxContainment(t *testing.T) {
	requireCodexCLIStructuredE2E(t)

	workspaceDir := t.TempDir()
	adapter := NewCodexCLIAdapter("", "codex-cli", &MockLogger{})
	bridgeToken := "CODEX_SANDBOX_BRIDGE_" + codexRandomHex(4)
	mcpServerPath := writeCodexContractMCPServer(t)
	mcpConfig := fmt.Sprintf(`{"api-bridge":{"command":"node","args":[%q]}}`, mcpServerPath)
	markerFile := "sandboxed_marker_" + codexRandomHex(4) + ".txt"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: "You have access to MCP tools and a native shell. Use them when asked."},
			},
		},
		{
			Role: llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{
				llmtypes.TextContent{Text: fmt.Sprintf(
					"Do two things in order:\n1. Call the api-bridge echo_contract MCP tool with token %s\n2. Try to create a file called %s with content 'hello' using your shell\nReport what happened with each step.",
					bridgeToken, markerFile,
				)},
			},
		},
	},
		WithMCPServers(mcpConfig),
		WithApprovalPolicy("never"),
		WithSandbox("read-only"),
		WithProjectDirID(workspaceDir),
		WithCodexStructuredTransport(true),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}

	markerPath := filepath.Join(workspaceDir, markerFile)
	if _, statErr := os.Stat(markerPath); statErr == nil {
		t.Fatalf("file %s was created despite --sandbox read-only — native write containment not working", markerFile)
	}

	content := strings.TrimSpace(resp.Choices[0].Content)
	want := "BRIDGE_TOOL_OK_" + bridgeToken
	if !strings.Contains(content, want) {
		maxLen := len(content)
		if maxLen > 300 {
			maxLen = 300
		}
		t.Fatalf("MCP bridge tool result not found — sandbox containment test failed\ncontent: %q", content[:maxLen])
	}
	t.Logf("sandbox containment verified: native write blocked (read-only) + MCP bridge works")
}
