import { describe, expect, it } from 'vitest';
import { MAX_TOASTS, addToast, dismissAfter, removeToast, toastRole, type Toast } from './toasts';

const t = (id: number): Toast => ({ id, kind: 'info', message: `m${id}` });

describe('toasts', () => {
  it('keeps errors until closed and auto-dismisses the rest', () => {
    expect(dismissAfter('error')).toBeNull();
    expect(dismissAfter('success')).toBeGreaterThan(0);
    expect(dismissAfter('info')).toBeGreaterThan(0);
  });
  it('announces errors as alerts and others as status', () => {
    expect(toastRole('error')).toBe('alert');
    expect(toastRole('success')).toBe('status');
  });
  it('caps the list, dropping the oldest', () => {
    let l: Toast[] = [];
    for (let i = 1; i <= MAX_TOASTS + 2; i++) l = addToast(l, t(i));
    expect(l).toHaveLength(MAX_TOASTS);
    expect(l[0].id).toBe(3);
  });
  it('removes by id', () => {
    expect(removeToast([t(1), t(2)], 1).map((x) => x.id)).toEqual([2]);
  });
});
