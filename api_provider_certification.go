package llmproviders

import (
	"sort"
	"strings"
)

// This file is the API-transport sibling of coding_agent_certification.go:
// capability flag -> required certification -> a real, named live proof,
// enforced by a test that fails when a provider claims something it cannot
// demonstrate.
//
// It is deliberately a SEPARATE registry rather than an extension of the
// coding-agent one. The coding-agent contract's required set is CLI-shaped by
// construction -- CertFreshLaunch, CertWorkingDirectory, CertTrustAuthPrompts,
// CertMCPBridge, CertLiveInput, CertParallelIsolation all describe launching
// and steering a subprocess. A direct API call launches nothing, has no
// working directory, no tmux pane, no MCP bridge subprocess and no live input,
// so most of that set is not merely unmet but meaningless. Bolting the six API
// adapters into CodingAgentProviderContracts would have made them permanently
// non-compliant against certifications that describe a thing they are not.
//
// What carries over is the *mechanism*, not the list.
//
// Scope note: these six adapters are not reachable from AgentWorks workflow
// steps today (those run the CLI adapters' structured transport). This exists
// so the contract is already in place, and its gaps already visible, before
// they are wired in -- not after.

// APIProviderCertificationID is an executable proof point for one part of the
// direct-API provider contract.
type APIProviderCertificationID string

const (
	// CertAPIPlainText is the floor: a real request returns real assistant
	// text. Everything else is meaningless if this does not hold.
	CertAPIPlainText APIProviderCertificationID = "api_plain_text"
	// CertAPIToolCalling proves the provider's tool schema is translated
	// correctly and a tool call comes back in the shape callers parse.
	CertAPIToolCalling APIProviderCertificationID = "api_tool_calling"
	// CertAPIStructuredOutput proves JSON mode / schema-constrained output,
	// which orchestration depends on for machine-readable step results.
	CertAPIStructuredOutput APIProviderCertificationID = "api_structured_output"
	// CertAPIUsageAccounting proves token counts and a cost estimate reach
	// GenerationInfo. Without it the cost ledger silently writes bare rows --
	// the same class of gap SurfacesTokenUsage exists for on the CLI side.
	CertAPIUsageAccounting APIProviderCertificationID = "api_usage_accounting"
	// CertAPIAuthFailureClassified proves a bad credential surfaces as a
	// classified auth error rather than an opaque transport failure. This is
	// what lets callers distinguish "fix your key" from "retry later".
	CertAPIAuthFailureClassified APIProviderCertificationID = "api_auth_failure_classified"
	// CertAPIRateLimitClassified proves a 429/quota response is classified as
	// rate limiting, which is what drives backoff and provider failover
	// instead of surfacing a hard failure to the user.
	CertAPIRateLimitClassified APIProviderCertificationID = "api_rate_limit_classified"
	// CertAPITransportLabel proves the adapter declares transport="api" on its
	// observability config, so the frontend transport chip is correct.
	//
	// Included because this is precisely the property that silently regressed
	// on the CLI side: the structured adapters' own "structured_cli" label was
	// orphaned when structured mode was split out of the tmux path, and
	// nothing caught it because the only test hand-constructed the terminal
	// and passed the label itself rather than exercising a real adapter.
	CertAPITransportLabel APIProviderCertificationID = "api_transport_label"
)

// requiredP0APICertificationIDs are release-blocking for every active,
// non-deprecated direct-API provider.
//
// Deliberately smaller than the coding-agent P0 set. Each entry is here
// because its absence is a user-visible defect, not because the equivalent
// exists on the CLI side:
//   - PlainText: the call does not work at all.
//   - UsageAccounting: cost/usage silently wrong, and wrong money is worse
//     than missing money because nothing looks broken.
//   - AuthFailureClassified / RateLimitClassified: the difference between
//     correct failover and a hard user-facing failure.
//   - TransportLabel: cheap, and the one property with a demonstrated history
//     of silent regression.
//
// ToolCalling and StructuredOutput are capability-gated below rather than
// unconditional, because not every provider/model pair supports them.
var requiredP0APICertificationIDs = []APIProviderCertificationID{
	CertAPIPlainText,
	CertAPIUsageAccounting,
	CertAPIAuthFailureClassified,
	CertAPIRateLimitClassified,
	CertAPITransportLabel,
}

