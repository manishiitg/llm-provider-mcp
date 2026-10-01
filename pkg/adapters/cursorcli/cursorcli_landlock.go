package cursorcli

import (
	"path/filepath"

	"github.com/manishiitg/multi-llm-provider-go/internal/clisandbox"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// cursorLandlockReads are the files a confined Cursor launch needs outside
// its granted folders: files named in argv, the project configs Cursor loads
// from <cwd>/.cursor (MCP servers, hooks, permissions) and the programs and
// files they name, and this process's MCP bridge token folder.
func cursorLandlockReads(args []string, workingDir string) []string {
	configs := clisandbox.ConfigDirFiles(filepath.Join(workingDir, ".cursor"))
	read := clisandbox.ArgFilePaths(args)
	read = append(read, clisandbox.MCPCommandPaths(configs)...)
	read = append(read, clisandbox.JSONFilePaths(configs)...)
	return append(read, cursorBridgeTokenDirFor(workingDir))
}

func cursorLandlockArgs(opts *llmtypes.CallOptions, args []string, workingDir string) ([]string, func(), error) {
	if opts == nil {
		return args, func() {}, nil
	}
	return clisandbox.LandlockArgs(opts.CLISecurity, args, workingDir, cursorLandlockReads(args, workingDir), nil)
}
