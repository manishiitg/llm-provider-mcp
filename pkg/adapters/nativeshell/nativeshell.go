// Package nativeshell holds the one switch that decides whether a coding CLI
// keeps its own built-in shell in Full mode.
//
// Owner decision 2026-10-05 (PLAT-491): the built-in shell runs as the app
// account with the platform's environment, so it is OFF by default. Shell work
// goes through the bridge shell (execute_shell_command), which runs as the
// user's slot. File read/edit, skills and the CLI's other native tools stay.
package nativeshell

import (
	"os"
	"strings"
)

// EnvVar turns the CLI's built-in shell back on for Full mode when set to on.
const EnvVar = "AGENTWORKS_CLI_NATIVE_SHELL"

// Enabled reports whether the escape hatch is set. The default is false.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar))) {
	case "on", "1", "true", "yes":
		return true
	}
	return false
}
