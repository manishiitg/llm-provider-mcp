package llmtypes

import "strings"

// CLISecurityMode describes how a coding CLI may access host filesystem state.
// Compatibility is the backward-compatible default for callers that do not
// explicitly select a mode.
type CLISecurityMode string

const (
	CLISecurityModeCompatibility CLISecurityMode = "compatibility"
	// CLISecurityModeIsolated is a MAJOR BLOCKER for shipping this as a real
	// product feature (both AgentWorks and SparkQuill/family-server let the
	// user freely switch the coding CLI provider — Claude Code, Codex CLI,
	// Cursor CLI, Pi CLI — at any time via settings), for two separate reasons:
	//
	//  1. Only codex-cli has real enforcement (internal/clisandbox.PrepareCodexCommand,
	//     macOS sandbox-exec). Claude Code, Cursor CLI, and Pi CLI all call
	//     ValidateCLISecurityLaunch with zero enforced modes, so requesting
	//     Isolated for them fails closed (safe) but breaks the product outright —
	//     there is no fallback/degrade path, just a raw error.
	//  2. Even where enforcement exists, this mode only isolates FILE-based
	//     credentials by redirecting $HOME to a private directory (this works
	//     for codex-cli: ~/.codex/auth.json is a plain file). It does NOT isolate
	//     macOS-Keychain-based credentials, because Keychain lookups are scoped
	//     to the OS login session, not $HOME. Confirmed live: Claude Code's
	//     session is 100% Keychain-only (service "Claude Code-credentials", no
	//     token file anywhere under ~/.claude/) — a sandboxed process with a
	//     fake $HOME still authenticates via the SAME real Keychain entry.
	//     Cursor CLI is a hybrid: the real access/refresh tokens are Keychain
	//     entries ("cursor-access-token"/"cursor-refresh-token"), but
	//     ~/.cursor/cli-config.json also carries file-based account-identity
	//     metadata (authInfo, authCacheKey) — untested whether a fresh private
	//     $HOME makes it re-prompt for login or silently falls through to the
	//     same real Keychain token.
	//
	// A product cannot honestly offer this mode until per-provider sandbox
	// enforcement exists for all four CLIs AND the Keychain-credential gap is
	// resolved (likely requires a genuinely separate OS user account, since
	// Keychain ACLs don't respect a private HOME directory the way file-based
	// auth does) — or the UI explicitly restricts which provider can be
	// selected while this mode is on. Tracked at a product level in GitHub
	// issue coding-agent-loop#142 ("certify Claude Code, Cursor CLI, and Pi
	// independently" is listed there as separate, not-yet-done follow-up work).
	CLISecurityModeIsolated CLISecurityMode = "isolated"
	CLISecurityModeVerified CLISecurityMode = "verified"
)

// NormalizeCLISecurityMode returns the canonical mode. Empty and unknown values
// resolve to compatibility so adding this field cannot break existing callers.
func NormalizeCLISecurityMode(mode CLISecurityMode) CLISecurityMode {
	switch CLISecurityMode(strings.ToLower(strings.TrimSpace(string(mode)))) {
	case CLISecurityModeIsolated:
		return CLISecurityModeIsolated
	case CLISecurityModeVerified:
		return CLISecurityModeVerified
	default:
		return CLISecurityModeCompatibility
	}
}

// CLISecurityPolicy is the immutable, resolved launch policy passed to a coding
// provider. It is produced by trusted application code; model/tool arguments
// must never be decoded directly into this type.
type CLISecurityPolicy struct {
	Mode                 CLISecurityMode `json:"mode"`
	Provider             string          `json:"provider"`
	ProfileVersion       string          `json:"profile_version,omitempty"`
	WorkspaceReadPaths   []string        `json:"workspace_read_paths,omitempty"`
	WorkspaceWritePaths  []string        `json:"workspace_write_paths,omitempty"`
	HostReadPaths        []string        `json:"host_read_paths,omitempty"`
	HostWritePaths       []string        `json:"host_write_paths,omitempty"`
	EnvironmentVariables []string        `json:"environment_variables,omitempty"`
	PrivateHome          string          `json:"private_home,omitempty"`
	ApprovedCapabilities []string        `json:"approved_capabilities,omitempty"`
}

// Clone returns a deep copy so a running session cannot observe later mutations
// to configuration slices owned by the caller.
func (p CLISecurityPolicy) Clone() CLISecurityPolicy {
	copyPolicy := p
	copyPolicy.Mode = NormalizeCLISecurityMode(p.Mode)
	copyPolicy.Provider = strings.ToLower(strings.TrimSpace(p.Provider))
	copyPolicy.ProfileVersion = strings.TrimSpace(p.ProfileVersion)
	copyPolicy.PrivateHome = strings.TrimSpace(p.PrivateHome)
	copyPolicy.WorkspaceReadPaths = append([]string(nil), p.WorkspaceReadPaths...)
	copyPolicy.WorkspaceWritePaths = append([]string(nil), p.WorkspaceWritePaths...)
	copyPolicy.HostReadPaths = append([]string(nil), p.HostReadPaths...)
	copyPolicy.HostWritePaths = append([]string(nil), p.HostWritePaths...)
	copyPolicy.EnvironmentVariables = append([]string(nil), p.EnvironmentVariables...)
	copyPolicy.ApprovedCapabilities = append([]string(nil), p.ApprovedCapabilities...)
	return copyPolicy
}
