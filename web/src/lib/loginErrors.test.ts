import { describe, expect, it } from 'vitest';
import { loginErrorMessage } from './loginErrors';

describe('loginErrorMessage', () => {
  it.each(['login_failed', 'idp_unavailable', 'account_disabled', 'no_access', 'rate_limited'])(
    'maps %s to a message',
    (code) => {
      expect(loginErrorMessage(code)).toMatch(/\S/);
    },
  );

  it('returns null for missing or unknown codes', () => {
    expect(loginErrorMessage(null)).toBeNull();
    expect(loginErrorMessage('')).toBeNull();
    expect(loginErrorMessage('<script>')).toBeNull();
  });

  it('does not resolve prototype properties', () => {
    expect(loginErrorMessage('constructor')).toBeNull();
    expect(loginErrorMessage('toString')).toBeNull();
  });
});
