package musecli

import (
	"context"
	"github.com/manishiitg/multi-llm-provider-go/internal/shelllaunch"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"os"
	"path/filepath"
)

type museAccountContextKey struct{}
type museAccountRuntime struct {
	opts   *llmtypes.CallOptions
	apiKey string
}

func museWithAccount(ctx context.Context, opts *llmtypes.CallOptions, apiKey string) context.Context {
	return context.WithValue(ctx, museAccountContextKey{}, museAccountRuntime{opts, apiKey})
}
func museAccount(ctx context.Context) museAccountRuntime {
	value, _ := ctx.Value(museAccountContextKey{}).(museAccountRuntime)
	return value
}
func museAccountDataHome(ctx context.Context) string {
	if root := llmtypes.ProviderAccountEnvironment(museAccount(ctx).opts)["XDG_DATA_HOME"]; root != "" {
		return root
	}
	return museXDGDataHome()
}
func museAccountConfig(ctx context.Context, mcpJSON string, tools []string) (string, func(), error) {
	source := ""
	if root := llmtypes.ProviderAccountEnvironment(museAccount(ctx).opts)["XDG_CONFIG_HOME"]; root != "" {
		source = filepath.Join(root, "muse", "settings.json")
	}
	return musePrepareIsolatedConfig(mcpJSON, tools, source)
}
func museAccountLaunch(ctx context.Context, argv []string, workdir string) (string, func(), error) {
	runtime := museAccount(ctx)
	env, unset := llmtypes.ScopedCodingAgentEnvironmentPlan(os.Environ(), nil, runtime.opts)
	// A selected key must override ambient login credentials without appearing in argv.
	if runtime.apiKey != "" {
		env = append(env, "META_API_KEY="+runtime.apiKey)
		unset = append(unset, "META_API_KEY")
	}
	return shelllaunch.CommandWithScopedEnv(argv, workdir, env, unset, nil)
}
