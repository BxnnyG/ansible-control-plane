import { describe, expect, it } from 'vitest';
import {
  buildSearch,
  enumParam,
  fleetFiltersFromParams,
  fleetFiltersToUpdates,
  flagParam,
} from './urlState';

const sp = (s: string) => new URLSearchParams(s);

describe('enumParam', () => {
  it('accepts allowed values and falls back otherwise', () => {
    expect(enumParam(sp('state=resolved'), 'state', ['open', 'resolved'], 'open')).toBe('resolved');
    expect(enumParam(sp('state=x'), 'state', ['open', 'resolved'], 'open')).toBe('open');
    expect(enumParam(sp(''), 'state', ['open', 'resolved'], 'open')).toBe('open');
  });
});

describe('buildSearch', () => {
  it('drops defaults and keeps unrelated keys', () => {
    expect(buildSearch(sp('host_id=1&state=resolved'), { state: '' })).toBe('?host_id=1');
    expect(buildSearch(sp(''), { state: 'all' })).toBe('?state=all');
    expect(buildSearch(sp('a=1'), { a: null })).toBe('');
  });
  it('writes flags as 1 and removes false', () => {
    expect(buildSearch(sp(''), { alerts: true })).toBe('?alerts=1');
    expect(buildSearch(sp('alerts=1'), { alerts: false })).toBe('');
    expect(flagParam(sp('alerts=1'), 'alerts')).toBe(true);
    expect(flagParam(sp('alerts=0'), 'alerts')).toBe(false);
  });
  it('encodes values', () => {
    expect(buildSearch(sp(''), { q: 'a b&c' })).toBe('?q=a+b%26c');
  });
});

describe('fleet filters round trip', () => {
  it('parses and serialises', () => {
    const f = fleetFiltersFromParams(sp('network=n1&q=web&conflicts=1&hidden=1'));
    expect(f).toEqual({ network: 'n1', q: 'web', conflicts: true, alerts: false, hidden: true });
    expect(buildSearch(sp(''), fleetFiltersToUpdates(f))).toBe(
      '?network=n1&q=web&conflicts=1&hidden=1',
    );
    expect(buildSearch(sp('q=x'), fleetFiltersToUpdates(fleetFiltersFromParams(sp(''))))).toBe('');
  });
});
