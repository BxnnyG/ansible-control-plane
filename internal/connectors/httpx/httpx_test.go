package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadOnlyTransportRefusesWrites(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, "PROPFIND", http.MethodOptions} {
		req, _ := http.NewRequest(m, srv.URL+"/x", nil)
		if _, err := c.hc.Do(req); !errors.Is(err, ErrMethodNotAllowed) {
			t.Errorf("%s: err=%v, want ErrMethodNotAllowed", m, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("server saw %d requests from refused methods", hits.Load())
	}
	if _, err := c.Get(context.Background(), "/ok", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Head(context.Background(), "/ok", nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits=%d, want 2", hits.Load())
	}
}

func TestGetJSONQueryAuthAndPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/things" || r.URL.Query().Get("q") != "a b" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if u, p, ok := r.BasicAuth(); !ok || u != "key1" || p != "secret1" {
			http.Error(w, "auth", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"n":3}`))
	}))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL + "/api/v1/", Auth: Auth{BasicUser: "key1", BasicPwd: "secret1"}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ N int }
	if err := c.GetJSON(context.Background(), "things", url.Values{"q": {"a b"}}, &out); err != nil || out.N != 3 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	var se *StatusError
	c2, _ := New(Options{BaseURL: srv.URL + "/api/v1/"})
	if err := c2.GetJSON(context.Background(), "things", nil, &out); !errors.As(err, &se) || se.Code != http.StatusBadRequest {
		t.Fatalf("err=%v, want status 400", err)
	}
}

func TestResponseSizeLimitAndNoRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	}))
	defer srv.Close()
	c, _ := New(Options{BaseURL: srv.URL, MaxBody: 50})
	if _, err := c.Get(context.Background(), "/big", nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err=%v, want ErrTooLarge", err)
	}
	var se *StatusError
	if _, err := c.Get(context.Background(), "/redir", nil); !errors.As(err, &se) || se.Code != http.StatusFound {
		t.Fatalf("redirect must not be followed, err=%v", err)
	}
}

func TestErrorsMaskCredentials(t *testing.T) {
	const tok = "s3cr3t-token-value"
	c, err := New(Options{BaseURL: "http://127.0.0.1:1", Auth: Auth{Bearer: tok}, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Get(context.Background(), "/x", url.Values{"apikey": {tok}})
	if err == nil {
		t.Fatal("expected connection error")
	}
	if strings.Contains(err.Error(), tok) {
		t.Fatalf("error leaks credential: %v", err)
	}
	// A transport that echoes the header into its error must be masked too.
	c, _ = New(Options{BaseURL: "http://example.invalid", Auth: Auth{Bearer: tok}, Transport: echoErr{}})
	_, err = c.Get(context.Background(), "/x", nil)
	if err == nil || strings.Contains(err.Error(), tok) || !strings.Contains(err.Error(), mask) {
		t.Fatalf("err=%v, want masked credential", err)
	}
}

type echoErr struct{}

func (echoErr) RoundTrip(r *http.Request) (*http.Response, error) {
	return nil, errors.New("boom " + r.Header.Get("Authorization"))
}

func TestNewValidation(t *testing.T) {
	for _, u := range []string{"", "ftp://h", "http://", "http://user:pw@h", "h:80"} {
		if _, err := New(Options{BaseURL: u}); err == nil {
			t.Errorf("base URL %q accepted", u)
		}
	}
	if _, err := New(Options{BaseURL: "https://h", CAPEM: []byte("junk")}); err == nil {
		t.Error("junk CA accepted")
	}
	c, _ := New(Options{BaseURL: "http://h"})
	if _, err := c.Get(context.Background(), "http://evil/x", nil); err == nil {
		t.Error("absolute path accepted")
	}
}
