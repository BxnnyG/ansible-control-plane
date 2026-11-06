package semaphore

import "github.com/BxnnyG/ansible-control-plane/internal/scan"

// Mapping kinds stored in migration_mappings.
const (
	KindSecret     = "secret"
	KindInventory  = "inventory"
	KindTemplate   = "template"
	KindSchedule   = "schedule"
	KindRepository = "repository"
	KindVault      = "vault_identity"
)

// Item statuses.
const (
	StatusNew        = "new"        // will be created by --apply
	StatusExists     = "exists"     // already imported (migration_mappings)
	StatusMatched    = "matched"    // resolved to an existing ACP object
	StatusSkipped    = "skipped"    // intentionally not migrated
	StatusUnmappable = "unmappable" // cannot be mapped, see Reason
)

// Item is one line of the dry-run report.
type Item struct {
	Kind     string   `json:"kind"`
	SourceID string   `json:"source_id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Target   string   `json:"target,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Notes    []string `json:"notes,omitempty"`
}

// PlannedSecret describes an ACP secret to create. The value is decrypted at
// apply time only.
type PlannedSecret struct {
	SourceKeyID int64  `json:"source_key_id"`
	Name        string `json:"name"`
	Type        string `json:"type"` // ssh_key, password, vault_password
	Login       string `json:"login,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// PlannedVault describes a vault identity backed by the secret of a vault key.
type PlannedVault struct {
	SourceKeyID int64  `json:"source_key_id"`
	Label       string `json:"label"`
}

// PlannedInventory describes an archived DB inventory for a static inventory.
type PlannedInventory struct {
	SourceID int64  `json:"source_id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Format   string `json:"format"` // ini or yaml
}

// PlannedTemplate describes an ACP template.
type PlannedTemplate struct {
	SourceID     int64             `json:"source_id"`
	Name         string            `json:"name"`
	Playbook     string            `json:"playbook"`
	ProjectID    string            `json:"project_id"`
	Inventory    string            `json:"inventory"` // ACP inventory slug
	InventoryM   bool              `json:"inventory_mapped"`
	InventorySrc int64             `json:"inventory_source_id"`
	SSHKeyID     *int64            `json:"ssh_key_id,omitempty"`
	BecomeID     *int64            `json:"become_key_id,omitempty"`
	VaultKeys    []int64           `json:"vault_key_ids,omitempty"`
	ExtraVars    map[string]any    `json:"extra_vars,omitempty"`
	EnvVars      map[string]string `json:"env_vars,omitempty"`
	Limit        string            `json:"limit,omitempty"`
	Tags         string            `json:"tags,omitempty"`
	SkipTags     string            `json:"skip_tags,omitempty"`
	Verbosity    int               `json:"verbosity,omitempty"`
	Check        bool              `json:"check,omitempty"`
	Diff         bool              `json:"diff,omitempty"`
	Survey       []scan.Field      `json:"survey,omitempty"`
}

// PlannedSchedule describes an ACP schedule (always disabled).
type PlannedSchedule struct {
	SourceID   int64  `json:"source_id"`
	TemplateID int64  `json:"template_id"`
	Name       string `json:"name"`
	Cron       string `json:"cron"`
}

// Suggestion proposes an ACP inventory for a Semaphore inventory.
type Suggestion struct {
	SourceID   int64   `json:"source_id"`
	SourceName string  `json:"source_name"`
	Slug       string  `json:"slug"`
	Overlap    float64 `json:"overlap"` // 0..1, share of source hosts found in the ACP inventory
}

// UserSummary is the reported-only user section.
type UserSummary struct {
	Username string `json:"username"`
	Admin    bool   `json:"admin"`
	Roles    string `json:"roles,omitempty"`
}

// Report is the result of mapping a Snapshot.
type Report struct {
	Workspace    string        `json:"workspace"`
	Secrets      []Item        `json:"secrets"`
	Repositories []Item        `json:"repositories"`
	Inventories  []Item        `json:"inventories"`
	Environments []Item        `json:"environments"`
	Templates    []Item        `json:"templates"`
	Schedules    []Item        `json:"schedules"`
	Users        []UserSummary `json:"users"`
	TaskCount    int64         `json:"task_history_not_imported"`
	Suggestions  []Suggestion  `json:"inventory_suggestions,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`

	// Plans drive --apply; they are part of the JSON report for review.
	PlanSecrets     []PlannedSecret    `json:"plan_secrets"`
	PlanVaults      []PlannedVault     `json:"plan_vaults"`
	PlanInventories []PlannedInventory `json:"plan_inventories"`
	PlanTemplates   []PlannedTemplate  `json:"plan_templates"`
	PlanSchedules   []PlannedSchedule  `json:"plan_schedules"`
}
