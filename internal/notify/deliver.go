package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

const maxErrorLen = 300

// SecretResolver decrypts workspace secrets; *secrets.Service implements it.
type SecretResolver interface {
	Resolve(ctx context.Context, ws, id uuid.UUID) (secrets.Resolved, error)
	Get(ctx context.Context, ws, id uuid.UUID) (secrets.Meta, error)
}

// sendVia resolves the channel credential, sends the message and returns a
// sanitized error. The credential is zeroed before returning.
func sendVia(ctx context.Context, res SecretResolver, senders func(string) (Sender, error), row dbgen.NotificationChannel, msg Message) error {
	snd, err := senders(row.Type)
	if err != nil {
		return err
	}
	var cred []byte
	if row.SecretID.Valid {
		if res == nil {
			return fmt.Errorf("secret store unavailable")
		}
		r, err := res.Resolve(ctx, row.WorkspaceID, uuid.UUID(row.SecretID.Bytes))
		if err != nil {
			return fmt.Errorf("resolve channel secret: %s", sanitize(err.Error(), nil))
		}
		defer r.Value.Zero()
		cred = r.Value.Reveal()
	}
	ch := Channel{Type: row.Type, Config: row.Config, TLSCAPEM: row.TlsCaPem, TLSSkipVerify: row.TlsSkipVerify}
	return sendCred(ctx, snd, ch, cred, msg)
}

// sendCred sends msg with an already resolved credential and returns a
// sanitized error. The caller owns (and zeroes) cred.
func sendCred(ctx context.Context, snd Sender, ch Channel, cred []byte, msg Message) error {
	if err := snd.Send(ctx, ch, cred, msg); err != nil {
		return fmt.Errorf("%s", sanitize(err.Error(), cred))
	}
	return nil
}

// sanitize removes the credential from an error text, strips control
// characters and truncates it. Stored delivery errors pass through here.
func sanitize(s string, cred []byte) string {
	if len(cred) > 0 {
		s = strings.ReplaceAll(s, string(cred), "***")
	}
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > maxErrorLen {
		s = s[:maxErrorLen] + "..."
	}
	return s
}
