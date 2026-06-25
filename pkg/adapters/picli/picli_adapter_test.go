package picli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/internal/shelllaunch"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type mockLogger struct{}

func (m *mockLogger) Debugf(format string, args ...interface{}) {}
func (m *mockLogger) Infof(format string, args ...interface{})  {}
func (m *mockLogger) Warnf(format string, args ...interface{})  {}
func (m *mockLogger) Errorf(format string, args ...interface{}) {}

func TestResolvePiProviderModel(t *testing.T) {
	tests := []struct {
		name             string
		modelID          string
		providerOverride string
		wantProvider     string
		wantModel        string
	}{
		{name: "default", wantProvider: "google", wantModel: "gemini-3.5-flash"},
		{name: "provider model", modelID: "google/gemini-3.5-flash", wantProvider: "google", wantModel: "gemini-3.5-flash"},
		{name: "override", modelID: "gemini-3.5-flash", providerOverride: "google-vertex", wantProvider: "google-vertex", wantModel: "gemini-3.5-flash"},
		{name: "pi cli alias", modelID: "pi-cli", wantProvider: "google", wantModel: "gemini-3.5-flash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProvider, gotModel := resolvePiProviderModel(tt.modelID, tt.providerOverride)
			if gotProvider != tt.wantProvider || gotModel != tt.wantModel {
				t.Fatalf("resolvePiProviderModel() = %q/%q, want %q/%q", gotProvider, gotModel, tt.wantProvider, tt.wantModel)
			}
		})
	}
}

