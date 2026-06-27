package picli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		{name: "openrouter nested model", modelID: "openrouter/minimax/minimax-m3-20260531", wantProvider: "openrouter", wantModel: "minimax/minimax-m3-20260531"},
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

func TestPiAPIKeyEnvUsesSelectedProvider(t *testing.T) {
	tests := []struct {
		provider string
		want     []string
	}{
		{provider: "google", want: []string{"GEMINI_API_KEY=test-key", "GOOGLE_API_KEY=test-key", "PI_API_KEY=test-key"}},
		{provider: "openrouter", want: []string{"OPENROUTER_API_KEY=test-key"}},
		{provider: "zai", want: []string{"ZAI_API_KEY=test-key"}},
		{provider: "zai-coding-cn", want: []string{"ZAI_CODING_CN_API_KEY=test-key"}},
		{provider: "kimi-coding", want: []string{"KIMI_API_KEY=test-key"}},
		{provider: "minimax-cn", want: []string{"MINIMAX_CN_API_KEY=test-key"}},
		{provider: "deepseek", want: []string{"DEEPSEEK_API_KEY=test-key"}},
		{provider: "opencode-go", want: []string{"OPENCODE_API_KEY=test-key"}},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			got := piAPIKeyEnv(tt.provider, "test-key")
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Fatalf("piAPIKeyEnv(%q) = %#v, want %#v", tt.provider, got, tt.want)
			}
		})
	}
}

func TestPiRedactArgsCoversProviderKeys(t *testing.T) {
	got := piRedactArgs([]string{
		"OPENROUTER_API_KEY=openrouter-secret",
		"ZAI_API_KEY=zai-secret",
		"ZAI_CODING_CN_API_KEY=zai-cn-secret",
		"KIMI_API_KEY=kimi-secret",
		"MINIMAX_CN_API_KEY=minimax-secret",
		"DEEPSEEK_API_KEY=deepseek-secret",
		"OPENCODE_API_KEY=opencode-secret",
	})
	for _, secret := range []string{"openrouter-secret", "zai-secret", "zai-cn-secret", "kimi-secret", "minimax-secret", "deepseek-secret", "opencode-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("piRedactArgs leaked %q in %q", secret, got)
		}
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
	t.Setenv(EnvPiNodeOptions, "")
	t.Setenv(EnvPiNodeMaxOldSpaceMB, "")
	t.Setenv(EnvPiMCPResultMaxChars, "1234")
	t.Setenv(EnvPiMCPResultMaxLines, "55")
	t.Setenv(EnvPiMCPResultMaxLineChars, "99")
	t.Setenv("NODE_OPTIONS", "")
	sessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", sessionDir)
	opts := &llmtypes.CallOptions{}
	WithMCPConfig(`{"mcpServers":{"api-bridge":{"command":"node","args":["server.js"]}}}`)(opts)
	WithBridgeOnlyTools(true)(opts)

	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})
	args, env, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/mcp-output-guard.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
	if err != nil {
		t.Fatalf("piLaunchArgs() error = %v", err)
	}
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"--no-extensions",
		"-e\x00/tmp/marker.ts",
		"-e\x00/tmp/mcp-output-guard.ts",
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
	if got := strings.Join(env, "\n"); !strings.Contains(got, "NODE_OPTIONS=--max-old-space-size=4096") {
		t.Fatalf("env = %#v, want Pi node old-space guard", env)
	}
	if got := strings.Join(env, "\n"); !strings.Contains(got, "PI_CLI_MCP_RESULT_MAX_CHARS=1234") || !strings.Contains(got, "PI_CLI_MCP_RESULT_MAX_LINES=55") || !strings.Contains(got, "PI_CLI_MCP_RESULT_MAX_LINE_CHARS=99") {
		t.Fatalf("env = %#v, want Pi MCP output guard limits", env)
	}
	if got := strings.Join(env, "\n"); !strings.Contains(got, "PI_STATUSLINE_PRESET=classic") {
		t.Fatalf("env = %#v, want classic Pi statusline preset", env)
	}
	if got := strings.Join(env, "\n"); !strings.Contains(got, "PI_CODING_AGENT_SESSION_DIR="+sessionDir) {
		t.Fatalf("env = %#v, want Pi session dir", env)
	}
}

