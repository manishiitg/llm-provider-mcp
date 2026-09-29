package llmtypes

import (
	"path/filepath"
	"runtime"
	"strings"
)

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
	// LandlockRunner is the host's Landlock launcher (absolute path). With a
	// strict mode on Linux, the CLI starts under it: it can write only the
	// workspace write paths and PrivateHome, read only the granted paths and
	// the launcher's system baseline, and everything else is refused by the
	// kernel for the CLI and every process it starts.
	LandlockRunner string `json:"landlock_runner,omitempty"`
	// CredentialHome is where the CLI's login lives: the provider account's
	// home, or the server's HOME for the server account. Only the CLI's own
	// credential files are linked from it into PrivateHome; the rest of that
	// home (other people's sessions and history) stays outside the sandbox.
	CredentialHome string `json:"credential_home,omitempty"`
}

// LandlockEnforced reports whether this policy confines the CLI with the
// Landlock launcher on this host.
func (p *CLISecurityPolicy) LandlockEnforced() bool {
	return p != nil && runtime.GOOS == "linux" && strings.TrimSpace(p.LandlockRunner) != "" &&
		strings.TrimSpace(p.PrivateHome) != "" && NormalizeCLISecurityMode(p.Mode) != CLISecurityModeCompatibility
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
	copyPolicy.LandlockRunner = strings.TrimSpace(p.LandlockRunner)
	copyPolicy.CredentialHome = strings.TrimSpace(p.CredentialHome)
	return copyPolicy
}

// SandboxHomeEnvironment is the CLI's private home layout when the Landlock
// launcher confines it, else nil. It replaces the account's (or server's)
// home for the CLI and for every server-side reader of the CLI's files.
func SandboxHomeEnvironment(opts *CallOptions) map[string]string {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return nil
	}
	home := filepath.Clean(opts.CLISecurity.PrivateHome)
	return map[string]string{
		"HOME":              home,
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME":    filepath.Join(home, ".local", "state"),
		"CODEX_HOME":        filepath.Join(home, ".codex"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		// The shared /tmp is outside the sandbox; the CLI's temp files live
		// in its private home.
		"TMPDIR": filepath.Join(home, "tmp"),
	}
}

// LandlockEnforcedModes lists the policy's mode when the Landlock launcher
// enforces it, for ValidateCLISecurityLaunch: an adapter that wraps its
// launch accepts the strict mode, and still refuses it when it cannot.
func LandlockEnforcedModes(opts *CallOptions) []CLISecurityMode {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return nil
	}
	return []CLISecurityMode{NormalizeCLISecurityMode(opts.CLISecurity.Mode)}
}

// CLIHomeEnvironment is where the CLI keeps its files for this call: the
// sandbox's private home when confined, else the provider account's paths
// (empty for the server account, meaning the process HOME).
func CLIHomeEnvironment(opts *CallOptions) map[string]string {
	if env := SandboxHomeEnvironment(opts); env != nil {
		return env
	}
	return ProviderAccountEnvironment(opts)
}
