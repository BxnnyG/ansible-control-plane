package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
)

// DefaultWorkspaceSlug is the workspace IdP roles are mapped onto.
const DefaultWorkspaceSlug = "default"

// idpRoles maps IdP role names to workspace roles in ascending privilege.
// The owner role is never granted by the IdP.
var idpRoles = []struct{ idp, role string }{
	{"acp-viewer", "viewer"},
	{"acp-operator", "operator"},
	{"acp-editor", "editor"},
	{"acp-admin", "admin"},
}

// MapIdPRoles returns the highest workspace role granted by the IdP roles, or
// "" if none of them is mapped. Unknown roles are ignored.
func MapIdPRoles(roles []string) string {
	have := make(map[string]bool, len(roles))
	for _, r := range roles {
		have[r] = true
	}
	best := ""
	for _, m := range idpRoles {
		if have[m.idp] {
			best = m.role
		}
	}
	return best
}

// SyncStore is the persistence the role syncer needs; *dbgen.Queries implements it.
type SyncStore interface {
	GetWorkspaceBySlug(ctx context.Context, slug string) (dbgen.Workspace, error)
	GetMembership(ctx context.Context, arg dbgen.GetMembershipParams) (dbgen.Membership, error)
	CreateIdPMembership(ctx context.Context, arg dbgen.CreateIdPMembershipParams) (dbgen.Membership, error)
	UpdateIdPMembershipRole(ctx context.Context, arg dbgen.UpdateIdPMembershipRoleParams) (int64, error)
	DeleteIdPMembership(ctx context.Context, id uuid.UUID) (int64, error)
	InsertAudit(ctx context.Context, arg dbgen.InsertAuditParams) (dbgen.InsertAuditRow, error)
}

// IdPRoleSyncer maps IdP roles onto memberships of the default workspace.
//
// It only creates, updates and deletes memberships with source 'idp'. Local
// memberships (including every owner) are never touched, so removing a role in
// the IdP cannot lock out a locally managed account.
type IdPRoleSyncer struct {
	store SyncStore
	slug  string
	log   *slog.Logger
}

// NewIdPRoleSyncer returns a syncer for the default workspace.
func NewIdPRoleSyncer(store SyncStore, log *slog.Logger) *IdPRoleSyncer {
	return &IdPRoleSyncer{store: store, slug: DefaultWorkspaceSlug, log: log}
}

// SyncRoles implements RoleSyncer.
func (s *IdPRoleSyncer) SyncRoles(ctx context.Context, user dbgen.User, roles []string) error {
	ws, err := s.store.GetWorkspaceBySlug(ctx, s.slug)
	if errors.Is(err, pgx.ErrNoRows) {
		s.log.Warn("role sync skipped: workspace missing", "slug", s.slug)
		return nil
	}
	if err != nil {
		return fmt.Errorf("role sync: load workspace: %w", err)
	}
	want := MapIdPRoles(roles)
	cur, err := s.store.GetMembership(ctx, dbgen.GetMembershipParams{WorkspaceID: ws.ID, UserID: user.ID})
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("role sync: load membership: %w", err)
	}
	if exists && cur.Source != "idp" {
		return nil // locally managed, the IdP has no say
	}
	switch {
	case !exists && want == "":
		return nil
	case !exists:
		if _, err := s.store.CreateIdPMembership(ctx, dbgen.CreateIdPMembershipParams{
			WorkspaceID: ws.ID, UserID: user.ID, Role: want}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("role sync: create membership: %w", err)
		}
		s.audit(ctx, ws.ID, user, "created", "", want)
	case want == "":
		if _, err := s.store.DeleteIdPMembership(ctx, cur.ID); err != nil {
			return fmt.Errorf("role sync: delete membership: %w", err)
		}
		s.audit(ctx, ws.ID, user, "removed", cur.Role, "")
	case cur.Role != want:
		if _, err := s.store.UpdateIdPMembershipRole(ctx, dbgen.UpdateIdPMembershipRoleParams{ID: cur.ID, Role: want}); err != nil {
			return fmt.Errorf("role sync: update membership: %w", err)
		}
		s.audit(ctx, ws.ID, user, "updated", cur.Role, want)
	}
	return nil
}

func (s *IdPRoleSyncer) audit(ctx context.Context, wsID uuid.UUID, u dbgen.User, change, from, to string) {
	b, _ := json.Marshal(map[string]string{"change": change, "from": from, "to": to, "source": "idp"})
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := s.store.InsertAudit(actx, dbgen.InsertAuditParams{
		ActorLabel: "idp-sync", Action: "membership.idp_sync", TargetType: "user", TargetID: u.ID.String(),
		WorkspaceID: pgtype.UUID{Bytes: wsID, Valid: true}, Details: b,
	})
	if err != nil {
		s.log.Error("audit write failed", "action", "membership.idp_sync", "error", err)
	}
}
