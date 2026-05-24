package llmproviders

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
		{name: "gemini cli", provider: ProviderGeminiCLI, wantFound: true},
		{name: "opencode cli", provider: ProviderOpenCodeCLI, wantFound: true},
		{name: "removed kimi code cli", provider: ProviderKimi, modelID: "kimi-code"},
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

// TestNativeResumeContractMatchesRegistry guards against drift between
// contract.SupportsNativeResume and the actual end-to-end wiring (Agent
// session-id field → adapter populator → public WithXxxResumeSessionID
// re-export → server.go restore switch case).
//
// nativeResumeRegistry in coding_agent_resume_registry.go is the single
// source of truth for "this provider has a public resume option func".
// A contract that claims SupportsNativeResume=true must have a registry
// entry; a registry entry implies the contract must be true. Any mismatch
// fails this test — that's the whole point.
//
// In the past, two contract drifts went undetected for months: Codex was
// fully wired but contract.SupportsNativeResume said false, hiding the
// feature from any caller that gated on the contract value; Cursor's
// contract honestly said false but the adapter quietly accepted --resume
// metadata that nobody plumbed in. Either kind of drift now fails CI.
func TestNativeResumeContractMatchesRegistry(t *testing.T) {
	for _, contract := range CodingAgentProviderContracts() {
		_, hasRegistry := nativeResumeRegistry[contract.Provider]
		if contract.SupportsNativeResume != hasRegistry {
			t.Errorf("native-resume contract drift for %s: contract.SupportsNativeResume=%v but nativeResumeRegistry presence=%v. Update coding_agent_contract.go and coding_agent_resume_registry.go together — see the comment block in coding_agent_resume_registry.go for the 4-layer wiring checklist.",
				contract.Provider, contract.SupportsNativeResume, hasRegistry)
		}
	}
	// Inverse: registry must not list a provider whose contract is missing
	// or already says false. Catches the case where someone adds a registry
	// entry but forgets to flip the contract.
	for provider := range nativeResumeRegistry {
		contract, ok := GetCodingAgentProviderContract(provider, "")
		if !ok {
			t.Errorf("nativeResumeRegistry has %s but no coding-agent contract is registered for it", provider)
			continue
		}
		if !contract.SupportsNativeResume {
			t.Errorf("nativeResumeRegistry has %s but its contract.SupportsNativeResume is false — flip the contract", provider)
		}
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

func TestClaudeAndCodexCapabilityClaimsHaveRegisteredCertification(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCode, ProviderCodexCLI} {
		contract, ok := GetCodingAgentProviderContract(provider, "")
		if !ok {
			t.Fatalf("missing coding-agent contract for %s", provider)
		}
		if missing := MissingCodingAgentCertifications(contract); len(missing) > 0 {
			t.Fatalf("%s claims capabilities without registered certification: %v", provider, missing)
		}
	}
}

func TestClaudeAndCodexCertificationReferencesExistingTests(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCode, ProviderCodexCLI} {
		certs := CodingAgentProviderCertifications(provider)
		if len(certs) == 0 {
			t.Fatalf("%s has no registered certifications", provider)
		}
		seen := make(map[CodingAgentCertificationID]string, len(certs))
		for _, cert := range certs {
			if cert.ID == "" {
				t.Fatalf("%s has certification with empty id: %#v", provider, cert)
			}
			if previous := seen[cert.ID]; previous != "" {
				t.Fatalf("%s certification %s registered twice: %s and %s", provider, cert.ID, previous, cert.TestName)
			}
			seen[cert.ID] = cert.TestName
			if strings.TrimSpace(cert.TestFile) == "" || strings.TrimSpace(cert.TestName) == "" {
				t.Fatalf("%s certification %s must name a test file and test function: %#v", provider, cert.ID, cert)
			}
			path := filepath.Clean(cert.TestFile)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%s certification %s references unreadable test file %s: %v", provider, cert.ID, path, err)
			}
			if !strings.Contains(string(raw), "func "+cert.TestName+"(") {
				t.Fatalf("%s certification %s references missing test %s in %s", provider, cert.ID, cert.TestName, path)
			}
		}
	}
}

func TestClaudeAndCodexSessionLossRecoveryCertificationUsesRealE2E(t *testing.T) {
	for _, provider := range []Provider{ProviderClaudeCode, ProviderCodexCLI} {
		var found *CodingAgentCertification
		for _, cert := range CodingAgentProviderCertifications(provider) {
			if cert.ID == CertSessionLossRecovery {
				certCopy := cert
				found = &certCopy
				break
			}
		}
		if found == nil {
			t.Fatalf("%s missing %s certification", provider, CertSessionLossRecovery)
		}
		if !found.RealE2E {
			t.Fatalf("%s %s certification must be marked RealE2E: %#v", provider, CertSessionLossRecovery, *found)
		}
		if found.TestFile != "coding_agent_continuation_real_test.go" || found.TestName != "TestCodingAgentContinuationRealE2EAfterTmuxLoss" {
			t.Fatalf("%s %s points at %s/%s, want real tmux-loss continuation E2E", provider, CertSessionLossRecovery, found.TestFile, found.TestName)
		}
		wantEnv := "RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1"
		hasEnv := false
		for _, env := range found.Env {
			if env == wantEnv {
				hasEnv = true
				break
			}
		}
		if !hasEnv {
			t.Fatalf("%s %s missing env guard %q: %#v", provider, CertSessionLossRecovery, wantEnv, found.Env)
		}
	}
}

func TestStructuredCLIAdaptersMirrorAssistantTextToTerminal(t *testing.T) {
	tests := []struct {
		name        string
		adapterFile string
		testFile    string
		testName    string
	}{
		{
			name:        "gemini cli",
			adapterFile: "pkg/adapters/geminicli/geminicli_adapter.go",
			testFile:    "pkg/adapters/geminicli/geminicli_adapter_test.go",
			testName:    "TestGeminiCLIStructuredStreamMirrorsAssistantTextToTerminal",
		},
		{
			name:        "cursor cli",
			adapterFile: "pkg/adapters/cursorcli/cursorcli_adapter.go",
			testFile:    "pkg/adapters/cursorcli/cursorcli_adapter_test.go",
			testName:    "TestCursorCLIStructuredStreamMirrorsAssistantTextToTerminal",
		},
		{
			name:        "opencode cli",
			adapterFile: "pkg/adapters/opencodecli/opencodecli_adapter.go",
			testFile:    "pkg/adapters/opencodecli/opencodecli_adapter_test.go",
			testName:    "TestOpenCodeCLIStructuredStreamMirrorsAssistantTextToTerminal",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapterRaw, err := os.ReadFile(filepath.Clean(tt.adapterFile))
			if err != nil {
				t.Fatalf("read adapter file: %v", err)
			}
			if strings.Contains(string(adapterRaw), "_ = sink") {
				t.Fatalf("%s discards StreamSink; structured adapters must emit through sink.Emit so terminal panes mirror assistant/tool output", tt.adapterFile)
			}

			testRaw, err := os.ReadFile(filepath.Clean(tt.testFile))
			if err != nil {
				t.Fatalf("read test file: %v", err)
			}
			if !strings.Contains(string(testRaw), "func "+tt.testName+"(") {
				t.Fatalf("%s missing terminal mirror regression test %s", tt.testFile, tt.testName)
			}
		})
	}
}
