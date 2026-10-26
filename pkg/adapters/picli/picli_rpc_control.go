package picli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

func sendPiRPCInput(ctx context.Context, owner, message string) error {
	s := piRPCSessionForOwner(owner)
	if s == nil {
		return fmt.Errorf("no active Pi RPC session for owner %s", owner)
	}
	s.mu.Lock()
	r, turn := s.rpc, s.turnID
	if r == nil || turn == "" {
		s.mu.Unlock()
		return fmt.Errorf("Pi has no active turn; start a normal turn")
	}
	receipt := &piRPCReceipt{text: strings.TrimSpace(message), at: time.Now(), observed: make(chan struct{})}
	s.receipts = append(s.receipts, receipt)
	s.mu.Unlock()
	var result struct {
		Disposition string `json:"disposition"`
	}
	if err := r.call(ctx, "steer", map[string]any{"message": message}, &result); err != nil {
		return err
	}
	if result.Disposition == "handled" {
		return fmt.Errorf("Pi input handler consumed the steering message; no queued user message")
	}
	return nil
}
func awaitPiRPCInputDurable(ctx context.Context, owner, message string, timeout time.Duration) (PiDurableAck, error) {
	start := time.Now()
	s := piRPCSessionForOwner(owner)
	if s == nil {
		return PiDurableAck{}, fmt.Errorf("no active Pi RPC session")
	}
	s.mu.Lock()
	var receipt *piRPCReceipt
	for i := 0; i < len(s.receipts); i++ {
		if !s.receipts[i].claimed && s.receipts[i].text == strings.TrimSpace(message) {
			receipt = s.receipts[i]
			receipt.claimed = true
			break
		}
	}
	s.mu.Unlock()
	if receipt == nil {
		return PiDurableAck{}, fmt.Errorf("no matching Pi RPC send receipt")
	}
	if timeout <= 0 {
		timeout = piDurableAckBudget()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case <-receipt.observed:
	case <-ctx.Done():
		return PiDurableAck{Outcome: PiDurableAckUnflushed, Latency: time.Since(start)}, ctx.Err()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		file, stamp := s.sessionFile, receipt.timestamp
		s.mu.Unlock()
		if piRPCUserPersisted(file, receipt.text, stamp, receipt.occurrence) {
			return PiDurableAck{Outcome: PiDurableAckConfirmed, Latency: time.Since(start), ProofPath: file, RowTimestamp: time.UnixMilli(stamp)}, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return PiDurableAck{Outcome: PiDurableAckUnflushed, Latency: time.Since(start)}, ctx.Err()
		}
	}
}
func piRPCUserPersisted(file, text string, stamp int64, occurrence int) bool {
	f, err := os.Open(file)
	if err != nil {
		return false
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 16*1024*1024)
	found := 0
	for scan.Scan() {
		var row struct {
			Type    string             `json:"type"`
			Message piRPCNativeMessage `json:"message"`
		}
		if json.Unmarshal(scan.Bytes(), &row) == nil && row.Type == "message" && row.Message.Role == "user" && row.Message.Timestamp == stamp && piTranscriptText(piRPCParts(row.Message.Content)) == text {
			found++
			if found >= occurrence {
				return true
			}
		}
	}
	return false
}
func interruptPiRPC(ctx context.Context, owner, key string) error {
	if key != "Escape" && key != "Esc" && key != "C-c" && key != "Ctrl-C" {
		return fmt.Errorf("Pi RPC does not support terminal key %q", key)
	}
	s := piRPCSessionForOwner(owner)
	if s == nil {
		return fmt.Errorf("no active Pi RPC session")
	}
	s.mu.Lock()
	r := s.rpc
	s.mu.Unlock()
	if r == nil {
		return fmt.Errorf("Pi RPC is not running")
	}
	if err := r.call(ctx, "clear_queue", nil, nil); err != nil {
		return err
	}
	return r.call(ctx, "abort", nil, nil)
}