func TestPiMarkerParserAggregatesTextDeltas(t *testing.T) {
	dir := t.TempDir()
	markerPath := dir + "/markers.jsonl"
	body := strings.Join([]string{
		`{"type":"message_update","updateType":"text_delta","delta":"hello "}`,
		`{"type":"tool_execution_start","toolCallId":"tool1","toolName":"bash"}`,
		`{"type":"tool_execution_end","toolCallId":"tool1","toolName":"bash","isError":false}`,
		`{"type":"message_update","updateType":"text_delta","delta":"world"}`,
		`{"type":"agent_end"}`,
		"",
	}, "\n")
	if err := os.WriteFile(markerPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	session := &piInteractiveSession{
		tmuxSessionName: "missing-session",
		markerPath:      markerPath,
		modelID:         "google/gemini-3.5-flash",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream := make(chan llmtypes.StreamChunk, 8)
	content, err := waitForPiInteractiveResponse(ctx, session, 0, stream)
	if err != nil {
		t.Fatalf("waitForPiInteractiveResponse() error = %v", err)
	}
	if content != "hello world" {
		t.Fatalf("content = %q, want hello world", content)
	}
	close(stream)
	var sawToolStart, sawToolEnd bool
	for chunk := range stream {
		if chunk.Type == llmtypes.StreamChunkTypeToolCallStart && chunk.ToolName == "bash" {
			sawToolStart = true
		}
		if chunk.Type == llmtypes.StreamChunkTypeToolCallEnd && chunk.ToolName == "bash" {
			sawToolEnd = true
		}
	}
	if !sawToolStart || !sawToolEnd {
		t.Fatalf("tool chunks start=%v end=%v, want both", sawToolStart, sawToolEnd)
	}
}

func TestPiLaunchArgsAddsMCPAdapterAndBridgeOnly(t *testing.T) {
	t.Setenv(EnvPiStatuslineExtension, "")
	sessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", sessionDir)
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"node","args":["server.js"]}}}`)(opts)
	WithBridgeOnlyTools(true)(opts)

	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})
	args, env, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
	if err != nil {
		t.Fatalf("piLaunchArgs() error = %v", err)
	}
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"--no-extensions",
		"-e\x00/tmp/marker.ts",
		"-e\x00npm:@narumitw/pi-statusline@0.8.0",
		"-e\x00npm:pi-mcp-adapter",
		"--session-id\x00mlp-pi-test-123",
		"--session-dir\x00" + sessionDir,
		"--no-builtin-tools",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args = %#v, want %q", args, want)
		}
	}
	if strings.Contains(joined, "--no-session") {
		t.Fatalf("args = %#v, must not include --no-session when native resume is enabled", args)
	}
	if got := strings.Join(env, "\n"); !strings.Contains(got, "MLP_PI_MARKER_FILE=/tmp/markers.jsonl") {
		t.Fatalf("env = %#v, want marker file", env)
	}
	if got := strings.Join(env, "\n"); !strings.Contains(got, "PI_CODING_AGENT_SESSION_DIR="+sessionDir) {
		t.Fatalf("env = %#v, want Pi session dir", env)
	}
}

func TestPiLaunchArgsStatuslineExtensionCanBeOverriddenOrDisabled(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())
	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})

	t.Run("call option override", func(t *testing.T) {
		opts := &llmtypes.CallOptions{}
		WithStatuslineExtension("npm:@example/statusline@1.2.3")(opts)
		args, _, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
		if err != nil {
			t.Fatalf("piLaunchArgs() error = %v", err)
		}
		if joined := strings.Join(args, "\x00"); !strings.Contains(joined, "-e\x00npm:@example/statusline@1.2.3") {
			t.Fatalf("args = %#v, want statusline override", args)
		}
	})

	t.Run("env override", func(t *testing.T) {
		t.Setenv(EnvPiStatuslineExtension, "npm:@example/env-statusline@2.0.0")
		args, _, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", nil)
		if err != nil {
			t.Fatalf("piLaunchArgs() error = %v", err)
		}
		if joined := strings.Join(args, "\x00"); !strings.Contains(joined, "-e\x00npm:@example/env-statusline@2.0.0") {
			t.Fatalf("args = %#v, want env statusline override", args)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		opts := &llmtypes.CallOptions{}
		WithStatuslineExtension("off")(opts)
		args, _, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
		if err != nil {
			t.Fatalf("piLaunchArgs() error = %v", err)
		}
		if joined := strings.Join(args, "\x00"); strings.Contains(joined, "statusline") {
			t.Fatalf("args = %#v, statusline extension should be disabled", args)
		}
	})
}

func TestPiLaunchArgsDerivesSessionScopedMCPEnv(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{
		"mcpServers": {
			"api-bridge": {
				"command": "mcpbridge",
				"env": {
					"MCP_API_URL": "http://127.0.0.1:18743",
					"MCP_API_TOKEN": "token-123",
					"MCP_SESSION_ID": "step-session-123",
					"MCP_VIRTUAL_SCOPE_ID": "step-session-123:vt:scope",
					"MCP_TOOLS": "not-exported"
				}
			}
		}
	}`)(opts)
	WithBridgeOnlyTools(true)(opts)

	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})
	_, env, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
	if err != nil {
		t.Fatalf("piLaunchArgs() error = %v", err)
	}

	byKey := map[string]string{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			byKey[key] = value
		}
	}
	if got := byKey["MCP_API_URL"]; got != "http://127.0.0.1:18743/s/step-session-123" {
		t.Fatalf("MCP_API_URL = %q, want session-scoped URL", got)
	}
	if got := byKey["MCP_CUSTOM"]; got != "http://127.0.0.1:18743/s/step-session-123/tools/custom" {
		t.Fatalf("MCP_CUSTOM = %q, want session-scoped custom endpoint", got)
	}
	if got := byKey["MCP_AUTH"]; got != "Authorization: Bearer token-123" {
		t.Fatalf("MCP_AUTH = %q, want bearer header", got)
	}
	if _, ok := byKey["MCP_TOOLS"]; ok {
		t.Fatal("MCP_TOOLS must not be exported into the Pi shell environment")
	}
	if got := byKey["MCP_VIRTUAL_SCOPE_ID"]; got != "step-session-123:vt:scope" {
		t.Fatalf("MCP_VIRTUAL_SCOPE_ID = %q, want bridge virtual scope", got)
	}
}

