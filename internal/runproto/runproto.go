// Package runproto defines the wire format shared by the app's internal
// worker API and the worker supervisor (ADR-0016).
package runproto

import "encoding/json"

// API paths below /api/internal/worker.
const (
	BasePath = "/api/internal/worker"
	// LeaseHeader carries the lease ID on every per-run request.
	LeaseHeader = "X-Lease-ID"
	// MaxClaimWait is the longest long-poll of the claim endpoint.
	MaxClaimWait = 25 // seconds
)

// Names of the files inside the bundle (tar.gz).
const (
	FileSpec       = "spec.json"
	FileInventory  = "inventory.json"
	FileExtraVars  = "extra_vars.json"
	FileSSHKey     = "secrets/ssh_key"
	FileBecomePass = "secrets/become_pass"
	DirVault       = "secrets/vault/" // one password file per vault identity label
	DirProject     = "project"        // project tree at the run's revision
)

// ClaimRequest is the body of POST claim.
type ClaimRequest struct {
	WorkerID string `json:"worker_id"`
}

// ClaimResponse is returned by POST claim (204 when there is no work).
type ClaimResponse struct {
	RunID        string `json:"run_id"`
	LeaseID      string `json:"lease_id"`
	TemplateName string `json:"template_name"`
}

// Spec is the command specification inside the bundle. It never contains
// credentials, only the list of values the worker must mask (Mask).
type Spec struct {
	RunID     string            `json:"run_id"`
	Revision  string            `json:"revision"`
	Playbook  string            `json:"playbook"`
	Limit     string            `json:"limit,omitempty"`
	Tags      string            `json:"tags,omitempty"`
	SkipTags  string            `json:"skip_tags,omitempty"`
	Check     bool              `json:"check,omitempty"`
	Diff      bool              `json:"diff,omitempty"`
	Verbosity int               `json:"verbosity,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	SSHKey    bool              `json:"ssh_key,omitempty"`
	Become    bool              `json:"become_password,omitempty"`
	VaultIDs  []string          `json:"vault_ids,omitempty"`
	Mask      []string          `json:"mask,omitempty"`
}

// Chunk is one piece of the combined stdout/stderr stream; Seq is monotonic
// per run, starting at 1.
type Chunk struct {
	Seq  int64  `json:"seq"`
	Data string `json:"data"`
}

// EventsRequest is the body of POST events: log chunks and raw JSON events
// of the acp Ansible callback.
type EventsRequest struct {
	Chunks []Chunk           `json:"chunks,omitempty"`
	Events []json.RawMessage `json:"events,omitempty"`
}

// HeartbeatResponse is the body of the heartbeat response.
type HeartbeatResponse struct {
	Cancel bool `json:"cancel"`
}

// FinishRequest is the body of POST finish.
type FinishRequest struct {
	ExitCode  int  `json:"exit_code"`
	Cancelled bool `json:"cancelled,omitempty"`
}

// Event is the subset of a callback event the app evaluates.
type Event struct {
	Event   string                    `json:"event"`
	Host    string                    `json:"host"`
	Task    string                    `json:"task"`
	Ignored bool                      `json:"ignored"`
	Hosts   map[string]map[string]int `json:"hosts"`
}
