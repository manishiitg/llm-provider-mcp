package opencodecli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func opencodeWorkingDirFromOptions(opts *llmtypes.CallOptions) string {
	if opts == nil || opts.Metadata == nil || opts.Metadata.Custom == nil {
		return ""
	}
	if dir, ok := opts.Metadata.Custom[MetadataKeyWorkingDir].(string); ok {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			return filepath.Clean(trimmed)
		}
	}
	return ""
}

func opencodeBinaryPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("OPENCODE_BIN")); configured != "" {
		if info, err := os.Stat(configured); err == nil && !info.IsDir() {
			return configured, nil
		}
		return "", fmt.Errorf("OPENCODE_BIN points to a missing or invalid executable: %s", configured)
	}
	if path, err := exec.LookPath("opencode"); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		for _, candidate := range []string{
			filepath.Join(home, ".opencode", "bin", "opencode"),
			filepath.Join(home, ".cache", "opencode", "bin", "opencode"),
		} {
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("opencode not found in PATH. Install OpenCode CLI or set OPENCODE_BIN to the opencode executable")
}

func opencodeRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func writeOpenCodeRestoredFile(path string, content []byte) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create OpenCode config dir: %w", err)
	}
	previous, readErr := os.ReadFile(path)
	existed := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("failed to read existing OpenCode config %s: %w", path, readErr)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return nil, fmt.Errorf("failed to write OpenCode config %s: %w", path, err)
	}
	return func() {
		if existed {
			_ = os.WriteFile(path, previous, 0o600)
		} else {
			_ = os.Remove(path)
		}
	}, nil
}

// buildOpenCodeMCPConfigJSON transforms the standard {"mcpServers":{...}} format
// into OpenCode's {"mcp":{...}} format, adding required "type" and "enabled" fields.
func buildOpenCodeMCPConfigJSON(mcpServersJSON string) ([]byte, error) {
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(mcpServersJSON), &input); err != nil {
		return nil, fmt.Errorf("invalid MCP config JSON: %w", err)
	}

	servers, ok := input["mcpServers"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("MCP config JSON missing mcpServers object")
	}

	mcpSection := make(map[string]interface{})
	for name, serverRaw := range servers {
		server, ok := serverRaw.(map[string]interface{})
		if !ok {
			continue
		}
		if _, has := server["type"]; !has {
			server["type"] = "local"
		}
		if _, has := server["enabled"]; !has {
			server["enabled"] = true
		}
		mcpSection[name] = server
	}

	output := map[string]interface{}{
		"mcp": mcpSection,
	}
	return json.MarshalIndent(output, "", "  ")
}
