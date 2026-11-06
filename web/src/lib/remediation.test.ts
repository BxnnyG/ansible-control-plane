import { describe, expect, it } from 'vitest';
import { ApiError, type RemediationRef } from './api';
import { describeRemediationError, describeTarget, fixLabel, rulesFor } from './remediation';

const ref = (p: Partial<RemediationRef> = {}): RemediationRef => ({
  rule_id: 'r1',
  name: 'restart',
  template: 't',
  target: 'host',
  group: '',
  check_first: false,
  ...p,
});

describe('remediation helpers', () => {
  it('describes targets', () => {
    expect(describeTarget(ref())).toBe('host');
    expect(describeTarget(ref({ target: 'group', group: 'web' }))).toBe('group web');
    expect(describeTarget(ref({ target: 'network' }))).toBe('network of the host');
  });

  it('labels check-first rules as a check', () => {
    expect(fixLabel(ref())).toBe('Fix: restart');
    expect(fixLabel(ref({ check_first: true }))).toBe('Check: restart');
  });

  it('looks up applicable rules without failing on missing data', () => {
    expect(rulesFor(null, 'a')).toEqual([]);
    expect(rulesFor({ a: [ref()] }, 'a')).toHaveLength(1);
    expect(rulesFor({ a: [ref()] }, 'b')).toEqual([]);
  });

  it('maps API errors', () => {
    expect(describeRemediationError(new ApiError(409, 'x', 'rule_disabled'))).toMatch(/disabled/);
    expect(describeRemediationError(new ApiError(409, 'x', 'not_applicable'))).toMatch(/no longer/);
    expect(describeRemediationError(new ApiError(403, 'forbidden'))).toMatch(/scope/);
    expect(describeRemediationError(new ApiError(422, 'bad group', 'invalid'))).toBe('bad group');
    expect(describeRemediationError(new ApiError(500, 'boom'))).toBeNull();
    expect(describeRemediationError(new Error('x'))).toBeNull();
  });
});
