import { describe, expect, it } from 'vitest';
import {
  coerceField,
  selectedChoices,
  surveyToFormFields,
  surveyToVars,
  toggleChoice,
  varsToSurvey,
} from './survey';

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

describe('multiselect and coercion', () => {
  const [ms] = surveyToFormFields([
    { name: 'n', type: 'list', required: false, elements: 'int', choices: [1, 2, 3] },
  ]);

  it('maps list fields with choices to multiselect', () => {
    expect(ms.kind).toBe('multiselect');
    expect(ms.choices).toEqual(['1', '2', '3']);
  });

  it('toggles choices keeping choice order', () => {
    let v = toggleChoice('', ms.choices, '3', true);
    v = toggleChoice(v, ms.choices, '1', true);
    expect(v).toBe('["1","3"]');
    expect(selectedChoices(v)).toEqual(['1', '3']);
    expect(toggleChoice(v, ms.choices, '1', false)).toBe('["3"]');
    expect(selectedChoices('nonsense')).toEqual([]);
  });

  it('coerces by type', () => {
    expect(coerceField(ms, '["1","3"]').value).toEqual([1, 3]);
    const [i, fl, b, l] = surveyToFormFields([
      { name: 'i', type: 'int', required: false },
      { name: 'f', type: 'float', required: false },
      { name: 'b', type: 'bool', required: false },
      { name: 'l', type: 'list', required: false },
    ]);
    expect(coerceField(i, '4').value).toBe(4);
    expect(coerceField(i, '4.5').error).toMatch(/integer/);
    expect(coerceField(fl, '1.5').value).toBe(1.5);
    expect(coerceField(fl, 'x').error).toMatch(/number/);
    expect(coerceField(b, 'true').value).toBe(true);
    expect(coerceField(l, '[1').error).toMatch(/JSON/);
  });
});

describe('surveyToVars / varsToSurvey', () => {
  const fields = surveyToFormFields([
    { name: 'name', type: 'str', required: true },
    {
      name: 'cfg',
      type: 'dict',
      required: false,
      fields: [{ name: 'port', type: 'int', required: false }],
    },
  ]);

  it('reports missing required fields and builds nested vars', () => {
    expect(surveyToVars(fields, { name: '', 'cfg.port': '' }).errors).toEqual([
      'name is required.',
    ]);
    expect(surveyToVars(fields, { name: 'a', 'cfg.port': '80' }).vars).toEqual({
      name: 'a',
      cfg: { port: 80 },
    });
  });

  it('splits stored vars into form values and remainder', () => {
    const r = varsToSurvey(fields, { name: 'a', cfg: { port: 80 }, other: 1 });
    expect(r.survey).toEqual({ name: 'a', 'cfg.port': '80' });
    expect(r.rest).toEqual({ other: 1 });
  });
});
