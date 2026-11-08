package notify

import (
	"bufio"
	"encoding/base64"
	"context"
	"crypto/tls"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const dummyCred = "dummy-credential-value"

func testMsg() Message {
	return Message{Kind: KindFailed, RunID: "r1", Workspace: "ws", Template: "site", Status: "failed", Trigger: "manual",
		HostsOK: 1, HostsFailed: 2, Duration: 90 * time.Second, URL: "https://acp.example.test/runs/r1", TxnID: "txn-1"}
}

func chFor(typ, cfg string) Channel { return Channel{Type: typ, Config: []byte(cfg)} }

type captured struct {
	method, path, ctype string
	header              http.Header
	body                string
}

func capture(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.method, c.path, c.ctype, c.header, c.body = r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Type"), r.Header.Clone(), string(b)
		w.WriteHeader(status)
		_, _ = w.Write([]byte("response body"))
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestWebhookHMAC(t *testing.T) {
	srv, c := capture(t, 200)
	err := webhookSender{}.Send(context.Background(), chFor(TypeWebhook, `{"url":"`+srv.URL+`/hook"}`), []byte(dummyCred), testMsg())
	if err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/hook" || c.ctype != "application/json" {
		t.Fatalf("request: %+v", c)
	}
	if got, want := c.header.Get("X-ACP-Signature"), Sign([]byte(dummyCred), []byte(c.body)); got != want || !strings.HasPrefix(got, "sha256=") {
		t.Fatalf("signature %q want %q", got, want)
	}
	if c.header.Get("X-ACP-Event") != KindFailed || c.header.Get("Authorization") != "" {
		t.Fatalf("headers: %v", c.header)
	}
	if !strings.Contains(c.body, `"version":1`) || !strings.Contains(c.body, `"template":"site"`) || !strings.Contains(c.body, `"hosts_failed":2`) {
		t.Fatalf("body: %s", c.body)
	}
}

func TestSignKnownVector(t *testing.T) {
	// HMAC-SHA256("key", "The quick brown fox jumps over the lazy dog")
	got := Sign([]byte("key"), []byte("The quick brown fox jumps over the lazy dog"))
	if want := "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"; got != want {
		t.Fatalf("got %s", got)
	}
}

func TestWebhookBearerAndNoAuth(t *testing.T) {
	srv, c := capture(t, 204)
	cfg := `{"url":"` + srv.URL + `","auth":"bearer"}`
	if err := (webhookSender{}).Send(context.Background(), chFor(TypeWebhook, cfg), []byte(dummyCred), testMsg()); err != nil {
		t.Fatal(err)
	}
	if c.header.Get("Authorization") != "Bearer "+dummyCred || c.header.Get("X-ACP-Signature") != "" {
		t.Fatalf("headers: %v", c.header)
	}
	if err := (webhookSender{}).Send(context.Background(), chFor(TypeWebhook, `{"url":"`+srv.URL+`"}`), nil, testMsg()); err != nil {
		t.Fatal(err)
	}
	if c.header.Get("Authorization") != "" || c.header.Get("X-ACP-Signature") != "" {
		t.Fatalf("unexpected auth headers: %v", c.header)
	}
}

func TestMatrixSend(t *testing.T) {
	srv, c := capture(t, 200)
	cfg := `{"homeserver":"` + srv.URL + `/","room_id":"!room:example.test"}`
	if err := (matrixSender{}).Send(context.Background(), chFor(TypeMatrix, cfg), []byte(dummyCred), testMsg()); err != nil {
		t.Fatal(err)
	}
	if c.method != "PUT" || c.path != "/_matrix/client/v3/rooms/%21room:example.test/send/m.room.message/txn-1" {
		t.Fatalf("request %s %s", c.method, c.path)
	}
	if c.header.Get("Authorization") != "Bearer "+dummyCred || !strings.Contains(c.body, `"msgtype":"m.text"`) {
		t.Fatalf("headers %v body %s", c.header, c.body)
	}
	if err := (matrixSender{}).Send(context.Background(), chFor(TypeMatrix, cfg), nil, testMsg()); err == nil {
		t.Fatal("missing token must fail")
	}
}

func TestGotifyAndNtfy(t *testing.T) {
	srv, c := capture(t, 200)
	if err := (gotifySender{}).Send(context.Background(), chFor(TypeGotify, `{"url":"`+srv.URL+`/"}`), []byte(dummyCred), testMsg()); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/message" || c.header.Get("X-Gotify-Key") != dummyCred || !strings.Contains(c.body, `"title"`) {
		t.Fatalf("gotify: %+v", c)
	}
	if err := (ntfySender{}).Send(context.Background(), chFor(TypeNtfy, `{"url":"`+srv.URL+`","topic":"acp-alerts"}`), []byte(dummyCred), testMsg()); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/acp-alerts" || c.header.Get("Title") == "" || c.header.Get("Authorization") != "Bearer "+dummyCred ||
		!strings.Contains(c.body, "https://acp.example.test/runs/r1") {
		t.Fatalf("ntfy: %+v", c)
	}
}