// APIProviderContract is the capability declaration for a direct-API provider.
type APIProviderContract struct {
	Provider Provider
	// Deprecated marks providers kept runnable for existing configs but not
	// offered for new setup. Deprecated providers are exempt from P0, matching
	// RequiredP0CodingAgentCertificationIDs' own treatment.
	Deprecated bool

	SupportsToolCalling     bool
	SupportsStructuredOuput bool
}

// APIProviderContracts returns the declared contract for every direct-API
// provider that routes through llmtypes.WithObservability.
//
// kimi and zai are API-shaped but deliberately absent: neither routes through
// WithObservability, so they have no transport declaration point and are not
// covered by this contract. Adding them means restructuring those adapters
// first -- a different change, not a silent omission.
func APIProviderContracts() []APIProviderContract {
	contracts := []APIProviderContract{
		{Provider: ProviderOpenAI, SupportsToolCalling: true, SupportsStructuredOuput: true},
		{Provider: ProviderAnthropic, SupportsToolCalling: true, SupportsStructuredOuput: true},
		{Provider: ProviderVertex, SupportsToolCalling: true, SupportsStructuredOuput: true},
		{Provider: ProviderAzure, SupportsToolCalling: true, SupportsStructuredOuput: true},
		{Provider: ProviderBedrock, SupportsToolCalling: true, SupportsStructuredOuput: true},
		{Provider: ProviderMiniMax, SupportsToolCalling: true, SupportsStructuredOuput: true},
	}
	sort.Slice(contracts, func(i, j int) bool {
		return contracts[i].Provider < contracts[j].Provider
	})
	return contracts
}

// RequiredP0APICertificationIDs returns the non-negotiable proofs for one
// active direct-API provider.
func RequiredP0APICertificationIDs(contract APIProviderContract) []APIProviderCertificationID {
	if contract.Deprecated {
		return nil
	}
	ids := append([]APIProviderCertificationID(nil), requiredP0APICertificationIDs...)
	if contract.SupportsToolCalling {
		ids = append(ids, CertAPIToolCalling)
	}
	if contract.SupportsStructuredOuput {
		ids = append(ids, CertAPIStructuredOutput)
	}
	return ids
}

// APIProviderCertification records the real test that proves one ID.
type APIProviderCertification struct {
	ID          APIProviderCertificationID
	TestFile    string
	TestName    string
	Env         []string
	Description string
	// RealE2E marks a proof that runs against the live provider API rather
	// than a fixture. Every P0 proof must be one: a deterministic test cannot
	// certify that a real credential, a real 429, or a real usage payload is
	// handled correctly.
	RealE2E bool
}

