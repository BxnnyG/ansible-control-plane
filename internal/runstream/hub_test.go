package runstream

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func newHub(t *testing.T) (*Hub, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("ACP_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ACP_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := New(func(ctx context.Context) (*pgx.Conn, error) { return pgx.Connect(ctx, dsn) }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	pub, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pub.Close(context.Background()) })
	return h, pub
}

func expect(t *testing.T, s *Sub, want bool) {
	t.Helper()
	select {
	case <-s.C:
		if !want {
			t.Fatal("unexpected signal")
		}
	case <-time.After(2 * time.Second):
		if want {
			t.Fatal("no signal")
		}
	}
}

func notify(t *testing.T, c *pgx.Conn, ch string) {
	t.Helper()
	if _, err := c.Exec(context.Background(), "SELECT pg_notify($1, 'x')", ch); err != nil {
		t.Fatal(err)
	}
}

func TestHubFanOutAndIsolation(t *testing.T) {
	h, pub := newHub(t)
	ctx := context.Background()
	a1, err := h.Subscribe(ctx, "acp_run_aaaa")
	if err != nil {
		t.Fatal(err)
	}
	a2, err := h.Subscribe(ctx, "acp_run_aaaa")
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Subscribe(ctx, "acp_run_bbbb")
	if err != nil {
		t.Fatal(err)
	}
	// Drain the initial wake-up of the first connect, if any.
	for _, s := range []*Sub{a1, a2, b} {
		select {
		case <-s.C:
		default:
		}
	}
	notify(t, pub, "acp_run_aaaa")
	expect(t, a1, true)
	expect(t, a2, true)
	notify(t, pub, "acp_run_bbbb")
	expect(t, b, true)
	expect(t, a1, false)
}

func TestHubUnsubscribeStopsListening(t *testing.T) {
	h, pub := newHub(t)
	ctx := context.Background()
	s, err := h.Subscribe(ctx, "acp_run_cccc")
	if err != nil {
		t.Fatal(err)
	}
	if h.Channels() != 1 {
		t.Fatalf("channels = %d", h.Channels())
	}
	s.Close()
	s.Close() // idempotent
	if h.Channels() != 0 {
		t.Fatalf("channels = %d", h.Channels())
	}
	// A new subscription on the same channel still works after the UNLISTEN.
	s2, err := h.Subscribe(ctx, "acp_run_cccc")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s2.C:
	default:
	}
	notify(t, pub, "acp_run_cccc")
	expect(t, s2, true)
}

func TestHubReconnectWakesSubscribers(t *testing.T) {
	h, pub := newHub(t)
	ctx := context.Background()
	s, err := h.Subscribe(ctx, "acp_run_dddd")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.C:
	default:
	}
	// Kill the hub's backend connection; the hub must reconnect, wake the
	// subscriber and keep delivering.
	if _, err := pub.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid()"); err != nil {
		t.Fatal(err)
	}
	expect(t, s, true)
	time.Sleep(200 * time.Millisecond)
	notify(t, pub, "acp_run_dddd")
	expect(t, s, true)
}
