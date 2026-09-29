package picli

import (
	"fmt"
	"os/exec"
	"path/filepath"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// piLandlockReads: Pi starts from a generated launch script in its own temp
// folder (with its MCP config beside it), plus files named in argv and the
// programs those configs name.
func piLandlockReads(args []string) []string {
	read := clisandbox.ArgFilePaths(args)
	for _, file := range read {
		if filepath.Base(file) == "launch-pi.sh" {
			dir := filepath.Dir(file)
			read = append(read, dir)
			configs := clisandbox.ConfigDirFiles(dir)
			read = append(read, clisandbox.MCPCommandPaths(configs)...)
			read = append(read, clisandbox.JSONFilePaths(configs)...)
		}
	}
	return read
}

func piLandlockArgs(opts *llmtypes.CallOptions, args []string, workingDir string) ([]string, func(), error) {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return args, func() {}, nil
	}
	wrapped, cleanup, err := clisandbox.LandlockArgs(opts.CLISecurity, args, workingDir, piLandlockReads(args), nil)
	if err != nil {
		return nil, func() {}, fmt.Errorf("confine Pi: %w", err)
	}
	return wrapped, cleanup, nil
}

func piLandlockCmd(opts *llmtypes.CallOptions, cmd *exec.Cmd, workingDir string, runtimeDirs ...string) (func(), error) {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return func() {}, nil
	}
	cleanup, err := clisandbox.LandlockCmd(opts.CLISecurity, cmd, workingDir, piLandlockReads(cmd.Args), runtimeDirs)
	if err != nil {
		return func() {}, fmt.Errorf("confine Pi: %w", err)
	}
	return cleanup, nil
}
