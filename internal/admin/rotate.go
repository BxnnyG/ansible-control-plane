package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// RotateCounts reports how many envelopes were processed. No key material.
type RotateCounts struct {
	Secrets     int // rows in the secrets table re-wrapped
	TOTP        int // break-glass TOTP envelopes re-wrapped
	AlreadyDone int // envelopes that were already wrapped by the new key
}

// RotateMasterKey re-wraps the data key of every stored envelope (secrets table
// and break-glass TOTP secrets) under newKey in a single transaction. oldKey
// stays registered as a retired key while decrypting. Payloads are untouched.
// Any failure rolls everything back.
func RotateMasterKey(ctx context.Context, db TxBeginner, oldKey, newKey []byte) (RotateCounts, error) {
	var c RotateCounts
	if secrets.KeyIDFor(oldKey) == secrets.KeyIDFor(newKey) {
		return c, errors.New("new master key is identical to the current key")
	}
	sp, err := secrets.NewLocalProvider(newKey, oldKey)
	if err != nil {
		return c, fmt.Errorf("provider: %w", err)
	}
	newID := sp.KeyID()
	tx, err := db.Begin(ctx)
	if err != nil {
		return c, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Block concurrent writers so no envelope is written with the old key meanwhile.
	if _, err := tx.Exec(ctx, "LOCK TABLE secrets, users IN SHARE ROW EXCLUSIVE MODE"); err != nil {
		return c, fmt.Errorf("lock: %w", err)
	}
	q := dbgen.New(tx)

	rows, err := q.ListAllSecrets(ctx)
	if err != nil {
		return c, fmt.Errorf("list secrets: %w", err)
	}
	for _, r := range rows {
		if r.EnvKeyID == newID {
			c.AlreadyDone++
			continue
		}
		env := &secrets.Envelope{Version: int(r.EnvVersion), KeyID: r.EnvKeyID, WrapNonce: r.EnvWrapNonce,
			WrappedDEK: r.EnvWrappedDek, Nonce: r.EnvNonce, Ciphertext: r.EnvCiphertext}
		out, err := sp.Rotate(env, secrets.AAD{SecretID: r.ID.String(), WorkspaceID: r.WorkspaceID.String()})
		if err != nil {
			return RotateCounts{}, fmt.Errorf("secret %s: %w", r.ID, err)
		}
		if err := q.ReplaceSecretEnvelope(ctx, dbgen.ReplaceSecretEnvelopeParams{
			ID: r.ID, WorkspaceID: r.WorkspaceID, EnvVersion: int32(out.Version), //nolint:gosec // small constant
			EnvKeyID: out.KeyID, EnvWrapNonce: out.WrapNonce, EnvWrappedDek: out.WrappedDEK,
			EnvNonce: out.Nonce, EnvCiphertext: out.Ciphertext,
		}); err != nil {
			return RotateCounts{}, fmt.Errorf("store secret %s: %w", r.ID, err)
		}
		c.Secrets++
	}

	users, err := q.ListUsers(ctx)
	if err != nil {
		return RotateCounts{}, fmt.Errorf("list users: %w", err)
	}
	for _, u := range users {
		if len(u.TotpEnvelope) == 0 {
			continue
		}
		var env secrets.Envelope
		if err := json.Unmarshal(u.TotpEnvelope, &env); err != nil {
			return RotateCounts{}, fmt.Errorf("user %s: decode totp envelope: %w", u.ID, err)
		}
		if env.KeyID == newID {
			c.AlreadyDone++
			continue
		}
		out, err := sp.Rotate(&env, auth.TOTPAAD(u.ID.String()))
		if err != nil {
			return RotateCounts{}, fmt.Errorf("user %s: %w", u.ID, err)
		}
		b, err := json.Marshal(out)
		if err != nil {
			return RotateCounts{}, fmt.Errorf("user %s: encode: %w", u.ID, err)
		}
		if err := q.ReplaceUserTOTPEnvelope(ctx, dbgen.ReplaceUserTOTPEnvelopeParams{ID: u.ID, TotpEnvelope: b}); err != nil {
			return RotateCounts{}, fmt.Errorf("store totp %s: %w", u.ID, err)
		}
		c.TOTP++
	}

	if _, err := q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorLabel: "cli", Action: "admin.master_key_rotated", TargetType: "secrets",
		ActorID: pgtype.UUID{}, WorkspaceID: pgtype.UUID{},
		Details: []byte(fmt.Sprintf(`{"secrets":%d,"totp":%d,"already_done":%d,"new_key_id":%q}`, c.Secrets, c.TOTP, c.AlreadyDone, newID)),
	}); err != nil {
		return RotateCounts{}, fmt.Errorf("audit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RotateCounts{}, fmt.Errorf("commit: %w", err)
	}
	return c, nil
}

// SetBreakGlassPassword replaces the password of an existing break-glass user
// and clears the lockout state. TOTP is left untouched.
func SetBreakGlassPassword(ctx context.Context, db TxBeginner, username, password string) error {
	hash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)
	u, err := q.GetBreakGlassUser(ctx, pgtype.Text{String: username, Valid: true})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %q", ErrNoSuchUser, username)
		}
		return fmt.Errorf("lookup user: %w", err)
	}
	if n, err := q.SetBreakGlassPassword(ctx, dbgen.SetBreakGlassPasswordParams{
		ID: u.ID, PasswordHash: pgtype.Text{String: hash, Valid: true},
	}); err != nil || n != 1 {
		return fmt.Errorf("store password: %w", errors.Join(err, errRows(n)))
	}
	if err := audit(ctx, q, "admin.breakglass_password_set", u.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func errRows(n int64) error {
	if n == 1 {
		return nil
	}
	return fmt.Errorf("updated %d rows", n)
}
