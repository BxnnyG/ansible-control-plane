// Package runstream fans PostgreSQL NOTIFY messages out to SSE subscribers
// (ADR-0008) over one shared LISTEN connection, so the number of database
// connections does not grow with the number of open browser streams.
package runstream

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// Connector opens the dedicated LISTEN connection.
type Connector func(ctx context.Context) (*pgx.Conn, error)

// Hub holds one LISTEN connection and dispatches wake-ups per channel.
// Notifications carry no state: subscribers re-read the database on wake-up,
// which also makes missed notifications (reconnects) harmless.
type Hub struct {
	connect Connector
	log     *slog.Logger

	mu     sync.Mutex
	subs   map[string]map[*Sub]struct{}
	ops    []*op
	wakeup chan struct{} // interrupts the blocking wait so ops are processed
}

type op struct {
	listen  bool
	channel string
	done    chan error // nil for fire-and-forget
}

// Sub is one subscription. C receives a (coalesced) signal whenever the
// channel was notified or the hub reconnected.
type Sub struct {
	C       chan struct{}
	channel string
	hub     *Hub
	once    sync.Once
}

// New builds a Hub; call Run to start it.
func New(connect Connector, log *slog.Logger) *Hub {
	return &Hub{connect: connect, log: log, subs: map[string]map[*Sub]struct{}{}, wakeup: make(chan struct{}, 1)}
}

// Subscribe registers for channel and returns once the LISTEN is active, so a
// replay that starts afterwards cannot miss a notification.
func (h *Hub) Subscribe(ctx context.Context, channel string) (*Sub, error) {
	s := &Sub{C: make(chan struct{}, 1), channel: channel, hub: h}
	h.mu.Lock()
	set := h.subs[channel]
	first := set == nil
	if first {
		set = map[*Sub]struct{}{}
		h.subs[channel] = set
	}
	set[s] = struct{}{}
	var done chan error
	if first {
		done = make(chan error, 1)
		h.ops = append(h.ops, &op{listen: true, channel: channel, done: done})
	}
	h.mu.Unlock()
	if !first {
		return s, nil
	}
	h.poke()
	select {
	case err := <-done:
		if err != nil {
			s.Close()
			return nil, err
		}
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
	return s, nil
}

// Close unsubscribes; the LISTEN ends with the last subscriber of a channel.
func (s *Sub) Close() {
	s.once.Do(func() {
		h := s.hub
		if h == nil { // detached (test) subscription
			return
		}
		h.mu.Lock()
		set := h.subs[s.channel]
		delete(set, s)
		last := len(set) == 0
		if last {
			delete(h.subs, s.channel)
			h.ops = append(h.ops, &op{listen: false, channel: s.channel})
		}
		h.mu.Unlock()
		if last {
			h.poke()
		}
	})
}

// Channels returns the number of channels currently listened on.
func (h *Hub) Channels() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *Hub) poke() {
	select {
	case h.wakeup <- struct{}{}:
	default:
	}
}

func (h *Hub) deliver(channel string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs[channel] {
		signal(s.C)
	}
}

func (h *Hub) deliverAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, set := range h.subs {
		for s := range set {
			signal(s.C)
		}
	}
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// Run keeps the LISTEN connection alive until ctx is cancelled, reconnecting
// with a short backoff and waking all subscribers after each reconnect.
func (h *Hub) Run(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := h.connect(ctx)
		if err != nil {
			if ctx.Err() == nil {
				h.log.Warn("run stream: connect failed", "error", err)
			}
			sleep(ctx, 2*time.Second)
			continue
		}
		err = h.serve(ctx, conn)
		_ = conn.Close(context.WithoutCancel(ctx))
		if ctx.Err() != nil {
			break
		}
		h.log.Warn("run stream: listen connection lost, reconnecting", "error", err)
		h.deliverAll()
		sleep(ctx, time.Second)
	}
	h.failPending(errors.New("run stream hub stopped"))
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (h *Hub) failPending(err error) {
	h.mu.Lock()
	ops := h.ops
	h.ops = nil
	h.mu.Unlock()
	for _, o := range ops {
		if o.done != nil {
			o.done <- err
		}
	}
}

// serve re-listens on all known channels, then alternates between executing
// queued LISTEN/UNLISTEN ops and waiting for notifications. The wait is
// interrupted through a context; pgx treats the resulting deadline as a
// timeout and keeps the connection usable.
func (h *Hub) serve(ctx context.Context, conn *pgx.Conn) error {
	h.mu.Lock()
	known := make([]string, 0, len(h.subs))
	for c := range h.subs {
		known = append(known, c)
	}
	h.mu.Unlock()
	for _, c := range known {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{c}.Sanitize()); err != nil {
			return err
		}
	}
	h.deliverAll() // anything may have been missed while disconnected
	for {
		if err := h.runOps(ctx, conn); err != nil {
			return err
		}
		wctx, cancel := context.WithCancel(ctx)
		stop := make(chan struct{})
		go func() {
			select {
			case <-h.wakeup:
				cancel()
			case <-stop:
			}
		}()
		n, err := conn.WaitForNotification(wctx)
		close(stop)
		interrupted := wctx.Err() != nil
		cancel()
		switch {
		case err == nil:
			h.deliver(n.Channel)
		case ctx.Err() != nil:
			return ctx.Err()
		case interrupted && !conn.IsClosed():
			// woken up for pending ops
		default:
			return err
		}
	}
}

func (h *Hub) runOps(ctx context.Context, conn *pgx.Conn) error {
	h.mu.Lock()
	ops := h.ops
	h.ops = nil
	h.mu.Unlock()
	for i, o := range ops {
		verb := "UNLISTEN "
		if o.listen {
			verb = "LISTEN "
		}
		_, err := conn.Exec(ctx, verb+pgx.Identifier{o.channel}.Sanitize())
		if o.done != nil {
			o.done <- err
		}
		if err != nil {
			h.mu.Lock()
			h.ops = append(append([]*op{}, ops[i+1:]...), h.ops...)
			h.mu.Unlock()
			return err
		}
	}
	return nil
}
