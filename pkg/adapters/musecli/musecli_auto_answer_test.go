package musecli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/manishiitg/multi-llm-provider-go/llmerrors"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const museQuestionFixture = "Historical 429 quota exhausted\n◆ Request user input Color — running (1s)\nChoose a color\n› 1. Red\n  2. Blue (Recommended)\n  3. Green\n1 of 3\nEnter to select · Esc to interrupt\n❯"

func TestMuseRecommendedQuestion(t *testing.T) {
	a, ok := museRecommendedQuestion(museQuestionFixture)
	if !ok || a.current != 0 || a.target != 0 || a.label != "Red" {
		t.Fatalf("wrong first option: %+v %v", a, ok)
	}
	moved, ok := museRecommendedQuestion(strings.Replace(strings.Replace(museQuestionFixture, "› 1.", "  1.", 1), "  2.", "› 2.", 1))
	if !ok || moved.key != a.key || moved.current != 1 || moved.target != 0 {
		t.Fatal("cursor movement changed question identity")
	}
	timer, ok := museRecommendedQuestion(strings.Replace(museQuestionFixture, "(1s)", "(20s)", 1))
	if !ok || timer.key != a.key {
		t.Fatal("running timer changed question identity")
	}
	next, ok := museRecommendedQuestion(strings.Replace(museQuestionFixture, "1 of 3", "2 of 3", 1))
	if !ok || next.key == a.key {
		t.Fatal("next question must have a new identity")
	}
	for _, pane := range []string{
		strings.ReplaceAll(museQuestionFixture, " (Recommended)", ""),
		strings.Replace(museQuestionFixture, "Red", "Red (Recommended)", 1),
	} {
		if answer, ok := museRecommendedQuestion(pane); !ok || answer.target != 0 {
			t.Fatalf("must select first option regardless of labels: %+v %v", answer, ok)
		}
	}
	for _, pane := range []string{
		strings.ReplaceAll(museQuestionFixture, "›", " "),
		strings.Replace(museQuestionFixture, "Choose a color", "Do you trust this workspace?", 1),
		"Do you trust this folder?\n› 1. Yes (Recommended)\nEnter to select",
		"Earlier we said Blue (Recommended).\n❯",
	} {
		if _, ok := museRecommendedQuestion(pane); ok {
			t.Fatalf("must not auto-answer:\n%s", pane)
		}
	}
}

func TestMuseAutoAnswerDisabledCanceledAndDuplicate(t *testing.T) {
	opts := &llmtypes.CallOptions{}
	WithAutoSelectRecommended(false)(opts)
	ctx := museWithAutoAnswer(context.Background(), opts)
	if _, err := museHandlePendingQuestion(ctx, "must-not-access-tmux", museQuestionFixture); llmerrors.KindOf(err) != llmerrors.KindUserInputRequired {
		t.Fatalf("disabled: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := museHandlePendingQuestion(museWithAutoAnswer(canceled, nil), "must-not-access-tmux", museQuestionFixture); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	answer, _ := museRecommendedQuestion(museQuestionFixture)
	duplicate := context.WithValue(context.Background(), museAutoAnswerKey{}, &museAutoAnswerState{lastQuestion: answer.key})
	if pending, err := museHandlePendingQuestion(duplicate, "must-not-access-tmux", museQuestionFixture); !pending || err != nil {
		t.Fatalf("duplicate: %v %v", pending, err)
	}
}

func TestMuseRecommendedReview(t *testing.T) {
	pane := "◆ Request user input Color, Drink, Layout — running (5s)\nReview answers before submit · Enter to edit or submit · ↑/↓ to move · Esc to go back\n  Color: Blue (Recommended)\n  Drink: Tea (Recommended)\n  Layout: Compact (Recommended)\n> Submit answers\n  Interrupt turn\n──────────────────\n❯"
	answer, ok := museRecommendedQuestion(pane)
	if !ok || !answer.review || answer.current != 3 || answer.target != 3 {
		t.Fatalf("review = %+v, %v", answer, ok)
	}
	if musePendingUserInputError(pane) == nil {
		t.Fatal("review must not be treated as idle")
	}
	changed := strings.Replace(pane, "Tea (Recommended)", "Coffee", 1)
	if answer, ok := museRecommendedQuestion(changed); !ok || !answer.review || answer.target != 3 {
		t.Fatal("must submit completed answers without recommendation labels")
	}
	if _, ok := museRecommendedQuestion(strings.Replace(pane, "Tea (Recommended)", "", 1)); ok {
		t.Fatal("must not submit an empty answer")
	}
}

func TestMuseFirstOptionBotChannel(t *testing.T) {
	pane := "◆ Request user input Channel, Chat, Behavior — running (7s)\nWhich bot channel do you want to connect to this workflow - Slack or WhatsApp?\n  1. Slack\n› 2. WhatsApp\n  3. None of the above\n1 of 3\nEnter to select · Esc to interrupt"
	answer, ok := museRecommendedQuestion(pane)
	if !ok || answer.current != 1 || answer.target != 0 || answer.label != "Slack" {
		t.Fatalf("must navigate to first channel: %+v %v", answer, ok)
	}
}

func TestMuseFirstOptionLivePaneShape(t *testing.T) {
	pane := "Agent pid 3118\nIdentity added: /Users/test/.ssh/id_ed25519\n\n  Muse Code 1.3.0\n\n❯ Integration test of your native request_user_input widget. Ask all THREE questions in a SINGLE request_user_input call.\n\n◈ Request user input Color, Drink, Layout — running (14s)\n\n  Which color do you prefer?\n\n  › 1. Red                 A bold and warm choice.\n    2. Blue (Recommended)  A balanced and popular choice.\n    3. Green               A fresh and natural choice.\n    4. None of the above   Optionally, add details in notes (tab).\n\n  1 of 3\n  Enter to select · ↑/↓ to move · ←/→ to switch question · Tab for an optional note · Esc to interrupt\n\n────────────────────────────────────────────────────────────────────────\n❯\n────────────────────────────────────────────────────────────────────────\n  muse-spark-1.3-contributor · max · /tmp/work · Auto-review"
	answer, ok := museRecommendedQuestion(pane)
	if !ok || answer.current != 0 || answer.target != 0 {
		t.Fatalf("live pane not actionable: %+v %v", answer, ok)
	}
	if llmerrors.KindOf(musePendingUserInputError(pane)) != llmerrors.KindUserInputRequired || !musePaneHasRunningTool(pane) {
		t.Fatal("live Muse tool marker must register as a pending question and running tool")
	}
	for _, marker := range []string{"◆", "◇"} {
		frame, frameOK := museRecommendedQuestion(strings.Replace(pane, "◈ Request", marker+" Request", 1))
		if !frameOK || frame.key != answer.key {
			t.Fatalf("animated marker changed question identity: %q, %v", frame.key, frameOK)
		}
	}
}

func TestMuseStoppedAutoAnswerCannotSendKeys(t *testing.T) {
	state := &museAutoAnswerState{}
	state.stopped.Store(true)
	ctx := context.WithValue(context.Background(), museAutoAnswerKey{}, state)
	if _, err := museHandlePendingQuestion(ctx, "must-not-access-tmux", museQuestionFixture); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped session: %v", err)
	}
}
