import { describe, expect, it } from 'vitest';
import { legacyRedirect } from './legacyRoutes';

describe('legacyRedirect', () => {
  const cases: [string, string | null][] = [
    ['/auth/login', '/login'],
    ['/project/1/views/1/templates', '/templates'],
    ['/project/1/templates/5', '/templates'],
    ['/project/1/history', '/runs'],
    ['/project/1/tasks/42', '/runs'],
    ['/project/1/inventory', '/inventories'],
    ['/project/1/keys', '/secrets'],
    ['/project/1/environment', '/'],
    ['/project/1', '/'],
    ['/project/1/', '/'],
    ['/templates', null],
    ['/projects/abc', null],
    ['/', null],
  ];
  it.each(cases)('%s -> %s', (path, want) => {
    expect(legacyRedirect(path)).toBe(want);
  });
});
