package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Channel types.
const (
	TypeWebhook = "webhook"
	TypeMatrix  = "matrix"
	TypeSMTP    = "smtp"
	TypeGotify  = "gotify"
	TypeNtfy    = "ntfy"
)

// ErrInvalid wraps channel validation failures; messages are safe for clients.
var ErrInvalid = errors.New("invalid notification channel")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// ValidType reports whether t is a known channel type.
func ValidType(t string) bool {
	switch t {
	case TypeWebhook, TypeMatrix, TypeSMTP, TypeGotify, TypeNtfy:
		return true
	}
	return false
}

// Channel is the sender-facing view of a channel row. Credential is the
// resolved secret value (empty when the channel has none).
type Channel struct {
	Type          string
	Config        json.RawMessage
	TLSCAPEM      string
	TLSSkipVerify bool
}

// Webhook auth modes.
const (
	AuthHMAC   = "hmac"
	AuthBearer = "bearer"
)

// SMTP security modes.
const (
	SecStartTLS = "starttls"
	SecTLS      = "tls"
	SecNone     = "none"
)

type webhookConfig struct {
	URL  string `json:"url"`
	Auth string `json:"auth,omitempty"` // hmac (default) or bearer
}

type matrixConfig struct {
	Homeserver string `json:"homeserver"`
	RoomID     string `json:"room_id"`
}

type smtpConfig struct {
	Host     string   `json:"host"`
	Port     int      `json:"port,omitempty"`
	Security string   `json:"security,omitempty"` // starttls (default), tls, none
	Insecure bool     `json:"insecure,omitempty"` // required for security=none
	Username string   `json:"username,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
}

type gotifyConfig struct {
	URL      string `json:"url"`
	Priority int    `json:"priority,omitempty"`
}

type ntfyConfig struct {
	URL      string `json:"url"`
	Topic    string `json:"topic"`
	Priority int    `json:"priority,omitempty"`
}

func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return invalid("config: %v", err)
	}
	return nil
}

func checkURL(field, s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("%s must be an http(s) URL", field)
	}
	if u.User != nil {
		return invalid("%s must not contain credentials", field)
	}
	return nil
}

// ValidateConfig checks the non-secret config of a channel type and whether the
// secret requirement is met (hasSecret).
func ValidateConfig(typ string, raw []byte, hasSecret bool) error {
	if !ValidType(typ) {
		return invalid("unknown type %q", typ)
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	switch typ {
	case TypeWebhook:
		var c webhookConfig
		if err := decodeStrict(raw, &c); err != nil {
			return err
		}
		if err := checkURL("url", c.URL); err != nil {
			return err
		}
		switch c.Auth {
		case "", AuthHMAC:
		case AuthBearer:
			if !hasSecret {
				return invalid("bearer auth requires a secret")
			}
		default:
			return invalid("auth must be hmac or bearer")
		}
	case TypeMatrix:
		var c matrixConfig
		if err := decodeStrict(raw, &c); err != nil {
			return err
		}
		if err := checkURL("homeserver", c.Homeserver); err != nil {
			return err
		}
		if !strings.HasPrefix(c.RoomID, "!") || !strings.Contains(c.RoomID, ":") {
			return invalid("room_id must look like !room:server")
		}
		if !hasSecret {
			return invalid("matrix requires a secret holding the access token")
		}
	case TypeSMTP:
		var c smtpConfig
		if err := decodeStrict(raw, &c); err != nil {
			return err
		}
		return validateSMTP(c, hasSecret)
	case TypeGotify:
		var c gotifyConfig
		if err := decodeStrict(raw, &c); err != nil {
			return err
		}
		if err := checkURL("url", c.URL); err != nil {
			return err
		}
		if !hasSecret {
			return invalid("gotify requires a secret holding the application token")
		}
	case TypeNtfy:
		var c ntfyConfig
		if err := decodeStrict(raw, &c); err != nil {
			return err
		}
		if err := checkURL("url", c.URL); err != nil {
			return err
		}
		if c.Topic == "" || strings.ContainsAny(c.Topic, "/?# ") {
			return invalid("topic must be a single path segment")
		}
	}
	return nil
}

func validateSMTP(c smtpConfig, hasSecret bool) error {
	if c.Host == "" || strings.ContainsAny(c.Host, "/ ") {
		return invalid("host is required")
	}
	if c.Port < 0 || c.Port > 65535 {
		return invalid("port out of range")
	}
	switch c.Security {
	case "", SecStartTLS, SecTLS:
	case SecNone:
		if !c.Insecure {
			return invalid("security none requires insecure=true")
		}
	default:
		return invalid("security must be starttls, tls or none")
	}
	if c.From == "" || strings.ContainsAny(c.From, "\r\n") {
		return invalid("from is required")
	}
	if len(c.To) == 0 {
		return invalid("at least one recipient is required")
	}
	for _, a := range append([]string{c.From}, c.To...) {
		if strings.ContainsAny(a, "\r\n<>") || !strings.Contains(a, "@") {
			return invalid("invalid e-mail address")
		}
	}
	if hasSecret && c.Username == "" {
		return invalid("username is required when a password secret is set")
	}
	return nil
}
