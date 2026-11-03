package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakePurger struct {
	calls atomic.Int32
	n     int64
	err   error
}

func (f *fakePurger) DeleteExpiredSessions(context.Context) (int64, error) {
	f.calls.Add(1)
	return f.n, f.err
}

func TestRunSessionCleanup(t *testing.T) {
	tests := []struct {
		name string
		p    *fakePurger
	}{
		{"removes sessions", &fakePurger{n: 3}},
		{"nothing to remove", &fakePurger{}},
		{"store error keeps running", &fakePurger{err: errors.New("boom")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				RunSessionCleanup(ctx, tc.p, 5*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
				close(done)
			}()
			deadline := time.Now().Add(2 * time.Second)
			for tc.p.calls.Load() < 2 && time.Now().Before(deadline) {
				time.Sleep(2 * time.Millisecond)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("did not stop after cancel")
			}
			if tc.p.calls.Load() < 2 {
				t.Errorf("calls = %d, want >= 2", tc.p.calls.Load())
			}
		})
	}
}
