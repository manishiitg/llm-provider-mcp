package musecli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

func TestQuestionReaderCommittedRowsAndSettlement(t *testing.T) {
	owner := "question-reader-test"
	path := filepath.Join(t.TempDir(), "session.jsonl")
	key, _ := musePersistentKey(owner)
	musePersistentPool.Lock()
	musePersistentPool.m[key] = &musePersistentSession{nativeSessionID: "native-question-test", logPath: path, userChoice: true}
	musePersistentPool.Unlock()
	t.Cleanup(func() { musePersistentPool.Lock(); delete(musePersistentPool.m, key); musePersistentPool.Unlock() })
	requested := `{"sequence":1,"payload_type":"runtime.session","payload":{"kind":"run","run_id":"run-1","event":{"kind":"user_input_prompt_requested","prompt_id":"prompt-1","questions":[{"id":"color","header":"Color","question":"Choose a color","options":[{"label":"Red"},{"label":"Blue","description":"The sky"}]}]}}}`
	settled := `{"sequence":2,"payload_type":"runtime.session","payload":{"kind":"run","run_id":"run-1","event":{"kind":"user_input_prompt_settled","prompt_id":"prompt-1","outcome":"answered","answers":[{"id":"color","selected_label":"Blue"}]}}}`
	if err := os.WriteFile(path, []byte(requested), 0600); err != nil {
		t.Fatal(err)
	}
	reader := NewQuestionReaderForOwner(owner)
	rows, err := reader.Poll()
	if err != nil || len(rows) != 0 {
		t.Fatalf("uncommitted row: %+v %v", rows, err)
	}
	if err := os.WriteFile(path, []byte(requested+"\n"+settled+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err = reader.Poll()
	if err != nil || len(rows) != 2 || rows[0].Questions[0].Options[1].Description != "The sky" || rows[1].Answers[0].SelectedLabel != "Blue" {
		t.Fatalf("rows: %+v %v", rows, err)
	}
	if err := SubmitQuestionAnswers(context.Background(), owner, "prompt-1", []QuestionAnswer{{ID: "color", SelectedLabel: "Red"}}); err == nil {
		t.Fatal("settled prompt was accepted")
	}
}

func TestMuseUserChoiceWaitsWithoutAutoSelecting(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithUserChoice(true)(opts)
	ctx := museWithAutoAnswer(context.Background(), opts)
	if pending, err := museHandlePendingQuestion(ctx, "must-not-touch-tmux", museQuestionFixture); !pending || err != nil {
		t.Fatalf("choice mode: %v %v", pending, err)
	}
}
