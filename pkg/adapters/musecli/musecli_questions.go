package musecli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type Question struct {
	ID       string           `json:"id"`
	Header   string           `json:"header,omitempty"`
	Question string           `json:"question"`
	Options  []QuestionOption `json:"options"`
}
type QuestionAnswer struct {
	ID            string `json:"id"`
	SelectedLabel string `json:"selected_label"`
}
type QuestionEvent struct {
	Sequence        int64            `json:"sequence"`
	NativeSessionID string           `json:"native_session_id"`
	RunID           string           `json:"run_id"`
	PromptID        string           `json:"prompt_id"`
	Kind            string           `json:"kind"`
	Questions       []Question       `json:"questions,omitempty"`
	Answers         []QuestionAnswer `json:"answers,omitempty"`
	Outcome         string           `json:"outcome,omitempty"`
}

// QuestionReader follows committed native rows. Its owner may acquire a
// native session only after the reader starts.
type QuestionReader struct {
	owner, nativeID, path string
	offset                int64
}

func NewQuestionReaderForOwner(owner string) *QuestionReader { return &QuestionReader{owner: owner} }

func (r *QuestionReader) Poll() ([]QuestionEvent, error) {
	key, err := musePersistentKey(r.owner)
	if err != nil {
		return nil, err
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	var nativeID, path string
	if entry != nil {
		nativeID = entry.nativeSessionID
		path = entry.logPath
		if path == "" && nativeID != "" {
			path = museSessionLogPath(nativeID, entry.accountDataHome)
		}
	}
	musePersistentPool.Unlock()
	if nativeID == "" || path == "" {
		return nil, nil
	}
	if r.nativeID != nativeID || r.path != path {
		r.nativeID, r.path, r.offset = nativeID, path, 0
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if r.offset > info.Size() {
		r.offset = 0
	}
	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(f)
	var result []QuestionEvent
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return result, readErr
		}
		r.offset += int64(len(line))
		var row struct {
			Sequence int64  `json:"sequence"`
			Type     string `json:"payload_type"`
			Payload  struct {
				Kind  string `json:"kind"`
				RunID string `json:"run_id"`
				Event struct {
					Kind      string           `json:"kind"`
					PromptID  string           `json:"prompt_id"`
					Questions []Question       `json:"questions"`
					Answers   []QuestionAnswer `json:"answers"`
					Outcome   string           `json:"outcome"`
				} `json:"event"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil || row.Type != "runtime.session" || row.Payload.Kind != "run" {
			continue
		}
		e := row.Payload.Event
		if e.PromptID == "" || (e.Kind != "user_input_prompt_requested" && e.Kind != "user_input_prompt_settled") {
			continue
		}
		if e.Kind == "user_input_prompt_requested" && (len(e.Questions) == 0 || len(e.Questions) > 12) {
			continue
		}
		result = append(result, QuestionEvent{Sequence: row.Sequence, NativeSessionID: nativeID, RunID: row.Payload.RunID, PromptID: e.PromptID, Kind: e.Kind, Questions: e.Questions, Answers: e.Answers, Outcome: e.Outcome})
	}
	return result, nil
}

var questionSubmissionMu sync.Mutex

// SubmitQuestionAnswers accepts only labels from the latest outstanding
// structured prompt, then verifies each live widget before sending keys.
func SubmitQuestionAnswers(ctx context.Context, owner, promptID string, answers []QuestionAnswer) error {
	questionSubmissionMu.Lock()
	defer questionSubmissionMu.Unlock()
	if promptID == "" {
		return fmt.Errorf("prompt_id is required")
	}
	reader := NewQuestionReaderForOwner(owner)
	events, err := reader.Poll()
	if err != nil {
		return err
	}
	var prompt *QuestionEvent
	for i := range events {
		e := &events[i]
		if e.Kind == "user_input_prompt_requested" {
			prompt = e
		} else if prompt != nil && e.PromptID == prompt.PromptID {
			prompt = nil
		}
	}
	if prompt == nil || prompt.PromptID != promptID {
		return fmt.Errorf("Muse question is no longer pending")
	}
	if len(answers) != len(prompt.Questions) {
		return fmt.Errorf("answer every question")
	}
	selected := make([]string, len(answers))
	for i, q := range prompt.Questions {
		if q.ID != answers[i].ID {
			return fmt.Errorf("question order changed")
		}
		matches := 0
		for _, option := range q.Options {
			if option.Label == answers[i].SelectedLabel {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("unknown option for %s", q.ID)
		}
		selected[i] = answers[i].SelectedLabel
	}
	key, err := musePersistentKey(owner)
	if err != nil {
		return err
	}
	musePersistentPool.Lock()
	entry := musePersistentPool.m[key]
	if entry == nil || !entry.userChoice || entry.nativeSessionID != prompt.NativeSessionID {
		musePersistentPool.Unlock()
		return fmt.Errorf("Muse choice session is unavailable")
	}
	session := entry.tmuxName
	musePersistentPool.Unlock()
	if !museTmuxSessionAlive(ctx, session) {
		return fmt.Errorf("Muse terminal is unavailable")
	}
	for i, label := range selected {
		var choice museRecommendedAnswer
		matched := false
		for retry := 0; retry < 12; retry++ {
			pane, err := museTmuxCapturePane(ctx, session)
			if err != nil {
				return err
			}
			choice, matched = museRecommendedQuestion(pane)
			if matched && !choice.review && strings.Contains(pane[museQuestionWidgetStart(pane):], prompt.Questions[i].Question) {
				break
			}
			matched = false
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		if !matched {
			return fmt.Errorf("Muse question widget changed")
		}
		pane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return err
		}
		widget := pane[museQuestionWidgetStart(pane):]
		target := -1
		var widgetOptions []string
		for _, line := range strings.Split(widget, "\n") {
			m := museQuestionOptionRE.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			widgetOptions = append(widgetOptions, strings.TrimSpace(m[3]))
		}
		if len(widgetOptions) != len(prompt.Questions[i].Options) {
			return fmt.Errorf("Muse option count changed")
		}
		for index, option := range prompt.Questions[i].Options {
			text := widgetOptions[index]
			if text != option.Label && !strings.HasPrefix(text, option.Label+" ") && !strings.HasPrefix(text, option.Label+" ·") {
				return fmt.Errorf("Muse option labels changed")
			}
			if option.Label == label {
				target = index
			}
		}
		if target < 0 {
			return fmt.Errorf("selected option is absent from Muse widget")
		}
		for pos := choice.current; pos != target; {
			button := "Down"
			if pos > target {
				button = "Up"
				pos--
			} else {
				pos++
			}
			if err := museSendQuestionKey(ctx, session, button); err != nil {
				return err
			}
		}
		fresh, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return err
		}
		current, ok := museRecommendedQuestion(fresh)
		if !ok || current.review || current.current != target || !strings.Contains(fresh[museQuestionWidgetStart(fresh):], prompt.Questions[i].Question) {
			return fmt.Errorf("Muse question changed during selection")
		}
		if err := museSendQuestionKey(ctx, session, "Enter"); err != nil {
			return err
		}
	}
	// Multi-question prompts end at a review screen. Check every selected
	// label there before committing the answer.
	for retry := 0; retry < 12; retry++ {
		pane, err := museTmuxCapturePane(ctx, session)
		if err != nil {
			return err
		}
		review, ok := museRecommendedQuestion(pane)
		if ok && review.review {
			for i, q := range prompt.Questions {
				if !strings.Contains(pane, q.Header+": "+selected[i]) {
					return fmt.Errorf("Muse review disagrees with selected answer")
				}
			}
			for pos := review.current; pos != review.target; {
				button := "Down"
				if pos > review.target {
					button = "Up"
					pos--
				} else {
					pos++
				}
				if err := museSendQuestionKey(ctx, session, button); err != nil {
					return err
				}
			}
			return museSendQuestionKey(ctx, session, "Enter")
		}
		// A single question may submit directly without a review page.
		if len(selected) == 1 && musePendingUserInputError(pane) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("Muse answer review did not appear")
}

func museSendQuestionKey(ctx context.Context, session, key string) error {
	out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, key).CombinedOutput()
	if err != nil {
		return fmt.Errorf("send Muse choice key: %w: %s", err, out)
	}
	return nil
}
