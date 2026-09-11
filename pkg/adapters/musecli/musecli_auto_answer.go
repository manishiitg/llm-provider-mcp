package musecli

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
)

const metadataMuseAutoSelectRecommended = "muse_auto_select_recommended"

// WithAutoSelectRecommended controls native question widgets in the tmux lane.
// Enabled by default. Only a single explicitly labelled recommendation is
// eligible; missing/ambiguous recommendations remain user_input_required.
func WithAutoSelectRecommended(enabled bool) llmtypes.CallOption {
	return func(o *llmtypes.CallOptions) {
		ensureMetadata(o)
		o.Metadata.Custom[metadataMuseAutoSelectRecommended] = enabled
	}
}

type museAutoAnswerKey struct{}
type museAutoAnswerState struct {
	mu               sync.Mutex
	stopped          atomic.Bool
	lastQuestion     string
	observedQuestion string
	observedCursor   int
}
type museRecommendedAnswer struct {
	key, label      string
	review          bool
	current, target int
}

var museQuestionOptionRE = regexp.MustCompile(`^\s*([›❯>]?)\s*(\d+)\.\s+(.+)$`)
var museQuestionRunningRE = regexp.MustCompile(`(?i)\s*[—·]\s*running\s*\([^)]*\)`)

// Parse only the active native widget, never recommendation text in history.
func museRecommendedQuestion(pane string) (museRecommendedAnswer, bool) {
	if musePendingUserInputError(pane) == nil {
		return museRecommendedAnswer{}, false
	}
	start := strings.LastIndex(strings.ToLower(pane), "◆ request user input")
	widget := pane[start:]
	if strings.Contains(strings.ToLower(widget), "review answers before submit") {
		return museRecommendedReview(widget)
	}
	end := strings.Index(strings.ToLower(widget), "enter to select")
	widget = widget[:end]
	if musePaneShowsBlockingGate(widget) {
		return museRecommendedAnswer{}, false
	}
	var result museRecommendedAnswer
	result.current = -1
	result.target = -1
	var identity []string
	count, recommended, cursors := 0, 0, 0
	for _, line := range strings.Split(widget, "\n") {
		match := museQuestionOptionRE.FindStringSubmatch(line)
		if match == nil {
			identity = append(identity, museQuestionRunningRE.ReplaceAllString(strings.TrimSpace(line), ""))
			continue
		}
		label := strings.TrimSpace(match[3])
		identity = append(identity, match[2]+". "+label)
		if match[1] != "" {
			result.current = count
			cursors++
		}
		if strings.Contains(strings.ToLower(label), "(recommended)") {
			result.target = count
			result.label = label
			recommended++
		}
		count++
	}
	result.key = strings.Join(identity, "\n")
	return result, count > 0 && recommended == 1 && cursors == 1
}

func museWithAutoAnswer(ctx context.Context, opts *llmtypes.CallOptions) context.Context {
	if opts != nil && opts.Metadata != nil {
		if value, ok := opts.Metadata.Custom[metadataMuseAutoSelectRecommended].(bool); ok && !value {
			return ctx
		}
	}
	state := &museAutoAnswerState{}
	return context.WithValue(ctx, museAutoAnswerKey{}, state)
}