func TestHTTPFailureModes(t *testing.T) {
	t.Run("non-2xx", func(t *testing.T) {
		srv, _ := capture(t, 500)
		err := (webhookSender{}).Send(context.Background(), chFor(TypeWebhook, `{"url":"`+srv.URL+`"}`), nil, testMsg())
		if err == nil || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "response body") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("redirects are not followed", func(t *testing.T) {
		hits := 0
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
		defer target.Close()
		redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		}))
		defer redir.Close()
		err := (webhookSender{}).Send(context.Background(), chFor(TypeWebhook, `{"url":"`+redir.URL+`"}`), nil, testMsg())
		if err == nil || hits != 0 {
			t.Fatalf("err = %v, redirect target hits = %d", err, hits)
		}
	})
	t.Run("proxy environment is ignored", func(t *testing.T) {
		t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
		t.Setenv("http_proxy", "http://127.0.0.1:1")
		srv, _ := capture(t, 200)
		if err := (webhookSender{}).Send(context.Background(), chFor(TypeWebhook, `{"url":"`+srv.URL+`"}`), nil, testMsg()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("tls verification and ca bundle", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		cfg := `{"url":"` + srv.URL + `"}`
		if err := (webhookSender{}).Send(context.Background(), chFor(TypeWebhook, cfg), nil, testMsg()); err == nil {
			t.Fatal("untrusted certificate must fail")
		}
		ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
		if err := (webhookSender{}).Send(context.Background(), Channel{Type: TypeWebhook, Config: []byte(cfg), TLSCAPEM: ca}, nil, testMsg()); err != nil {
			t.Fatalf("with CA: %v", err)
		}
		if err := (webhookSender{}).Send(context.Background(), Channel{Type: TypeWebhook, Config: []byte(cfg), TLSSkipVerify: true}, nil, testMsg()); err != nil {
			t.Fatalf("skip verify: %v", err)
		}
	})
	t.Run("transport error hides the url", func(t *testing.T) {
		err := (webhookSender{}).Send(context.Background(), chFor(TypeWebhook, `{"url":"http://127.0.0.1:1/secret-path-token"}`), nil, testMsg())
		if err == nil || strings.Contains(err.Error(), "secret-path-token") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSanitize(t *testing.T) {
	got := sanitize("bad "+dummyCred+" here\nnext\x00"+strings.Repeat("x", 400), []byte(dummyCred))
	if strings.Contains(got, dummyCred) || strings.ContainsAny(got, "\n\x00") || len(got) > maxErrorLen+3 {
		t.Fatalf("sanitize: %q", got)
	}
}

// fakeSMTP is a minimal SMTP server that records the transaction.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	auth string
	from string
	to   []string
	data string
	done chan struct{}
	// mechs is the AUTH list advertised in EHLO (default "PLAIN").
	mechs string
	// rejectAuth makes every AUTH attempt fail with 535.
	rejectAuth bool
}

func startSMTP(t *testing.T, cfg *tls.Config) *fakeSMTP {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		ln = tls.NewListener(ln, cfg)
	}
	f := &fakeSMTP{ln: ln, done: make(chan struct{}, 1)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		f.serve(conn)
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(conn)
	w := func(s string) { _, _ = fmt.Fprint(conn, s+"\r\n") }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.mu.Lock()
		switch {
		case strings.HasPrefix(line, "EHLO"):
			mechs := f.mechs
			if mechs == "" {
				mechs = "PLAIN"
			}
			w("250-fake\r\n250 AUTH " + mechs)
		case strings.HasPrefix(line, "AUTH") && f.rejectAuth:
			w("535 5.7.8 Error: authentication failed")
		case strings.HasPrefix(line, "AUTH PLAIN"):
			f.auth = line
			w("235 ok")
		case line == "AUTH LOGIN":
			w("334 VXNlcm5hbWU6")
			u, _ := r.ReadString('\n')
			w("334 UGFzc3dvcmQ6")
			p, _ := r.ReadString('\n')
			f.auth = "LOGIN " + strings.TrimSpace(u) + " " + strings.TrimSpace(p)
			w("235 ok")
		case strings.HasPrefix(line, "MAIL FROM:"):
			f.from = line
			w("250 ok")
		case strings.HasPrefix(line, "RCPT TO:"):
			f.to = append(f.to, line)
			w("250 ok")
		case line == "DATA":
			w("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.data = b.String()
			w("250 queued")
		case line == "QUIT":
			w("221 bye")
			f.mu.Unlock()
			f.done <- struct{}{}
			return
		default:
			w("250 ok")
		}
		f.mu.Unlock()
	}
}

func smtpCfg(addr, security string, extra string) string {
	host, port, _ := net.SplitHostPort(addr)
	return fmt.Sprintf(`{"host":%q,"port":%s,"security":%q,%s"from":"acp@example.test","to":["a@example.test","b@example.test"]}`, host, port, security, extra)
}

func TestSMTPPlainInsecure(t *testing.T) {
	f := startSMTP(t, nil)
	cfg := smtpCfg(f.ln.Addr().String(), "none", `"insecure":true,"username":"user",`)
	if err := (smtpSender{}).Send(context.Background(), chFor(TypeSMTP, cfg), []byte(dummyCred), testMsg()); err != nil {
		t.Fatal(err)
	}
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.to) != 2 || !strings.Contains(f.from, "acp@example.test") || f.auth == "" {
		t.Fatalf("transaction: from=%q to=%v auth=%q", f.from, f.to, f.auth)
	}
	if !strings.Contains(f.data, "Subject: [ACP] Run failed: site") || !strings.Contains(f.data, "https://acp.example.test/runs/r1") {
		t.Fatalf("data: %s", f.data)
	}
}

func TestSMTPAuthMechanisms(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	tests := []struct {
		name       string
		mechs      string
		reject     bool
		wantAuth   string
		wantErrSub string
	}{
		{"plain only", "PLAIN", false, "AUTH PLAIN " + b64([]byte("\x00user\x00"+dummyCred)), ""},
		{"plain preferred over login", "LOGIN PLAIN", false, "AUTH PLAIN " + b64([]byte("\x00user\x00"+dummyCred)), ""},
		{"login fallback", "LOGIN", false, "LOGIN " + b64([]byte("user")) + " " + b64([]byte(dummyCred)), ""},
		{"rejected shows reply", "PLAIN LOGIN", true, "", "535 5.7.8 Error: authentication failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := startSMTP(t, nil)
			f.mechs, f.rejectAuth = tt.mechs, tt.reject
			cfg := smtpCfg(f.ln.Addr().String(), "none", `"insecure":true,"username":"user",`)
			err := (smtpSender{}).Send(context.Background(), chFor(TypeSMTP, cfg), []byte(dummyCred), testMsg())
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) || strings.Contains(err.Error(), dummyCred) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			<-f.done
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.auth != tt.wantAuth {
				t.Fatalf("auth = %q, want %q", f.auth, tt.wantAuth)
			}
		})
	}
}

