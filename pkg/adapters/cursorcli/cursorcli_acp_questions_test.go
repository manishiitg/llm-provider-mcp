package cursorcli

import (
	"context"
	"encoding/json"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"testing"
)

// PLAT-872-21: a native question/plan must receive the person's exact decision.
func TestCursorACPQuestionsDoNotInferApproval(t *testing.T) {
	for _, tc := range []struct{ method, request, answer, want string }{
		{"cursor/ask_question", `{"sessionId":"s","questions":[{"id":"color","prompt":"Color?","options":[{"id":"b","label":"Blue"},{"id":"g","label":"Green"}]}]}`, `{"id":"color","selected_labels":["Green"]}`, `{"outcome":{"answers":[{"questionId":"color","selectedOptionIds":["g"]}],"outcome":"answered"}}`},
		{"cursor/create_plan", `{"name":"Change","plan":"Update the file"}`, `{"id":"plan","selected_labels":["Reject plan"]}`, `{"outcome":{"outcome":"rejected"}}`},
		{"cursor/create_plan", `{"name":"Change","plan":"Update the file"}`, `{"id":"plan","selected_labels":["Approve plan"]}`, `{"outcome":{"outcome":"accepted"}}`},
		{"session/request_permission", `{"sessionId":"s","toolCall":{"title":"Run command"},"options":[{"optionId":"yes","name":"Allow once","kind":"allow_once"},{"optionId":"no","name":"Reject","kind":"reject_once"}]}`, `{"id":"permission","selected_labels":["Reject"]}`, `{"outcome":{"optionId":"no","outcome":"selected"}}`},
	} {
		ctx := llmtypes.WithCodingAgentQuestionHandler(context.Background(), "cursor-cli", "owner", func(context.Context, map[string]any) (string, error) {
			return `{"status":"answered","answers":[` + tc.answer + `]}`, nil
		})
		result, err := cursorNativeQuestion(ctx, tc.method, json.RawMessage(tc.request), "s")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(result)
		if string(b) != tc.want {
			t.Fatalf("%s: %s", tc.method, b)
		}
		result, err = cursorNativeQuestion(context.Background(), tc.method, json.RawMessage(tc.request), "s")
		b, _ = json.Marshal(result)
		if err != nil || string(b) != `{"outcome":{"outcome":"cancelled"}}` {
			t.Fatalf("unattended: %s %v", b, err)
		}
	}
}
