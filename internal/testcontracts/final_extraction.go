package testcontracts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"github.com/manishiitg/multi-llm-provider-go/pkg/adapters/vertex"
)

func AssertCleanFinalExtraction(t testing.TB, provider, content string, wantContains, forbidden []string) {
	t.Helper()

	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		t.Fatalf("%s final extraction is empty", provider)
	}
	for _, want := range wantContains {
		if !strings.Contains(trimmed, want) {
			t.Fatalf("%s final extraction missing %q:\n%s", provider, want, trimmed)
		}
	}
	for _, bad := range forbidden {
		if strings.Contains(trimmed, bad) {
			t.Fatalf("%s final extraction leaked %q:\n%s", provider, bad, trimmed)
		}
	}
}

type FinalExtractionJudgeCase struct {
	Provider     string
	TmuxScreen   string
	Extracted    string
	UserGoal     string
	MustContain  []string
	Forbidden    []string
	ExpectedNote string
}

func RequireVertexFinalExtractionJudgeE2E(t testing.TB) {
	t.Helper()
	if vertexJudgeAPIKey() == "" {
		t.Fatal("Vertex final-extraction judge requires GEMINI_API_KEY, VERTEX_API_KEY, or GOOGLE_API_KEY; missing key is a contract failure because final-output quality is judged semantically")
	}
}

func AssertVertexJudgesFinalExtraction(t testing.TB, c FinalExtractionJudgeCase) {
	t.Helper()
	if strings.TrimSpace(c.TmuxScreen) == "" {
		t.Fatalf("%s final extraction judge case has empty raw provider output", c.Provider)
	}
	AssertCleanFinalExtraction(t, c.Provider, c.Extracted, c.MustContain, c.Forbidden)
	RequireVertexFinalExtractionJudgeE2E(t)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	ctx = vertex.WithResponseSchemaFromJSON(ctx, finalExtractionJudgeSchema())

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  vertexJudgeAPIKey(),
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		t.Fatalf("create Vertex judge client: %v", err)
	}

	model := strings.TrimSpace(os.Getenv("VERTEX_FINAL_EXTRACTION_JUDGE_MODEL"))
	if model == "" {
		model = vertex.ModelGemini35Flash
	}
	adapter := vertex.NewGoogleGenAIAdapter(client, model, noopJudgeLogger{})
	resp, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{
		{
			Role: llmtypes.ChatMessageTypeSystem,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: `You are a strict test oracle for coding-CLI final-response extraction.
Return only valid JSON. Do not explain outside JSON.`}},
		},
		{
			Role:  llmtypes.ChatMessageTypeHuman,
			Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: buildFinalExtractionJudgePrompt(c)}},
		},
	},
		llmtypes.WithJSONMode(),
		llmtypes.WithTemperature(0),
		llmtypes.WithMaxTokens(1024),
	)
	if err != nil {
		t.Fatalf("Vertex final-extraction judge call failed: %v", err)
	}
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0] == nil {
		t.Fatal("Vertex final-extraction judge returned no choices")
	}

	var verdict struct {
		Pass            bool   `json:"pass"`
		MatchesUserGoal bool   `json:"matches_user_goal"`
		FormattingOK    bool   `json:"formatting_ok"`
		NoiseFree       bool   `json:"noise_free"`
		Reason          string `json:"reason"`
	}
	raw := strings.TrimSpace(resp.Choices[0].Content)
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &verdict); err != nil {
		t.Fatalf("Vertex final-extraction judge returned invalid JSON: %v\ncontent=%q", err, raw)
	}
	if !verdict.Pass || !verdict.MatchesUserGoal || !verdict.FormattingOK || !verdict.NoiseFree {
		t.Fatalf("Vertex final-extraction judge rejected %s extraction: %+v\nextracted:\n%s", c.Provider, verdict, c.Extracted)
	}
}

func vertexJudgeAPIKey() string {
	for _, name := range []string{"GEMINI_API_KEY", "VERTEX_API_KEY", "GOOGLE_API_KEY"} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func finalExtractionJudgeSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pass": map[string]interface{}{
				"type": "boolean",
			},
			"matches_user_goal": map[string]interface{}{
				"type": "boolean",
			},
			"formatting_ok": map[string]interface{}{
				"type": "boolean",
			},
			"noise_free": map[string]interface{}{
				"type": "boolean",
			},
			"reason": map[string]interface{}{
				"type": "string",
			},
		},
		"required": []interface{}{
			"pass",
			"matches_user_goal",
			"formatting_ok",
			"noise_free",
			"reason",
		},
	}
}

func buildFinalExtractionJudgePrompt(c FinalExtractionJudgeCase) string {
	return fmt.Sprintf(`Judge whether EXTRACTED_FINAL is the clean final assistant response from RAW_PROVIDER_OUTPUT.

Provider: %s
User goal: %s
Expected note: %s
Must contain these fragments: %s
Must not contain these noise fragments: %s

Pass criteria:
- EXTRACTED_FINAL answers the user's goal using the final assistant response in RAW_PROVIDER_OUTPUT.
- It preserves meaningful formatting from the final answer, including line breaks, bullets, code blocks, and paths.
- It omits terminal chrome, prompts, thought/process headers, tool cards, MCP names, shell/curl/header fragments, JSON tool output, and earlier assistant drafts.
- Do not require exact wording if the extraction is semantically the same and formatting is preserved.

Return JSON exactly like:
{"pass":true,"matches_user_goal":true,"formatting_ok":true,"noise_free":true,"reason":"short reason"}

RAW_PROVIDER_OUTPUT:
%s

EXTRACTED_FINAL:
%s
`, c.Provider, c.UserGoal, c.ExpectedNote, strings.Join(c.MustContain, " | "), strings.Join(c.Forbidden, " | "), truncateForJudge(c.TmuxScreen), c.Extracted)
}

func truncateForJudge(s string) string {
	const max = 12000
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return "[truncated to last screen rows]\n" + string(runes[len(runes)-max:])
}

func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end >= start {
		return s[start : end+1]
	}
	return s
}

type noopJudgeLogger struct{}

func (noopJudgeLogger) Infof(string, ...any)          {}
func (noopJudgeLogger) Errorf(string, ...any)         {}
func (noopJudgeLogger) Debugf(string, ...interface{}) {}