// apiProviderCertifications maps each provider to its registered proofs.
//
// Entries below are the live tests that ALREADY existed before this contract
// was written; this registry names them rather than inventing coverage. The
// gaps are therefore real measured gaps, not an artifact of a new file:
// azure has no test files at all, and minimax has no live E2E.
var apiProviderCertifications = map[Provider][]APIProviderCertification{
	ProviderVertex: {
		{
			ID:          CertAPIPlainText,
			TestFile:    "pkg/adapters/vertex/google_genai_adapter_real_test.go",
			TestName:    "TestVertexRealPlainText",
			Env:         []string{"RUN_VERTEX_REAL_E2E"},
			Description: "a real Gemini request returns assistant text",
			RealE2E:     true,
		},
		{
			ID:          CertAPIToolCalling,
			TestFile:    "pkg/adapters/vertex/google_genai_adapter_real_test.go",
			TestName:    "TestVertexRealToolChoiceModes",
			Env:         []string{"RUN_VERTEX_REAL_E2E"},
			Description: "real tool-choice modes drive real tool selection",
			RealE2E:     true,
		},
		{
			ID:          CertAPIStructuredOutput,
			TestFile:    "pkg/adapters/vertex/google_genai_adapter_real_test.go",
			TestName:    "TestVertexRealJSONMode",
			Env:         []string{"RUN_VERTEX_REAL_E2E"},
			Description: "JSON mode returns parseable structured output",
			RealE2E:     true,
		},
		{
			ID:          CertAPIUsageAccounting,
			TestFile:    "pkg/adapters/vertex/google_genai_adapter_real_test.go",
			TestName:    "TestVertexRealCostEstimateOnPlainText",
			Env:         []string{"RUN_VERTEX_REAL_E2E"},
			Description: "token usage and a cost estimate reach GenerationInfo",
			RealE2E:     true,
		},
		{
			ID:          CertAPIAuthFailureClassified,
			TestFile:    "pkg/adapters/vertex/google_genai_adapter_real_test.go",
			TestName:    "TestVertexRealAuthFailureClassified",
			Env:         []string{"RUN_VERTEX_REAL_E2E"},
			Description: "a real bad credential surfaces as a classified auth error",
			RealE2E:     true,
		},
		{
			ID:       CertAPIRateLimitClassified,
			TestFile: "pkg/adapters/vertex/google_genai_adapter_real_test.go",
			TestName: "TestVertexRealRateLimitClassified",
			Env:      []string{"RUN_VERTEX_REAL_E2E"},
			// Best-effort by construction: it fires 30 concurrent requests to
			// PROVOKE a real 429 and t.Skip()s when the account's headroom is
			// too high to trigger one (observed skipping on 2026-08-19). So a
			// green run is not by itself evidence the classifier works --
			// treat a skip as "unproven this run", not "passed".
			Description: "a real quota/429 response is classified as rate limiting (skips when no 429 can be provoked)",
			RealE2E:     true,
		},
		{
			ID:          CertAPITransportLabel,
			TestFile:    "pkg/adapters/vertex/google_genai_api_transport_live_test.go",
			TestName:    "TestVertexRealDeclaresAPITransportOnTerminalChunks",
			Env:         []string{"RUN_VERTEX_REAL_E2E"},
			Description: "every synthetic-terminal chunk from a real call carries transport=api",
			RealE2E:     true,
		},
	},
	ProviderAnthropic: {
		{
			ID:          CertAPIPlainText,
			TestFile:    "pkg/adapters/anthropic/anthropic_inspector_real_test.go",
			TestName:    "TestAnthropicRealInspectorContract",
			Env:         []string{"RUN_ANTHROPIC_REAL_E2E"},
			Description: "a real Anthropic request returns assistant text and emits the inspector contract",
			RealE2E:     true,
		},
		{
			ID:          CertAPIToolCalling,
			TestFile:    "pkg/adapters/anthropic/anthropic_real_test.go",
			TestName:    "TestAnthropicRealToolChoiceModes",
			Env:         []string{"RUN_ANTHROPIC_REAL_E2E"},
			Description: "real tool-choice modes drive real tool selection",
			RealE2E:     true,
		},
		{
			ID:          CertAPIStructuredOutput,
			TestFile:    "pkg/adapters/anthropic/anthropic_real_test.go",
			TestName:    "TestAnthropicRealJSONSchemaStrictViaTool",
			Env:         []string{"RUN_ANTHROPIC_REAL_E2E"},
			Description: "schema-constrained output returns parseable structured JSON",
			RealE2E:     true,
		},
		{
			ID:          CertAPIUsageAccounting,
			TestFile:    "pkg/adapters/anthropic/anthropic_real_test.go",
			TestName:    "TestAnthropicRealCostEstimateOnPlainText",
			Env:         []string{"RUN_ANTHROPIC_REAL_E2E"},
			Description: "token usage and a cost estimate reach GenerationInfo",
			RealE2E:     true,
		},
		{
			ID:          CertAPIAuthFailureClassified,
			TestFile:    "pkg/adapters/anthropic/anthropic_real_test.go",
			TestName:    "TestAnthropicRealAuthFailureClassified",
			Env:         []string{"RUN_ANTHROPIC_REAL_E2E"},
			Description: "a real bad credential surfaces as a classified auth error",
			RealE2E:     true,
		},
		{
			ID:          CertAPIRateLimitClassified,
			TestFile:    "pkg/adapters/anthropic/anthropic_real_test.go",
			TestName:    "TestAnthropicRealRateLimitClassified",
			Env:         []string{"RUN_ANTHROPIC_REAL_E2E"},
			Description: "a real quota/429 response is classified as rate limiting",
			RealE2E:     true,
		},
	},
}

