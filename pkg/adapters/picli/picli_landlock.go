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

// piLandlockWrites is the launch folder itself: Pi's marker extension there appends to
// markers.jsonl, so read access alone made Pi exit at start with "Failed to load extension ...
// EACCES ... markers.jsonl" once the server's temp folder moved out of the shared /tmp
// (Confida 2026-09-30). The folder is created for this one launch and removed after it.
func piLandlockWrites(args []string) []string {
	var write []string
	for _, file := range clisandbox.ArgFilePaths(args) {
		if filepath.Base(file) == "launch-pi.sh" {
			write = append(write, filepath.Dir(file))
		}
	}
	return write
}

func piLandlockArgs(opts *llmtypes.CallOptions, args []string, workingDir string) ([]string, func(), error) {
	if opts == nil || !opts.CLISecurity.LandlockEnforced() {
		return args, func() {}, nil
	}
	wrapped, cleanup, err := clisandbox.LandlockArgs(opts.CLISecurity, args, workingDir, piLandlockReads(args), piLandlockWrites(args))
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
