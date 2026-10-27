package codexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/manishiitg/multi-llm-provider-go/interfaces"
	"github.com/manishiitg/multi-llm-provider-go/llmtypes"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
)

// Each owned process carries one native thread. Responses are dispatched before
// notifications, so turn/steer and turn/interrupt work while GenerateContent is
// consuming the turn's stream. No terminal text participates in this protocol.
type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexRPC struct {
	logger  interfaces.Logger
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan codexRPCMessage
	err     error
	next    atomic.Uint64
	events  chan codexRPCMessage
	done    chan struct{}
	exited  chan struct{}
	stop    context.CancelFunc
	cleanup func()
	once    sync.Once
}

func (r *codexRPC) write(v any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return json.NewEncoder(r.stdin).Encode(v)
}

func (r *codexRPC) call(ctx context.Context, method string, params any, result any) error {
	id := fmt.Sprintf("aw-%d", r.next.Add(1))
	key, _ := json.Marshal(id)
	ch := make(chan codexRPCMessage, 1)
	r.mu.Lock()
	r.pending[string(key)] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, string(key)); r.mu.Unlock() }()
	if err := r.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return fmt.Errorf("codex app-server %s: %w", method, err)
	}
	select {
	case response := <-ch:
		if r.logger != nil {
			r.logger.Debugf("codex app-server response method=%s id=%s error=%t", method, id, response.Error != nil)
		}
		if response.Error != nil {
			if method == "turn/steer" {
				return fmt.Errorf("%w: Codex rejected steer (%d)", llmtypes.ErrCodingAgentInputNotSent, response.Error.Code)
			}
			return fmt.Errorf("codex app-server %s (%d): %s", method, response.Error.Code, response.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return r.failure()
	}
}

func (r *codexRPC) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	return fmt.Errorf("codex app-server connection closed")
}

func (r *codexRPC) finish(err error) {
	r.once.Do(func() {
		r.mu.Lock()
		r.err = err
		r.mu.Unlock()
		close(r.done)
		r.stop()
		_ = r.stdin.Close()
	})
}

func (r *codexRPC) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg codexRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			r.finish(fmt.Errorf("invalid codex app-server JSON: %w", err))
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
		// Legacy codex/event notifications duplicate the typed v2 events.
		if len(msg.Method) >= 12 && msg.Method[:12] == "codex/event/" {
			continue
		}
		select {
		case r.events <- msg:
		case <-r.done:
			return
		default:
			r.finish(fmt.Errorf("codex app-server event consumer fell behind"))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		r.finish(fmt.Errorf("read codex app-server: %w", err))
	} else {
		r.finish(io.EOF)
	}
}

func (r *codexRPC) close() {
	r.finish(fmt.Errorf("codex app-server closed"))
	<-r.exited
}
