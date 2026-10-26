package picli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
)

// Pi RPC uses LF-delimited JSON, not terminal output. Replies are dispatched
// independently of events so live steer/abort cannot wait behind a model turn.
type piRPCRecord struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
	piJSONEvent
	Message json.RawMessage `json:"message"`
	Raw     json.RawMessage `json:"-"`
}
type piRPC struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	writeMu      sync.Mutex
	mu           sync.Mutex
	pending      map[string]chan piRPCRecord
	err          error
	next         atomic.Uint64
	events       chan piRPCRecord
	done, exited chan struct{}
	stop         context.CancelFunc
	once         sync.Once
}

func (r *piRPC) write(v any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return json.NewEncoder(r.stdin).Encode(v)
}
func (r *piRPC) call(ctx context.Context, command string, params map[string]any, result any) error {
	id := fmt.Sprintf("aw-%d", r.next.Add(1))
	request := map[string]any{"id": id, "type": command}
	for k, v := range params {
		request[k] = v
	}
	ch := make(chan piRPCRecord, 1)
	r.mu.Lock()
	r.pending[id] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	written := make(chan error, 1)
	go func() { written <- r.write(request) }()
	select {
	case err := <-written:
		if err != nil {
			return fmt.Errorf("Pi RPC %s: %w", command, err)
		}
	case <-ctx.Done():
		r.finish(ctx.Err())
		return ctx.Err()
	case <-r.done:
		return r.failure()
	}
	select {
	case msg := <-ch:
		if !msg.Success {
			return fmt.Errorf("Pi RPC %s: %s", command, msg.Error)
		}
		if result != nil && len(msg.Data) > 0 {
			return json.Unmarshal(msg.Data, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-r.done:
		return r.failure()
	}
}
func (r *piRPC) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	return fmt.Errorf("Pi RPC closed")
}
func (r *piRPC) finish(err error) {
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
func (r *piRPC) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var msg piRPCRecord
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			r.finish(fmt.Errorf("invalid Pi RPC JSON: %w", err))
			return
		}
		msg.piJSONEvent.Type = msg.Type
		if msg.Type == "response" {
			r.mu.Lock()
			ch := r.pending[msg.ID]
			r.mu.Unlock()
			if ch != nil {
				select {
				case ch <- msg:
				default:
				}
			}
			continue
		}
		msg.Raw = append(json.RawMessage(nil), scanner.Bytes()...)
		select {
		case r.events <- msg:
		case <-r.done:
			return
		default:
			r.finish(fmt.Errorf("Pi RPC event consumer fell behind"))
			return
		}
	}
	if err := scanner.Err(); err != nil {
		r.finish(fmt.Errorf("read Pi RPC: %w", err))
	} else {
		r.finish(io.EOF)
	}
}
func (r *piRPC) close() { r.finish(fmt.Errorf("Pi RPC closed")); <-r.exited }
