package cursorcli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/internal/nativeinput"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type cursorACPTool struct {
	name, args, result        string
	started                   time.Time
	emitted, ended, delivered bool
}

func (c *CursorCLIAdapter) consumeACPTurn(ctx context.Context, s *cursorACPSession, r *cursorACP, prompt string, opts *llmtypes.CallOptions, emit func(llmtypes.StreamChunk)) (*llmtypes.ContentResponse, error) {
	var requests nativeinput.Requests
	defer requests.Close()
	meta := map[string]any{"cursor_session_id": s.native, "cursor_stream_source": "acp"}
	var text strings.Builder
	tools := map[string]*cursorACPTool{}
	// ACP reports only success for MCP results. Read the same native session's
	// committed tool records, keyed by its exact tool IDs, for complete receipts.
	home, _ := os.UserHomeDir()
	for _, e := range r.cmd.Env {
		if strings.HasPrefix(e, "HOME=") {
			home = strings.TrimPrefix(e, "HOME=")
		}
	}
	storePath := filepath.Join(home, ".cursor", "acp-sessions", s.native, "store.db")
	for _, e := range r.cmd.Env {
		if strings.HasPrefix(e, "XDG_CONFIG_HOME=") && strings.TrimPrefix(e, "XDG_CONFIG_HOME=") != "" {
			storePath = filepath.Join(strings.TrimPrefix(e, "XDG_CONFIG_HOME="), "cursor", "acp-sessions", s.native, "store.db")
		}
	}
	for _, e := range r.cmd.Env {
		if strings.HasPrefix(e, "CURSOR_CONFIG_DIR=") && strings.TrimPrefix(e, "CURSOR_CONFIG_DIR=") != "" {
			storePath = filepath.Join(strings.TrimPrefix(e, "CURSOR_CONFIG_DIR="), "acp-sessions", s.native, "store.db")
		}
	}
	_, oldRefs := cursorStoreMessagesExcept(storePath, nil)
	baseline := map[string]struct{}{}
	for _, ref := range oldRefs {
		baseline[ref] = struct{}{}
	}
	messages := []llmtypes.MessageContent{}
	flushTools := func(final bool) {
		pending := final
		for _, t := range tools {
			if t.ended && !t.delivered {
				pending = true
			}
		}
		if !pending {
			return
		}
		current := readCursorStoreDBMessagesAfter(storePath, baseline)
		committed := map[string]string{}
		for _, m := range current {
			for _, p := range m.Parts {
				if out, ok := p.(llmtypes.ToolCallResponse); ok {
					committed[out.ToolCallID] = out.Content
				}
			}
		}
		for id, t := range tools {
			if !t.ended || t.delivered {
				continue
			}
			if value, ok := committed[id]; ok {
				t.result = value
			} else if !final {
				continue
			}
			t.delivered = true
			emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallEnd, ToolCallID: id, ToolName: t.name, ToolArgs: t.args, ToolResult: t.result, ToolDuration: time.Since(t.started), Metadata: meta})
		}
		if final {
			messages = current
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var usage *llmtypes.Usage
	var result struct {
		StopReason string `json:"stopReason"`
		Usage      *struct {
			Input  int `json:"inputTokens"`
			Output int `json:"outputTokens"`
			Total  int `json:"totalTokens"`
			Cached int `json:"cachedReadTokens"`
		} `json:"usage"`
	}
	complete := make(chan error, 1)
	go func() {
		complete <- r.call(ctx, "session/prompt", map[string]any{"sessionId": s.native, "prompt": []any{map[string]any{"type": "text", "text": prompt}}}, &result)
	}()
	// This marks submission to the native protocol, not a durable steer receipt.
	emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypePromptAccepted, Metadata: meta})
	handle := func(msg cursorACPMessage) error {
		if len(msg.ID) > 0 {
			requests.Start(ctx, string(msg.ID), func(questionCtx context.Context) {
				reply, err := cursorNativeQuestion(questionCtx, msg.Method, msg.Params, s.native, cursorACPBool(opts, MetadataKeyAutoApproveWebSearch))
				out := map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": reply}
				if err != nil {
					delete(out, "result")
					code := -32602
					if strings.HasPrefix(err.Error(), "unsupported Cursor ACP request") {
						code = -32601
					}
					out["error"] = map[string]any{"code": code, "message": err.Error()}
				}
				if questionCtx.Err() == nil {
					if err = r.writeContext(questionCtx, out); err != nil {
						r.finish(err)
					}
				}
			})
			return nil
		}
		if msg.Method != "session/update" {
			return nil
		}
		var envelope struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				Kind       string          `json:"sessionUpdate"`
				Content    json.RawMessage `json:"content"`
				ToolCallID string          `json:"toolCallId"`
				Title      string          `json:"title"`
				Status     string          `json:"status"`
				RawInput   json.RawMessage `json:"rawInput"`
				RawOutput  json.RawMessage `json:"rawOutput"`
				Used       int             `json:"used"`
				Size       int             `json:"size"`
			} `json:"update"`
		}
		if err := json.Unmarshal(msg.Params, &envelope); err != nil {
			return err
		}
		if envelope.SessionID != s.native {
			return nil
		}
		u := envelope.Update
		switch u.Kind {
		case "agent_message_chunk", "agent_thought_chunk":
			var content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(u.Content, &content)
			if content.Type != "text" {
				return nil
			}
			kind := llmtypes.StreamChunkTypeContent
			if u.Kind == "agent_thought_chunk" {
				kind = llmtypes.StreamChunkTypeReasoning
			} else {
				text.WriteString(content.Text)
			}
			delta := map[string]any{"cursor_session_id": s.native, "cursor_stream_source": "acp", llmtypes.ContentDeltaMetadataKey: true}
			emit(llmtypes.StreamChunk{Type: kind, Content: content.Text, Metadata: delta})
		case "tool_call", "tool_call_update":
			t := tools[u.ToolCallID]
			if t == nil {
				t = &cursorACPTool{started: time.Now()}
				tools[u.ToolCallID] = t
			}
			if u.Title != "" && !strings.HasPrefix(t.name, "mcp__") {
				t.name = u.Title
			}
			if len(u.RawInput) > 0 && string(u.RawInput) != "{}" {
				t.args = string(u.RawInput)
				var mcp struct {
					Server string          `json:"providerIdentifier"`
					Name   string          `json:"toolName"`
					Args   json.RawMessage `json:"args"`
				}
				if json.Unmarshal(u.RawInput, &mcp) == nil && mcp.Server != "" && mcp.Name != "" {
					t.name = "mcp__" + mcp.Server + "__" + mcp.Name
					t.args = string(mcp.Args)
				}
			}
			if !t.emitted && (u.Status == "in_progress" || u.Status == "completed" || u.Status == "failed") {
				t.emitted = true
				if t.args == "" {
					t.args = "{}"
				}
				emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallStart, ToolCallID: u.ToolCallID, ToolName: t.name, ToolArgs: t.args, Metadata: meta})
			}
			if len(u.RawOutput) > 0 {
				t.result = string(u.RawOutput)
			} else if len(u.Content) > 0 {
				t.result = string(u.Content)
			}
			if !t.ended && (u.Status == "completed" || u.Status == "failed") {
				t.ended = true
				// Completion is emitted after native committed output is joined below.
			}
		case "usage_update":
			status := &llmtypes.StatusLine{Provider: "cursor-cli", Model: s.model, Metadata: meta}
			if u.Size > 0 {
				status.SetContextUsage(u.Used, u.Size)
			}
			s.mu.Lock()
			s.status = status
			s.mu.Unlock()
			emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeStatusLine, StatusLine: status, Metadata: meta})
		}
		return nil
	}
	for {
		select {
		case msg := <-r.events:
			if err := handle(msg); err != nil {
				return nil, err
			}
		case err := <-complete:
			if err != nil {
				return nil, err
			}
			// The reader queues all preceding events before dispatching the response.
			for len(r.events) > 0 {
				if err = handle(<-r.events); err != nil {
					return nil, err
				}
			}
			if result.StopReason == "cancelled" {
				return nil, fmt.Errorf("Cursor turn cancelled")
			}
			if result.Usage != nil {
				u := result.Usage
				usage = &llmtypes.Usage{InputTokens: u.Input, OutputTokens: u.Output, TotalTokens: u.Total, CacheTokens: &u.Cached}
			}
			flushTools(true)
			if len(tools) == 0 {
				if err := cursorACPPlanFailure(text.String(), s.model); err != nil {
					return nil, err
				}
			}
			if strings.TrimSpace(text.String()) == "" && len(tools) == 0 {
				return nil, fmt.Errorf("Cursor returned no answer (stop reason: %s)", result.StopReason)
			}
			response := cursorACPResponse(s, text.String(), usage)
			llmtypes.AttachCodingProviderIntermediateMessages(response.Choices[0].GenerationInfo, llmtypes.CodingProviderIntermediateMessages{Provider: "cursor-cli", Transport: "acp", Messages: messages})
			return response, nil
		case <-ticker.C:
			flushTools(false)
		case <-ctx.Done():
			r.close()
			return nil, ctx.Err()
		case <-r.done:
			return nil, r.failure()
		}
	}
}

func cursorACPPlanFailure(text, model string) error {
	// Cursor emits this standalone account refusal as a normal message chunk.
	// Do not classify ordinary explanations or quoted discussions of plans.
	if strings.TrimSpace(text) != "Upgrade your plan to continue" {
		return nil
	}
	return &llmerrors.Error{Kind: llmerrors.KindBilling, Provider: "cursor-cli", Model: model,
		Err: fmt.Errorf("This Cursor account needs a plan upgrade to continue. Check its plan or select another Cursor account.")}
}
func (c *CursorCLIAdapter) GetStatusLine(ctx context.Context, owner string) (*llmtypes.StatusLine, error) {
	cursorACPSessions.Lock()
	s := cursorACPSessions.sessions[owner]
	cursorACPSessions.Unlock()
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status == nil {
		return nil, nil
	}
	copy := *s.status
	return &copy, nil
}
