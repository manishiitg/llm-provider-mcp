package agycli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/codingready"
)

// AgyCLIAdapter drives the Antigravity CLI (`agy`). Turns route by lane:
// persistent interactive runs inside the TUI sidecar's conversation
// (Builder chats); everything else uses the JSON exec lane (`agy -p
// --output-format json`, resumed with `--conversation`). Live input and
// control keys target the sidecar, which now carries the turn itself.
type AgyCLIAdapter struct {
	apiKey  string
	modelID string
	logger  interfaces.Logger
}

// NewAgyCLIAdapter builds an Antigravity adapter. Auth resolves in-CLI:
// stored Google login by default, GEMINI_API_KEY env when modelProvider is
// "gemini". apiKey is accepted for signature symmetry and never exported.
func NewAgyCLIAdapter(apiKey string, modelID string, logger interfaces.Logger) *AgyCLIAdapter {
	if modelID == "" {
		modelID = DefaultModelID
	}
	return &AgyCLIAdapter{apiKey: apiKey, modelID: modelID, logger: logger}
}

// GetModelID returns the configured model id.
func (a *AgyCLIAdapter) GetModelID() string { return a.modelID }

// GetModelMetadata returns metadata for an agy model id.
func (a *AgyCLIAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	return GetAgyModelMetadata(modelID)
}

// GenerateContent routes one agy turn by lane: persistent interactive +
// an owner session id runs the turn inside the TUI sidecar's own
// conversation (Builder chats: paste, await, extract, meter from the
// conversation .db); everything else stays on the headless JSON exec lane
// (steps/workflows). Both lanes fold system+human identically and share
// the mount manager, so the tool surface is the same either way. The
// sidecar needs a pre-trusted cwd (trust gates fail loudly, muse parity).
func (a *AgyCLIAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}
	if agyStringMetadata(opts, MetadataKeyPersistentInteractive) == "true" {
		return a.generateContentInteractive(ctx, messages, opts)
	}
	return a.generateContentExec(ctx, messages, opts)
}

// generateContentInteractive runs one turn in the owner's sidecar TUI:
// same prompt fold as exec, reply extracted from the pane, usage summed
// from the conversation .db steps the turn appended. Schema-mode and
// explicit effort are exec-only and fail loudly here rather than silently
// downgrading the turn.
func (a *AgyCLIAdapter) generateContentInteractive(ctx context.Context, messages []llmtypes.MessageContent, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	if err := llmtypes.ValidateCLISecurityLaunch(opts); err != nil {
		return nil, err
	}
	owner := agyStringMetadata(opts, MetadataKeyInteractiveSessionID)
	if owner == "" {
		return nil, fmt.Errorf("agy persistent interactive turn needs an owner session id (WithInteractiveSessionID)")
	}
	workdir := agyStringMetadata(opts, MetadataKeyWorkingDir)
	if workdir == "" {
		var err error
		workdir, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve agy working directory: %w", err)
		}
	}
	if schemaJSON, err := agyExecSchemaJSON(opts); err != nil {
		return nil, err
	} else if strings.TrimSpace(schemaJSON) != "" {
		return nil, fmt.Errorf("agy schema-mode turns are exec-only; persistent interactive does not support --json-schema")
	}
	if effort, err := agyExecEffort(opts); err != nil {
		return nil, err
	} else if strings.TrimSpace(effort) != "" {
		return nil, fmt.Errorf("agy explicit effort is exec-only; sidecar turns use the booted model's baked-in effort")
	}
	prompt, err := agyBuildExecPrompt(messages, llmtypes.CodingProviderLaunchSystemPromptFromOptions(opts))
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(a.modelID)
	if opts != nil && strings.TrimSpace(opts.Model) != "" {
		model = strings.TrimSpace(opts.Model)
	}
	if model == "" {
		model = DefaultModelID
	}
	mcpJSON := agyStringMetadata(opts, MetadataKeyMCPConfig)
	if mcpJSON != "" && opts != nil && opts.Metadata != nil && opts.Metadata.Custom != nil {
		if readyFile := codingready.MCPReadyFileFromMetadata(opts.Metadata.Custom); strings.TrimSpace(readyFile) != "" {
			_ = codingready.WaitForMCPReadyFile(ctx, readyFile, codingready.MCPReadyWait())
		}
	}
	resumeConversation := agyStringMetadata(opts, MetadataKeyResumeSessionID)
	session, err := ensureAgyInteractiveSessionForTurn(ctx, owner, workdir, model, mcpJSON, resumeConversation)
	if err != nil {
		return nil, err
	}
	reply, usage, toolCalls, err := runAgyInteractiveTurn(ctx, owner, prompt)
	if err != nil {
		return nil, err
	}
	if opts != nil && opts.StreamChan != nil {
		// Post-hoc tool events: the sidecar never streams live, so each
		// real invocation from the .db goes out as a Start,End pair in
		// completion order, ahead of the final content chunk.
		for _, call := range toolCalls {
			opts.StreamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallStart, ToolName: call.Name, ToolArgs: call.Args, ToolCallID: call.CallID}
			opts.StreamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallEnd, ToolName: call.Name, ToolCallID: call.CallID, ToolResult: call.ErrorText}
		}
		opts.StreamChan <- llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: reply}
	}
	gi := &llmtypes.GenerationInfo{}
	llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
		Provider:        "agy-cli",
		Transport:       llmtypes.CodingProviderTransportTmux,
		NativeSessionID: session.conversationID,
		TmuxSession:     session.tmuxSessionName,
		WorkingDir:      workdir,
		Model:           model,
	})
	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{{
			Content:        reply,
			StopReason:     "completed",
			GenerationInfo: gi,
		}},
		Usage: &usage,
	}, nil
}
