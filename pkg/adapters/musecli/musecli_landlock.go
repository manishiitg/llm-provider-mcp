package musecli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// museLandlockGrants are what a confined Muse launch needs outside its
// granted folders. Muse runs on a per-launch config folder
// (XDG_CONFIG_HOME=/tmp/agentworks-muse-config-*: its login, trust and
// settings with the MCP servers and hooks); it may write there, and it
// reads the programs and hook scripts that settings name.
func museLandlockGrants(args []string, env []string) (read, write []string) {
	configHome := ""
	for _, entry := range append(append([]string(nil), args...), env...) {
		if value, ok := strings.CutPrefix(entry, "XDG_CONFIG_HOME="); ok {
			configHome = value
		}
	}
	read = clisandbox.ArgFilePaths(args)
	if configHome != "" {
		settings := []string{filepath.Join(configHome, "muse", "settings.json")}
		read = append(read, clisandbox.MCPCommandPaths(settings)...)
		read = append(read, clisandbox.JSONFilePaths(settings)...)
		write = append(write, configHome)
	}
	if dir, err := museHookDir(); err == nil {
		read = append(read, dir)
	}
	return read, write
}

func museLandlockArgs(opts *llmtypes.CallOptions, args []string, workdir string) ([]string, func(), error) {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return args, func() {}, nil
	}
	read, write := museLandlockGrants(args, nil)
	wrapped, cleanup, err := clisandbox.LandlockArgs(opts.CLISecurity, args, workdir, read, write)
	if err != nil {
		return nil, func() {}, fmt.Errorf("confine Muse: %w", err)
	}
	return wrapped, cleanup, nil
}

func museLandlockCmd(opts *llmtypes.CallOptions, cmd *exec.Cmd, workdir string) (func(), error) {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return func() {}, nil
	}
	read, write := museLandlockGrants(cmd.Args, cmd.Env)
	cleanup, err := clisandbox.LandlockCmd(opts.CLISecurity, cmd, workdir, read, write)
	if err != nil {
		return func() {}, fmt.Errorf("confine Muse: %w", err)
	}
	return cleanup, nil
}
