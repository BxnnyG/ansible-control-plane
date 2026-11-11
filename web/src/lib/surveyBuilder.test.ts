import { describe, expect, it } from 'vitest';
import {
  builderToSurvey,
  builderToText,
  emptyRow,
  stripSecretDefaults,
  surveyToBuilder,
  textToBuilder,
} from './surveyBuilder';

const list = [
  { name: 'version', type: 'str', required: true, description: 'Release', default: '1.0' },
  { name: 'count', type: 'int', required: false, default: 3, choices: [1, 2, 3] },
  { name: 'tags', type: 'list', required: false, default: ['a', 'b'], elements: 'str' },
  {
    name: 'cfg',
    type: 'dict',
    required: false,
    fields: [{ name: 'port', type: 'int', required: true }],
    custom: 'kept',
  },
  { name: 'token', type: 'str', required: true, secret: true },
  { name: 'dry', type: 'bool', required: false, default: false },
];

describe('survey builder round trip', () => {
  it('keeps a list survey intact', () => {
    const { value, problems } = builderToSurvey(surveyToBuilder(list));
    expect(problems).toEqual([]);
    expect(value).toEqual(list);
  });
  it('keeps the object shape and its other keys', () => {
    const obj = { role: 'web', entry_point: 'main', fields: list };
    const { value } = builderToSurvey(surveyToBuilder(obj));
    expect(value).toEqual(obj);
  });
  it('survives builder -> JSON text -> builder', () => {
    const text = builderToText(surveyToBuilder(list)).text;
    const back = textToBuilder(text);
    expect(back.error).toBeNull();
    expect(builderToSurvey(back.state!).value).toEqual(list);
  });
  it('maps no survey to null and back', () => {
    expect(builderToSurvey(surveyToBuilder(null)).value).toBeNull();
    expect(textToBuilder('').state?.rows).toEqual([]);
  });
});

describe('builder validation', () => {
  it('reports missing and duplicate names and bad defaults', () => {
    const a = emptyRow();
    const b = { ...emptyRow(), name: 'x', type: 'int', defaultText: 'abc' };
    const c = { ...emptyRow(), name: 'x' };
    const { problems } = builderToSurvey({ rows: [a, b, c], wrapper: null });
    expect(problems.map((p) => p.row)).toEqual([0, 1, 2]);
  });
  it('rejects text that is not a survey', () => {
    expect(textToBuilder('{').error).toMatch(/valid JSON/);
    expect(textToBuilder('{"a":1}').error).toMatch(/list/);
    expect(textToBuilder('[{"type":"str"}]').error).toMatch(/name/);
  });
});

describe('stripSecretDefaults', () => {
  it('drops defaults of secret fields only, also nested', () => {
    const out = stripSecretDefaults([
      { name: 'a', type: 'str', secret: true, default: 'x' },
      { name: 'b', type: 'str', default: 'y' },
      { name: 'c', type: 'dict', fields: [{ name: 'd', type: 'str', secret: true, default: 'z' }] },
    ]) as Record<string, unknown>[];
    expect(out[0]).not.toHaveProperty('default');
    expect(out[1].default).toBe('y');
    expect((out[2].fields as Record<string, unknown>[])[0]).not.toHaveProperty('default');
  });
});
