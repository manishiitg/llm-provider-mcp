package musecli

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestMuseCLIRealAutoFirstOptionThreeQuestionsP0(t *testing.T) {
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
	handle := resp.Choices[0].GenerationInfo.CodingProviderSessionHandle
	if handle == nil {
		t.Fatal("missing native Muse session handle")
	}
	transcript, err := os.ReadFile(museSessionLogPath(handle.NativeSessionID))
	if err != nil {
		t.Fatal(err)
	}
	var expected []string
	for _, line := range strings.Split(string(transcript), "\n") {
		if !strings.Contains(line, `"user_input_prompt_requested"`) {
			continue
		}
		var record struct {
			Payload struct {
				Event struct {
					Questions []struct {
						Options []struct {
							Label string `json:"label"`
						} `json:"options"`
					} `json:"questions"`
				} `json:"event"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		for _, question := range record.Payload.Event.Questions {
			if len(question.Options) == 0 {
				t.Fatal("native Muse question had no options")
			}
			expected = append(expected, strings.TrimSpace(strings.ReplaceAll(question.Options[0].Label, "(Recommended)", "")))
		}
		break
	}
	if len(expected) != 3 || strings.Trim(selectedNames, "`\n") != strings.ReplaceAll(strings.Join(expected, "|"), " ", "") {
		t.Fatalf("selected %q, first displayed options %q", final, expected)
	}
	for _, chunk := range museDrainStream(stream) {
		if strings.HasPrefix(chunk.Content, "Selected Muse’s recommended answer:") {
			t.Fatalf("automatic selection leaked into the UI stream: %q", chunk.Content)
		}
	}
	t.Logf("PASS: auto-selected all three first options %q without selection announcements; final=%q", expected, final)
}
