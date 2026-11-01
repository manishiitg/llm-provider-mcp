package musecli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

type museMSPItem struct {
	ItemID        string `json:"itemId"`
	Kind          string `json:"kind"`
	TurnID        string `json:"turnId"`
	Status        string `json:"status"`
	Text          string `json:"text"`
	Tool          string `json:"tool"`
	Args          string `json:"args"`
	VisibleOutput string `json:"visibleOutput"`
	FailureReason string `json:"failureReason"`
	TokensBefore  int    `json:"tokensBefore"`
	TokensAfter   int    `json:"tokensAfter"`
	Outcome       string `json:"outcome"`
	Reason        string `json:"reason"`
	Trigger       string `json:"trigger"`
}

type museMSPTokenUsage struct {
	TurnID       string `json:"turnId"`
	ModelID      string `json:"modelId"`
	PromptTokens int    `json:"promptTokens"`
	TotalTokens  int    `json:"totalTokens"`
	Usage        struct {
		InputTokens      int `json:"inputTokens"`
		OutputTokens     int `json:"outputTokens"`
		CacheReadTokens  int `json:"cacheReadTokens"`
		CacheWriteTokens int `json:"cacheWriteTokens"`
	} `json:"usage"`
}

// consumeMuseMSPTurn follows one turn to its end: text and reasoning stream as
// they are written, each tool call is reported when it starts and when it
// ends, and the host's questions and approvals are answered as the tmux lane
// answers them. turn/completed ends it.
func (a *MuseCLIAdapter) consumeMuseMSPTurn(ctx context.Context, s *museMSPSession, turnID string, opts *llmtypes.CallOptions, emit func(llmtypes.StreamChunk)) (*llmtypes.ContentResponse, error) {
	meta := func(extra map[string]interface{}) map[string]interface{} {
		m := map[string]interface{}{"muse_session_id": s.sessionID, "muse_cli_stream_source": "transcript"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	items := map[string]*museMSPItem{}
	toolStarts := map[string]time.Time{}
	var answers []string
	var lastAnswer string
	var input, output, cacheRead, cacheWrite int
	model := ""
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.rpc.done:
			return nil, s.rpc.failure()
		case msg := <-s.rpc.events:
			if len(msg.ID) > 0 && msg.Method != "" {
				a.answerMuseMSPRequest(ctx, s, opts, msg)
				continue
			}
			switch msg.Method {
			case "item/started", "item/updated", "item/completed":
				var p struct {
					Item museMSPItem `json:"item"`
				}
				if json.Unmarshal(msg.Params, &p) != nil || (p.Item.TurnID != "" && p.Item.TurnID != turnID) {
					continue
				}
				item := p.Item
				items[item.ItemID] = &item
				switch item.Kind {
				case "toolCall":
					if msg.Method == "item/started" {
						toolStarts[item.ItemID] = time.Now()
						emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallStart, ToolName: item.Tool, ToolCallID: item.ItemID, ToolArgs: item.Args, Metadata: meta(nil)})
					} else if msg.Method == "item/completed" {
						result := item.VisibleOutput
						if result == "" {
							result = item.FailureReason
						}
						chunk := llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeToolCallEnd, ToolCallID: item.ItemID, ToolResult: result, Metadata: meta(nil)}
						if started, ok := toolStarts[item.ItemID]; ok {
							chunk.ToolDuration = time.Since(started)
						}
						emit(chunk)
					}
				case "agentMessage":
					if msg.Method == "item/completed" && strings.TrimSpace(item.Text) != "" {
						lastAnswer = strings.TrimSpace(item.Text)
						answers = append(answers, lastAnswer)
					}
				case "compaction":
					if msg.Method == "item/completed" {
						emit(museMSPCompactionChunk(item))
					}
				}
			case "item/delta":
				var p struct {
					ItemID string `json:"itemId"`
					Field  string `json:"field"`
					Delta  string `json:"delta"`
				}
				if json.Unmarshal(msg.Params, &p) != nil || p.Delta == "" {
					continue
				}
				item := items[p.ItemID]
				if item == nil {
					continue
				}
				switch {
				case item.Kind == "agentMessage" && (p.Field == "" || p.Field == "text"):
					emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeContent, Content: p.Delta, Metadata: meta(map[string]interface{}{llmtypes.ContentDeltaMetadataKey: true})})
				case item.Kind == "reasoning":
					emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeReasoning, Content: p.Delta, Metadata: meta(map[string]interface{}{"thinking_active": true, llmtypes.ContentDeltaMetadataKey: true})})
				}
			case "session/tokenUsage":
				var p museMSPTokenUsage
				if json.Unmarshal(msg.Params, &p) == nil && (p.TurnID == "" || p.TurnID == turnID) {
					input += p.Usage.InputTokens
					output += p.Usage.OutputTokens
					cacheRead += p.Usage.CacheReadTokens
					cacheWrite += p.Usage.CacheWriteTokens
					if p.ModelID != "" {
						model = p.ModelID
					}
					status := s.statusLine(model, input, output, cacheRead, cacheWrite)
					emit(llmtypes.StreamChunk{Type: llmtypes.StreamChunkTypeStatusLine, StatusLine: status, Metadata: meta(nil)})
				}
			case "session/contextUsage":
				var p struct {
					UsedTokens   int `json:"usedTokens"`
					WindowTokens int `json:"windowTokens"`
				}
				if json.Unmarshal(msg.Params, &p) == nil {
					s.mu.Lock()
					s.context = museMSPContext{used: p.UsedTokens, window: p.WindowTokens}
					s.mu.Unlock()
				}
			case "usage/changed":
				var p museMSPPlanUsage
				if json.Unmarshal(msg.Params, &p) == nil {
					s.mu.Lock()
					s.usage = &p
					s.mu.Unlock()
				}
			case "turn/completed":
				var p struct {
					TurnID   string `json:"turnId"`
					Terminal string `json:"terminal"`
					Reason   string `json:"reason"`
					Error    *struct {
						Kind    string `json:"kind"`
						Message string `json:"message"`
						Quota   *struct {
							LimitKind  string `json:"limitKind"`
							ResetsAtMs int64  `json:"resetsAtMs"`
						} `json:"quota"`
					} `json:"error"`
				}
				if json.Unmarshal(msg.Params, &p) != nil || p.TurnID != turnID {
					continue
				}
				switch p.Terminal {
				case "completed":
				case "cancelled":
					return nil, context.Canceled
				default:
					if p.Error != nil && p.Error.Quota != nil {
						// The host says it is the quota wall, with its reset time.
						return nil, &llmerrors.Error{Kind: llmerrors.KindQuotaExhausted, Provider: "muse-cli", Model: a.modelID, RetryAt: time.UnixMilli(p.Error.Quota.ResetsAtMs).UTC(), Err: fmt.Errorf("Muse usage limit reached (%s)", p.Error.Quota.LimitKind)}
					}
					reason := p.Reason
					if p.Error != nil && p.Error.Message != "" {
						reason = p.Error.Message
					}
					return nil, fmt.Errorf("muse turn %s: %s", p.Terminal, strings.TrimSpace(reason))
				}
				return a.museMSPResponse(s, lastAnswer, strings.Join(answers, "\n\n"), model, input, output, cacheRead, cacheWrite), nil
			}
		}
	}
}

