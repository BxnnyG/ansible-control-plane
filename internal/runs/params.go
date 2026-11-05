package runs

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/auth"
	"github.com/BxnnyG/ansible-control-plane/internal/secrets"
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
	SecretVars []string `json:"secret_vars,omitempty"`
	// SecretVarsEnc holds the values of SecretVars as an envelope (JSON object
	// key -> value). The values in ExtraVars are masked; only the worker bundle
	// builder opens the envelope. It is removed when the run ends.
	SecretVarsEnc    *secrets.Envelope `json:"secret_vars_enc,omitempty"`
	EnvVars          map[string]any    `json:"env_vars"`
	Check            bool              `json:"check"`
	Diff             bool              `json:"diff"`
	Verbosity        int               `json:"verbosity"`
	SSHSecretID      *uuid.UUID        `json:"ssh_secret_id"`
	BecomeSecretID   *uuid.UUID        `json:"become_secret_id"`
	VaultIdentityIDs []uuid.UUID       `json:"vault_identity_ids"`
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

// secretVarsAAD binds the sealed survey secrets to the workspace.
func secretVarsAAD(ws uuid.UUID) secrets.AAD {
	return secrets.AAD{SecretID: "run-secret-vars", WorkspaceID: ws.String()} //nolint:gosec // fixed label, not a credential
}

// SealSecretVars moves the values of the secret survey fields out of
// ExtraVars into an envelope; ExtraVars keeps MaskedValue placeholders. It is
// a no-op without secret variables.
func (p *Params) SealSecretVars(prov secrets.Provider, ws uuid.UUID) error {
	vals := map[string]any{}
	for _, k := range p.SecretVars {
		if v, ok := p.ExtraVars[k]; ok {
			vals[k] = v
		}
	}
	if len(vals) == 0 {
		return nil
	}
	if prov == nil {
		return fmt.Errorf("seal secret variables: no secrets provider configured")
	}
	raw, err := json.Marshal(vals)
	if err != nil {
		return fmt.Errorf("encode secret variables: %w", err)
	}
	env, err := secrets.Seal(prov, raw, secretVarsAAD(ws))
	clear(raw)
	if err != nil {
		return fmt.Errorf("seal secret variables: %w", err)
	}
	p.SecretVarsEnc = env
	p.ExtraVars = maskVars(p.ExtraVars, p.SecretVars)
	return nil
}

// OpenSecretVars returns ExtraVars with the real values of the secret survey
// fields. Only the worker bundle builder may call it. After the run ended
// the envelope is gone and the masked placeholders are returned.
func (p Params) OpenSecretVars(prov secrets.Provider, ws uuid.UUID) (map[string]any, error) {
	out := make(map[string]any, len(p.ExtraVars))
	for k, v := range p.ExtraVars {
		out[k] = v
	}
	if p.SecretVarsEnc == nil {
		return out, nil
	}
	v, err := secrets.Open(prov, p.SecretVarsEnc, secretVarsAAD(ws))
	if err != nil {
		return nil, fmt.Errorf("open secret variables: %w", err)
	}
	defer v.Zero()
	var vals map[string]any
	if err := json.Unmarshal(v.Reveal(), &vals); err != nil {
		return nil, fmt.Errorf("decode secret variables: %w", err)
	}
	for k, x := range vals {
		out[k] = x
	}
	return out, nil
}
