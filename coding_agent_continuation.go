package llmproviders

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type CodingAgentContinuationErrorKind string

const (
	CodingAgentContinuationErrorNonApplicable  CodingAgentContinuationErrorKind = "non_applicable"
	CodingAgentContinuationErrorNonContinuable CodingAgentContinuationErrorKind = "non_continuable"
	CodingAgentContinuationErrorStaleHandle    CodingAgentContinuationErrorKind = "stale_handle"
)

type CodingAgentContinuationError struct {
	Kind     CodingAgentContinuationErrorKind
	Provider Provider
	Reason   string
}

func (e *CodingAgentContinuationError) Error() string {
	if e == nil {
		return ""
	}
	provider := strings.TrimSpace(string(e.Provider))
	if provider == "" {
		provider = "unknown"
	}
	if strings.TrimSpace(e.Reason) == "" {
		return fmt.Sprintf("coding-agent continuation %s for provider %s", e.Kind, provider)
	}
	return fmt.Sprintf("coding-agent continuation %s for provider %s: %s", e.Kind, provider, e.Reason)
}

func IsCodingAgentContinuationError(err error, kind CodingAgentContinuationErrorKind) bool {
	var continuationErr *CodingAgentContinuationError
	return errors.As(err, &continuationErr) && continuationErr.Kind == kind
}

// ContinueCodingAgentSession continues a provider-native coding-agent session
// using only the latest user message. It owns translating the opaque provider
// handle into provider-specific resume/workdir options; callers should not
// construct Claude/Codex/Gemini resume flags themselves.
func ContinueCodingAgentSession(ctx context.Context, model llmtypes.Model, handle llmtypes.CodingProviderSessionHandle, message string, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	if model == nil {
		return nil, &CodingAgentContinuationError{Kind: CodingAgentContinuationErrorNonContinuable, Reason: "model is nil"}
	}
	provider := Provider(strings.ToLower(strings.TrimSpace(handle.Provider)))
	if provider == "" {
		return nil, &CodingAgentContinuationError{Kind: CodingAgentContinuationErrorNonContinuable, Reason: "provider handle is missing provider"}
	}
	if _, ok := GetCodingAgentProviderContract(provider, strings.TrimSpace(handle.Model)); !ok {
		return nil, &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonApplicable,
			Provider: provider,
			Reason:   "provider is not a coding-agent provider",
		}
	}
	if strings.TrimSpace(message) == "" {
		return nil, &CodingAgentContinuationError{Kind: CodingAgentContinuationErrorNonContinuable, Provider: provider, Reason: "message is empty"}
	}
	if strings.TrimSpace(handle.NativeSessionID) == "" {
		return nil, &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: provider,
			Reason:   "provider handle is missing native session id",
		}
	}

	continuationOptions, err := appendCodingAgentContinuationOptions(provider, handle, options)
	if err != nil {
		return nil, err
	}
	messages := []llmtypes.MessageContent{
		llmtypes.TextPart(llmtypes.ChatMessageTypeHuman, message),
	}
	resp, err := model.GenerateContent(ctx, messages, continuationOptions...)
	if err == nil || !isCodingAgentContinuationRetryableTmuxLoss(err) {
		return resp, err
	}

	// A killed tmux pane can leave a process-local persistent-session registry
	// pointing at a dead pane for one call. Adapters clean up that stale mapping
	// when the first call fails; retry once so the continuation can start a fresh
	// tmux TUI with the provider-native resume id. Disable StreamChan on the
	// retry because the first failed GenerateContent owns closing it.
	retryOptions := append([]llmtypes.CallOption{}, continuationOptions...)
	retryOptions = append(retryOptions, llmtypes.WithStreamingChan(nil))
	resp, retryErr := model.GenerateContent(ctx, messages, retryOptions...)
	if retryErr != nil {
		return nil, fmt.Errorf("coding-agent continuation retry after tmux loss failed: first error: %w; retry error: %w", err, retryErr)
	}
	return resp, nil
}

