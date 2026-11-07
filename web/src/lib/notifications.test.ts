import { describe, expect, it } from 'vitest';
import {
  buildChannelConfig,
  channelsWithoutRules,
  emptyFields,
  fieldsFromConfig,
  parseRecipients,
  secretRequired,
  toggleEvent,
  validateChannel,
} from './notifications';

const f = (over: Partial<ReturnType<typeof emptyFields>>) => ({ ...emptyFields(), ...over });

describe('buildChannelConfig', () => {
  it('builds webhook config', () => {
    expect(buildChannelConfig('webhook', f({ url: ' https://h/x ', auth: 'bearer' }))).toEqual({
      url: 'https://h/x',
      auth: 'bearer',
    });
  });
  it('builds smtp config with recipients and omits empty optionals', () => {
    expect(
      buildChannelConfig(
        'smtp',
        f({
          host: 'mx',
          from: 'a@b',
          to: 'x@y, z@y',
          security: 'none',
          insecure: true,
          port: '25',
        }),
      ),
    ).toEqual({
      host: 'mx',
      port: 25,
      security: 'none',
      insecure: true,
      from: 'a@b',
      to: ['x@y', 'z@y'],
    });
  });
  it('builds ntfy and gotify config with priority', () => {
    expect(buildChannelConfig('ntfy', f({ url: 'https://n', topic: 't', priority: '4' }))).toEqual({
      url: 'https://n',
      topic: 't',
      priority: 4,
    });
    expect(buildChannelConfig('gotify', f({ url: 'https://g' }))).toEqual({ url: 'https://g' });
  });
  it('builds matrix config', () => {
    expect(buildChannelConfig('matrix', f({ homeserver: 'https://m', roomId: '!r:m' }))).toEqual({
      homeserver: 'https://m',
      room_id: '!r:m',
    });
  });
});

describe('fieldsFromConfig', () => {
  it('round-trips smtp', () => {
    const cfg = {
      host: 'mx',
      port: 465,
      security: 'tls',
      from: 'a@b',
      to: ['x@y', 'z@y'],
      username: 'u',
    };
    const fields = fieldsFromConfig('smtp', cfg);
    expect(fields.to).toBe('x@y, z@y');
    expect(buildChannelConfig('smtp', fields)).toEqual(cfg);
  });
});

describe('validateChannel', () => {
  it('requires a name and http(s) URL', () => {
    expect(validateChannel('webhook', '', f({}), false)).toMatch(/Name/);
    expect(validateChannel('webhook', 'n', f({ url: 'ftp://x' }), false)).toMatch(/URL/);
    expect(validateChannel('webhook', 'n', f({ url: 'https://u:p@x' }), false)).toMatch(/URL/);
    expect(validateChannel('webhook', 'n', f({ url: 'https://x' }), false)).toBeNull();
  });
  it('requires a secret for bearer, matrix and gotify', () => {
    expect(validateChannel('webhook', 'n', f({ url: 'https://x', auth: 'bearer' }), false)).toMatch(
      /secret/,
    );
    expect(
      validateChannel('matrix', 'n', f({ homeserver: 'https://m', roomId: '!r:m' }), false),
    ).toMatch(/secret/);
    expect(validateChannel('gotify', 'n', f({ url: 'https://g' }), false)).toMatch(/secret/);
    expect(validateChannel('gotify', 'n', f({ url: 'https://g' }), true)).toBeNull();
    expect(secretRequired('webhook', f({ auth: 'hmac' }))).toBe(false);
    expect(secretRequired('webhook', f({ auth: 'bearer' }))).toBe(true);
  });
  it('checks the matrix room id', () => {
    expect(
      validateChannel('matrix', 'n', f({ homeserver: 'https://m', roomId: 'room' }), true),
    ).toMatch(/Room/);
  });
  it('validates smtp rules', () => {
    const ok = f({ host: 'mx', from: 'a@b', to: 'x@y' });
    expect(validateChannel('smtp', 'n', ok, false)).toBeNull();
    expect(validateChannel('smtp', 'n', { ...ok, security: 'none' }, false)).toMatch(/insecure/);
    expect(
      validateChannel('smtp', 'n', { ...ok, security: 'none', insecure: true }, false),
    ).toBeNull();
    expect(validateChannel('smtp', 'n', { ...ok, to: '' }, false)).toMatch(/recipient/);
    expect(validateChannel('smtp', 'n', { ...ok, to: 'nope' }, false)).toMatch(/recipient/);
    expect(validateChannel('smtp', 'n', { ...ok, port: '70000' }, false)).toMatch(/Port/);
    expect(validateChannel('smtp', 'n', ok, true)).toMatch(/Username/);
  });
  it('validates ntfy topic', () => {
    expect(validateChannel('ntfy', 'n', f({ url: 'https://n', topic: 'a/b' }), false)).toMatch(
      /Topic/,
    );
    expect(validateChannel('ntfy', 'n', f({ url: 'https://n', topic: 'ops' }), false)).toBeNull();
  });
});

describe('helpers', () => {
  it('parses recipients', () => {
    expect(parseRecipients('a@b; c@d\n e@f,')).toEqual(['a@b', 'c@d', 'e@f']);
  });
  it('toggles events in canonical order', () => {
    expect(toggleEvent(['run.drift'], 'run.failed')).toEqual(['run.failed', 'run.drift']);
    expect(toggleEvent(['run.failed'], 'run.failed')).toEqual([]);
  });
});

describe('channelsWithoutRules', () => {
  it('returns channels that no rule references', () => {
    const ch = (id: string) => ({ id }) as never;
    const rule = (channel_id: string) => ({ channel_id }) as never;
    expect(
      channelsWithoutRules([ch('a'), ch('b')], [rule('a')]).map((c: { id: string }) => c.id),
    ).toEqual(['b']);
  });
});
