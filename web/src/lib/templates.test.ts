import { describe, expect, it } from 'vitest';
import type { Template } from './api';
import { buildStartRequest, initialStartForm } from './runStart';
import { surveyToFormFields } from './survey';
import {
  formatEnvLines,
  parseEnvLines,
  parseJsonObject,
  parseSurveyText,
  surveyFields,
} from './templates';

describe('env lines', () => {
  it('parses and round-trips', () => {
    const r = parseEnvLines('# c\nA=1\n\nB=x=y');
    expect(r).toEqual({ vars: { A: '1', B: 'x=y' }, error: null });
    expect(formatEnvLines(r.vars)).toBe('A=1\nB=x=y');
  });
  it('reports the failing line', () => {
    expect(parseEnvLines('A=1\n1bad=2').error).toMatch(/Line 2/);
    expect(parseEnvLines('novalue').error).toMatch(/Line 1/);
  });
});

describe('json and survey parsing', () => {
  it('parses objects only', () => {
    expect(parseJsonObject('').value).toEqual({});
    expect(parseJsonObject('[1]').error).toBeTruthy();
    expect(parseJsonObject('{').error).toBeTruthy();
  });
  it('validates survey text', () => {
    expect(parseSurveyText('').value).toBeNull();
    expect(parseSurveyText('{').error).toBeTruthy();
    expect(parseSurveyText('{"a":1}').error).toBeTruthy();
    expect(parseSurveyText('[{"name":"x"}]').error).toBeTruthy();
    expect(parseSurveyText('{"fields":[{"name":"x","type":"str"}]}').error).toBeNull();
  });
  it('extracts fields from both shapes', () => {
    expect(surveyFields([{ name: 'a', type: 'str', required: false }])).toHaveLength(1);
    expect(surveyFields({ fields: [{ name: 'a' }, 3] })).toHaveLength(1);
    expect(surveyFields(null)).toEqual([]);
  });
});

const tpl = {
  default_limit: 'web',
  default_tags: '',
  default_skip_tags: '',
  verbosity: 0,
  check_mode: false,
  diff_mode: false,
  allow_limit_override: true,
  allow_tags_override: false,
  allow_vars_override: false,
  allow_check_override: true,
  allow_diff_override: false,
  allow_verbosity_override: false,
} as Template;

describe('buildStartRequest', () => {
  const fields = surveyToFormFields([
    { name: 'port', type: 'int', required: true, default: 80 },
    { name: 'token', type: 'str', required: false, secret: true },
    { name: 'dry', type: 'bool', required: false },
    {
      name: 'cfg',
      type: 'dict',
      required: false,
      fields: [{ name: 'a', type: 'str', required: false }],
    },
  ]);

  it('sends only permitted, changed overrides and builds nested vars', () => {
    const form = initialStartForm(tpl, fields);
    form.limit = 'db';
    form.tags = 'x'; // not allowed: dropped
    form.check = true;
    form.survey['cfg.a'] = 'v';
    const { overrides, errors } = buildStartRequest(tpl, fields, form);
    expect(errors).toEqual([]);
    expect(overrides).toEqual({
      limit: 'db',
      check: true,
      extra_vars: { port: 80, dry: false, cfg: { a: 'v' } },
    });
  });

  it('omits empty optional (secret) fields and flags missing required ones', () => {
    const form = initialStartForm(tpl, fields);
    form.survey.port = '';
    const { errors } = buildStartRequest(tpl, fields, form);
    expect(errors).toEqual(['port is required.']);
  });

  it('passes secret values through and validates numbers', () => {
    const form = initialStartForm(tpl, fields);
    form.survey.token = 's3cret';
    form.survey.port = '8x';
    const r = buildStartRequest(tpl, fields, form);
    expect(r.errors).toEqual(['port must be an integer.']);
    form.survey.port = '8080';
    expect(buildStartRequest(tpl, fields, form).overrides.extra_vars).toMatchObject({
      port: 8080,
      token: 's3cret',
    });
  });

  it('merges free-form extra vars only when allowed', () => {
    const t = { ...tpl, allow_vars_override: true } as Template;
    const form = initialStartForm(t, []);
    form.extraVarsJson = '{"x":1}';
    expect(buildStartRequest(t, [], form).overrides.extra_vars).toEqual({ x: 1 });
    form.extraVarsJson = '[1]';
    expect(buildStartRequest(t, [], form).errors).toHaveLength(1);
    expect(
      buildStartRequest(tpl, [], { ...form, extraVarsJson: '{"x":1}' }).overrides.extra_vars,
    ).toBeUndefined();
  });
});
