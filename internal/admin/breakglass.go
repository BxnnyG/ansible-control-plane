// Package admin implements operator bootstrap tasks run through `acp admin`.
package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
)

// Default workspace used for the bootstrap membership. The break-glass admin
// is owner of the workspace with slug "default"; it is created when missing,
// so a fresh install is usable even before any IdP user has logged in.
const (
	DefaultWorkspaceSlug = "default"
	DefaultWorkspaceName = "Default"
)

// ErrUserExists is returned when the break-glass username is already taken.
var ErrUserExists = errors.New("user already exists")

// ErrNoSuchUser is returned when a break-glass user cannot be found.
var ErrNoSuchUser = errors.New("break-glass user not found")

// TxBeginner is satisfied by *pgxpool.Pool.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Enrollment carries the one-time TOTP enrollment data. The caller prints it
// once and must not persist it.
type Enrollment struct {
	UserID uuid.UUID
	Secret string // base32
	URI    string // otpauth://
}

// CreateBreakGlass creates the user, seals a fresh TOTP secret and makes the
// user owner of the default workspace, all in one transaction.
func CreateBreakGlass(ctx context.Context, db TxBeginner, sp secrets.Provider, username, password string) (Enrollment, error) {
	if username == "" || len(username) > 128 {
		return Enrollment{}, errors.New("username must be 1-128 characters")
	}
	hash, err := auth.HashPassword(ctx, password)
	if err != nil {
		return Enrollment{}, err
	}
	id := uuid.New()
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		return Enrollment{}, err
	}
	env, err := auth.SealTOTPSecret(sp, id.String(), secret)
	if err != nil {
		return Enrollment{}, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return Enrollment{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)
	if _, err := q.CreateBreakGlassUser(ctx, dbgen.CreateBreakGlassUserParams{
		ID: id, Username: pgtype.Text{String: username, Valid: true},
		PasswordHash: pgtype.Text{String: hash, Valid: true}, TotpEnvelope: env,
	}); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			return Enrollment{}, fmt.Errorf("%w: %q", ErrUserExists, username)
		}
		return Enrollment{}, fmt.Errorf("create user: %w", err)
	}
	ws, err := q.GetWorkspaceBySlug(ctx, DefaultWorkspaceSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		ws, err = q.CreateWorkspace(ctx, dbgen.CreateWorkspaceParams{Slug: DefaultWorkspaceSlug, Name: DefaultWorkspaceName})
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("default workspace: %w", err)
	}
	if _, err := q.CreateMembership(ctx, dbgen.CreateMembershipParams{WorkspaceID: ws.ID, UserID: id, Role: "owner"}); err != nil {
		return Enrollment{}, fmt.Errorf("create membership: %w", err)
	}
	if err := audit(ctx, q, "admin.breakglass_created", id); err != nil {
		return Enrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Enrollment{}, fmt.Errorf("commit: %w", err)
	}
	return Enrollment{UserID: id, Secret: auth.EncodeTOTPSecret(secret), URI: auth.TOTPURI(username, secret)}, nil
}

// ResetBreakGlassTOTP replaces the TOTP secret of an existing break-glass
// user and clears lockout and replay state. The old secret stops working.
func ResetBreakGlassTOTP(ctx context.Context, db TxBeginner, sp secrets.Provider, username string) (Enrollment, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Enrollment{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbgen.New(tx)
	u, err := q.GetBreakGlassUser(ctx, pgtype.Text{String: username, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return Enrollment{}, fmt.Errorf("%w: %q", ErrNoSuchUser, username)
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("lookup user: %w", err)
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		return Enrollment{}, err
	}
	env, err := auth.SealTOTPSecret(sp, u.ID.String(), secret)
	if err != nil {
		return Enrollment{}, err
	}
	if _, err := q.SetBreakGlassTOTP(ctx, dbgen.SetBreakGlassTOTPParams{ID: u.ID, TotpEnvelope: env}); err != nil {
		return Enrollment{}, fmt.Errorf("store totp: %w", err)
	}
	if err := audit(ctx, q, "admin.breakglass_totp_reset", u.ID); err != nil {
		return Enrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Enrollment{}, fmt.Errorf("commit: %w", err)
	}
	return Enrollment{UserID: u.ID, Secret: auth.EncodeTOTPSecret(secret), URI: auth.TOTPURI(username, secret)}, nil
}

func audit(ctx context.Context, q *dbgen.Queries, action string, id uuid.UUID) error {
	if _, err := q.InsertAudit(ctx, dbgen.InsertAuditParams{
		ActorLabel: "cli", Action: action, TargetType: "user", TargetID: id.String(),
		ActorID: pgtype.UUID{}, WorkspaceID: pgtype.UUID{}, Details: []byte(`{}`),
	}); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}
