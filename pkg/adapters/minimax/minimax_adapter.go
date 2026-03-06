// Package minimax provides an adapter for MiniMax's API.
// MiniMax exposes an OpenAI-compatible endpoint at /v1/text/chatcompletion_v2.
// We use the OpenAI Go SDK with a URL-rewriting middleware to hit the correct path.
package minimax

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

const (
	// MiniMaxBaseURL is the base URL for MiniMax's API
	MiniMaxBaseURL = "https://api.minimax.io"
	// MiniMaxEndpointPath is the path for chat completions (OpenAI-compatible)
	MiniMaxEndpointPath = "/v1/text/chatcompletion_v2"
)

// MiniMaxAdapter implements the llmtypes.Model interface using MiniMax's
// OpenAI-compatible API at /v1/text/chatcompletion_v2.
type MiniMaxAdapter struct {
	client  *openai.Client
	modelID string
	logger  interfaces.Logger
}

// NewMiniMaxAdapter creates a new MiniMax adapter.
// A middleware rewrites all chat completion requests to MiniMax's endpoint path.
func NewMiniMaxAdapter(apiKey, modelID string, logger interfaces.Logger) *MiniMaxAdapter {
	client := openai.NewClient(
		option.WithAPIKey(apiKey),
		option.WithBaseURL(MiniMaxBaseURL),
		option.WithMiddleware(func(req *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
			// Rewrite to MiniMax's non-standard endpoint path
			req.URL.Path = MiniMaxEndpointPath
			return next(req)
		}),
	)
	return &MiniMaxAdapter{
		client:  &client,
		modelID: modelID,
		logger:  logger,
	}
}

// GetModelID implements the llmtypes.Model interface
func (m *MiniMaxAdapter) GetModelID() string {
	return m.modelID
}

// GetModelMetadata implements the llmtypes.Model interface
func (m *MiniMaxAdapter) GetModelMetadata(modelID string) (*llmtypes.ModelMetadata, error) {
	return GetMiniMaxModelMetadata(modelID)
}

// GenerateContent implements the llmtypes.Model interface
func (m *MiniMaxAdapter) GenerateContent(ctx context.Context, messages []llmtypes.MessageContent, options ...llmtypes.CallOption) (*llmtypes.ContentResponse, error) {
	opts := &llmtypes.CallOptions{}
	for _, opt := range options {
		opt(opts)
	}

	modelID := m.modelID
	if opts.Model != "" {
		modelID = opts.Model
	}

	openaiMessages := convertMessages(messages)

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(modelID),
		Messages: openaiMessages,
	}

	if opts.Temperature > 0 {
		params.Temperature = param.NewOpt(opts.Temperature)
	}
	if opts.MaxTokens > 0 {
		params.MaxTokens = param.NewOpt(int64(opts.MaxTokens))
	}

	if opts.JSONMode {
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &openai.ResponseFormatJSONObjectParam{},
		}
	}

	if len(opts.Tools) > 0 {
		params.Tools = convertTools(opts.Tools)
		if opts.ToolChoice != nil {
			if tc := convertToolChoice(opts.ToolChoice); tc != nil {
				params.ToolChoice = *tc
			}
		}
	}

	if m.logger != nil {
		m.logger.Debugf("[MINIMAX] GenerateContent model=%s messages=%d", modelID, len(messages))
	}

	if opts.StreamChan != nil {
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		}
		return m.generateStreaming(ctx, modelID, params, opts)
	}

	result, err := m.client.Chat.Completions.New(ctx, params)
	if err != nil {
		if m.logger != nil {
			m.logger.Errorf("[MINIMAX] API error: %v", err)
		}
		return nil, fmt.Errorf("minimax generate content: %w", err)
	}

	if m.logger != nil && len(result.Choices) > 0 {
		m.logger.Debugf("[MINIMAX] Response stop_reason=%s prompt_tokens=%d completion_tokens=%d total_tokens=%d",
			result.Choices[0].FinishReason, result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens)
		if result.Usage.PromptTokensDetails.CachedTokens > 0 {
			m.logger.Debugf("[MINIMAX] Cache details: cached_tokens=%d", result.Usage.PromptTokensDetails.CachedTokens)
		}
	}

	return convertResponse(result), nil
}

// Call implements a convenience method for simple text generation
func (m *MiniMaxAdapter) Call(ctx context.Context, prompt string, options ...llmtypes.CallOption) (string, error) {
	messages := []llmtypes.MessageContent{
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}},
		},
	}
	resp, err := m.GenerateContent(ctx, messages, options...)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return resp.Choices[0].Content, nil
}

