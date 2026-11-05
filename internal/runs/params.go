package runs

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
)

// Errors returned by the Service. Validation errors wrap ErrInvalid.
var (
	ErrNotFound  = errors.New("not found")
	ErrInvalid   = errors.New("invalid request")
	ErrConflict  = errors.New("conflict")
	ErrNotSynced = errors.New("project has no synced revision; sync the project first")
)

// Run triggers (runs.trigger).
const (
	TriggerManual   = "manual"
	TriggerSchedule = "schedule"
	TriggerWebhook  = "webhook"
	TriggerAPI      = "api"
)

// Run statuses (runs.status).
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
	StatusLost      = "lost"
)

// MaskedValue replaces secret survey values in API output.
const MaskedValue = "***"

// Params is the immutable snapshot of the effective parameters of one run:
// template defaults merged with the overrides, the pinned project revision and
// the credential references. The worker API builds the bundle from it; later
// edits of the template do not affect queued runs.
type Params struct {
	Playbook        string         `json:"playbook"`
	ProjectID       uuid.UUID      `json:"project_id"`
	ProjectRevision string         `json:"project_revision"`
	InventoryID     uuid.UUID      `json:"inventory_id"`
	Limit           string         `json:"limit"`
	Hosts           []string       `json:"hosts"` // effective host set (inventory and limit)
	Tags            string         `json:"tags"`
	SkipTags        string         `json:"skip_tags"`
	ExtraVars       map[string]any `json:"extra_vars"`
	// SecretVars lists top-level extra_vars keys flagged secret by the survey;
	// their values are masked in every API response.
	SecretVars       []string       `json:"secret_vars,omitempty"`
	EnvVars          map[string]any `json:"env_vars"`
	Check            bool           `json:"check"`
	Diff             bool           `json:"diff"`
	Verbosity        int            `json:"verbosity"`
	SSHSecretID      *uuid.UUID     `json:"ssh_secret_id"`
	BecomeSecretID   *uuid.UUID     `json:"become_secret_id"`
	VaultIdentityIDs []uuid.UUID    `json:"vault_identity_ids"`
}

// DecodeParams parses the stored params column.
func DecodeParams(raw []byte) (Params, error) {
	var p Params
	if err := json.Unmarshal(raw, &p); err != nil {
		return Params{}, fmt.Errorf("decode run params: %w", err)
	}
	return p, nil
}

// Masked returns a copy that is safe to return through the API: values of
// secret survey fields are replaced.
func (p Params) Masked() Params {
	p.ExtraVars = maskVars(p.ExtraVars, p.SecretVars)
	return p
}

func maskVars(vars map[string]any, secret []string) map[string]any {
	if len(secret) == 0 || vars == nil {
		return vars
	}
	out := make(map[string]any, len(vars))
	for k, v := range vars {
		out[k] = v
	}
	for _, k := range secret {
		if _, ok := out[k]; ok {
			out[k] = MaskedValue
		}
	}
	return out
}

// Overrides are the per-run changes to the template defaults. A nil or empty
// field leaves the default. Each group must be allowed by the template's
// allow_*_override flag.
type Overrides struct {
	Limit     *string        `json:"limit,omitempty"`
	Tags      *string        `json:"tags,omitempty"`
	SkipTags  *string        `json:"skip_tags,omitempty"`
	ExtraVars map[string]any `json:"extra_vars,omitempty"`
	Check     *bool          `json:"check,omitempty"`
	Diff      *bool          `json:"diff,omitempty"`
	Verbosity *int           `json:"verbosity,omitempty"`
}

// Actor identifies who triggers a run. UserID is recorded as triggered_by
// (the schedule creator for scheduled runs). Principal must be set for
// interactive triggers (manual, api): the effective host set is then
// authorized against the caller's scopes. Schedules and webhooks run with the
// template's workspace authority and pass a nil Principal (ADR-0016).
type Actor struct {
	UserID    uuid.UUID
	Principal *auth.Principal
}
