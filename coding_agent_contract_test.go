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
		{name: "agy cli", provider: ProviderAgyCLI, wantTmux: true, wantFound: true},
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
			if !contract.RequiresMCPBridgeConfig {
				t.Fatal("coding-agent contract must require MCP bridge config")
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

func TestDeprecatedCodingAgentContractsKeepRuntimeButPointToReplacement(t *testing.T) {
	tests := []struct {
		provider Provider
		want     Provider
	}{
		{provider: ProviderAgyCLI, want: ProviderPiCLI},
	}

	for _, tt := range tests {
		t.Run(string(tt.provider), func(t *testing.T) {
			contract, ok := GetCodingAgentProviderContract(tt.provider, "")
			if !ok {
				t.Fatalf("missing coding-agent contract for %s", tt.provider)
			}
			if !contract.Deprecated {
				t.Fatalf("%s should be marked deprecated for new setup", tt.provider)
			}
			if contract.ReplacementProvider != tt.want {
				t.Fatalf("replacement = %q, want %q", contract.ReplacementProvider, tt.want)
			}
			if strings.TrimSpace(contract.DeprecationReason) == "" {
				t.Fatal("deprecated provider must explain the replacement path")
			}
			if !contract.SupportsFinalExtraction {
				t.Fatal("deprecated provider remains runnable and must keep its runtime contract")
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

func TestCodingAgentOptionRegistriesMatchContracts(t *testing.T) {
	for _, contract := range CodingAgentProviderContracts() {
		if contract.Transport == CodingAgentTransportTmux && contract.RequiresOwnerSessionID {
			if opt := CodingAgentInteractiveSessionOption(contract.Provider, "owner-session"); opt == nil {
				t.Errorf("%s requires an owner session id but has no interactive-session option registry entry", contract.Provider)
			}
		}
		if contract.Transport == CodingAgentTransportTmux && contract.UsesPersistentSession {
			if opt := CodingAgentPersistentInteractiveOption(contract.Provider, true); opt == nil {
				t.Errorf("%s uses persistent tmux sessions but has no persistent-session option registry entry", contract.Provider)
			}
		}
		if contract.RequiresWorkingDir {
			if opt := CodingAgentWorkingDirOption(contract.Provider, "/tmp/work"); opt == nil {
				t.Errorf("%s requires a working dir but has no working-dir option registry entry", contract.Provider)
			}
		}
	}

	for provider := range codingAgentInteractiveSessionRegistry {
		contract, ok := GetCodingAgentProviderContract(provider, "")
		if !ok {
			t.Errorf("interactive-session registry has %s but no coding-agent contract", provider)
			continue
		}
		if contract.Transport != CodingAgentTransportTmux || !contract.RequiresOwnerSessionID {
			t.Errorf("interactive-session registry has %s but contract does not require tmux owner sessions", provider)
		}
	}
	for provider := range codingAgentPersistentInteractiveRegistry {
		contract, ok := GetCodingAgentProviderContract(provider, "")
		if !ok {
			t.Errorf("persistent-session registry has %s but no coding-agent contract", provider)
			continue
		}
		if contract.Transport != CodingAgentTransportTmux || !contract.UsesPersistentSession {
			t.Errorf("persistent-session registry has %s but contract does not claim persistent tmux sessions", provider)
		}
	}
	for provider := range codingAgentWorkingDirRegistry {
		contract, ok := GetCodingAgentProviderContract(provider, "")
		if !ok {
			t.Errorf("working-dir registry has %s but no coding-agent contract", provider)
			continue
		}
		if !contract.RequiresWorkingDir {
			t.Errorf("working-dir registry has %s but contract does not require working dir", provider)
		}
	}
	for provider := range codingAgentProjectDirIDRegistry {
		contract, ok := GetCodingAgentProviderContract(provider, "")
		if !ok {
			t.Errorf("project-dir-id registry has %s but no coding-agent contract", provider)
			continue
		}
		if !contract.SupportsNativeResume {
			t.Errorf("project-dir-id registry has %s but contract does not support native resume", provider)
		}
	}
}

func TestCodingAgentMCPBridgeIsRequiredWhenUsed(t *testing.T) {
	for _, contract := range CodingAgentProviderContracts() {
		if contract.UsesMCPBridge && !contract.RequiresMCPBridgeConfig {
			t.Errorf("%s uses the MCP bridge but does not require bridge config", contract.Provider)
		}
		if contract.RequiresMCPBridgeConfig && !contract.UsesMCPBridge {
			t.Errorf("%s requires bridge config but does not declare UsesMCPBridge", contract.Provider)
		}
	}
}

func TestProjectInstructionOnlyRegistryIsIntentional(t *testing.T) {
	expected := map[Provider]bool{
		ProviderClaudeCode: true,
		ProviderCodexCLI:   true,
	}
	for provider := range expected {
		if opt := CodingAgentProjectInstructionOnlyOption(provider, true); opt == nil {
			t.Errorf("%s should have a project-instruction-only option", provider)
		}
	}
	for provider := range codingAgentProjectInstructionOnlyRegistry {
		if !expected[provider] {
			t.Errorf("unexpected project-instruction-only registry entry for %s", provider)
		}
	}
}

// TestTokenUsageContractIsWellFormed catches two classes of drift on the
// SurfacesTokenUsage / TokenUsageSource pair:
//
//  1. A contract that claims SurfacesTokenUsage:true must also name a
//     TokenUsageSource from validTokenUsageSources. Empty or freeform
//     strings fail. This stops "well we surface SOMETHING" claims with
//     no audit trail of where the data comes from.
//  2. A contract that claims SurfacesTokenUsage:false must NOT name a
//     source — if there's no usage, there's no source to declare. Stops
//     the inverse drift where someone fills in the source but forgets
//     to flip the bool.
//
// Add new TokenUsageSource values to validTokenUsageSources first; the
// test will then accept them.
func TestTokenUsageContractIsWellFormed(t *testing.T) {
	for _, c := range CodingAgentProviderContracts() {
		if c.SurfacesTokenUsage {
			if c.TokenUsageSource == "" {
				t.Errorf("%s claims SurfacesTokenUsage but TokenUsageSource is empty — declare one of stream-json|transcript-file|estimated", c.Provider)
				continue
			}
			if !IsValidTokenUsageSource(c.TokenUsageSource) {
				t.Errorf("%s declares TokenUsageSource=%q which is not in validTokenUsageSources — add it there first or fix the typo", c.Provider, c.TokenUsageSource)
			}
		} else if c.TokenUsageSource != "" {
			t.Errorf("%s has TokenUsageSource=%q but SurfacesTokenUsage=false — flip the bool or clear the source", c.Provider, c.TokenUsageSource)
		}
	}
}

// TestTranscriptReaderContractMatchesRegistry mirrors the resume drift
// test for AdapterReadsTranscript. Contract claim must match registry
// membership in both directions, AND the contract's TranscriptPathTemplate
// must equal the registry's PathTemplate when both are set — so docs in
// the contract stay aligned with what the registry advertises.
func TestTranscriptReaderContractMatchesRegistry(t *testing.T) {
	for _, c := range CodingAgentProviderContracts() {
		info, hasReader := TranscriptReaderFor(c.Provider)
		if c.AdapterReadsTranscript != hasReader {
			t.Errorf("transcript-reader contract drift for %s: contract.AdapterReadsTranscript=%v but transcriptReaderRegistry presence=%v. Update coding_agent_contract.go and coding_agent_transcript_registry.go together — see the comment block in coding_agent_transcript_registry.go for the 3-step checklist.",
				c.Provider, c.AdapterReadsTranscript, hasReader)
			continue
		}
		if hasReader && info.PathTemplate != c.TranscriptPathTemplate {
			t.Errorf("transcript path template drift for %s: contract=%q registry=%q. Keep them in sync.",
				c.Provider, c.TranscriptPathTemplate, info.PathTemplate)
		}
		if !hasReader && c.TranscriptPathTemplate != "" {
			t.Errorf("%s has TranscriptPathTemplate=%q but no entry in transcriptReaderRegistry — register the reader or clear the template",
				c.Provider, c.TranscriptPathTemplate)
		}
	}
	// Inverse: any registry entry must have its contract flag flipped to true.
	for provider, info := range transcriptReaderRegistry {
		contract, ok := GetCodingAgentProviderContract(provider, "")
		if !ok {
			t.Errorf("transcriptReaderRegistry has %s but no coding-agent contract is registered for it", provider)
			continue
		}
		if !contract.AdapterReadsTranscript {
			t.Errorf("transcriptReaderRegistry has %s but its contract.AdapterReadsTranscript is false — flip the contract", provider)
		}
		if contract.TranscriptPathTemplate != info.PathTemplate {
			t.Errorf("registry path template for %s (%q) does not match contract.TranscriptPathTemplate (%q)",
				provider, info.PathTemplate, contract.TranscriptPathTemplate)
		}
	}
}

// TestAPIKeyEnvVarsContractIsWellFormed catches drift in the APIKeyEnvVars
// dimension by enforcing two invariants:
//
//  1. Every coding-agent contract must declare APIKeyEnvVars. nil is
//     treated as "the operator forgot to populate the field" and fails the
//     test. To declare "CLI uses native auth, no env shortcut", use an
//     explicit empty slice ([]string{}). The distinction matters because
//     a nil here is almost always an oversight when adding a new CLI.
//  2. Listed env var names must be SHOUT_SNAKE_CASE letters/digits/_ — no
//     spaces, hyphens, or accidentally-pasted values. Catches typos like
//     "ANTHROPIC API KEY" or "ANTHROPIC-API-KEY".
//
// This intentionally does NOT verify the adapter actually reads each
// listed env var (that needs adapter-level integration tests we may add
// later). It only enforces the contract's own internal consistency.
func TestAPIKeyEnvVarsContractIsWellFormed(t *testing.T) {
	for _, c := range CodingAgentProviderContracts() {
		if c.APIKeyEnvVars == nil {
			t.Errorf("%s has APIKeyEnvVars=nil — declare an explicit []string (use []string{} if the CLI uses native auth only, never nil)", c.Provider)
			continue
		}
		for _, name := range c.APIKeyEnvVars {
			if strings.TrimSpace(name) == "" {
				t.Errorf("%s APIKeyEnvVars contains empty entry: %v", c.Provider, c.APIKeyEnvVars)
				continue
			}
			for _, r := range name {
				ok := (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
				if !ok {
					t.Errorf("%s APIKeyEnvVars entry %q contains invalid character %q — env vars must be SHOUT_SNAKE_CASE", c.Provider, name, string(r))
					break
				}
			}
		}
	}
}

// TestStructuredStreamingContractMatchesRegistry guards the
// SupportsStructuredStreaming dimension the same way the resume/transcript-reader
// drift tests do: the flag must agree, in both directions, with the presence of a
// registered CertStructuredStreaming certification — so "this provider streams
// structured chunks" can never be a bare claim without a real E2E behind it, and
// a registered streaming cert can never sit under a provider that doesn't claim
// the capability. The streaming SOURCE is provider-specific (transcript tail vs
// injected markers), so this does NOT require AdapterReadsTranscript.
func TestStructuredStreamingContractMatchesRegistry(t *testing.T) {
	for _, c := range CodingAgentProviderContracts() {
		hasStreamingCert := false
		for _, cert := range CodingAgentProviderCertifications(c.Provider) {
			if cert.ID == CertStructuredStreaming {
				hasStreamingCert = true
				if !cert.RealE2E {
					t.Errorf("%s CertStructuredStreaming must be a real streaming E2E: %#v", c.Provider, cert)
				}
			}
		}
		if c.SupportsStructuredStreaming != hasStreamingCert {
			t.Errorf("structured-streaming contract drift for %s: contract.SupportsStructuredStreaming=%v but a registered CertStructuredStreaming exists=%v. Register the streaming E2E in codingAgentProviderCertifications and flip the contract flag together.",
				c.Provider, c.SupportsStructuredStreaming, hasStreamingCert)
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

// knownCertificationGaps tracks certifications a coding-agent contract
// CLAIMS via its capability bools but has not yet provided an e2e test for.
// Listed IDs are TOLERATED — they're a public TODO list, not silently
// ignored. Removing an ID from this map without adding the certification
// entry will fail TestAllCodingAgentCapabilityClaimsHaveRegisteredCertification.
//
// History: every provider's coding-agent capability claims were originally
// enforced only for Claude Code + Codex. Cursor, Agy, Gemini
// declared the same capabilities without proof, which let real bugs ship
// (e.g. cursor's "claims UsesMCPBridge=true" while in --print mode it
// returned zero tokens / zero tool calls on a real workflow run). The
// allowance below makes that drift visible to anyone touching the file.
//
// Drain this map by writing the missing e2e tests + registering them in
// codingAgentProviderCertifications.
var knownCertificationGaps = map[Provider][]CodingAgentCertificationID{
	// Cursor CLI is fully wired in the orchestrator and most tmux claims now
	// point at real cursor-agent E2Es. Remaining IDs are real gaps that need
	// their own e2e tests, tracked as follow-up tasks.
	ProviderCursorCLI: {
		CertBoundedRetention,
		CertLifecyclePolicy,
		CertPersistentCancelReuse,
		CertStaleDraftCleanup,
	},
}

// TestAllCodingAgentCapabilityClaimsHaveRegisteredCertification iterates every
// coding-agent contract and asserts that every capability the contract claims
// has a corresponding entry in codingAgentProviderCertifications — proving an
// actual e2e test exists for that claim instead of trusting the bool blindly.
//
// Providers in knownCertificationGaps are allowed to have ALL listed IDs
// missing; any deviation (different missing set, or new gap) fails the test.
// The allowance is intentionally low-friction so adding a new provider
// surfaces its gap immediately rather than silently skipping checks.
func TestAllCodingAgentCapabilityClaimsHaveRegisteredCertification(t *testing.T) {
	for _, contract := range CodingAgentProviderContracts() {
		missing := MissingCodingAgentCertifications(contract)
		if len(missing) == 0 {
			continue
		}
		allowed := knownCertificationGaps[contract.Provider]
		// Build a set of allowed IDs for fast lookup.
		allowedSet := make(map[CodingAgentCertificationID]struct{}, len(allowed))
		for _, id := range allowed {
			allowedSet[id] = struct{}{}
		}
		// missing IDs not in allowedSet → real new failures.
		var unexpected []CodingAgentCertificationID
		for _, id := range missing {
			if _, ok := allowedSet[id]; !ok {
				unexpected = append(unexpected, id)
			}
		}
		if len(unexpected) > 0 {
			t.Errorf("%s claims capabilities without registered certification: %v. If this is intentional, add the IDs to knownCertificationGaps and file a follow-up task to write the e2e tests.",
				contract.Provider, unexpected)
		}
		// Also flag stale allowances — an ID listed in knownCertificationGaps
		// that is no longer missing means somebody added the cert entry but
		// forgot to remove the allowance. Drop it to keep the gap list honest.
		for _, id := range allowed {
			stillMissing := false
			for _, m := range missing {
				if m == id {
					stillMissing = true
					break
				}
			}
			if !stillMissing {
				t.Errorf("%s knownCertificationGaps still lists %s but the certification is now registered — remove the allowance",
					contract.Provider, id)
			}
		}
	}
}

func TestCodingAgentCertificationReferencesExistingTests(t *testing.T) {
	for provider := range codingAgentProviderCertifications {
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

func TestActiveCodingAgentProvidersSatisfyP0Contract(t *testing.T) {
	for _, contract := range CodingAgentProviderContracts() {
		if contract.Transport != CodingAgentTransportTmux || contract.Deprecated {
			continue
		}
		if missing := MissingP0CodingAgentCertifications(contract); len(missing) > 0 {
			t.Fatalf("%s is missing release-blocking P0 certifications: %v", contract.Provider, missing)
		}

		byID := make(map[CodingAgentCertificationID]CodingAgentCertification)
		for _, cert := range CodingAgentProviderCertifications(contract.Provider) {
			byID[cert.ID] = cert
		}
		for _, id := range RequiredP0CodingAgentCertificationIDs(contract) {
			cert := byID[id]
			if cert.Priority != CodingAgentCertificationPriorityP0 {
				t.Fatalf("%s certification %s priority = %q, want P0", contract.Provider, id, cert.Priority)
			}
			if !cert.RealE2E {
				t.Fatalf("%s P0 certification %s must be backed by a real CLI E2E: %#v", contract.Provider, id, cert)
			}
			if len(cert.Env) != 1 || cert.Env[0] != "-coding-cli-p0-live" {
				t.Fatalf("%s P0 certification %s must use only the live P0 gate, got %#v", contract.Provider, id, cert.Env)
			}
		}
	}
}

func TestPiCLICertificationsUseRealE2EOnly(t *testing.T) {
	certs := CodingAgentProviderCertifications(ProviderPiCLI)
	if len(certs) == 0 {
		t.Fatal("pi-cli has no registered certifications")
	}
	for _, cert := range certs {
		if !cert.RealE2E {
			t.Fatalf("pi-cli certification %s must be backed by real Pi E2E, got %#v", cert.ID, cert)
		}
		hasPiEnvGuard := false
		for _, env := range cert.Env {
			if env == "-coding-cli-p0-live" || strings.HasPrefix(env, "RUN_PI_CLI_") || env == "RUN_CODING_AGENT_CONTINUATION_REAL_E2E=1" {
				hasPiEnvGuard = true
				break
			}
		}
		if !hasPiEnvGuard {
			t.Fatalf("pi-cli certification %s must name its real E2E env guard, got %#v", cert.ID, cert.Env)
		}
	}
}

func TestCursorCLICertificationsUseRealE2EOnly(t *testing.T) {
	certs := CodingAgentProviderCertifications(ProviderCursorCLI)
	if len(certs) == 0 {
		t.Fatal("cursor-cli has no registered certifications")
	}
	for _, cert := range certs {
		if !cert.RealE2E {
			t.Fatalf("cursor-cli certification %s must be backed by real Cursor E2E, got %#v", cert.ID, cert)
		}
		hasCursorEnvGuard := false
		for _, env := range cert.Env {
			if env == "-coding-cli-p0-live" || strings.HasPrefix(env, "RUN_CURSOR_CLI_") {
				hasCursorEnvGuard = true
				break
			}
		}
		if !hasCursorEnvGuard {
			t.Fatalf("cursor-cli certification %s must name its real E2E env guard, got %#v", cert.ID, cert.Env)
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
