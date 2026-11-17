package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/BxnnyG/ansible-control-plane/internal/db/dbgen"
	"github.com/BxnnyG/ansible-control-plane/internal/runproto"
)

// ProbeTemplateName is the template_name snapshot of built-in probe runs.
const ProbeTemplateName = "Reachability probe"

// ProbePlaybook is the file name of the probe playbook in the worker image.
const ProbePlaybook = "probe.yml"

// ProbeInput describes one built-in probe run (ADR-0022, NET-1). Targets is
// passed to the playbook as the extra variable acp_probe_targets.
type ProbeInput struct {
	InventoryID uuid.UUID
	SSHSecretID *uuid.UUID
	Hosts       []string // probe sources (inventory host names)
	Targets     []map[string]any
}

// EnqueueProbe creates a queued run of the built-in probe playbook. Such a run
// has no template and no project: template_id and project_id stay NULL and
// params.builtin marks the source. It is started with workspace authority
// (scheduler or an operator holding the reachability run permission), so no
// per-host authorization happens here; callers check permissions.
func (s *Service) EnqueueProbe(ctx context.Context, ws uuid.UUID, in ProbeInput, trigger string, actor Actor) (Run, error) {
	switch trigger {
	case TriggerManual, TriggerSchedule, TriggerAPI:
	default:
		return Run{}, fmt.Errorf("%w: unknown trigger", ErrInvalid)
	}
	if len(in.Hosts) == 0 || len(in.Targets) == 0 {
		return Run{}, fmt.Errorf("%w: a probe run needs sources and targets", ErrInvalid)
	}
	p := Params{
		Playbook: ProbePlaybook, Builtin: runproto.BuiltinProbe, InventoryID: in.InventoryID,
		Limit: strings.Join(in.Hosts, ","), Hosts: in.Hosts, SSHSecretID: in.SSHSecretID,
		ExtraVars: map[string]any{"acp_probe_targets": in.Targets},
		EnvVars:   map[string]any{}, VaultIdentityIDs: []uuid.UUID{},
	}
	pJSON, err := json.Marshal(p)
	if err != nil {
		return Run{}, fmt.Errorf("encode params: %w", err)
	}
	var by *uuid.UUID
	if actor.UserID != uuid.Nil {
		by = &actor.UserID
	}
	row, err := s.q.EnqueueBuiltinRun(ctx, dbgen.EnqueueBuiltinRunParams{
		WorkspaceID: ws, TemplateName: ProbeTemplateName, Trigger: trigger, TriggeredBy: toPg(by),
		Overrides: []byte("{}"), Params: pJSON,
	})
	if err != nil {
		return Run{}, fmt.Errorf("enqueue probe run: %w", mapErr(err))
	}
	s.notifyList(ctx, row.ID)
	run, err := toRun(row)
	if err != nil {
		return Run{}, err
	}
	s.fillName(ctx, &run)
	return run, nil
}
