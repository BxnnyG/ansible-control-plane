package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakePing struct{ err error }

func (f fakePing) Ping(context.Context) error { return f.err }

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	ok := Router(log, fakePing{}, DefaultStatic())
	bad := Router(log, fakePing{errors.New("down")}, DefaultStatic())
	if c := get(ok, "/healthz").Code; c != 200 {
		t.Errorf("healthz %d", c)
	}
	if c := get(ok, "/readyz").Code; c != 200 {
		t.Errorf("readyz %d", c)
	}
	if c := get(bad, "/readyz").Code; c != 503 {
		t.Errorf("readyz down %d", c)
	}
	if c := get(ok, "/metrics").Code; c != 200 {
		t.Errorf("metrics %d", c)
	}
	if c := get(ok, "/some/spa/route").Code; c != 200 {
		t.Errorf("spa fallback %d", c)
	}
}
