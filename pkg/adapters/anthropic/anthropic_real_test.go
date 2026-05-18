package anthropic

import (
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

// requireAnthropicRealE2E gates every real-API test in this file on
//   - RUN_ANTHROPIC_REAL_E2E=1 (opt-in flag, so CI without the key does not
//     accidentally bill)
//   - ANTHROPIC_API_KEY (the secret itself)
//
// Tests skip cleanly when either is missing.
func requireAnthropicRealE2E(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("RUN_ANTHROPIC_REAL_E2E") == "" {
		t.Skip("set RUN_ANTHROPIC_REAL_E2E=1 to run real Anthropic Messages API tests")
	}
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		t.Skip("set ANTHROPIC_API_KEY to run real Anthropic Messages API tests")
	}
	model := strings.TrimSpace(os.Getenv("ANTHROPIC_REAL_E2E_MODEL"))
	if model == "" {
		// Haiku 4.5 is the fastest + cheapest current Anthropic model
		// and is the default for these tests. Override with
		// ANTHROPIC_REAL_E2E_MODEL when validating a different model.
		model = ModelClaudeHaiku45
	}
	return key, model
}

func newRealAnthropicAdapter(t *testing.T) (*AnthropicAdapter, string) {
	t.Helper()
	key, model := requireAnthropicRealE2E(t)
	client := anthropic.NewClient(anthropicoption.WithAPIKey(key))
	return NewAnthropicAdapter(client, model, &MockLogger{}), model
}

