package llmtypes

import (
	"context"
	"errors"
	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
)

// NativeGoal is provider-owned state, not a host instruction to keep starting
// turns. Optional counters remain unknown when the native program omits them.
type NativeGoal struct {
	Objective       string `json:"objective"`
	Status          string `json:"status"`
	TokensUsed      *int64 `json:"tokens_used,omitempty"`
	TokenBudget     *int64 `json:"token_budget,omitempty"`
	TimeUsedSeconds *int64 `json:"time_used_seconds,omitempty"`
	Checks          *int   `json:"checks,omitempty"`
	LastCheck       string `json:"last_check,omitempty"`
}

type NativeGoalUpdate struct {
	Provider        string      `json:"provider"`
	NativeSessionID string      `json:"native_session_id"`
	Goal            *NativeGoal `json:"goal"`
	Reason          string      `json:"reason,omitempty"`
}

type NativeGoalCommand struct {
	Action    string `json:"action"`
	Objective string `json:"objective,omitempty"`
}

type nativeGoalCommandKey struct{}

// The host installs only an authenticated person's explicit command. Context
// keeps it out of saved prompts and out of instruction/launch fingerprints.
func WithNativeGoalCommand(ctx context.Context, command NativeGoalCommand) context.Context {
	return context.WithValue(ctx, nativeGoalCommandKey{}, command)
}

func NativeGoalCommandFromContext(ctx context.Context) (NativeGoalCommand, bool) {
	command, ok := ctx.Value(nativeGoalCommandKey{}).(NativeGoalCommand)
	return command, ok
}

type NativeGoalUnavailableError struct{ Program string }

func (e *NativeGoalUnavailableError) Error() string {
	return "Goal mode is not available for " + e.Program + "."
}

func (e *NativeGoalUnavailableError) Unwrap() error {
	return &llmerrors.Error{Kind: llmerrors.KindInvalidRequest, Provider: e.Program, Err: errors.New(e.Error())}
}

const StreamChunkTypeNativeGoal StreamChunkType = "native_goal"

func NativeGoalChunk(update NativeGoalUpdate) StreamChunk {
	return StreamChunk{Type: StreamChunkTypeNativeGoal, NativeGoal: &update}
}
