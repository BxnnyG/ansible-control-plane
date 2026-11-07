package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Sender delivers one message over one channel. credential is the resolved
// secret (nil when the channel has none); implementations must not keep it and
// must not put it into returned errors.
type Sender interface {
	Send(ctx context.Context, ch Channel, credential []byte, msg Message) error
}

// SenderFor returns the sender of a channel type.
func SenderFor(typ string) (Sender, error) {
	switch typ {
	case TypeWebhook:
		return webhookSender{}, nil
	case TypeMatrix:
		return matrixSender{}, nil
	case TypeSMTP:
		return smtpSender{}, nil
	case TypeGotify:
		return gotifySender{}, nil
	case TypeNtfy:
		return ntfySender{}, nil
	}
	return nil, fmt.Errorf("unknown channel type %q", typ)
}

// do performs the request with the dedicated client and drains a capped body.
// Transport errors lose the URL (url.Error) to keep tokens in paths or queries
// out of stored errors.
func do(ch Channel, req *http.Request) error {
	cl, err := newHTTPClient(ch)
	if err != nil {
		return err
	}
	resp, err := cl.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
	return statusError(resp)
}

func newRequest(ctx context.Context, method, target string, body []byte, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "ansible-control-plane")
	return req, nil
}

// Sign returns the X-ACP-Signature value for body.
func Sign(key, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type webhookSender struct{}

func (webhookSender) Send(ctx context.Context, ch Channel, cred []byte, msg Message) error {
	var c webhookConfig
	if err := json.Unmarshal(ch.Config, &c); err != nil {
		return fmt.Errorf("channel config: %w", err)
	}
	body, err := msg.WebhookBody()
	if err != nil {
		return err
	}
	req, err := newRequest(ctx, http.MethodPost, c.URL, body, "application/json")
	if err != nil {
		return err
	}
	req.Header.Set("X-ACP-Event", msg.Kind)
	if len(cred) > 0 {
		if c.Auth == AuthBearer {
			req.Header.Set("Authorization", "Bearer "+string(cred))
		} else {
			req.Header.Set("X-ACP-Signature", Sign(cred, body))
		}
	}
	return do(ch, req)
}

type matrixSender struct{}

func (matrixSender) Send(ctx context.Context, ch Channel, cred []byte, msg Message) error {
	var c matrixConfig
	if err := json.Unmarshal(ch.Config, &c); err != nil {
		return fmt.Errorf("channel config: %w", err)
	}
	if len(cred) == 0 {
		return errors.New("matrix access token is missing")
	}
	body, err := json.Marshal(map[string]string{"msgtype": "m.text", "body": msg.Text()})
	if err != nil {
		return err
	}
	txn := msg.TxnID
	if txn == "" {
		txn = uuid.NewString()
	}
	target := strings.TrimRight(c.Homeserver, "/") + "/_matrix/client/v3/rooms/" +
		url.PathEscape(c.RoomID) + "/send/m.room.message/" + url.PathEscape(txn)
	req, err := newRequest(ctx, http.MethodPut, target, body, "application/json")
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+string(cred))
	return do(ch, req)
}

type gotifySender struct{}

func (gotifySender) Send(ctx context.Context, ch Channel, cred []byte, msg Message) error {
	var c gotifyConfig
	if err := json.Unmarshal(ch.Config, &c); err != nil {
		return fmt.Errorf("channel config: %w", err)
	}
	if len(cred) == 0 {
		return errors.New("gotify application token is missing")
	}
	prio := c.Priority
	if prio == 0 {
		prio = 5
	}
	body, err := json.Marshal(map[string]any{"title": msg.Title(), "message": msg.Text(), "priority": prio})
	if err != nil {
		return err
	}
	req, err := newRequest(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/message", body, "application/json")
	if err != nil {
		return err
	}
	req.Header.Set("X-Gotify-Key", string(cred))
	return do(ch, req)
}

type ntfySender struct{}

func (ntfySender) Send(ctx context.Context, ch Channel, cred []byte, msg Message) error {
	var c ntfyConfig
	if err := json.Unmarshal(ch.Config, &c); err != nil {
		return fmt.Errorf("channel config: %w", err)
	}
	req, err := newRequest(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/"+url.PathEscape(c.Topic), []byte(msg.Text()), "text/plain; charset=utf-8")
	if err != nil {
		return err
	}
	req.Header.Set("Title", msg.Title())
	if c.Priority > 0 {
		req.Header.Set("Priority", strconv.Itoa(c.Priority))
	}
	if msg.URL != "" {
		req.Header.Set("Click", msg.URL)
	}
	if len(cred) > 0 {
		req.Header.Set("Authorization", "Bearer "+string(cred))
	}
	return do(ch, req)
}
