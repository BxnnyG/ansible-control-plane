package notify

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	// SendTimeout bounds one delivery attempt.
	SendTimeout = 10 * time.Second
	// maxResponseBody caps how much of a response body is read.
	maxResponseBody = 4 << 10
)

// tlsConfig builds the TLS settings of a channel: TLS 1.2+, optional extra CA
// bundle, optional skip-verify.
func tlsConfig(ch Channel, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName, InsecureSkipVerify: ch.TLSSkipVerify} //nolint:gosec // explicit per-channel opt-in
	if ch.TLSCAPEM != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(ch.TLSCAPEM)) {
			return nil, errors.New("tls_ca_pem contains no valid certificate")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// newHTTPClient returns the dedicated sender client: no proxy from the
// environment, no redirects, bounded timeout.
func newHTTPClient(ch Channel) (*http.Client, error) {
	tc, err := tlsConfig(ch, "")
	if err != nil {
		return nil, err
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: SendTimeout}).DialContext,
		TLSClientConfig:       tc,
		TLSHandshakeTimeout:   SendTimeout,
		ResponseHeaderTimeout: SendTimeout,
		DisableKeepAlives:     true,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   SendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

// statusError describes a non-2xx response without echoing the full body.
func statusError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("unexpected HTTP status %d", resp.StatusCode)
}
