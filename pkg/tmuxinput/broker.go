package tmuxinput

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Priority controls which queued transaction runs next. It never interrupts a
// transaction that is already writing to tmux.
type Priority uint8

const (
	PriorityNormal Priority = iota
	PriorityInterrupt
)

type Request struct {
	SessionID string
	MessageID string
	Source    string
	Priority  Priority
}

type Result struct {
	EnqueuedAt  time.Time
	StartedAt   time.Time
	CompletedAt time.Time
}

type Operation func(context.Context) error

type response struct {
	result Result
	err    error
}

type queuedRequest struct {
	ctx       context.Context
	request   Request
	operation Operation
	response  chan response
	result    Result
}

type sessionWorker struct {
	incoming chan *queuedRequest
}

// Broker serializes complete tmux input transactions by tmux session. A
// transaction may contain several tmux commands (for example load-buffer,
// paste-buffer, then Enter); no other source can interleave commands while it
// is running.
type Broker struct {
	mu      sync.Mutex
	workers map[string]*sessionWorker
}

func NewBroker() *Broker {
	return &Broker{workers: make(map[string]*sessionWorker)}
}

var Default = NewBroker()

func PriorityForKey(key string) Priority {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "escape", "esc", "c-c", "ctrl-c":
		return PriorityInterrupt
	default:
		return PriorityNormal
	}
}

func (b *Broker) Do(ctx context.Context, request Request, operation Operation) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.SessionID == "" {
		return Result{}, errors.New("tmux input session ID is required")
	}
	if operation == nil {
		return Result{}, errors.New("tmux input operation is required")
	}

	queued := &queuedRequest{
		ctx:       ctx,
		request:   request,
		operation: operation,
		response:  make(chan response, 1),
		result:    Result{EnqueuedAt: time.Now()},
	}
	worker := b.worker(request.SessionID)

	select {
	case worker.incoming <- queued:
	case <-ctx.Done():
		return queued.result, ctx.Err()
	}

	select {
	case result := <-queued.response:
		return result.result, result.err
	case <-ctx.Done():
		return queued.result, ctx.Err()
	}
}

func (b *Broker) worker(sessionID string) *sessionWorker {
	b.mu.Lock()
	defer b.mu.Unlock()

	if worker := b.workers[sessionID]; worker != nil {
		return worker
	}

	worker := &sessionWorker{incoming: make(chan *queuedRequest, 256)}
	b.workers[sessionID] = worker
	go worker.run()
	return worker
}

func (w *sessionWorker) run() {
	var interrupts []*queuedRequest
	var normal []*queuedRequest

	appendRequest := func(request *queuedRequest) {
		if request.request.Priority == PriorityInterrupt {
			interrupts = append(interrupts, request)
			return
		}
		normal = append(normal, request)
	}

	for {
		if len(interrupts) == 0 && len(normal) == 0 {
			appendRequest(<-w.incoming)
		}

		// Collect everything that arrived while the previous transaction was
		// running so an interrupt can go ahead of queued normal input.
		for {
			select {
			case request := <-w.incoming:
				appendRequest(request)
			default:
				goto execute
			}
		}

	execute:
		var request *queuedRequest
		if len(interrupts) > 0 {
			request = interrupts[0]
			interrupts = interrupts[1:]
		} else {
			request = normal[0]
			normal = normal[1:]
		}

		if err := request.ctx.Err(); err != nil {
			request.result.CompletedAt = time.Now()
			request.response <- response{result: request.result, err: err}
			continue
		}

		request.result.StartedAt = time.Now()
		err := request.operation(request.ctx)
		request.result.CompletedAt = time.Now()
		request.response <- response{result: request.result, err: err}
	}
}
