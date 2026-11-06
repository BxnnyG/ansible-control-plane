import { describe, expect, it } from 'vitest';
import type { ConnectorType, SecretMeta } from './api';
import {
  buildConfig,
  compatibleSecrets,
  fmtDuration,
  initialValues,
  invalidFields,
  secretValue,
} from './connectors';

const t: ConnectorType = {
  type: 'opnsense',
  label: 'OPNsense',
  secret_types: ['api_key_pair'],
  secret_required: true,
  base_url: true,
  fields: [
    { key: 'verify_tls', label: 'Verify TLS', kind: 'bool', required: false },
    { key: 'port', label: 'Port', kind: 'int', required: false },
    { key: 'site', label: 'Site', kind: 'string', required: true },
  ],
};

describe('buildConfig', () => {
  it('coerces kinds and omits empty optional fields', () => {
    expect(buildConfig(t, { verify_tls: 'true', port: ' 8443 ', site: ' main ' })).toEqual({
      verify_tls: true,
      port: 8443,
      site: 'main',
    });
    expect(buildConfig(t, { verify_tls: 'false', port: '', site: 'x' })).toEqual({
      verify_tls: false,
      site: 'x',
    });
  });
});

describe('invalidFields', () => {
  it('reports empty required and non-integer fields', () => {
    expect(invalidFields(t, { site: '', port: 'abc' })).toEqual(['Port', 'Site']);
    expect(invalidFields(t, { site: 'a', port: '1' })).toEqual([]);
  });
});

describe('initialValues', () => {
  it('defaults bools to false and reads existing config', () => {
    expect(initialValues(t, null)).toEqual({ verify_tls: 'false', port: '', site: '' });
    const c = { config: { port: 9, site: 's', verify_tls: true } } as never;
    expect(initialValues(t, c)).toEqual({ verify_tls: 'true', port: '9', site: 's' });
  });
});

describe('compatibleSecrets', () => {
  it('keeps only accepted secret types', () => {
    const list = [
      { id: '1', type: 'api_key_pair' },
      { id: '2', type: 'token' },
    ] as SecretMeta[];
    expect(compatibleSecrets(t, list).map((s) => s.id)).toEqual(['1']);
  });
});

describe('secretValue and fmtDuration', () => {
  it('serializes key pairs as JSON and tokens as-is', () => {
    expect(JSON.parse(secretValue('api_key_pair', 'k', 's'))).toEqual({ key: 'k', secret: 's' });
    expect(secretValue('api_token', 'tok', '')).toBe('tok');
  });
  it('formats durations', () => {
    expect(fmtDuration(0)).toBe('-');
    expect(fmtDuration(850)).toBe('850 ms');
    expect(fmtDuration(2400)).toBe('2.4 s');
  });
});