// APIProviderCertifications returns the registered proofs for one provider.
func APIProviderCertifications(provider Provider) []APIProviderCertification {
	provider = Provider(strings.ToLower(strings.TrimSpace(string(provider))))
	certs := append([]APIProviderCertification(nil), apiProviderCertifications[provider]...)
	sort.Slice(certs, func(i, j int) bool {
		if certs[i].ID == certs[j].ID {
			return certs[i].TestName < certs[j].TestName
		}
		return certs[i].ID < certs[j].ID
	})
	return certs
}

// MissingP0APICertifications returns the required IDs a provider has no
// registered proof for.
func MissingP0APICertifications(contract APIProviderContract) []APIProviderCertificationID {
	have := make(map[APIProviderCertificationID]struct{})
	for _, cert := range APIProviderCertifications(contract.Provider) {
		have[cert.ID] = struct{}{}
	}
	var missing []APIProviderCertificationID
	for _, id := range RequiredP0APICertificationIDs(contract) {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing
}

// knownAPICertificationGaps records providers whose P0 coverage is measured,
// acknowledged, and not yet written.
//
// This is the honest-ledger mechanism, mirroring knownCertificationGaps on the
// coding-agent side: a gap listed here is a TODO with a name, and the contract
// test below still fails on any gap NOT listed. What it must never become is a
// place to silence a provider that regressed -- entries are removed when the
// proof lands, and the test flags a stale allowance the moment it does.
//
// Measured 2026-08-19, before this contract existed:
//   - azure has no test files at all
//   - minimax has one test file and no live E2E
//   - openai has live E2E but none of it registered against these IDs yet
//
// None of these six are reachable from AgentWorks workflow steps today, which
// is why this is a ledger rather than a release blocker right now.
var knownAPICertificationGaps = map[Provider][]APIProviderCertificationID{
	ProviderOpenAI: {
		CertAPIPlainText, CertAPIToolCalling, CertAPIStructuredOutput,
		CertAPIUsageAccounting, CertAPIAuthFailureClassified,
		CertAPIRateLimitClassified, CertAPITransportLabel,
	},
	ProviderAzure: {
		CertAPIPlainText, CertAPIToolCalling, CertAPIStructuredOutput,
		CertAPIUsageAccounting, CertAPIAuthFailureClassified,
		CertAPIRateLimitClassified, CertAPITransportLabel,
	},
	ProviderBedrock: {
		CertAPIPlainText, CertAPIToolCalling, CertAPIStructuredOutput,
		CertAPIUsageAccounting, CertAPIAuthFailureClassified,
		CertAPIRateLimitClassified, CertAPITransportLabel,
	},
	ProviderMiniMax: {
		CertAPIPlainText, CertAPIToolCalling, CertAPIStructuredOutput,
		CertAPIUsageAccounting, CertAPIAuthFailureClassified,
		CertAPIRateLimitClassified, CertAPITransportLabel,
	},
	// Anthropic's transport-label proof is the one gap on an otherwise
	// fully-certified provider: the vertex equivalent
	// (TestVertexRealDeclaresAPITransportOnTerminalChunks) is written and
	// passing live, and the same test shape applies here directly.
	ProviderAnthropic: {CertAPITransportLabel},
}
