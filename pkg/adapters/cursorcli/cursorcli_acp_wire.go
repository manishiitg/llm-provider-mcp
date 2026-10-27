package cursorcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
)

// Each owned process carries one native thread. Responses are dispatched before
// notifications, so responses and session/cancel work while GenerateContent is
// consuming the turn's stream. No terminal text participates in this protocol.
type cursorACPMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *cursorACPError `json:"error,omitempty"`
}

type cursorACPError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *cursorACPError) Error() string {
	return fmt.Sprintf("(%d): %s", e.Code, e.Message)
}

type cursorACP struct {
	logger  interfaces.Logger
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan cursorACPMessage
	err     error
	next    atomic.Uint64
	events  chan cursorACPMessage
	done    chan struct{}
	exited  chan struct{}
	stop    context.CancelFunc
	cleanup func()
	once    sync.Once
}

func (r *cursorACP) write(v any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return json.NewEncoder(r.stdin).Encode(v)
}

func (r *cursorACP) call(ctx context.Context, method string, params any, result any) error {
	id := fmt.Sprintf("aw-%d", r.next.Add(1))
	key, _ := json.Marshal(id)
	ch := make(chan cursorACPMessage, 1)
	r.mu.Lock()
	r.pending[string(key)] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, string(key)); r.mu.Unlock() }()
	if err := r.writeContext(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("Cursor ACP %s: %w", method, err)
	}
	select {
	case response := <-ch:
		if r.logger != nil {
			r.logger.Debugf("Cursor ACP response method=%s id=%s error=%t", method, id, response.Error != nil)
		}
		if response.Error != nil {
			return fmt.Errorf("Cursor ACP %s %w", method, response.Error)
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return fmt.Errorf("Cursor ACP %s: %w", method, r.failure())
	}
}

func (r *cursorACP) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	return fmt.Errorf("Cursor ACP connection closed")
}

func (r *cursorACP) finish(err error) {
	r.once.Do(func() {
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(r.done)
		r.stop()
		if r.cmd != nil && r.cmd.Process != nil {
			_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		}
		_ = r.stdin.Close()
	})
}

func (r *cursorACP) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {

		var msg cursorACPMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			r.finish(fmt.Errorf("invalid Cursor ACP JSON: %w", err))
			return
		}
		if msg.Method == "" && len(msg.ID) > 0 {
			r.mu.Lock()
			ch := r.pending[string(msg.ID)]
			r.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}

		select {
		case r.events <- msg:
		case <-r.done:
			return
		default:
			r.finish(fmt.Errorf("Cursor ACP event consumer fell behind"))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		r.finish(fmt.Errorf("read Cursor ACP: %w", err))
	} else {
		// cmd.Wait owns EOF classification after draining stderr.
		<-r.exited
	}
}

func (r *cursorACP) close() {
	r.finish(fmt.Errorf("Cursor ACP closed"))
	<-r.exited
}

// Closing the pipe releases a stalled writer on cancellation.
func (r *cursorACP) writeContext(ctx context.Context, value any) error {
	done := make(chan error, 1)
	go func() { done <- r.write(value) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		r.finish(ctx.Err())
		return ctx.Err()
	case <-r.done:
		return r.failure()
	}
}

// PLAT-872: Cursor exits instead of replying to session/new for an unavailable
// model. Recognize only this fixed diagnostic; never retain raw stderr (which
// may contain credentials or chat data), including partial chunks.
type cursorACPLaunchDiagnostic struct {
	matched          int
	modelUnavailable bool
}

func (d *cursorACPLaunchDiagnostic) Write(p []byte) (int, error) {
	const marker = "Cannot use this model:"
	for _, b := range p {
		if b == marker[d.matched] {
			d.matched++
			if d.matched == len(marker) {
				d.modelUnavailable = true
				d.matched = 0
			}
		} else if b == marker[0] {
			d.matched = 1
		} else {
			d.matched = 0
		}
	}
	return len(p), nil
}