func TestNtfyBasicAuth(t *testing.T) {
	srv, c := capture(t, 200)
	cfg := `{"url":"` + srv.URL + `","topic":"acp","auth":"basic","username":"user"}`
	if err := (ntfySender{}).Send(context.Background(), chFor(TypeNtfy, cfg), []byte(dummyCred), testMsg()); err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+dummyCred))
	if got := c.header.Get("Authorization"); got != want {
		t.Fatalf("authorization = %q", got)
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	f := startSMTP(t, &tls.Config{Certificates: srv.TLS.Certificates, MinVersion: tls.VersionTLS12})
	cfg := smtpCfg(f.ln.Addr().String(), "tls", "")
	if err := (smtpSender{}).Send(context.Background(), chFor(TypeSMTP, cfg), nil, testMsg()); err == nil {
		t.Fatal("untrusted certificate must fail")
	}
	f = startSMTP(t, &tls.Config{Certificates: srv.TLS.Certificates, MinVersion: tls.VersionTLS12})
	cfg = smtpCfg(f.ln.Addr().String(), "tls", "")
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	if err := (smtpSender{}).Send(context.Background(), Channel{Type: TypeSMTP, Config: []byte(cfg), TLSCAPEM: ca}, nil, testMsg()); err != nil {
		t.Fatal(err)
	}
	<-f.done
}

func TestSMTPRefusesPlainCredentials(t *testing.T) {
	f := startSMTP(t, nil)
	// starttls requested but the fake server does not offer it
	cfg := smtpCfg(f.ln.Addr().String(), "starttls", `"username":"user",`)
	err := (smtpSender{}).Send(context.Background(), chFor(TypeSMTP, cfg), []byte(dummyCred), testMsg())
	if err == nil || strings.Contains(err.Error(), dummyCred) {
		t.Fatalf("err = %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "" {
		t.Fatal("credentials must not be sent without TLS")
	}
}
