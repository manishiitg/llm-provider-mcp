package nativeinput

import (
	"context"
	"testing"
	"time"
)

// PLAT-872-21: native resolution/turn completion must close pending UI waits.
func TestRequestsCancelOnlyMatchingQuestionAndCloseRemaining(t *testing.T) {
	var r Requests
	first, second := make(chan struct{}), make(chan struct{})
	r.Start(context.Background(), "first", func(ctx context.Context) { <-ctx.Done(); close(first) })
	r.Start(context.Background(), "second", func(ctx context.Context) { <-ctx.Done(); close(second) })
	r.Cancel("first")
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("resolved question stayed pending")
	}
	select {
	case <-second:
		t.Fatal("unrelated question cancelled")
	default:
	}
	r.Close()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("turn exit left question pending")
	}
}
