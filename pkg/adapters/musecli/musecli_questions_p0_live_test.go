package musecli

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestMuseCLIRealStructuredQuestionChoiceP0(t *testing.T) {
	requireMetaMuseCLIE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	owner := "structured-question-p0-" + museRandomHex(t, 3)
	t.Cleanup(func() { KillMusePersistentSession(owner) })
	adapter := museLiveAdapter()
	opts := []llmtypes.CallOption{WithUserChoice(true), WithPersistentInteractiveSession(true), WithInteractiveSessionID(owner), WithWorkingDir(t.TempDir()), llmtypes.WithReasoningEffort("low")}
	if _, err := adapter.GenerateContent(ctx, nil, append(opts, llmtypes.WithCodingProviderLaunchOnly())...); err != nil {
		t.Fatal(err)
	}
	token := "STRUCTURED-CHOICE-" + museRandomHex(t, 3)
	prompt := fmt.Sprintf("Integration test. Use your native request_user_input tool to ask ONE question: Choose a color? Provide exactly three options: Red, Blue, Green. Wait for my selection. Then reply with exactly %s and the selected color. Do not use other tools.", token)
	result := make(chan error, 1)
	go func() {
		response, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}}, opts...)
		if err == nil && !strings.Contains(response.Choices[0].Content, token) {
			err = fmt.Errorf("reply omitted token: %s", response.Choices[0].Content)
		}
		result <- err
	}()
	reader := NewQuestionReaderForOwner(owner)
	var requested *QuestionEvent
	deadline := time.After(3 * time.Minute)
	for requested == nil {
		rows, err := reader.Poll()
		if err != nil {
			t.Fatal(err)
		}
		for i := range rows {
			if rows[i].Kind == "user_input_prompt_requested" {
				requested = &rows[i]
			}
		}
		if requested != nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("turn ended before question: %v", err)
		case <-deadline:
			t.Fatal("structured question did not arrive")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if len(requested.Questions) != 1 || len(requested.Questions[0].Options) != 3 {
		t.Fatalf("question shape: %+v", requested)
	}
	promptID := requested.PromptID
	q := requested.Questions[0]
	if err := SubmitQuestionAnswers(ctx, owner, requested.PromptID, []QuestionAnswer{{ID: q.ID, SelectedLabel: q.Options[2].Label}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	rows, err := reader.Poll()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.Kind == "user_input_prompt_settled" && row.PromptID == requested.PromptID && row.Outcome == "answered" && len(row.Answers) == 1 && row.Answers[0].SelectedLabel == q.Options[2].Label {
			found = true
		}
	}
	if !found {
		t.Fatalf("selected answer not settled: %+v", rows)
	}
	// A second turn proves ordered questions and the final review screen on
	// the same retained session.
	result = make(chan error, 1)
	go func() {
		prompt := "Integration test of your native request_user_input widget. Ask all THREE questions in a SINGLE request_user_input call. Question 1 Color: Red, Blue (Recommended), Green. Question 2 Drink: Coffee, Tea (Recommended), Water. Question 3 Layout: Compact (Recommended), Spacious. Wait for actual answers and then report the three selected names separated by |. Do not use other tools."
		_, err := adapter.GenerateContent(ctx, []llmtypes.MessageContent{{Role: llmtypes.ChatMessageTypeHuman, Parts: []llmtypes.ContentPart{llmtypes.TextContent{Text: prompt}}}}, opts...)
		result <- err
	}()
	requested = nil
	deadline = time.After(3 * time.Minute)
	for requested == nil {
		rows, err := reader.Poll()
		if err != nil {
			t.Fatal(err)
		}
		for i := range rows {
			if rows[i].Kind == "user_input_prompt_requested" && rows[i].PromptID != promptID {
				requested = &rows[i]
			}
		}
		if requested != nil {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("multi-question turn ended early: %v", err)
		case <-deadline:
			t.Fatal("multi-question prompt did not arrive")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if len(requested.Questions) != 3 {
		t.Fatalf("expected three questions: %+v", requested.Questions)
	}
	chosen := make([]QuestionAnswer, 3)
	for i, question := range requested.Questions {
		chosen[i] = QuestionAnswer{ID: question.ID, SelectedLabel: question.Options[len(question.Options)-1].Label}
	}
	if err := SubmitQuestionAnswers(ctx, owner, requested.PromptID, chosen); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	rows, err = reader.Poll()
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, row := range rows {
		if row.Kind == "user_input_prompt_settled" && row.PromptID == requested.PromptID && row.Outcome == "answered" && len(row.Answers) == 3 {
			found = true
		}
	}
	if !found {
		t.Fatalf("multi-question answer not settled: %+v", rows)
	}
}
