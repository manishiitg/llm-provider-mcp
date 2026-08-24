package picli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizePiMCPConfigDefaultsDisableProxyTool(t *testing.T) {
	input := `{"mcpServers":{"api-bridge":{"command":"mcpbridge"}}}`

	out, err := normalizePiMCPConfig(input)
	if err != nil {
		t.Fatalf("normalizePiMCPConfig: %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized config: %v", err)
	}
	settings, ok := got["settings"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected a settings object in normalized config, got: %s", out)
	}
	if disable, _ := settings["disableProxyTool"].(bool); !disable {
		t.Fatalf("expected settings.disableProxyTool=true by default, got: %s", out)
	}
}

func TestNormalizePiMCPConfigPreservesExplicitDisableProxyToolFalse(t *testing.T) {
	input := `{"settings":{"disableProxyTool":false},"mcpServers":{"api-bridge":{"command":"mcpbridge"}}}`

	out, err := normalizePiMCPConfig(input)
	if err != nil {
		t.Fatalf("normalizePiMCPConfig: %v", err)
	}
	if !strings.Contains(string(out), `"disableProxyTool": false`) {
		t.Fatalf("expected an explicit settings.disableProxyTool=false to survive normalization, got: %s", out)
	}
}

func TestNormalizePiMCPConfigStillDefaultsDirectToolsAndLifecycleAlongsideProxySetting(t *testing.T) {
	input := `{"mcpServers":{"api-bridge":{"command":"mcpbridge"}}}`

	out, err := normalizePiMCPConfig(input)
	if err != nil {
		t.Fatalf("normalizePiMCPConfig: %v", err)
	}

	var got struct {
		Settings struct {
			DisableProxyTool bool `json:"disableProxyTool"`
		} `json:"settings"`
		McpServers struct {
			ApiBridge struct {
				DirectTools interface{} `json:"directTools"`
				Lifecycle   string      `json:"lifecycle"`
			} `json:"api-bridge"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal normalized config: %v", err)
	}
	if got.McpServers.ApiBridge.DirectTools != true {
		t.Fatalf("expected api-bridge.directTools=true, got: %s", out)
	}
	if got.McpServers.ApiBridge.Lifecycle != "keep-alive" {
		t.Fatalf("expected api-bridge.lifecycle=keep-alive, got: %s", out)
	}
	if !got.Settings.DisableProxyTool {
		t.Fatalf("expected settings.disableProxyTool=true, got: %s", out)
	}
}
