/** Shared Tailwind class strings so pages stay visually consistent. */
export const btn =
  'rounded-md border border-border bg-surface px-3 py-1.5 text-sm hover:bg-surface-2 disabled:opacity-50';
export const btnPrimary =
  'rounded-md bg-accent px-3 py-1.5 text-sm font-medium text-accent-fg hover:opacity-90 disabled:opacity-50';
export const btnDanger =
  'rounded-md border border-red-600/50 px-3 py-1.5 text-sm text-red-600 hover:bg-red-600/10 disabled:opacity-50 dark:text-red-400';
export const input =
  'w-full rounded-md border border-border bg-surface px-3 py-1.5 text-sm text-fg disabled:opacity-60';
export const card = 'rounded-lg border border-border bg-surface p-4';
export const label = 'mb-1 block text-sm font-medium';
export const th = 'px-3 py-2 text-left text-xs font-medium uppercase tracking-wide text-muted';
export const td = 'px-3 py-2 text-sm';

/** Formats an ISO timestamp for display; '-' when absent. */
export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return '-';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}
