package llmproviders

import "testing"

func TestCodingAgentProviderContractCurrentProviders(t *testing.T) {
	tests := []struct {
		name      string
		provider  Provider
		modelID   string
		wantTmux  bool
		wantFound bool
	}{
		{name: "claude code", provider: ProviderClaudeCode, wantTmux: true, wantFound: true},
		{name: "codex cli", provider: ProviderCodexCLI, wantTmux: true, wantFound: true},
		{name: "cursor cli", provider: ProviderCursorCLI, wantTmux: true, wantFound: true},
		{name: "gemini cli", provider: ProviderGeminiCLI, wantTmux: true, wantFound: true},
		{name: "kimi code cli", provider: ProviderKimi, modelID: "kimi-code", wantFound: true},
		{name: "kimi api model", provider: ProviderKimi, modelID: "kimi-k2.6"},
		{name: "openai", provider: ProviderOpenAI},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contract, found := GetCodingAgentProviderContract(tt.provider, tt.modelID)
			if found != tt.wantFound {
				t.Fatalf("found = %v, want %v", found, tt.wantFound)
			}
			if !found {
				return
			}
			if got := contract.Transport == CodingAgentTransportTmux; got != tt.wantTmux {
				t.Fatalf("tmux = %v, want %v", got, tt.wantTmux)
			}
			if !contract.RequiresWorkingDir {
				t.Fatal("coding-agent contract must require an explicit working directory")
			}
			if !contract.UsesMCPBridge {
				t.Fatal("coding-agent contract must use the MCP bridge")
			}
			if !contract.UsesNativeSystemPrompt {
				t.Fatal("coding-agent contract must use native system/developer instructions")
			}
			if contract.Transport == CodingAgentTransportTmux {
				for name, ok := range map[string]bool{
					"owner session id":   contract.RequiresOwnerSessionID,
					"persistent session": contract.UsesPersistentSession,
					"live input":         contract.SupportsLiveInput,
					"interrupt":          contract.SupportsInterrupt,
					"terminal stream":    contract.SupportsTerminalStream,
					"final extraction":   contract.SupportsFinalExtraction,
					"login shell launch": contract.LaunchesViaLoginShell,
					"scoped cleanup":     contract.ProcessScopedCleanup,
					"session loss":       contract.HandlesTmuxSessionLoss,
				} {
					if !ok {
						t.Fatalf("tmux coding-agent contract missing %s", name)
					}
				}
			}
		})
	}
}

func TestCodingAgentProviderContractsAreSorted(t *testing.T) {
	contracts := CodingAgentProviderContracts()
	if len(contracts) == 0 {
		t.Fatal("expected coding-agent contracts")
	}
	for i := 1; i < len(contracts); i++ {
		prev, cur := contracts[i-1], contracts[i]
		if prev.Provider > cur.Provider || (prev.Provider == cur.Provider && prev.ModelID > cur.ModelID) {
			t.Fatalf("contracts are not sorted at %d: %#v before %#v", i, prev, cur)
		}
	}
}