// Returns pending=true while a widget exists, including after sending Enter.
// Callers must wait for it to disappear before treating the pane as idle.
func museHandlePendingQuestion(ctx context.Context, session, pane string) (pending bool, err error) {
	state, _ := ctx.Value(museAutoAnswerKey{}).(*museAutoAnswerState)
	if state != nil {
		state.mu.Lock()
		defer state.mu.Unlock()
	}
	pendingErr := musePendingUserInputError(pane)
	if pendingErr == nil {
		if state != nil {
			state.lastQuestion = ""
			state.observedQuestion = ""
		}
		return false, nil
	}
	answer, ok := museRecommendedQuestion(pane)
	if state == nil || !ok {
		return true, pendingErr
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if state.stopped.Load() {
		return true, context.Canceled
	}
	if state.lastQuestion == answer.key {
		return true, nil
	}
	// Cursor's readiness handler also requires consecutive visible captures;
	// first paint can precede the native input loop becoming interactive.
	if state.observedQuestion != answer.key || state.observedCursor != answer.current {
		state.observedQuestion = answer.key
		state.observedCursor = answer.current
		return true, nil
	}
	currentPane, captureErr := museTmuxCapturePane(ctx, session)
	if captureErr != nil {
		return true, captureErr
	}
	current, valid := museRecommendedQuestion(currentPane)
	if !valid || current.key != answer.key || current.current != answer.current {
		return true, nil
	}
	// Move from the observed cursor, not from an assumed first/default option.
	for offset := answer.current; offset != answer.target; {
		if state.stopped.Load() {
			return true, context.Canceled
		}
		key := "Down"
		if offset > answer.target {
			key = "Up"
			offset--
		} else {
			offset++
		}
		if out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, key).CombinedOutput(); err != nil {
			return true, fmt.Errorf("navigate Muse recommendation: %w: %s", err, out)
		}
	}
	// Recheck the widget and cursor after navigation before submitting anything.
	fresh, err := museTmuxCapturePane(ctx, session)
	if err != nil {
		return true, err
	}
	selected, ok := museRecommendedQuestion(fresh)
	if !ok || selected.key != answer.key || selected.current != answer.target {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if state.stopped.Load() {
		return true, context.Canceled
	}
	if out, err := exec.CommandContext(ctx, "tmux", "send-keys", "-t", session, "Enter").CombinedOutput(); err != nil {
		return true, fmt.Errorf("submit Muse recommendation: %w: %s", err, out)
	}
	state.lastQuestion = answer.key
	return true, nil
}

// Muse multi-question widgets have a final review row. Submit only a review
// whose every visible answer is explicitly marked recommended.
func museRecommendedReview(widget string) (museRecommendedAnswer, bool) {
	result := museRecommendedAnswer{current: -1, target: -1, review: true, label: "Submit answers"}
	var identity []string
	inReview := false
	rows, answers, cursors := 0, 0, 0
	for _, raw := range strings.Split(widget, "\n") {
		line := strings.TrimSpace(raw)
		if strings.Contains(strings.ToLower(line), "review answers before submit") {
			inReview = true
			continue
		}
		if !inReview || line == "" {
			continue
		}
		if strings.HasPrefix(line, "─") {
			break
		}
		cursor := strings.HasPrefix(line, ">") || strings.HasPrefix(line, "›") || strings.HasPrefix(line, "❯")
		if cursor {
			result.current = rows
			cursors++
			line = strings.TrimSpace(strings.TrimLeft(line, ">›❯"))
		}
		switch line {
		case "Submit answers":
			result.target = rows
		case "Interrupt turn":
		default:
			_, answer, ok := strings.Cut(line, ":")
			if !ok || !strings.Contains(strings.ToLower(answer), "(recommended)") {
				return result, false
			}
			answers++
		}
		identity = append(identity, line)
		rows++
	}
	result.key = "review\n" + strings.Join(identity, "\n")
	return result, inReview && answers > 0 && result.target >= 0 && cursors == 1 && !musePaneShowsBlockingGate(widget)
}

// Share only selector state across live delivery, generation, and retained
// polling. Automatic selections do not emit user-facing stream messages.
func museBindPersistentAutoAnswer(ctx context.Context, entry *musePersistentSession) context.Context {
	requested, _ := ctx.Value(museAutoAnswerKey{}).(*museAutoAnswerState)
	musePersistentPool.Lock()
	defer musePersistentPool.Unlock()
	if requested == nil {
		entry.autoAnswer = nil
		return ctx
	}
	if entry.autoAnswer == nil {
		entry.autoAnswer = requested
	}
	return context.WithValue(ctx, museAutoAnswerKey{}, entry.autoAnswer)
}
