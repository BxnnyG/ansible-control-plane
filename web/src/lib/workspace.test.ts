import { describe, expect, it } from 'vitest';
import { pickWorkspace } from './workspace';

describe('pickWorkspace', () => {
  it('prefers the stored workspace when still a member', () => {
    expect(pickWorkspace(['default', 'lab'], 'lab')).toBe('lab');
  });
  it('ignores a stored workspace that is no longer available', () => {
    expect(pickWorkspace(['default', 'lab'], 'gone')).toBe('default');
  });
  it('falls back to the first workspace', () => {
    expect(pickWorkspace(['lab'], null)).toBe('lab');
  });
  it('returns null without memberships', () => {
    expect(pickWorkspace([], null)).toBeNull();
  });
});