func TestPiLaunchArgsRejectsBridgeOnlyWithoutMCPConfig(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())
	opts := &llmtypes.CallOptions{}
	WithBridgeOnlyTools(true)(opts)

	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})
	if _, _, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts); err == nil {
		t.Fatal("piLaunchArgs() error = nil, want bridge-only without MCP config to fail")
	}
}

func TestPiLaunchArgsRejectsInvalidNativeSessionID(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())
	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})
	if _, _, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/markers.jsonl", "", "-bad-", nil); err == nil {
		t.Fatal("piLaunchArgs() error = nil, want invalid native session id to fail")
	}
}

func TestWritePiLaunchScriptKeepsTmuxCommandShort(t *testing.T) {
	longPrompt := strings.Repeat("workflow-system-prompt ", 4096)
	args := []string{"pi", "--provider", "google", "--append-system-prompt", longPrompt}
	scriptPath := filepath.Join(t.TempDir(), "launch-pi.sh")
	if err := writePiLaunchScript(scriptPath, args); err != nil {
		t.Fatalf("writePiLaunchScript() error = %v", err)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat launch script: %v", err)
	}
	if info.Mode()&0o100 == 0 {
		t.Fatalf("launch script mode = %v, want executable", info.Mode())
	}
	body, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read launch script: %v", err)
	}
	if !strings.Contains(string(body), longPrompt) {
		t.Fatal("launch script should carry the long prompt outside the tmux command")
	}
	tmuxCommand := shelllaunch.Command([]string{scriptPath}, t.TempDir())
	if strings.Contains(tmuxCommand, longPrompt) {
		t.Fatal("tmux command should reference the launch script, not inline the long prompt")
	}
}

func TestPiSessionHandleUsesNativeSessionID(t *testing.T) {
	session := &piInteractiveSession{
		ownerSessionID:  "app-owner-session",
		nativeSessionID: "mlp-pi-native-session",
		tmuxSessionName: "mlp-pi-cli-int-test",
		workingDir:      "/tmp/pi-work",
		modelID:         "google/gemini-3.5-flash",
	}

	handle := piSessionHandle(session, llmtypes.CodingProviderSessionStatusIdle)
	if handle.NativeSessionID != "mlp-pi-native-session" {
		t.Fatalf("NativeSessionID = %q, want Pi native session id", handle.NativeSessionID)
	}
	if handle.NativeSessionID == session.ownerSessionID {
		t.Fatalf("NativeSessionID must not reuse app owner session id")
	}
	additional := piResponseAdditional(session, true)
	if additional["pi_session_id"] != "mlp-pi-native-session" {
		t.Fatalf("pi_session_id = %v, want native session id", additional["pi_session_id"])
	}
}

func TestPreparePiProjectFilesWritesMCPConfigAndCleansUp(t *testing.T) {
	workDir := t.TempDir()
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"node","args":["server.js"]}}}`)(opts)

	cleanup, err := preparePiProjectFiles(workDir, opts)
	if err != nil {
		t.Fatalf("preparePiProjectFiles() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("preparePiProjectFiles() cleanup = nil, want cleanup")
	}

	mcpPath := filepath.Join(workDir, ".pi", "mcp.json")
	body, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read Pi MCP config: %v", err)
	}
	var config struct {
		MCPServers map[string]struct {
			Command     string `json:"command"`
			DirectTools bool   `json:"directTools"`
			Lifecycle   string `json:"lifecycle"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatalf("Pi MCP config invalid JSON: %v\n%s", err, body)
	}
	bridge := config.MCPServers["api-bridge"]
	if bridge.Command != "node" || !bridge.DirectTools || bridge.Lifecycle != "keep-alive" {
		t.Fatalf("api-bridge config = %#v, want node directTools keep-alive", bridge)
	}

	cleanup()
	if _, err := os.Stat(mcpPath); !os.IsNotExist(err) {
		t.Fatalf("mcp.json should be removed after cleanup, err=%v", err)
	}
}