// generateStreaming handles streaming responses
func (m *MiniMaxAdapter) generateStreaming(ctx context.Context, modelID string, params openai.ChatCompletionNewParams, opts *llmtypes.CallOptions) (*llmtypes.ContentResponse, error) {
	defer func() {
		if opts.StreamChan != nil {
			close(opts.StreamChan)
		}
	}()

	stream := m.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	var accumulatedContent strings.Builder
	var accumulatedToolCalls []llmtypes.ToolCall
	var finishReason string
	var usage *openai.CompletionUsage
	toolCallMap := make(map[int64]*llmtypes.ToolCall)

	for stream.Next() {
		chunk := stream.Current()

		// Capture usage from final chunk
		if chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
			u := chunk.Usage
			usage = &u
		}

		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finishReason = string(choice.FinishReason)
			}

			// Handle text delta
			if choice.Delta.Content != "" {
				accumulatedContent.WriteString(choice.Delta.Content)
				if opts.StreamChan != nil {
					select {
					case opts.StreamChan <- llmtypes.StreamChunk{
						Type:    llmtypes.StreamChunkTypeContent,
						Content: choice.Delta.Content,
					}:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			}

			// Handle tool call deltas
			for _, tcDelta := range choice.Delta.ToolCalls {
				idx := tcDelta.Index
				if toolCallMap[idx] == nil {
					toolCallMap[idx] = &llmtypes.ToolCall{
						ID:           tcDelta.ID,
						Type:         "function",
						FunctionCall: &llmtypes.FunctionCall{},
					}
				}
				tc := toolCallMap[idx]
				if tcDelta.ID != "" {
					tc.ID = tcDelta.ID
				}
				if tcDelta.Function.Name != "" {
					tc.FunctionCall.Name = tcDelta.Function.Name
				}
				if tcDelta.Function.Arguments != "" {
					tc.FunctionCall.Arguments += tcDelta.Function.Arguments
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("minimax streaming error: %w", err)
	}

	// Collect completed tool calls in order
	for i := int64(0); int(i) < len(toolCallMap); i++ {
		if tc, ok := toolCallMap[i]; ok {
			accumulatedToolCalls = append(accumulatedToolCalls, *tc)
		}
	}

	// Stream tool calls to channel
	if opts.StreamChan != nil {
		for _, tc := range accumulatedToolCalls {
			tcCopy := tc
			select {
			case opts.StreamChan <- llmtypes.StreamChunk{
				Type:     llmtypes.StreamChunkTypeToolCall,
				ToolCall: &tcCopy,
			}:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	if m.logger != nil && usage != nil {
		m.logger.Debugf("[MINIMAX] Stream complete stop_reason=%s prompt_tokens=%d completion_tokens=%d total_tokens=%d",
			finishReason, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
		if usage.PromptTokensDetails.CachedTokens > 0 {
			m.logger.Debugf("[MINIMAX] Cache details: cached_tokens=%d", usage.PromptTokensDetails.CachedTokens)
		}
	}

	choice := &llmtypes.ContentChoice{
		Content:    accumulatedContent.String(),
		StopReason: finishReason,
		ToolCalls:  accumulatedToolCalls,
	}

	var genInfo *llmtypes.GenerationInfo
	if usage != nil {
		inputTokens := int(usage.PromptTokens)
		outputTokens := int(usage.CompletionTokens)
		totalTokens := int(usage.TotalTokens)
		genInfo = &llmtypes.GenerationInfo{
			InputTokens:     &inputTokens,
			OutputTokens:    &outputTokens,
			TotalTokens:     &totalTokens,
			InputTokensCap:  &inputTokens,
			OutputTokensCap: &outputTokens,
		}
		if usage.PromptTokensDetails.CachedTokens > 0 {
			cachedTokens := int(usage.PromptTokensDetails.CachedTokens)
			genInfo.CachedContentTokens = &cachedTokens
		}
	}
	choice.GenerationInfo = genInfo

	usageInfo := llmtypes.ExtractUsageFromGenerationInfo(genInfo)
	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{choice},
		Usage:   usageInfo,
	}, nil
}

// convertMessages converts llmtypes messages to OpenAI message format
func convertMessages(langMessages []llmtypes.MessageContent) []openai.ChatCompletionMessageParamUnion {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(langMessages))

	for _, msg := range langMessages {
		var textParts []string
		var toolCallID string
		var toolResponseContent string
		var toolCalls []llmtypes.ToolCall

		for _, part := range msg.Parts {
			switch p := part.(type) {
			case llmtypes.TextContent:
				textParts = append(textParts, p.Text)
			case llmtypes.ToolCallResponse:
				toolCallID = p.ToolCallID
				toolResponseContent = p.Content
			case llmtypes.ToolCall:
				toolCalls = append(toolCalls, p)
			}
		}

		content := strings.Join(textParts, "\n")

		switch string(msg.Role) {
		case string(llmtypes.ChatMessageTypeSystem):
			msgs = append(msgs, openai.SystemMessage(content))

		case string(llmtypes.ChatMessageTypeHuman):
			msgs = append(msgs, openai.UserMessage(content))

		case string(llmtypes.ChatMessageTypeAI):
			if len(toolCalls) > 0 {
				openaiToolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(toolCalls))
				for _, tc := range toolCalls {
					openaiToolCalls = append(openaiToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
						OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
							ID:   tc.ID,
							Type: "function",
							Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
								Name:      tc.FunctionCall.Name,
								Arguments: tc.FunctionCall.Arguments,
							},
						},
					})
				}
				assistantMsg := openai.ChatCompletionAssistantMessageParam{
					ToolCalls: openaiToolCalls,
				}
				if content != "" {
					assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
						OfString: param.NewOpt(content),
					}
				}
				msgs = append(msgs, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistantMsg})
			} else {
				msgs = append(msgs, openai.AssistantMessage(content))
			}

		case string(llmtypes.ChatMessageTypeTool):
			if toolCallID != "" {
				msgs = append(msgs, openai.ToolMessage(toolResponseContent, toolCallID))
			}

		default:
			msgs = append(msgs, openai.UserMessage(content))
		}
	}

	return msgs
}

