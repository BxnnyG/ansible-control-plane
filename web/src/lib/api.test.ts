import { describe, expect, it } from 'vitest';
import { csrfToken, oidcLoginUrl } from './api';

describe('csrfToken', () => {
  it('reads the plain cookie', () => {
    expect(csrfToken('a=1; acp_csrf=tok%2B1; b=2')).toBe('tok+1');
  });
  it('reads the __Host- cookie', () => {
    expect(csrfToken('__Host-acp_csrf=abc')).toBe('abc');
  });
  it('returns empty when absent', () => {
    expect(csrfToken('a=1')).toBe('');
  });
});

describe('oidcLoginUrl', () => {
  it('encodes the return path', () => {
    expect(oidcLoginUrl('/runs?x=1&y=2')).toBe(
      '/api/auth/oidc/login?return_to=%2Fruns%3Fx%3D1%26y%3D2',
    );
  });
});