// answerMuseMSPRequest answers what the host asks of the client. Approvals get
// today's answer: approved (the session runs with allowAll; Muse's own shell
// and writes are off under a tool allowlist), never a new person-facing gate.
// Questions go to the chat's question card when the chat lets its person
// choose, else the first option is taken, as the tmux lane does.
func (a *MuseCLIAdapter) answerMuseMSPRequest(ctx context.Context, s *museMSPSession, opts *llmtypes.CallOptions, msg museRPCMessage) {
	_ = s.rpc.reply(msg.ID, map[string]any{})
	switch msg.Method {
	case "approval/request":
		var p struct {
			ApprovalID       string `json:"approvalId"`
			AvailableChoices []struct {
				ChoiceID string `json:"choiceId"`
				Decision string `json:"decision"`
			} `json:"availableChoices"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		for _, choice := range p.AvailableChoices {
			if choice.Decision == "approved" {
				_ = s.rpc.call(ctx, "approval/decide", map[string]any{"commandId": museCommandID(), "approvalId": p.ApprovalID, "choiceId": choice.ChoiceID}, nil)
				return
			}
		}
	case "userInput/request":
		var p struct {
			UserInputID string     `json:"userInputId"`
			Questions   []Question `json:"questions"`
		}
		if json.Unmarshal(msg.Params, &p) != nil {
			return
		}
		if museMSPUserChoice(opts) {
			// The chat's question card answers it; if the person has not
			// answered by museUserChoiceWait, the first option is taken.
			go func(id string, questions []Question) {
				select {
				case <-time.After(museUserChoiceWait):
				case <-s.rpc.done:
					return
				}
				if prompt, err := pendingQuestionPrompt(s.owner); err == nil && prompt != nil && prompt.PromptID == id {
					_ = museMSPAnswer(context.Background(), s, id, FirstOptionAnswers(questions))
				}
			}(p.UserInputID, p.Questions)
			return
		}
		_ = museMSPAnswer(ctx, s, p.UserInputID, FirstOptionAnswers(p.Questions))
	}
}

func museMSPAnswer(ctx context.Context, s *museMSPSession, userInputID string, answers []QuestionAnswer) error {
	wire := make([]map[string]any, 0, len(answers))
	for _, answer := range answers {
		entry := map[string]any{"questionId": answer.ID}
		if len(answer.SelectedLabels) > 0 {
			entry["selectedLabels"] = answer.SelectedLabels
		} else {
			entry["selectedLabel"] = answer.SelectedLabel
		}
		wire = append(wire, entry)
	}
	return s.rpc.call(ctx, "userInput/answer", map[string]any{"commandId": museCommandID(), "sessionId": s.sessionID, "userInputId": userInputID, "answers": wire}, nil)
}

func museMSPCompactionChunk(item museMSPItem) llmtypes.StreamChunk {
	now := time.Now().UTC()
	end := llmtypes.ContextCompaction{Provider: "muse-cli", Phase: llmtypes.ContextCompactionPhaseEnd, ID: item.ItemID, Trigger: item.Trigger, TokensBefore: item.TokensBefore, TokensAfter: item.TokensAfter, EndedAt: now, Outcome: llmtypes.ContextCompactionOutcomeSuccess}
	if item.Outcome != "" && item.Outcome != "compacted" {
		end.Outcome = llmtypes.ContextCompactionOutcomeFailed
	}
	return llmtypes.ContextCompactionChunk(end)
}

func (a *MuseCLIAdapter) museMSPResponse(s *museMSPSession, answer, turnText, model string, input, output, cacheRead, cacheWrite int) *llmtypes.ContentResponse {
	prompt := input + cacheRead + cacheWrite
	total := prompt + output
	gi := &llmtypes.GenerationInfo{Additional: map[string]interface{}{"provider": "muse-cli", "muse_cli_mode": "msp", "muse_session_id": s.sessionID}}
	if total > 0 {
		gi.PromptTokens, gi.CompletionTokens, gi.TotalTokens = &prompt, &output, &total
		if cacheRead > 0 {
			gi.CachedContentTokens = &cacheRead
			gi.Additional["cache_read_input_tokens"] = cacheRead
		}
		if cacheWrite > 0 {
			gi.Additional["cache_creation_input_tokens"] = cacheWrite
		}
	}
	if model == "" {
		model = a.modelID
	}
	s.mu.Lock()
	if windows := s.windowsLocked(); len(windows) > 0 {
		gi.Additional[llmtypes.RateLimitWindowsMetaKey] = windows
	}
	s.mu.Unlock()
	if turnText != "" && turnText != answer {
		gi.Additional["assistant_turn_text"] = turnText
	}
	llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{
		Provider:        "muse-cli",
		Transport:       llmtypes.CodingProviderTransportMSP,
		NativeSessionID: s.sessionID,
		WorkingDir:      s.workdir,
		Model:           model,
		Status:          llmtypes.CodingProviderSessionStatusIdle,
	})
	return &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{Content: answer, StopReason: "stop", GenerationInfo: gi}}}
}

// compactMuseMSP runs one compaction on the chat's session: an idle
// compaction, or Compact now.
func (a *MuseCLIAdapter) compactMuseMSP(ctx context.Context, s *museMSPSession, emit func(llmtypes.StreamChunk)) (*llmtypes.ContentResponse, error) {
	started := time.Now().UTC()
	id := fmt.Sprintf("muse-compact-%d", started.UnixMilli())
	emit(llmtypes.ContextCompactionChunk(llmtypes.ContextCompaction{Provider: "muse-cli", Phase: llmtypes.ContextCompactionPhaseStart, ID: id, StartedAt: started}))
	if err := s.rpc.call(ctx, "session/compact", map[string]any{"commandId": museCommandID(), "sessionId": s.sessionID}, nil); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.rpc.done:
			return nil, s.rpc.failure()
		case msg := <-s.rpc.events:
			if msg.Method != "item/completed" {
				continue
			}
			var p struct {
				Item museMSPItem `json:"item"`
			}
			if json.Unmarshal(msg.Params, &p) != nil || p.Item.Kind != "compaction" {
				continue
			}
			end := museMSPCompactionChunk(p.Item)
			emit(end)
			result := llmtypes.IdleCompactResult{Compacted: p.Item.Outcome == "compacted", Reason: llmtypes.IdleCompactReasonCompacted, TokensBefore: p.Item.TokensBefore, TokensAfter: p.Item.TokensAfter}
			if !result.Compacted {
				result.Reason = llmtypes.IdleCompactReasonNoSession
				if p.Item.Outcome == "failed" || p.Item.Outcome == "cancelled" {
					return nil, fmt.Errorf("Muse compaction %s: %s", p.Item.Outcome, p.Item.Reason)
				}
			}
			gi := &llmtypes.GenerationInfo{Additional: map[string]interface{}{"provider": "muse-cli", "muse_cli_mode": "msp", "muse_session_id": s.sessionID}}
			llmtypes.AttachIdleCompactResult(gi, result)
			llmtypes.AttachCodingProviderSessionHandle(gi, llmtypes.CodingProviderSessionHandle{Provider: "muse-cli", Transport: llmtypes.CodingProviderTransportMSP, NativeSessionID: s.sessionID, WorkingDir: s.workdir, Status: llmtypes.CodingProviderSessionStatusIdle})
			return &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{{GenerationInfo: gi}}}, nil
		}
	}
}

// SendMuseMSPInput adds a message to the chat's running turn (turn/steer).
func SendMuseMSPInput(ctx context.Context, owner, message string) error {
	s := museMSPSessionFor(owner)
	if s == nil {
		return fmt.Errorf("no Muse serve session for %s", owner)
	}
	s.mu.Lock()
	turnID, sessionID := s.turnID, s.sessionID
	s.mu.Unlock()
	if turnID == "" {
		return fmt.Errorf("%w: no Muse turn is running", llmtypes.ErrCodingAgentInputNotSent)
	}
	if err := s.rpc.call(ctx, "turn/steer", map[string]any{"commandId": museCommandID(), "sessionId": sessionID, "expectedTurnId": turnID, "input": []map[string]any{{"type": "text", "text": message}}}, nil); err != nil {
		return err
	}
	s.mu.Lock()
	if s.steered == nil {
		s.steered = map[string]time.Time{}
	}
	s.steered[message] = time.Now()
	s.mu.Unlock()
	return nil
}

// InterruptMuseMSP stops the chat's running turn.
func InterruptMuseMSP(ctx context.Context, owner string) error {
	s := museMSPSessionFor(owner)
	if s == nil {
		return fmt.Errorf("no Muse serve session for %s", owner)
	}
	s.mu.Lock()
	turnID, sessionID := s.turnID, s.sessionID
	s.mu.Unlock()
	if turnID == "" {
		return nil
	}
	return s.rpc.call(ctx, "turn/interrupt", map[string]any{"commandId": museCommandID(), "sessionId": sessionID, "turnId": turnID}, nil)
}

// MuseMSPSessionActive reports whether a chat runs on `muse serve` now.
func MuseMSPSessionActive(owner string) bool { return museMSPSessionFor(owner) != nil }

// MuseMSPUsage is the chat's plan windows and context as its host last
// reported them; ok is false when the chat runs no serve session.
func MuseMSPUsage(owner string) (windows []llmtypes.RateLimitWindow, contextUsed, contextWindow int, ok bool) {
	s := museMSPSessionFor(owner)
	if s == nil {
		return nil, 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.windowsLocked(), s.context.used, s.context.window, true
}

// windowsLocked turns the host's plan usage into the shared window shape.
func (s *museMSPSession) windowsLocked() []llmtypes.RateLimitWindow {
	u := s.usage
	if u == nil {
		return nil
	}
	var windows []llmtypes.RateLimitWindow
	if u.Window != nil {
		name := "five_hour"
		if m := u.Window.WindowDurationMins; m > 0 && m != 300 {
			name = fmt.Sprintf("%d_minute", m)
		}
		windows = append(windows, llmtypes.RateLimitWindow{Name: name, UsedPercent: float64(u.Window.UsedPercent), ResetsAt: time.UnixMilli(u.Window.ResetsAtMs).UTC()})
	}
	if u.Weekly != nil {
		windows = append(windows, llmtypes.RateLimitWindow{Name: "seven_day", UsedPercent: float64(u.Weekly.UsedPercent), ResetsAt: time.UnixMilli(u.Weekly.ResetsAtMs).UTC()})
	}
	return windows
}

func (s *museMSPSession) statusLine(model string, input, output, cacheRead, cacheWrite int) *llmtypes.StatusLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := &llmtypes.StatusLine{Provider: "muse-cli", Model: model, InputTokens: input, OutputTokens: output, CacheReadInputTokens: cacheRead, CacheCreationInputTokens: cacheWrite, Metadata: map[string]interface{}{"muse_session_id": s.sessionID}}
	if s.context.window > 0 {
		status.SetContextUsage(s.context.used, s.context.window)
	}
	if windows := s.windowsLocked(); len(windows) > 0 {
		status.Metadata[llmtypes.RateLimitWindowsMetaKey] = windows
	}
	return status
}

func museMSPUserChoice(opts *llmtypes.CallOptions) bool {
	if opts == nil || opts.Metadata == nil {
		return false
	}
	choice, _ := opts.Metadata.Custom[metadataMuseUserChoice].(bool)
	return choice
}
