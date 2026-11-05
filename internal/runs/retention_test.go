package runs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

type fakePurger struct {
	cutoffs []time.Time
	n       int64
}

func (f *fakePurger) DeleteOldRunLogChunks(_ context.Context, c pgtype.Timestamptz) (int64, error) {
	f.cutoffs = append(f.cutoffs, c.Time)
	return f.n, nil
}

func TestPurgeLogs(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		days   int
		called bool
		cutoff time.Time
	}{
		{"keep forever", 0, false, time.Time{}},
		{"negative keeps", -3, false, time.Time{}},
		{"90 days", 90, true, time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePurger{n: 4}
			n, err := PurgeLogs(context.Background(), f, tc.days, now)
			if err != nil {
				t.Fatal(err)
			}
			if tc.called != (len(f.cutoffs) == 1) {
				t.Fatalf("calls = %d", len(f.cutoffs))
			}
			if tc.called && (!f.cutoffs[0].Equal(tc.cutoff) || n != 4) {
				t.Errorf("cutoff = %v n = %d", f.cutoffs[0], n)
			}
		})
	}
}

func TestRunLogRetentionLoop(t *testing.T) {
	f := &fakePurger{}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	RunLogRetention(ctx, f, 30, 10*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(f.cutoffs) < 3 {
		t.Errorf("passes = %d", len(f.cutoffs))
	}
	g := &fakePurger{}
	RunLogRetention(context.Background(), g, 0, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(g.cutoffs) != 0 {
		t.Error("disabled retention must not purge")
	}
}
