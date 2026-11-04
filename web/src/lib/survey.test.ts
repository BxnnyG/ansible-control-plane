import { describe, expect, it } from 'vitest';
import { surveyToFormFields } from './survey';

describe('surveyToFormFields', () => {
  it('maps types to input kinds', () => {
    const f = surveyToFormFields([
      { name: 'a', type: 'str', required: true, default: 'x' },
      { name: 'b', type: 'int', required: false, default: 3 },
      { name: 'c', type: 'bool', required: false },
      { name: 'd', type: 'str', required: false, choices: ['p', 'q'] },
      { name: 'e', type: 'list', required: false, default: [1] },
    ]);
    expect(f.map((x) => x.kind)).toEqual(['text', 'number', 'checkbox', 'select', 'textarea']);
    expect(f[1].value).toBe('3');
    expect(f[3].choices).toEqual(['p', 'q']);
    expect(f[4].value).toBe('[1]');
  });

  it('hides secret defaults and uses password inputs', () => {
    const [f] = surveyToFormFields([
      { name: 's', type: 'str', required: true, secret: true, default: 'hunter2' },
    ]);
    expect(f.kind).toBe('password');
    expect(f.value).toBe('');
  });

  it('flattens nested dict options with dotted keys', () => {
    const f = surveyToFormFields([
      {
        name: 'cfg',
        type: 'dict',
        required: false,
        fields: [{ name: 'port', type: 'int', required: true }],
      },
    ]);
    expect(f.map((x) => [x.key, x.depth, x.group])).toEqual([
      ['cfg', 0, true],
      ['cfg.port', 1, false],
    ]);
  });
});
