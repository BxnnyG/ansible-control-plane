package notify

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type smtpSender struct{}

// plainAuth sends credentials only over a connection that is already
// encrypted or explicitly marked insecure; net/smtp's PlainAuth refuses
// otherwise, and we additionally accept the explicit insecure flag.
type plainAuth struct {
	user, pass string
	allowPlain bool
}

func (a plainAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !a.allowPlain {
		return "", nil, errors.New("refusing to send credentials over an unencrypted connection")
	}
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

// loginAuth implements the legacy LOGIN mechanism for servers that do not
// advertise PLAIN.
type loginAuth struct {
	user, pass string
	allowPlain bool
}

func (a loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !a.allowPlain {
		return "", nil, errors.New("refusing to send credentials over an unencrypted connection")
	}
	return "LOGIN", nil, nil
}

func (a loginAuth) Next(challenge []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(challenge))) {
	case "username:", "user name:", "username":
		return []byte(a.user), nil
	case "password:", "password":
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected server challenge")
}

// smtpAuthFor picks PLAIN when advertised (or when no AUTH list is
// available), otherwise LOGIN if the server offers it.
func smtpAuthFor(advertised string, user, pass string, allowPlain bool) smtp.Auth {
	mechs := strings.Fields(strings.ToUpper(advertised))
	has := func(m string) bool {
		for _, x := range mechs {
			if x == m {
				return true
			}
		}
		return false
	}
	if !has("PLAIN") && has("LOGIN") {
		return loginAuth{user: user, pass: pass, allowPlain: allowPlain}
	}
	return plainAuth{user: user, pass: pass, allowPlain: allowPlain}
}

// smtpAuthError describes a failed AUTH with the server reply but never the
// credential.
func smtpAuthError(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) {
		return fmt.Errorf("smtp authentication failed: %d %s", te.Code, te.Msg)
	}
	return fmt.Errorf("smtp authentication failed: %w", err)
}

func (plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

func (smtpSender) Send(ctx context.Context, ch Channel, cred []byte, msg Message) error {
	var c smtpConfig
	if err := json.Unmarshal(ch.Config, &c); err != nil {
		return fmt.Errorf("channel config: %w", err)
	}
	sec := c.Security
	if sec == "" {
		sec = SecStartTLS
	}
	port := c.Port
	if port == 0 {
		switch sec {
		case SecTLS:
			port = 465
		case SecNone:
			port = 25
		default:
			port = 587
		}
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(port))
	tc, err := tlsConfig(ch, c.Host)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	d := &net.Dialer{Timeout: SendTimeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if sec == SecTLS {
		conn = tls.Client(conn, tc)
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer func() { _ = cl.Close() }()
	if sec == SecStartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tc); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if len(cred) > 0 {
		_, advertised := cl.Extension("AUTH")
		if err := cl.Auth(smtpAuthFor(advertised, c.Username, string(cred), sec == SecNone)); err != nil {
			return smtpAuthError(err)
		}
	}
	if err := cl.Mail(c.From); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	for _, rcpt := range c.To {
		if err := cl.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp RCPT TO: %w", err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(buildMail(c, msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp finish: %w", err)
	}
	return cl.Quit()
}

func buildMail(c smtpConfig, msg Message) []byte {
	var b strings.Builder
	b.WriteString("From: " + c.From + "\r\n")
	b.WriteString("To: " + strings.Join(c.To, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", msg.Title()) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(msg.Text(), "\r\n", "\n"), "\n", "\r\n"))
	return []byte(b.String())
}
