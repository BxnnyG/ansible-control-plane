package semaphore

import (
	"fmt"
	"io"
	"strings"
)

// WriteText renders the human-readable dry-run report (MIG-5).
func (r *Report) WriteText(w io.Writer) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	p("Semaphore migration dry run -> workspace %q", r.Workspace)
	p("")
	sections := []struct {
		title string
		items []Item
	}{
		{"Repositories", r.Repositories},
		{"Secrets (access keys)", r.Secrets},
		{"Inventories", r.Inventories},
		{"Environments", r.Environments},
		{"Templates", r.Templates},
		{"Schedules", r.Schedules},
	}
	counts := map[string]int{}
	for _, s := range sections {
		p("== %s (%d)", s.title, len(s.items))
		for _, it := range s.items {
			counts[it.Status]++
			line := fmt.Sprintf("  [%s] #%s %s", it.Status, it.SourceID, it.Name)
			if it.Target != "" {
				line += " -> " + it.Target
			}
			p("%s", line)
			if it.Reason != "" {
				p("      reason: %s", it.Reason)
			}
			for _, n := range it.Notes {
				p("      - %s", n)
			}
		}
		p("")
	}
	if len(r.Suggestions) > 0 {
		p("== Inventory suggestions (use --inventory-map <semaphore-id>=<acp-inventory>)")
		for _, s := range r.Suggestions {
			p("  --inventory-map %d=%s   (%q, %d%% host overlap)", s.SourceID, s.Slug, s.SourceName, int(s.Overlap*100+0.5))
		}
		p("")
	}
	p("== Users (reported only; ACP users are created via OIDC login) (%d)", len(r.Users))
	for _, u := range r.Users {
		admin := ""
		if u.Admin {
			admin = " admin"
		}
		p("  %s%s %s", u.Username, admin, strings.TrimSpace(u.Roles))
	}
	p("")
	p("== Task history: %d run(s) are not imported (stay in the backup and in Semaphore)", r.TaskCount)
	p("")
	if len(r.Warnings) > 0 {
		p("== Warnings")
		for _, wn := range r.Warnings {
			p("  - %s", wn)
		}
		p("")
	}
	p("Summary: new=%d exists=%d matched=%d skipped=%d unmappable=%d",
		counts[StatusNew], counts[StatusExists], counts[StatusMatched], counts[StatusSkipped], counts[StatusUnmappable])
	p("Dry run: nothing was written.")
}

// WriteText renders the apply report.
func (r *ApplyResult) WriteText(w io.Writer) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	p("Semaphore migration apply -> workspace %q", r.Workspace)
	for _, it := range r.Items {
		line := fmt.Sprintf("  [%s] %s #%s %s", it.Result, it.Kind, it.SourceID, it.Name)
		if it.ACPID != "" && it.Result == ResCreated {
			line += " -> " + it.ACPID
		}
		p("%s", line)
		if it.Reason != "" {
			p("      reason: %s", it.Reason)
		}
	}
	p("")
	p("Summary: created=%d exists=%d conflict=%d failed=%d skipped=%d",
		r.Counts[ResCreated], r.Counts[ResExists], r.Counts[ResConflict], r.Counts[ResFailed], r.Counts[ResSkipped])
	p("Schedules are created disabled; enable them after cutover.")
}

// WriteText renders the comparison.
func (r *CompareResult) WriteText(w io.Writer) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(w, format+"\n", a...) }
	p("Semaphore migration comparison -> workspace %q", r.Workspace)
	p("  %-16s %7s %7s %8s", "kind", "source", "mapped", "present")
	for _, row := range r.Rows {
		p("  %-16s %7d %7d %8d", row.Kind, row.Source, row.Mapped, row.Present)
		for _, m := range row.Missing {
			p("      missing: %s", m)
		}
	}
	if len(r.CheckRuns) > 0 {
		p("")
		p("Check-mode runs (not awaited):")
		for _, c := range r.CheckRuns {
			if c.Error != "" {
				p("  #%s %s: not queued: %s", c.SourceID, c.Template, c.Error)
			} else {
				p("  #%s %s: run %s", c.SourceID, c.Template, c.RunID)
			}
		}
	}
	p("")
	if r.Match {
		p("Result: counts match.")
	} else {
		p("Result: MISMATCH.")
	}
}
