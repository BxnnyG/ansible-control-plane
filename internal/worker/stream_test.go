package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/BxnnyG/ansible-control-plane/internal/runmask"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

// fakeAPI records what the worker sends.
type fakeAPI struct {
	mu        sync.Mutex
	bundle    []byte
	chunks    []runproto.Chunk
	events    []json.RawMessage
	finish    *runproto.FinishRequest
	cancelAt  int // heartbeat number at which cancel is returned (0 = never)
	beats     int
	leaseLost bool
	eventErrs int // first N Events calls fail
}

func (f *fakeAPI) Claim(context.Context, string) (*runproto.ClaimResponse, error) { return nil, nil }

func (f *fakeAPI) Bundle(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(f.bundle))), nil
}

func (f *fakeAPI) Events(_ context.Context, _, _ string, req runproto.EventsRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eventErrs > 0 {
		f.eventErrs--
		return &HTTPError{Status: 503}
	}
	if f.leaseLost {
		return ErrLeaseLost
	}
	f.chunks = append(f.chunks, req.Chunks...)
	f.events = append(f.events, req.Events...)
	return nil
}

func (f *fakeAPI) Heartbeat(context.Context, string, string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beats++
	if f.leaseLost {
		return false, ErrLeaseLost
	}
	return f.cancelAt > 0 && f.beats >= f.cancelAt, nil
}

func (f *fakeAPI) Finish(_ context.Context, _, _ string, req runproto.FinishRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finish = &req
	return nil
}

func (f *fakeAPI) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var sb strings.Builder
	for _, c := range f.chunks {
		sb.WriteString(c.Data)
	}
	return sb.String()
}

func newStreamer(api API, evPath string, secrets ...string) *streamer {
	return &streamer{api: api, run: "r", lease: "l", log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		masker: runmask.New(secrets), evPath: evPath}
}

func TestStreamerMasksChunksAndKeepsPartialLines(t *testing.T) {
	api := &fakeAPI{}
	s := newStreamer(api, "", "topsecret1")
	ctx := context.Background()
	_, _ = s.Write([]byte("line one topsecret1\npartial topsec"))
	if err := s.Flush(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := api.log(); got != "line one ***\n" {
		t.Errorf("after first flush: %q", got)
	}
	_, _ = s.Write([]byte("ret1 end\n"))
	if err := s.Flush(ctx, false); err != nil {
		t.Fatal(err)
	}
	if got := api.log(); got != "line one ***\npartial *** end\n" {
		t.Errorf("secret split across writes leaked: %q", got)
	}
	_, _ = s.Write([]byte("tail without newline"))
	_ = s.Flush(ctx, true)
	for i, c := range api.chunks {
		if c.Seq != int64(i+1) {
			t.Errorf("chunk %d has seq %d", i, c.Seq)
		}
	}
	if !strings.HasSuffix(api.log(), "tail without newline") {
		t.Error("final flush lost the partial line")
	}
}

func TestStreamerSplitsLargeOutput(t *testing.T) {
	api := &fakeAPI{}
	s := newStreamer(api, "")
	_, _ = s.Write([]byte(strings.Repeat("x", 3*maxChunk+10) + "\n"))
	if err := s.Flush(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(api.chunks) != 4 {
		t.Fatalf("chunks = %d", len(api.chunks))
	}
	for _, c := range api.chunks {
		if len(c.Data) > maxChunk {
			t.Errorf("chunk %d too large: %d", c.Seq, len(c.Data))
		}
	}
}

func TestStreamerEventsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	api := &fakeAPI{}
	s := newStreamer(api, path, "hunter22")
	ctx := context.Background()
	if err := s.Flush(ctx, false); err != nil { // file does not exist yet
		t.Fatal(err)
	}
	f, _ := os.Create(path)
	_, _ = f.WriteString(`{"event":"task_start","task":"a"}` + "\n" + `{"event":"runner_ok","host":"h","task":"hunter22"}` + "\n" + `{"event":"sta`)
	_ = f.Sync()
	_ = s.Flush(ctx, false)
	if len(api.events) != 0 {
		t.Fatalf("events must be held until the next flush: %s", api.events)
	}
	_ = s.Flush(ctx, false)
	if len(api.events) != 2 || strings.Contains(string(api.events[1]), "hunter22") {
		t.Fatalf("events: %s", api.events)
	}
	_, _ = f.WriteString(`ts","hosts":{}}` + "\n" + "not json\n")
	_ = f.Close()
	_ = s.Flush(ctx, true)
	if len(api.events) != 3 || !strings.Contains(string(api.events[2]), `"stats"`) {
		t.Errorf("events after completion: %s", api.events)
	}
}

func TestStreamerRetriesAndLeaseLoss(t *testing.T) {
	api := &fakeAPI{eventErrs: 2}
	s := newStreamer(api, "")
	_, _ = s.Write([]byte("hello\n"))
	if err := s.Flush(context.Background(), false); err != nil {
		t.Fatalf("flush with transient errors: %v", err)
	}
	if api.log() != "hello\n" || len(api.chunks) != 1 {
		t.Errorf("retried batch duplicated or lost: %q", api.log())
	}
	api.leaseLost = true
	lost := false
	s.onLeaseLost = func() { lost = true }
	_, _ = s.Write([]byte("more\n"))
	if err := s.Flush(context.Background(), false); !errors.Is(err, ErrLeaseLost) || !lost {
		t.Errorf("lease loss: err=%v lost=%v", err, lost)
	}
}
