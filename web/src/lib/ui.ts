/** Shared Tailwind class strings so pages stay visually consistent. */
const touch = 'max-md:min-h-11';
export const btn = `inline-flex items-center justify-center gap-2 rounded-lg border border-transparent bg-surface-2 px-3.5 py-1.5 text-sm font-medium hover:bg-border disabled:pointer-events-none disabled:opacity-50 ${touch}`;
export const btnPrimary = `bg-brand inline-flex items-center justify-center gap-2 rounded-lg border border-transparent px-3.5 py-1.5 text-sm font-semibold disabled:pointer-events-none disabled:opacity-50 ${touch}`;
export const btnDanger = `inline-flex items-center justify-center gap-2 rounded-lg border border-bad/40 px-3.5 py-1.5 text-sm font-medium text-bad hover:bg-bad/10 disabled:pointer-events-none disabled:opacity-50 ${touch}`;
export const input = `w-full rounded-lg border border-border bg-surface-2 px-3 py-1.5 text-base text-fg placeholder:text-muted/70 disabled:opacity-60 md:text-sm ${touch}`;
/** Soft elevated panel. */
export const card = 'rounded-xl border border-border bg-surface p-5 shadow-card';
export const label = 'mb-1 block text-sm font-medium';
export const th =
  'px-3 py-2 text-left text-[0.68rem] font-semibold uppercase tracking-wider text-muted';
export const td = 'px-3 py-1.5 text-sm';
/** Table container: hairline border, sticky header, stacked rows on small screens (see app.css). */
export const tblWrap = 'tbl-wrap';
export const emptyBox = 'empty';
/** Monospace + tabular figures for hostnames, ids, revisions, numbers. */
export const mono = 'font-mono text-[0.92em]';
/** Formats an ISO timestamp for display; '-' when absent. */
export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '-';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