// TestAnthropicRealToolDescriptionInfluencesSelection is the regression
// test for the dropped-tool-description bug. Before the fix, convertTools
// silently discarded tool.Function.Description, so Claude only saw the
// tool name + input schema. We construct TWO tools with intentionally
// confusable names and rely on the description to disambiguate which one
// Claude must call. If descriptions ever stop being forwarded, the model
// will fall back to alphabetical / name-similarity heuristics and pick
// the wrong tool, failing the assertion.
func TestAnthropicRealToolDescriptionInfluencesSelection(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)

	weatherProperties := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city or place to look up.",
		},
	}
	timeProperties := map[string]interface{}{
		"location": map[string]interface{}{
			"type":        "string",
			"description": "The city or place to look up.",
		},
	}

	tools := []llmtypes.Tool{
		{
			Type: "function",
			Function: &llmtypes.FunctionDefinition{
				Name: "lookup_alpha",
				// Description is the only signal Claude has to decide
				// this is the time tool.
				Description: "Returns the current local time in a given city. Use ONLY when the user asks about time, hours, or what time it is.",
				Parameters: &llmtypes.Parameters{
					Type:       "object",
					Properties: timeProperties,
					Required:   []string{"location"},
				},
			},
		},
		{
			Type: "function",
			Function: &llmtypes.FunctionDefinition{
				Name: "lookup_beta",
				// Description is the only signal Claude has to decide
				// this is the weather tool.
				Description: "Returns the current weather conditions (temperature, precipitation, wind) for a given city. Use ONLY when the user asks about weather or temperature.",
				Parameters: &llmtypes.Parameters{
					Type:       "object",
					Properties: weatherProperties,
					Required:   []string{"location"},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Ask a weather question. With descriptions forwarded, Claude must
	// call lookup_beta. Without them, the model has no semantic signal
	// at all — both tools have identical signatures and confusable
	// names — so it picks effectively at random.
	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{
				Role:  llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What's the weather like in Paris right now?"}},
			},
		},
		llmtypes.WithTools(tools),
		llmtypes.WithToolChoiceString("required"),
	)
	if err != nil {
		t.Fatalf("GenerateContent error = %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatalf("response had no choices")
	}
	toolCalls := resp.Choices[0].ToolCalls
	if len(toolCalls) == 0 {
		t.Fatalf("model did not emit any tool_use; content=%q", resp.Choices[0].Content)
	}

	got := toolCalls[0].FunctionCall.Name
	if got != "lookup_beta" {
		t.Fatalf("model selected %q for a weather question; tool description was likely dropped (lookup_beta should be the weather tool)", got)
	}
}

// TestAnthropicRealExtendedThinking proves extended thinking is fully
// plumbed: we set ThinkingBudget on a reasoning-class request, ask a
// question that benefits from chain-of-thought, and verify
//
//	(a) the model returns at least one thinking block (signed with a
//	    non-empty signature so it can be re-fed on the next turn), and
//	(b) the final answer still contains a meaningful response, and
//	(c) GenerationInfo.Additional surfaces the thinking text + signatures
//	    so callers can re-feed them on follow-up turns.
//
// Regression-locks the fix for the previously-missing thinking plumbing.
func TestAnthropicRealExtendedThinking(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{
				Role:  llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Three positive integers a, b, c satisfy a + b + c = 17 and a^2 + b^2 + c^2 = 105. Find all triples (a,b,c). Think step by step and then state the answer on a final line beginning with ANSWER:."}},
			},
		},
		llmtypes.WithThinkingBudget(2048),
		llmtypes.WithMaxTokens(8192),
	)
	if err != nil {
		t.Fatalf("GenerateContent with thinking error = %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatalf("response had no choices")
	}
	choice := resp.Choices[0]
	if choice.GenerationInfo == nil || choice.GenerationInfo.Additional == nil {
		t.Fatalf("expected GenerationInfo.Additional to be populated; got %+v", choice.GenerationInfo)
	}
	thinkingRaw, hasThinking := choice.GenerationInfo.Additional["thinking"]
	if !hasThinking {
		t.Fatalf("response missing thinking block in Additional; ThinkingBudget was 2048 but no chain-of-thought returned")
	}
	thinking, _ := thinkingRaw.(string)
	if strings.TrimSpace(thinking) == "" {
		t.Fatalf("thinking key present but empty: %q", thinking)
	}
	sigsRaw, hasSigs := choice.GenerationInfo.Additional["thinking_signatures"]
	sigs, _ := sigsRaw.([]string)
	if !hasSigs || len(sigs) == 0 || strings.TrimSpace(sigs[0]) == "" {
		t.Fatalf("expected at least one signed thinking block for re-feed on follow-up turns; got %v", sigsRaw)
	}
	if !strings.Contains(strings.ToUpper(choice.Content), "ANSWER:") {
		t.Fatalf("final user-facing content missing ANSWER: marker; thinking-mode may have eaten the response text. content=%q", choice.Content)
	}
}

// TestAnthropicRealInterleavedThinkingWithTools combines extended
// thinking with a tool call to prove the interleaved-thinking beta is
// actually opted-into and accepted by the API. Without the beta token
// the request would 400 with a different error ("interleaved-thinking
// header required for thinking + tools"); the test passing is itself
// the assertion the header engaged.
//
// Note: Anthropic disallows `tool_choice: required` together with
// thinking ("Thinking may not be enabled when tool_choice forces tool
// use."), so this test uses `tool_choice: auto` and frames the prompt
// to make tool use the obvious answer.
func TestAnthropicRealInterleavedThinkingWithTools(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)

	tools := []llmtypes.Tool{
		{
			Type: "function",
			Function: &llmtypes.FunctionDefinition{
				Name:        "evaluate_expression",
				Description: "Evaluates a simple arithmetic expression and returns the numeric result. ALWAYS call this tool for any arithmetic computation; never compute mentally.",
				Parameters: &llmtypes.Parameters{
					Type: "object",
					Properties: map[string]interface{}{
						"expression": map[string]interface{}{
							"type":        "string",
							"description": "A pure arithmetic expression, e.g. '17*23'.",
						},
					},
					Required: []string{"expression"},
				},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{
				Role:  llmtypes.ChatMessageTypeSystem,
				Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "You have access to the evaluate_expression tool. You MUST use it for any arithmetic. Do not compute results yourself."}},
			},
			{
				Role:  llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "What is 23 * 17? Use the tool."}},
			},
		},
		llmtypes.WithThinkingBudget(2048),
		llmtypes.WithMaxTokens(8192),
		llmtypes.WithTools(tools),
		llmtypes.WithToolChoiceString("auto"),
	)
	if err != nil {
		t.Fatalf("GenerateContent (thinking + tools) error = %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatalf("response had no choices")
	}
	choice := resp.Choices[0]
	if len(choice.ToolCalls) == 0 {
		t.Fatalf("model did not emit any tool_use; thinking+tools path likely broken. content=%q", choice.Content)
	}
	if choice.ToolCalls[0].FunctionCall.Name != "evaluate_expression" {
		t.Fatalf("model called %q, want evaluate_expression", choice.ToolCalls[0].FunctionCall.Name)
	}
	if choice.GenerationInfo == nil || choice.GenerationInfo.Additional == nil {
		t.Fatalf("expected GenerationInfo.Additional populated")
	}
	if _, hasThinking := choice.GenerationInfo.Additional["thinking"]; !hasThinking {
		t.Fatalf("interleaved-thinking + tools turn returned no thinking block; the interleaved beta may not have engaged")
	}
}

