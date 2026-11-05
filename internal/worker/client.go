package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

// ErrLeaseLost is returned when the app no longer knows the lease (the run was
// marked lost, finished or cancelled elsewhere). The worker must stop the run.
var ErrLeaseLost = errors.New("lease lost")

// API is the worker's view of the app's internal API.
type API interface {
	Claim(ctx context.Context, workerID string) (*runproto.ClaimResponse, error)
	Bundle(ctx context.Context, run, lease string) (io.ReadCloser, error)
	Events(ctx context.Context, run, lease string, req runproto.EventsRequest) error
	Heartbeat(ctx context.Context, run, lease string) (cancel bool, err error)
	Finish(ctx context.Context, run, lease string, req runproto.FinishRequest) error
}

// Client implements API over HTTP with bearer authentication.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

var _ API = (*Client)(nil)

func (c *Client) do(ctx context.Context, method, path, lease string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Base, "/")+runproto.BasePath+path, rd)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if lease != "" {
		req.Header.Set(runproto.LeaseHeader, lease)
	}
	return c.HTTP.Do(req)
}

// statusErr converts an unexpected response to an error without echoing the body.
func statusErr(resp *http.Response) error {
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusConflict {
		return ErrLeaseLost
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	return &HTTPError{Status: resp.StatusCode, Msg: e.Error}
}

// HTTPError is an unexpected status of the app.
type HTTPError struct {
	Status int
	Msg    string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("app returned %d: %s", e.Status, e.Msg) }

// Claim long-polls for a run; (nil, nil) means no work.
func (c *Client) Claim(ctx context.Context, workerID string) (*runproto.ClaimResponse, error) {
	resp, err := c.do(ctx, http.MethodPost, "/claim", "", runproto.ClaimRequest{WorkerID: workerID})
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusNoContent:
		_ = resp.Body.Close()
		return nil, nil
	case http.StatusOK:
		defer func() { _ = resp.Body.Close() }()
		var out runproto.ClaimResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
			return nil, fmt.Errorf("decode claim: %w", err)
		}
		return &out, nil
	}
	return nil, statusErr(resp)
}

// Bundle fetches the bundle (tar.gz); the caller closes the reader.
func (c *Client) Bundle(ctx context.Context, run, lease string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/runs/"+run+"/bundle", lease, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusErr(resp)
	}
	return resp.Body, nil
}

func (c *Client) post(ctx context.Context, path, lease string, body any) error {
	resp, err := c.do(ctx, http.MethodPost, path, lease, body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return statusErr(resp)
	}
	_ = resp.Body.Close()
	return nil
}

// Events sends a batch of log chunks and callback events.
func (c *Client) Events(ctx context.Context, run, lease string, req runproto.EventsRequest) error {
	return c.post(ctx, "/runs/"+run+"/events", lease, req)
}

// Heartbeat reports liveness and returns whether cancellation was requested.
func (c *Client) Heartbeat(ctx context.Context, run, lease string) (bool, error) {
	resp, err := c.do(ctx, http.MethodPost, "/runs/"+run+"/heartbeat", lease, nil)
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, statusErr(resp)
	}
	defer func() { _ = resp.Body.Close() }()
	var out runproto.HeartbeatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&out); err != nil {
		return false, fmt.Errorf("decode heartbeat: %w", err)
	}
	return out.Cancel, nil
}

// Finish reports the end of the run.
func (c *Client) Finish(ctx context.Context, run, lease string, req runproto.FinishRequest) error {
	return c.post(ctx, "/runs/"+run+"/finish", lease, req)
}

// retryable reports whether a failed request is worth repeating.
func retryable(err error) bool {
	if err == nil || errors.Is(err, ErrLeaseLost) || errors.Is(err, context.Canceled) {
		return false
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status >= 500 || he.Status == http.StatusTooManyRequests
	}
	return true // transport errors
}

// withRetry repeats fn with exponential backoff (0.5 s .. 8 s) until it
// succeeds, fails permanently or the total budget is used up.
func withRetry(ctx context.Context, budget time.Duration, fn func(context.Context) error) error {
	deadline := time.Now().Add(budget)
	delay := 500 * time.Millisecond
	for {
		err := fn(ctx)
		if !retryable(err) || time.Now().Add(delay).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 8*time.Second)
	}
}
