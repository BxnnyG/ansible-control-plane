import { addToast, dismissAfter, removeToast, type Toast, type ToastKind } from './toasts';

class ToastStore {
  items = $state<Toast[]>([]);
  private next = 1;
  private timers = new Map<number, ReturnType<typeof setTimeout>>();

  push(kind: ToastKind, message: string): number {
    const id = this.next++;
    this.items = addToast(this.items, { id, kind, message });
    const ms = dismissAfter(kind);
    if (ms !== null)
      this.timers.set(
        id,
        setTimeout(() => this.dismiss(id), ms),
      );
    // Drop timers of toasts that were pushed out by the cap.
    for (const k of this.timers.keys()) {
      if (!this.items.some((t) => t.id === k)) this.clearTimer(k);
    }
    return id;
  }

  success(message: string): number {
    return this.push('success', message);
  }
  error(message: string): number {
    return this.push('error', message);
  }
  info(message: string): number {
    return this.push('info', message);
  }

  dismiss(id: number) {
    this.clearTimer(id);
    this.items = removeToast(this.items, id);
  }

  clear() {
    for (const k of [...this.timers.keys()]) this.clearTimer(k);
    this.items = [];
  }

  private clearTimer(id: number) {
    const t = this.timers.get(id);
    if (t !== undefined) clearTimeout(t);
    this.timers.delete(id);
  }
}

export const toasts = new ToastStore();
