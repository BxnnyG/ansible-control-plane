import { describe, expect, it } from 'vitest';
import { ApiCache, cacheKey, isCacheableList, workspaceOf } from './apiCache';

const base = '/api/v1/workspaces/acme';

describe('isCacheableList', () => {
  it('accepts plain collections', () => {
    for (const r of ['templates', 'secrets', 'projects', 'inventories', 'connectors', 'schedules'])
      expect(isCacheableList(`${base}/${r}`)).toBe(true);
  });
  it('rejects details, sub-paths, queries and unknown resources', () => {
    expect(isCacheableList(`${base}/secrets/123`)).toBe(false);
    expect(isCacheableList(`${base}/templates/1/schedules`)).toBe(false);
    expect(isCacheableList(`${base}/templates?x=1`)).toBe(false);
    expect(isCacheableList(`${base}/runs`)).toBe(false);
    expect(isCacheableList(`${base}/vault-identities/1/decrypt`)).toBe(false);
    expect(isCacheableList('/api/v1/me')).toBe(false);
  });
});

describe('keys', () => {
  it('keys by workspace and URL', () => {
    expect(workspaceOf(`${base}/templates`)).toBe('acme');
    expect(workspaceOf('/api/v1/me')).toBeNull();
    expect(cacheKey(`${base}/templates`)).not.toBe(cacheKey('/api/v1/workspaces/other/templates'));
  });
});

describe('ApiCache', () => {
  it('stores and returns values', () => {
    const c = new ApiCache();
    c.set('a|x', [1], c.token());
    expect(c.get('a|x')).toEqual([1]);
  });
  it('invalidates only the mutated workspace', () => {
    const c = new ApiCache();
    c.set('a|x', 1, c.token());
    c.set('b|x', 2, c.token());
    c.invalidateWorkspace('a');
    expect(c.get('a|x')).toBeUndefined();
    expect(c.get('b|x')).toBe(2);
  });
  it('drops responses that raced a mutation', () => {
    const c = new ApiCache();
    const tok = c.token();
    c.invalidateWorkspace('a');
    c.set('a|x', 'stale', tok);
    expect(c.get('a|x')).toBeUndefined();
  });
  it('clears everything', () => {
    const c = new ApiCache();
    c.set('a|x', 1, c.token());
    c.clear();
    expect(c.size).toBe(0);
  });
});
