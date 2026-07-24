package llmtypes

import "strings"

// CLISecurityMode describes how a coding CLI may access host filesystem state.
// Compatibility is the backward-compatible default for callers that do not
// explicitly select a mode.
type CLISecurityMode string

const (
	CLISecurityModeCompatibility CLISecurityMode = "compatibility"
	CLISecurityModeIsolated      CLISecurityMode = "isolated"
	CLISecurityModeVerified      CLISecurityMode = "verified"
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
