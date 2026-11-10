/** Pure toast list logic; the reactive store lives in toasts.svelte.ts. */

export type ToastKind = 'success' | 'error' | 'info';

export interface Toast {
  id: number;
  kind: ToastKind;
  message: string;
}

/** Most toasts kept on screen at once; the oldest are dropped first. */
export const MAX_TOASTS = 4;

/** Auto-dismiss delay in ms; errors stay until closed (null). */
export function dismissAfter(kind: ToastKind): number | null {
  return kind === 'error' ? null : kind === 'success' ? 4000 : 6000;
}

/** ARIA live role: errors interrupt, the rest are polite. */
export function toastRole(kind: ToastKind): 'alert' | 'status' {
  return kind === 'error' ? 'alert' : 'status';
}

export function addToast(list: readonly Toast[], t: Toast): Toast[] {
  return [...list, t].slice(-MAX_TOASTS);
}

export function removeToast(list: readonly Toast[], id: number): Toast[] {
  return list.filter((t) => t.id !== id);
}
