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
	//  2. Credentials held in the macOS login keychain cannot be granted
	//     selectively. Denying the home directory also denies
	//     ~/Library/Keychains/login.keychain-db, so keychain lookups fail
	//     outright — measured, not assumed: `security find-generic-password -s
	//     "Claude Code-credentials"` succeeds unsandboxed and returns "could not
	//     be found" under a profile that denies home. Claude Code stores its
	//     session ONLY there (no token file anywhere under ~/.claude/), so it
	//     simply cannot authenticate under either strict mode. Granting it back
	//     is all-or-nothing: that one file also holds every other secret the
	//     user owns (Wi-Fi, Safari passwords, ...), and filesystem ACLs cannot
	//     expose a single keychain item. This is unlike ~/.codex/auth.json,
	//     which is a scoped, single-purpose file safe to grant via
	//     HostReadPaths. Cursor CLI is a hybrid — tokens in the keychain
	//     ("cursor-access-token"/"cursor-refresh-token") but file-based account
	//     identity in ~/.cursor/cli-config.json — and is untested.
	//
	// NOTE ON MODE CHOICE: this reason does NOT argue for Isolated over
	// Verified. Isolated (private $HOME + a fresh login inside it) buys ACCOUNT
	// separation, which is rarely the goal — a user normally WANTS the CLI on
	// their own subscription. The usual goal is FILESYSTEM isolation, and
	// Verified serves it directly: keep the real $HOME, deny it wholesale, then
	// grant back only the CLI's own credential directory. Reach for Verified
	// first; Isolated only when a genuinely separate account is required.
	//
	// A product cannot honestly offer either strict mode until per-provider
	// sandbox enforcement exists for all four CLIs (reason 1), and Claude Code
	// additionally needs a credential path that is not the shared login
	// keychain — otherwise a separate OS user account, whose keychain is its
	// own. Tracked at a product level in GitHub issue coding-agent-loop#142
	// ("certify Claude Code, Cursor CLI, and Pi independently" is listed there
	// as separate, not-yet-done follow-up work).
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