// TestAnthropicRealStopSequences proves the adapter forwards
// StopSequences to the Messages API: we ask Claude to produce a list
// where each line is "ITEM-N", then set "ITEM-3" as a stop sequence.
// The response must therefore terminate before "ITEM-3" appears.
//
// Stop sequences are the easiest sampling control to assert
// behaviorally — top_p / top_k only change the *distribution* of
// tokens, which is hard to test deterministically with one call.
func TestAnthropicRealStopSequences(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{
				Role:  llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Output exactly these five lines, in order, one per line: ITEM-1, ITEM-2, ITEM-3, ITEM-4, ITEM-5. Nothing else."}},
			},
		},
		llmtypes.WithStopSequences([]string{"ITEM-3"}),
	)
	if err != nil {
		t.Fatalf("GenerateContent with stop sequences error = %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatalf("response had no choices")
	}
	content := resp.Choices[0].Content
	if strings.Contains(content, "ITEM-3") {
		t.Fatalf("response contains the stop sequence; adapter did not forward StopSequences. content=%q", content)
	}
	// ITEM-1 and ITEM-2 should appear (they precede the stop).
	if !strings.Contains(content, "ITEM-1") || !strings.Contains(content, "ITEM-2") {
		t.Fatalf("response stopped too early; expected ITEM-1 and ITEM-2 before stop. content=%q", content)
	}
	// stop_reason should reflect the stop sequence path.
	stop := resp.Choices[0].StopReason
	if stop == "" {
		t.Logf("warning: empty StopReason on stop-sequence termination")
	}
}

// TestAnthropicRealTopPDoesNotError exercises the top_p plumbing. We
// can't deterministically assert behavioral changes from one call, so
// the test just proves the parameter is accepted by the API (the
// adapter forwards it, the SDK marshals it, the server validates it).
func TestAnthropicRealTopPDoesNotError(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply with the single word OK."}}},
		},
		llmtypes.WithTopP(0.9),
		llmtypes.WithMaxTokens(16),
	)
	if err != nil {
		t.Fatalf("GenerateContent with top_p=0.9 error = %v", err)
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Content) == "" {
		t.Fatalf("response with top_p was empty")
	}
}

// minimalPDFWithMarker returns the raw bytes of a tiny valid PDF whose
// only printable text is a caller-supplied marker. Generated inline so
// the test doesn't rely on an external fixture file. The xref offsets
// are computed dynamically (rather than hardcoded) so the PDF actually
// parses; lenient parsers tolerate wrong offsets, but Claude's parser
// is strict enough to reject malformed cross-reference tables.
func minimalPDFWithMarker(marker string) []byte {
	header := "%PDF-1.4\n%\xE2\xE3\xCF\xD3\n"
	objs := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"<</Type/Pages/Count 1/Kids[3 0 R]>>",
		"<</Type/Page/Parent 2 0 R/MediaBox[0 0 612 792]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>",
		"", // placeholder — content stream object filled in below.
		"<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>",
	}
	stream := "BT /F1 24 Tf 72 720 Td (" + marker + ") Tj ET"
	objs[3] = "<</Length " + iToA(len(stream)) + ">>\nstream\n" + stream + "\nendstream"

	var b strings.Builder
	b.WriteString(header)
	offsets := make([]int, len(objs)+1) // index 0 = free entry
	for i, body := range objs {
		offsets[i+1] = b.Len()
		b.WriteString(iToA(i+1) + " 0 obj\n" + body + "\nendobj\n")
	}
	xrefOff := b.Len()
	b.WriteString("xref\n0 ")
	b.WriteString(iToA(len(objs) + 1))
	b.WriteString("\n0000000000 65535 f \n")
	for _, off := range offsets[1:] {
		b.WriteString(pad10(off) + " 00000 n \n")
	}
	b.WriteString("trailer\n<</Size ")
	b.WriteString(iToA(len(objs) + 1))
	b.WriteString("/Root 1 0 R>>\nstartxref\n")
	b.WriteString(iToA(xrefOff))
	b.WriteString("\n%%EOF\n")
	return []byte(b.String())
}

func pad10(n int) string {
	s := iToA(n)
	if len(s) >= 10 {
		return s
	}
	return strings.Repeat("0", 10-len(s)) + s
}

