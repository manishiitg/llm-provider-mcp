package musecli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestMuseCLIRealAutoRecommendedThreeQuestionsP0(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	owner := "auto-question-p0-" + museRandomHex(t, 3)
	t.Cleanup(func() { KillMusePersistentSession(owner) })
	stream := make(chan llmtypes.StreamChunk, 1000)
	opts := []llmtypes.CallOption{WithPersistentInteractiveSession(true), WithInteractiveSessionID(owner), WithWorkingDir(t.TempDir()), llmtypes.WithStreamingChan(stream)}
	prompt := "Integration test of your native request_user_input widget. Ask all THREE questions in a SINGLE request_user_input call. Question 1 Color: Red, Blue (Recommended), Green. Question 2 Drink: Coffee, Tea (Recommended), Water. Question 3 Layout: Compact (Recommended), Spacious. You must call the native tool and wait for actual answers; do not select them yourself. Do not use other tools. After the answers, report ONLY the three selected names separated by | in question order."
	resp, err := museLiveAdapter().GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}}, opts...)
	if err != nil {
		t.Fatalf("auto-answer round trip: %v", err)
	}
	final := strings.TrimSpace(resp.Choices[0].Content)
	// Muse may echo the literal option labels, including their UI annotation.
	selectedNames := strings.ReplaceAll(strings.ReplaceAll(final, "(Recommended)", ""), " ", "")
	if strings.Trim(selectedNames, "`\n") != "Blue|Tea|Compact" {
		t.Fatalf("wrong selected answers: %q", final)
	}
	for _, chunk := range museDrainStream(stream) {
		if strings.HasPrefix(chunk.Content, "Selected Muse’s recommended answer:") {
			t.Fatalf("automatic selection leaked into the UI stream: %q", chunk.Content)
		}
	}
	t.Logf("PASS: auto-selected all three recommendations without selection announcements; final=%q", final)
}