func TestPiMCPOutputGuardExtensionSourceCoversMCPResults(t *testing.T) {
	source := piMCPOutputGuardExtensionSource()
	for _, want := range []string{
		`pi.on("tool_result"`,
		`hasOwn(details, "mcpResult")`,
		`toolName.startsWith("api_bridge_")`,
		`setToolsExpanded(false)`,
		`PI_CLI_MCP_RESULT_MAX_CHARS`,
		`PI_CLI_MCP_RESULT_MAX_LINES`,
		`PI_CLI_MCP_RESULT_MAX_LINE_CHARS`,
		`DEFAULT_MAX_RESULT_LINE_CHARS = 48`,
		`outputWrapped`,
		`mlp-mcp-output-guard`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("Pi MCP output guard source missing %q", want)
		}
	}
}

func TestPiMCPOutputGuardCanBeDisabled(t *testing.T) {
	t.Setenv(EnvPiMCPOutputGuard, "")
	if !piMCPOutputGuardEnabled() {
		t.Fatal("Pi MCP output guard should default to enabled")
	}
	t.Setenv(EnvPiMCPOutputGuard, "off")
	if piMCPOutputGuardEnabled() {
		t.Fatal("Pi MCP output guard should be disabled by env")
	}
}

func TestPiNodeOptionsEnv(t *testing.T) {
	t.Run("default old space guard", func(t *testing.T) {
		t.Setenv(EnvPiNodeOptions, "")
		t.Setenv(EnvPiNodeMaxOldSpaceMB, "")
		t.Setenv("NODE_OPTIONS", "")
		got := strings.Join(piNodeOptionsEnv(), "\n")
		if got != "NODE_OPTIONS=--max-old-space-size=4096" {
			t.Fatalf("piNodeOptionsEnv() = %q, want default max old space", got)
		}
	})

	t.Run("preserves existing options", func(t *testing.T) {
		t.Setenv(EnvPiNodeOptions, "")
		t.Setenv(EnvPiNodeMaxOldSpaceMB, "2048")
		t.Setenv("NODE_OPTIONS", "--trace-warnings")
		got := strings.Join(piNodeOptionsEnv(), "\n")
		if got != "NODE_OPTIONS=--trace-warnings --max-old-space-size=2048" {
			t.Fatalf("piNodeOptionsEnv() = %q, want existing options plus old space", got)
		}
	})

	t.Run("does not override explicit old space", func(t *testing.T) {
		t.Setenv(EnvPiNodeOptions, "")
		t.Setenv(EnvPiNodeMaxOldSpaceMB, "")
		t.Setenv("NODE_OPTIONS", "--max-old-space-size=8192")
		if got := piNodeOptionsEnv(); len(got) != 0 {
			t.Fatalf("piNodeOptionsEnv() = %#v, want inherited explicit old space", got)
		}
	})

	t.Run("adapter override wins", func(t *testing.T) {
		t.Setenv(EnvPiNodeOptions, "--max-old-space-size=3072 --trace-gc")
		t.Setenv(EnvPiNodeMaxOldSpaceMB, "")
		t.Setenv("NODE_OPTIONS", "--trace-warnings")
		got := strings.Join(piNodeOptionsEnv(), "\n")
		if got != "NODE_OPTIONS=--max-old-space-size=3072 --trace-gc" {
			t.Fatalf("piNodeOptionsEnv() = %q, want adapter override", got)
		}
	})
}

