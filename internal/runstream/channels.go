package runstream

import (
	"strings"

	"github.com/google/uuid"
)

// ListChannel announces status changes of any run; the payload is the run id.
const ListChannel = "acp_runs"

// RunChannel is the NOTIFY channel of one run (log and status wake-ups).
func RunChannel(run uuid.UUID) string { return "acp_run_" + strings.ReplaceAll(run.String(), "-", "") }