// StartCodingAgentTransportSession starts or reacquires the provider's
// launchable coding-agent transport without sending a user prompt. The current
// launchable terminal transport is tmux; callers should gate on the persisted
// handle/contract transport instead of checking provider names.
func StartCodingAgentTransportSession(ctx context.Context, model llmtypes.Model, handle llmtypes.CodingProviderSessionHandle, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	if model == nil {
		return nil, &CodingAgentContinuationError{Kind: CodingAgentContinuationErrorNonContinuable, Reason: "model is nil"}
	}
	provider := Provider(strings.ToLower(strings.TrimSpace(handle.Provider)))
	if provider == "" {
		return nil, &CodingAgentContinuationError{Kind: CodingAgentContinuationErrorNonContinuable, Reason: "provider handle is missing provider"}
	}
	contract, ok := GetCodingAgentProviderContract(provider, strings.TrimSpace(handle.Model))
	if !ok {
		return nil, &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonApplicable,
			Provider: provider,
			Reason:   "provider is not a coding-agent provider",
		}
	}

	transport := contract.Transport
	if handleTransport := strings.TrimSpace(handle.Transport); handleTransport != "" {
		transport = CodingAgentTransport(handleTransport)
	}
	switch transport {
	case CodingAgentTransportTmux:
		return startCodingAgentTmuxTransportSession(ctx, model, provider, handle, options...)
	default:
		return nil, &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonApplicable,
			Provider: provider,
			Reason:   fmt.Sprintf("transport %q does not expose a launchable terminal session", transport),
		}
	}
}

// StartCodingAgentTmuxSession starts or reacquires a tmux-backed coding-agent
// TUI for a provider-native continuation handle without sending a user prompt.
func StartCodingAgentTmuxSession(ctx context.Context, model llmtypes.Model, handle llmtypes.CodingProviderSessionHandle, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	return StartCodingAgentTransportSession(ctx, model, handle, options...)
}

func startCodingAgentTmuxTransportSession(ctx context.Context, model llmtypes.Model, provider Provider, handle llmtypes.CodingProviderSessionHandle, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	if strings.TrimSpace(handle.NativeSessionID) == "" {
		return nil, &CodingAgentContinuationError{
			Kind:     CodingAgentContinuationErrorNonContinuable,
			Provider: provider,
			Reason:   "provider handle is missing native session id",
		}
	}

	launchOptions, err := appendCodingAgentContinuationOptions(provider, handle, options)
	if err != nil {
		return nil, err
	}
	launchOptions = append(launchOptions, llmtypes.WithCodingProviderLaunchOnly())
	return model.GenerateContent(ctx, nil, launchOptions...)
}

func appendCodingAgentContinuationOptions(provider Provider, handle llmtypes.CodingProviderSessionHandle, options []llmtypes.CallOption) ([]llmtypes.CallOption, error) {
	resumeID := strings.TrimSpace(handle.NativeSessionID)
	workingDir := strings.TrimSpace(handle.WorkingDir)
	projectDirID := strings.TrimSpace(handle.ProjectDirID)

	out := append([]llmtypes.CallOption{}, options...)
	switch provider {
	case ProviderClaudeCode:
		out = append(out, WithResumeSessionID(resumeID))
		if workingDir != "" {
			out = append(out, WithClaudeCodeWorkingDir(workingDir))
		}
	case ProviderCodexCLI:
		out = append(out, WithCodexResumeSessionID(resumeID))
		if workingDir != "" {
			out = append(out, WithCodexProjectDirID(workingDir))
		} else if projectDirID != "" {
			out = append(out, WithCodexProjectDirID(projectDirID))
		}
	case ProviderGeminiCLI:
		out = append(out, WithGeminiResumeSessionID(resumeID))
		if projectDirID != "" {
			out = append(out, WithGeminiProjectDirID(projectDirID))
		}
		if workingDir != "" {
			out = append(out, WithGeminiWorkingDir(workingDir))
		}
	case ProviderCursorCLI:
		out = append(out, WithCursorResumeSessionID(resumeID))
		if workingDir != "" {
			out = append(out, WithCursorWorkingDir(workingDir))
		}
	case ProviderOpenCodeCLI:
		return nil, &CodingAgentContinuationError{Kind: CodingAgentContinuationErrorNonContinuable, Provider: provider, Reason: "opencode-cli has no provider-native continuation handle yet"}
	default:
		return nil, &CodingAgentContinuationError{Kind: CodingAgentContinuationErrorNonApplicable, Provider: provider, Reason: "provider-native coding-agent continuation is not implemented"}
	}
	return out, nil
}

func isCodingAgentContinuationRetryableTmuxLoss(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"can't find pane",
		"can't find session",
		"no server running on",
		"target pane not found",
		"tmux session",
		"failed to capture",
		"failed to wait for",
		"timed out waiting for",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
