import { describe, expect, it } from 'vitest';
import { normalizeScan } from './scan';

describe('normalizeScan', () => {
  it('fills missing lists', () => {
    const s = normalizeScan({ roles: [{ name: 'r', path: 'roles/r' }] });
    expect(s.playbooks).toEqual([]);
    expect(s.roles[0].surveys).toEqual([]);
  });

  it('accepts null', () => {
    expect(normalizeScan(null).vault_files).toEqual([]);
  });
});
