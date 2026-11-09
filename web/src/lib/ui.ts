/** Shared Tailwind class strings so pages stay visually consistent. */
const touch = 'max-md:min-h-11';
export const btn = `inline-flex items-center justify-center gap-2 rounded-lg border border-border bg-surface px-3.5 py-1.5 text-sm font-medium shadow-xs hover:border-border-strong hover:bg-surface-2 disabled:pointer-events-none disabled:opacity-50 ${touch}`;
export const btnPrimary = `bg-brand inline-flex items-center justify-center gap-2 rounded-lg px-3.5 py-1.5 text-sm font-medium shadow-sm hover:brightness-110 disabled:pointer-events-none disabled:opacity-50 ${touch}`;
export const btnDanger = `inline-flex items-center justify-center gap-2 rounded-lg border border-bad/40 px-3.5 py-1.5 text-sm font-medium text-bad hover:bg-bad/10 disabled:pointer-events-none disabled:opacity-50 ${touch}`;
export const input = `w-full rounded-lg border border-border bg-surface px-3 py-1.5 text-base text-fg placeholder:text-muted/70 disabled:opacity-60 md:text-sm ${touch}`;
export const card = 'rounded-2xl border border-border bg-surface p-4 shadow-card sm:p-5';
export const label = 'mb-1 block text-sm font-medium';
export const th = 'px-4 py-2.5 text-left text-xs font-semibold uppercase tracking-wide text-muted';
export const td = 'px-4 py-3 text-sm';
/** Table container: card look, sticky header, stacked cards on small screens (see app.css). */
export const tblWrap = 'tbl-wrap';
export const emptyBox = 'empty';
/** Formats an ISO timestamp for display; '-' when absent. */
export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '-';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