func TestPreparePiProjectFilesRestoresExistingMCPConfig(t *testing.T) {
	workDir := t.TempDir()
	piDir := filepath.Join(workDir, ".pi")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(piDir, "mcp.json")
	original := []byte(`{"mcpServers":{"existing":{"command":"old"}}}` + "\n")
	if err := os.WriteFile(mcpPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"new"}}}`)(opts)

	cleanup, err := preparePiProjectFiles(workDir, opts)
	if err != nil {
		t.Fatalf("preparePiProjectFiles() error = %v", err)
	}
	active, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read active mcp.json: %v", err)
	}
	if !strings.Contains(string(active), `"api-bridge"`) {
		t.Fatalf("active mcp.json = %s, want session bridge config", active)
	}

	cleanup()
	restored, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatalf("read restored mcp.json: %v", err)
	}
	if string(restored) != string(original) {
		t.Fatalf("restored mcp.json = %q, want original %q", restored, original)
	}
}

func TestPiWorkspaceMCPConfigLeaseRejectsConcurrentConflicts(t *testing.T) {
	workDir := t.TempDir()
	first := &piInteractiveSession{tmuxSessionName: "pi-a"}
	firstConfig := `{"mcpServers":{"api-bridge":{"command":"alpha"}}}`
	releaseFirst, err := acquirePiWorkspaceMCPConfigLease(workDir, firstConfig, first)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer releaseFirst()

	second := &piInteractiveSession{tmuxSessionName: "pi-b"}
	secondConfig := `{"mcpServers":{"api-bridge":{"command":"beta"}}}`
	if _, err := acquirePiWorkspaceMCPConfigLease(workDir, secondConfig, second); err == nil {
		t.Fatal("second conflicting lease error = nil, want conflict")
	}

	same := &piInteractiveSession{tmuxSessionName: "pi-c"}
	releaseSame, err := acquirePiWorkspaceMCPConfigLease(workDir, firstConfig, same)
	if err != nil {
		t.Fatalf("same-config lease error = %v", err)
	}
	releaseSame()

	releaseFirst()
	releaseSecond, err := acquirePiWorkspaceMCPConfigLease(workDir, secondConfig, second)
	if err != nil {
		t.Fatalf("second lease after release error = %v", err)
	}
	releaseSecond()
}

func TestPiCLITmuxGeminiMarkerE2E(t *testing.T) {
	if os.Getenv("RUN_PI_CLI_TMUX_E2E") != "1" {
		t.Skip("set RUN_PI_CLI_TMUX_E2E=1 to run real Pi CLI tmux integration test")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skipf("tmux not available: %v", err)
	}
	if _, _, err := piCommandPrefix(); err != nil {
		t.Skip(err)
	}
	apiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY is required for real Pi CLI Gemini test")
	}

	adapter := NewPiCLIAdapter(apiKey, "google/gemini-3.5-flash", &mockLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = CleanupPiCLIInteractiveSessions(context.Background()) })

	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, "Reply exactly: pi adapter ok"),
	}, WithWorkingDir(t.TempDir()))
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 {
		t.Fatal("GenerateContent() returned no choices")
	}
	got := strings.TrimSpace(resp.Choices[0].Content)
	if got != "pi adapter ok" {
		t.Fatalf("content = %q, want %q", got, "pi adapter ok")
	}
	handle, ok := llmtypes.ExtractCodingProviderSessionHandle(resp.Choices[0].GenerationInfo)
	if !ok || handle.Provider != "pi-cli" || handle.TmuxSession == "" {
		t.Fatalf("missing Pi coding provider handle: %#v ok=%v", handle, ok)
	}
	ClosePiCLIInteractiveSessionByTmux(handle.TmuxSession, "test cleanup")
}
