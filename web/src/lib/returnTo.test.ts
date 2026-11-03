import { describe, expect, it } from 'vitest';
import { safeReturnTo } from './returnTo';

describe('safeReturnTo', () => {
  it.each(['/', '/fleet', '/runs?id=1#top', '/a/b%2Fc'])('keeps %s', (p) => {
    expect(safeReturnTo(p)).toBe(p);
  });

  it.each([
    null,
    undefined,
    '',
    'fleet',
    'https://example.com/',
    '//example.com',
    '/\\example.com',
    '/a\\b',
    '/a\nb',
    '/a\u0000b',
    'javascript:alert(1)',
  ])('rejects %j', (p) => {
    expect(safeReturnTo(p)).toBe('/');
  });

  it('uses the given fallback', () => {
    expect(safeReturnTo('//x', '/fleet')).toBe('/fleet');
  });
});
