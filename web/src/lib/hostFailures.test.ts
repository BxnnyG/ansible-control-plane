import { describe, expect, it } from 'vitest';
import { extractHostFailures } from './hostFailures';

describe('extractHostFailures', () => {
  it('prefers module_stderr over msg for JSON payloads', () => {
    const m = extractHostFailures([
      'TASK [x]',
      'fatal: [web1.example.test]: FAILED! => {"changed": false, "msg": "MODULE FAILURE", "module_stderr": "Traceback: boom"}',
    ]);
    expect(m.get('web1.example.test')).toEqual({ message: 'Traceback: boom', line: 1 });
  });

  it('uses msg for unreachable hosts and strips the delegate', () => {
    const m = extractHostFailures([
      'fatal: [db1 -> 192.0.2.5]: UNREACHABLE! => {"msg": "Failed to connect", "unreachable": true}',
    ]);
    expect(m.get('db1')?.message).toBe('Failed to connect');
  });

  it('reads indented yaml callback output', () => {
    const m = extractHostFailures([
      'fatal: [h1]: FAILED! => changed=false',
      '  cmd: foo',
      '  msg: non-zero return code',
    ]);
    expect(m.get('h1')?.message).toBe('non-zero return code');
  });

  it('falls back to the raw text, keeps the first failure and truncates', () => {
    const long = 'x'.repeat(500);
    const m = extractHostFailures([
      `fatal: [h2]: FAILED! => not json ${long}`,
      'fatal: [h2]: FAILED! => {"msg": "later"}',
    ]);
    const f = m.get('h2');
    expect(f?.line).toBe(0);
    expect(f?.message.length).toBe(240);
    expect(f?.message.endsWith('...')).toBe(true);
  });

  it('returns nothing when there are no failures', () => {
    expect(extractHostFailures(['ok: [h1]', 'PLAY RECAP']).size).toBe(0);
  });
});
