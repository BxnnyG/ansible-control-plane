import type { Run, Schedule, Template } from './api';

/** Case-insensitive match on name, playbook and description; blank query keeps everything. */
export function filterTemplates(items: readonly Template[], query: string): Template[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...items];
  return items.filter((t) =>
    [t.name, t.playbook, t.description ?? ''].some((v) => v.toLowerCase().includes(q)),
  );
}

/** Newest run per template from a newest-first run list; runs without a template are skipped. */
export function lastRunByTemplate(runs: readonly Run[]): Map<string, Run> {
  const out = new Map<string, Run>();
  for (const r of runs) {
    if (r.template_id && !out.has(r.template_id)) out.set(r.template_id, r);
  }
  return out;
}

/** Short label for the schedule column: "-", "paused", or the number of active schedules. */
export function scheduleLabel(schedules: readonly Schedule[] | undefined): string {
  if (!schedules || schedules.length === 0) return '-';
  const active = schedules.filter((s) => s.enabled).length;
  if (active === 0) return 'paused';
  return active === 1 ? 'scheduled' : `${active} schedules`;
}