func convertTools(llmTools []llmtypes.Tool) []openai.ChatCompletionToolUnionParam {
	tools := make([]openai.ChatCompletionToolUnionParam, 0, len(llmTools))
	for _, tool := range llmTools {
		if tool.Function == nil {
			continue
		}
		var parameters shared.FunctionParameters
		if tool.Function.Parameters != nil {
			if b, err := json.Marshal(tool.Function.Parameters); err == nil {
				var paramsMap map[string]interface{}
				if err := json.Unmarshal(b, &paramsMap); err == nil {
					parameters = shared.FunctionParameters(paramsMap)
				}
			}
		}
		tools = append(tools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        tool.Function.Name,
			Description: param.NewOpt(tool.Function.Description),
			Parameters:  parameters,
		}))
	}
	return tools
}

func convertToolChoice(toolChoice interface{}) *openai.ChatCompletionToolChoiceOptionUnionParam {
	if toolChoice == nil {
		return nil
	}
	if choiceStr, ok := toolChoice.(string); ok {
		result := openai.ChatCompletionToolChoiceOptionUnionParam{
			OfAuto: param.NewOpt(choiceStr),
		}
		return &result
	}
	if tc, ok := toolChoice.(*llmtypes.ToolChoice); ok && tc != nil && tc.Function != nil && tc.Function.Name != "" {
		result := openai.ToolChoiceOptionFunctionToolChoice(openai.ChatCompletionNamedToolChoiceFunctionParam{
			Name: tc.Function.Name,
		})
		return &result
	}
	result := openai.ChatCompletionToolChoiceOptionUnionParam{
		OfAuto: param.NewOpt("auto"),
	}
	return &result
}

func convertResponse(result *openai.ChatCompletion) *llmtypes.ContentResponse {
	if result == nil || len(result.Choices) == 0 {
		return &llmtypes.ContentResponse{Choices: []*llmtypes.ContentChoice{}}
	}

	choice := result.Choices[0]
	llmChoice := &llmtypes.ContentChoice{
		Content:    choice.Message.Content,
		StopReason: string(choice.FinishReason),
	}

	if len(choice.Message.ToolCalls) > 0 {
		toolCalls := make([]llmtypes.ToolCall, 0, len(choice.Message.ToolCalls))
		for _, tc := range choice.Message.ToolCalls {
			toolCalls = append(toolCalls, llmtypes.ToolCall{
				ID:   tc.ID,
				Type: "function",
				FunctionCall: &llmtypes.FunctionCall{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		llmChoice.ToolCalls = toolCalls
	}

	inputTokens := int(result.Usage.PromptTokens)
	outputTokens := int(result.Usage.CompletionTokens)
	totalTokens := int(result.Usage.TotalTokens)
	genInfo := &llmtypes.GenerationInfo{
		InputTokens:     &inputTokens,
		OutputTokens:    &outputTokens,
		TotalTokens:     &totalTokens,
		InputTokensCap:  &inputTokens,
		OutputTokensCap: &outputTokens,
	}
	if result.Usage.PromptTokensDetails.CachedTokens > 0 {
		cachedTokens := int(result.Usage.PromptTokensDetails.CachedTokens)
		genInfo.CachedContentTokens = &cachedTokens
	}
	llmChoice.GenerationInfo = genInfo

	usageInfo := llmtypes.ExtractUsageFromGenerationInfo(genInfo)
	return &llmtypes.ContentResponse{
		Choices: []*llmtypes.ContentChoice{llmChoice},
		Usage:   usageInfo,
	}
}