func TestPiLaunchArgsStatuslineExtensionCanBeOverriddenOrDisabled(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())
	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})

	t.Run("call option override", func(t *testing.T) {
		opts := &llmtypes.CallOptions{}
		WithStatuslineExtension("npm:@example/statusline@1.2.3")(opts)
		args, env, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/mcp-output-guard.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
		if err != nil {
			t.Fatalf("piLaunchArgs() error = %v", err)
		}
		if joined := strings.Join(args, "\x00"); !strings.Contains(joined, "-e\x00npm:@example/statusline@1.2.3") {
			t.Fatalf("args = %#v, want statusline override", args)
		}
		if got := strings.Join(env, "\n"); !strings.Contains(got, "PI_STATUSLINE_PRESET=classic") {
			t.Fatalf("env = %#v, want default classic statusline preset", env)
		}
	})

	t.Run("env override", func(t *testing.T) {
		t.Setenv(EnvPiStatuslineExtension, "npm:@example/env-statusline@2.0.0")
		t.Setenv(EnvPiStatuslinePreset, "tokyo-night")
		args, env, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/mcp-output-guard.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", nil)
		if err != nil {
			t.Fatalf("piLaunchArgs() error = %v", err)
		}
		if joined := strings.Join(args, "\x00"); !strings.Contains(joined, "-e\x00npm:@example/env-statusline@2.0.0") {
			t.Fatalf("args = %#v, want env statusline override", args)
		}
		if got := strings.Join(env, "\n"); !strings.Contains(got, "PI_STATUSLINE_PRESET=tokyo-night") {
			t.Fatalf("env = %#v, want env statusline preset override", env)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		opts := &llmtypes.CallOptions{}
		WithStatuslineExtension("off")(opts)
		args, env, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/mcp-output-guard.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
		if err != nil {
			t.Fatalf("piLaunchArgs() error = %v", err)
		}
		if joined := strings.Join(args, "\x00"); strings.Contains(joined, "statusline") {
			t.Fatalf("args = %#v, statusline extension should be disabled", args)
		}
		if got := strings.Join(env, "\n"); strings.Contains(got, "PI_STATUSLINE_PRESET=") {
			t.Fatalf("env = %#v, statusline preset should not be set when statusline is disabled", env)
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
	_, env, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/mcp-output-guard.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts)
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
	if _, _, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/mcp-output-guard.ts", "/tmp/markers.jsonl", "", "mlp-pi-test-123", opts); err == nil {
		t.Fatal("piLaunchArgs() error = nil, want bridge-only without MCP config to fail")
	}
}

func TestPiLaunchArgsRejectsInvalidNativeSessionID(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", t.TempDir())
	adapter := NewPiCLIAdapter("", "google/gemini-3.5-flash", &mockLogger{})
	if _, _, err := adapter.piLaunchArgs("google", "gemini-3.5-flash", "/tmp/marker.ts", "/tmp/mcp-output-guard.ts", "/tmp/markers.jsonl", "", "-bad-", nil); err == nil {
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

func TestPiTmuxLaunchEnablesExtendedKeysBeforeNewSession(t *testing.T) {
	got := piTmuxNewSessionWithExtendedKeysArgs(
		piTmuxNewSessionArgs("mlp-pi-test", []string{"/tmp/launch-pi.sh"}, []string{"PI_API_KEY=secret", " "}, "/tmp/work"),
	)
	wantPrefix := []string{
		"set-option", "-g", "extended-keys", "on", ";",
		"set-option", "-g", "extended-keys-format", "csi-u", ";",
		"new-session", "-d", "-s", "mlp-pi-test",
	}
	if len(got) < len(wantPrefix) {
		t.Fatalf("tmux args = %#v, want prefix %#v", got, wantPrefix)
	}
	for i, want := range wantPrefix {
		if got[i] != want {
			t.Fatalf("tmux args[%d] = %q, want %q in %#v", i, got[i], want, got)
		}
	}
	joined := strings.Join(got, "\x00")
	if !strings.Contains(joined, "-e\x00PI_API_KEY=secret") {
		t.Fatalf("tmux args = %#v, want Pi API key env passed through", got)
	}
}

func TestIsTmuxUnknownExtendedKeysOption(t *testing.T) {
	if !isTmuxUnknownExtendedKeysOption(errors.New("tmux set-option -g extended-keys on failed: invalid option: extended-keys")) {
		t.Fatal("expected invalid extended-keys option to be detected")
	}
	if !isTmuxUnknownExtendedKeysOption(errors.New("tmux set-option -g extended-keys-format csi-u failed: unknown value: csi-u")) {
		t.Fatal("expected unsupported extended-keys-format value to be detected")
	}
	if isTmuxUnknownExtendedKeysOption(errors.New("tmux new-session failed: duplicate session")) {
		t.Fatal("duplicate session must not be treated as an extended-keys compatibility failure")
	}
}

func TestPiPaneShowsPromptDraftDetectsActiveEditor(t *testing.T) {
	prompt := `If goals exist, help me choose or confirm:
- enabled or disabled
- cadence and timezone
- whether it should notify only on decision-worthy changes
- whether the current pulse/org-pulse.html needs to be bootstrapped with the org-html skeleton

Only enable or change the built-in Org Pulse schedule after I confirm the cadence/timezone. Do not manually run Org Pulse
from this command unless I explicitly ask for a one-time run.`

	captured := "\x1b[38;2;178;148;187m─── ↑ 13 more ─────\x1b[39m\n" +
		"\n" +
		"If goals exist, help me choose or confirm:\n" +
		"- enabled or disabled\n" +
		"- cadence and timezone\n" +
		"- whether it should notify only on decision-worthy changes\n" +
		"- whether the current pulse/org-pulse.html needs to be bootstrapped with the org-html skeleton\n" +
		"\n" +
		"Only enable or change the built-in Org Pulse schedule after I confirm the cadence/timezone. Do not manually run Org Pulse\n" +
		"from this command unless I explicitly ask for a one-time run. \x1b[7m \x1b[0m\n" +
		"────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────\n" +
		"\x1b[1m\x1b[38;2;138;190;183mπ\x1b[0m • 🤖 gemini-3.1-pro-preview • 💤 idle\n"

	if !piPaneShowsPromptDraft(captured, prompt) {
		t.Fatalf("expected active Pi editor draft to be detected")
	}
}

func TestPiPaneShowsPromptDraftRequiresCursorNearDraftWhenANSIIsPresent(t *testing.T) {
	prompt := "Send this prompt once"
	captured := "Send this prompt once\n" +
		"\n\n\n\n\n" +
		"\x1b[7m \x1b[0m\n" +
		"────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────\n" +
		"\x1b[1m\x1b[38;2;138;190;183mπ\x1b[0m • 🤖 gemini-3.1-pro-preview • 💤 idle\n"

	if piPaneShowsPromptDraft(captured, prompt) {
		t.Fatalf("stale transcript text with a distant cursor must not be treated as active draft")
	}
}

func TestEnsurePiInputSubmittedSendsRecoveryEnter(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available on this host")
	}

	logFile := filepath.Join(t.TempDir(), "enter.log")
	sessionName := "mlp-pi-test-submit-" + piRandomHex(6)
	t.Cleanup(func() { _ = exec.CommandContext(context.Background(), "tmux", "kill-session", "-t", sessionName).Run() })

	loop := fmt.Sprintf(`while IFS= read -r line; do printf '%%s\n' "$line" >> %q; done`, logFile)
	if out, err := exec.CommandContext(context.Background(), "tmux", "new-session", "-d", "-s", sessionName, "-x", "120", "-y", "30", "bash", "-c", loop).CombinedOutput(); err != nil {
		t.Fatalf("failed to start tmux session: %v; output=%s", err, string(out))
	}

	draft := "MLP_PI_TEST_DRAFT_LINE_" + piRandomHex(4)
	if out, err := exec.CommandContext(context.Background(), "tmux", "send-keys", "-t", sessionName, "-l", draft).CombinedOutput(); err != nil {
		t.Fatalf("failed to type draft: %v; output=%s", err, string(out))
	}

	time.Sleep(250 * time.Millisecond)

	pane, err := capturePiPaneANSI(context.Background(), sessionName)
	if err != nil {
		t.Fatalf("capture pane: %v", err)
	}
	if !piPaneShowsPromptDraft(pane, draft) {
		t.Fatalf("setup precondition failed: pane does not show draft %q; pane:\n%s", draft, stripPiANSI(pane))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ensurePiInputSubmitted(ctx, sessionName, draft)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		content, _ := os.ReadFile(logFile)
		if strings.Contains(string(content), draft) {
			return
		}
		time.Sleep(75 * time.Millisecond)
	}
	content, _ := os.ReadFile(logFile)
	t.Fatalf("expected log to contain draft %q after recovery Enter; log contents=%q", draft, string(content))
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
