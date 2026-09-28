package agycli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
)

func TestAgyIsolatedHomesKeepSessionCredentialsSeparate(t *testing.T) {
	base := t.TempDir()
	t.Setenv("HOME", base)
	settingsPath := agySettingsPath(base)
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte(`{"permissions":{"allow":["view_file"]},"trustedWorkspaces":["/work"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	server := func(token string) []agyMCPServer {
		return []agyMCPServer{{name: "api-bridge", command: "/bin/true", env: map[string]string{"MCP_API_TOKEN": token}}}
	}
	homeA, releaseA, err := agyIsolatedHome(server("secret-A"))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	homeB, releaseB, err := agyIsolatedHome(server("secret-B"))
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	if homeA == homeB {
		t.Fatal("concurrent sessions reused a home")
	}
	for home, secrets := range map[string][2]string{homeA: {"secret-A", "secret-B"}, homeB: {"secret-B", "secret-A"}} {
		own, other := secrets[0], secrets[1]
		path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private config mode = %v, %v", info, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(raw), own) || strings.Contains(string(raw), other) {
			t.Fatalf("credentials crossed homes: err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".gemini", "antigravity-cli", "conversations")); err != nil {
			t.Fatal(err)
		}
		if _, err := exec.LookPath("agy"); err == nil {
			cmd := exec.CommandContext(context.Background(), "agy", "mcp", "list")
			cmd.Env = append(os.Environ(), "HOME="+home)
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "agentworks-api-bridge-") {
				t.Fatalf("AGY did not read its private config: %v", err)
			}
		}
	}
	global, err := os.ReadFile(settingsPath)
	if err != nil || strings.Contains(string(global), "secret-") {
		t.Fatalf("global settings changed or leaked credentials: %v", err)
	}
}

func TestAgyQuotaFailureIsTyped(t *testing.T) {
	for _, body := range []string{
		`{"status":"FAILED","response":"RESOURCE_EXHAUSTED: quota exceeded"}`,
		`{"status":"429","response":"rate limit exceeded"}`,
		`{"status":"FAILED","response":"The provider says you have reached your quota for today"}`,
	} {
		_, err := agyParseExecEnvelope([]byte(body))
		if llmerrors.KindOf(err) != llmerrors.KindQuotaExhausted {
			t.Fatalf("quota error kind = %q, err=%v", llmerrors.KindOf(err), err)
		}
	}
	if got := agyQuotaError("", "I can explain what quota exceeded means"); got != nil {
		t.Fatalf("ordinary explanation classified as quota: %v", got)
	}
}

func TestAgySweepLegacyDeadMountsKeepsLiveOnes(t *testing.T) {
	home := t.TempDir()
	dead := "agentworks-old-99999999-abcd"
	live := "agentworks-live-" + strconv.Itoa(os.Getpid()) + "-abcd"
	config := filepath.Join(home, ".gemini", "config", "mcp_config.json")
	settings := agySettingsPath(home)
	if err := agyWritePrivateJSON(config, map[string]interface{}{"mcpServers": map[string]interface{}{dead: map[string]interface{}{"command": "/bin/true"}, live: map[string]interface{}{"command": "/bin/true"}, "user-server": map[string]interface{}{"command": "/bin/true"}}}); err != nil {
		t.Fatal(err)
	}
	if err := agyWritePrivateJSON(settings, map[string]interface{}{"permissions": map[string]interface{}{"allow": []string{"mcp(" + dead + "/*)", "mcp(" + live + "/*)", "view_file"}}}); err != nil {
		t.Fatal(err)
	}
	if err := agySweepLegacyGlobalMounts(home); err != nil {
		t.Fatal(err)
	}
	configRaw, _ := os.ReadFile(config)
	settingsRaw, _ := os.ReadFile(settings)
	if strings.Contains(string(configRaw), dead) || strings.Contains(string(settingsRaw), dead) {
		t.Fatal("dead legacy mount survived sweep")
	}
	if !strings.Contains(string(configRaw), live) || !strings.Contains(string(settingsRaw), live) || !strings.Contains(string(configRaw), "user-server") {
		t.Fatal("sweep removed a live or user mount")
	}
}

func TestAgyPrivateHomeKeyModeLive(t *testing.T) {
	if os.Getenv("AGY_LIVE_PRIVATE_HOME") != "1" {
		t.Skip("set AGY_LIVE_PRIVATE_HOME=1 for the live API probe")
	}
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
		t.Skip("Gemini API key unavailable")
	}
	if _, err := exec.LookPath("agy"); err != nil {
		t.Skip("agy unavailable")
	}
	home, cleanup, err := agyIsolatedHome(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "agy", "--output-format", "json", "-p=Reply with exactly AGY_PRIVATE_HOME_OK", "--model", "gemini-3.7-flash-low")
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("private AGY key-mode probe failed: %v; output tail: %s", err, agyOutputTail(string(out)))
	}
	if !strings.Contains(string(out), "AGY_PRIVATE_HOME_OK") {
		t.Fatalf("private AGY response did not contain the probe marker: %s", agyOutputTail(string(out)))
	}
}
