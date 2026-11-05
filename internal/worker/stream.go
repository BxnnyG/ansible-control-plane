package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/BxnnyG/ansible-control-plane/internal/runmask"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

const (
	maxChunk       = 64 << 10
	flushInterval  = 500 * time.Millisecond
	partialLineAge = 2 * time.Second
	maxBatchBytes  = 512 << 10
	sendBudget     = 2 * time.Minute
)

// streamer buffers the combined process output and the callback events of a
// run and ships them to the app in ordered, numbered chunks. Secrets are
// masked before anything leaves the worker.
type streamer struct {
	api         API
	run, lease  string
	masker      *runmask.Masker
	log         *slog.Logger
	onLeaseLost func()

	mu       sync.Mutex
	buf      []byte
	bufSince time.Time
	events   []json.RawMessage
	chunks   []runproto.Chunk
	seq      int64

	sendMu sync.Mutex

	// events file tailing (single goroutine)
	evPath   string
	evOffset int64
	evPart   []byte
}

// Write implements io.Writer for the process output.
func (s *streamer) Write(p []byte) (int, error) {
	s.mu.Lock()
	if len(s.buf) == 0 {
		s.bufSince = time.Now()
	}
	s.buf = append(s.buf, p...)
	s.mu.Unlock()
	return len(p), nil
}

// Println writes a worker-originated status line into the log.
func (s *streamer) Println(msg string) { _, _ = s.Write([]byte(msg + "\n")) }

// cut moves ready output from buf into numbered chunks. Without final it
// keeps an incomplete last line back (so secrets are not split across chunks)
// unless it has been waiting for a while or the buffer is large.
func (s *streamer) cut(final bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.buf)
	if n == 0 {
		return
	}
	if !final {
		if i := bytes.LastIndexByte(s.buf, '\n'); i >= 0 {
			n = i + 1
		} else if n < maxChunk && time.Since(s.bufSince) < partialLineAge {
			return
		}
	}
	data := s.masker.Mask(string(s.buf[:n]))
	s.buf = append(s.buf[:0], s.buf[n:]...)
	s.bufSince = time.Now()
	for len(data) > 0 {
		k := min(len(data), maxChunk)
		for k < len(data) && k > 0 && !utf8.RuneStart(data[k]) {
			k--
		}
		s.seq++
		s.chunks = append(s.chunks, runproto.Chunk{Seq: s.seq, Data: data[:k]})
		data = data[k:]
	}
}

// tail reads new complete lines from the callback events file.
func (s *streamer) tail() {
	if s.evPath == "" {
		return
	}
	f, err := os.Open(s.evPath)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(s.evOffset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, 4<<20))
	if err != nil || len(data) == 0 {
		return
	}
	s.evOffset += int64(len(data))
	s.evPart = append(s.evPart, data...)
	var lines [][]byte
	for {
		i := bytes.IndexByte(s.evPart, '\n')
		if i < 0 {
			break
		}
		if line := bytes.TrimSpace(s.evPart[:i]); len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
		s.evPart = s.evPart[i+1:]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range lines {
		l = []byte(s.masker.Mask(string(l)))
		if json.Valid(l) {
			s.events = append(s.events, json.RawMessage(l))
		}
	}
}

// Flush ships everything that is ready. With final, incomplete lines and the
// remaining events are included too.
func (s *streamer) Flush(ctx context.Context, final bool) error {
	s.tail()
	s.cut(final)
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	for {
		s.mu.Lock()
		if len(s.chunks) == 0 && len(s.events) == 0 {
			s.mu.Unlock()
			return nil
		}
		var req runproto.EventsRequest
		size, nc := 0, 0
		for _, c := range s.chunks {
			if nc > 0 && size+len(c.Data) > maxBatchBytes {
				break
			}
			req.Chunks = append(req.Chunks, c)
			size += len(c.Data)
			nc++
		}
		ne := 0
		for _, e := range s.events {
			if size+len(e) > 2*maxBatchBytes {
				break
			}
			req.Events = append(req.Events, e)
			size += len(e)
			ne++
		}
		if nc == 0 && ne == 0 && len(s.events) > 0 { // single oversized event: drop it
			s.events = s.events[1:]
			s.mu.Unlock()
			continue
		}
		s.mu.Unlock()
		err := withRetry(ctx, sendBudget, func(c context.Context) error { return s.api.Events(c, s.run, s.lease, req) })
		if errors.Is(err, ErrLeaseLost) {
			if s.onLeaseLost != nil {
				s.onLeaseLost()
			}
			return err
		}
		if err != nil {
			s.log.Warn("sending run output failed", "run", s.run, "error", err)
			return err
		}
		s.mu.Lock()
		s.chunks = s.chunks[nc:]
		s.events = s.events[ne:]
		s.mu.Unlock()
	}
}

// loop flushes every flushInterval until ctx is done.
func (s *streamer) loop(ctx context.Context) {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = s.Flush(ctx, false)
		}
	}
}