// iToA is a local int→string helper. Hand-rolled so minimalPDFWithMarker
// has no extra imports (strconv.Itoa would do the same job).
func iToA(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestAnthropicRealPlainTextDocumentInput is the strict regression test
// for the document-block plumbing. Anthropic accepts plain-text
// documents via PlainTextSourceParam, which carries raw decoded text
// inline — there is no parsing layer between us and the model. We
// embed a unique marker in the document and assert the model reads it
// back. If convertMessages ever drops DocumentContent parts again, the
// model will respond "I don't see a document" and the test fails.
func TestAnthropicRealPlainTextDocumentInput(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)

	marker := "DOC_MARKER_" + time.Now().Format("150405")
	body := "Sample policy document for adapter test.\n\nMARKER LINE: " + marker + "\n\nEnd of document."

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.DocumentContent{
						SourceType: "raw", // PlainTextSource takes text as-is
						MediaType:  "text/plain",
						Data:       body,
						Title:      "policy-doc",
					},
					llmtypes.TextContent{Text: "The attached document contains a line that begins 'MARKER LINE:'. Reply with ONLY the value after that prefix — no other words."},
				},
			},
		},
		llmtypes.WithMaxTokens(64),
	)
	if err != nil {
		t.Fatalf("GenerateContent with text document error = %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatalf("response had no choices")
	}
	content := strings.TrimSpace(resp.Choices[0].Content)
	if !strings.Contains(content, marker) {
		t.Fatalf("response missing document marker %q (DocumentContent likely dropped). content=%q", marker, content)
	}
}

// TestAnthropicRealPDFInputDocumentReceived proves the application/pdf
// path of createDocumentBlock reaches Anthropic and the API accepts the
// document. We don't assert text extraction from the hand-rolled PDF
// (extraction depends on Claude's PDF parser tolerating our minimal
// content stream) — only that the request succeeds and the model
// produces a non-empty reply, i.e. it did NOT respond with "I don't see
// a PDF attached". The strict text-extraction assertion lives in
// TestAnthropicRealPlainTextDocumentInput, which doesn't depend on
// fragile PDF parsing.
func TestAnthropicRealPDFInputDocumentReceived(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)

	marker := "PDF_MARKER_" + time.Now().Format("150405")
	pdfBytes := minimalPDFWithMarker(marker)
	encoded := base64.StdEncoding.EncodeToString(pdfBytes)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{
				Role: llmtypes.ChatMessageTypeHuman,
				Parts: []llmtypes.ContentPart{
					llmtypes.DocumentContent{
						SourceType: "base64",
						MediaType:  "application/pdf",
						Data:       encoded,
						Title:      "test-pdf",
					},
					llmtypes.TextContent{Text: "Briefly describe what kind of file is attached. One short sentence."},
				},
			},
		},
		llmtypes.WithMaxTokens(80),
	)
	if err != nil {
		t.Fatalf("GenerateContent with PDF document error = %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatalf("response had no choices")
	}
	content := strings.ToLower(resp.Choices[0].Content)
	// The model must not be claiming the PDF is absent. The exact
	// phrasing varies, but if Anthropic rejected the document block we
	// know the canonical refusal text shape.
	for _, refusal := range []string{
		"i don't see",
		"i do not see",
		"no pdf attached",
		"no document attached",
		"don't see a pdf",
		"don't see any pdf",
	} {
		if strings.Contains(content, refusal) {
			t.Fatalf("model claims no PDF was attached; document block likely not forwarded. content=%q", resp.Choices[0].Content)
		}
	}
}

// TestAnthropicRealTopKDoesNotError mirrors TestAnthropicRealTopPDoesNotError
// for the top_k parameter. Anthropic accepts top_k (unlike OpenAI Chat
// Completions), so a non-zero value should round-trip cleanly.
func TestAnthropicRealTopKDoesNotError(t *testing.T) {
	adapter, _ := newRealAnthropicAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resp, err := adapter.GenerateContent(ctx,
		[]llmtypes.MessageContent{
			{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: "Reply with the single word OK."}}},
		},
		llmtypes.WithTopK(40),
		llmtypes.WithMaxTokens(16),
	)
	if err != nil {
		t.Fatalf("GenerateContent with top_k=40 error = %v", err)
	}
	if len(resp.Choices) == 0 || strings.TrimSpace(resp.Choices[0].Content) == "" {
		t.Fatalf("response with top_k was empty")
	}
}
