import { describe, expect, it } from 'vitest';
import type { RunParams, Template } from './api';
import {
  buildStartRequest,
  initialStartForm,
  overridesFromRun,
  startFormFromOverrides,
} from './runStart';
import { surveyToFormFields } from './survey';

const tpl = {
  default_limit: '',
  default_tags: '',
  default_skip_tags: '',
  check_mode: false,
  diff_mode: false,
  verbosity: 0,
  allow_limit_override: true,
  allow_tags_override: true,
  allow_vars_override: true,
  allow_check_override: true,
  allow_diff_override: false,
  allow_verbosity_override: false,
} as Template;
const fields = surveyToFormFields([{ name: 'pkg', type: 'str', required: false }]);

describe('startFormFromOverrides / buildStartRequest', () => {
  it('round-trips stored overrides', () => {
    const ov = { limit: 'web', check: true, extra_vars: { pkg: 'nginx', other: 1 } };
    const form = startFormFromOverrides(tpl, fields, ov);
    expect(form.survey.pkg).toBe('nginx');
    expect(JSON.parse(form.extraVarsJson)).toEqual({ other: 1 });
    const built = buildStartRequest(tpl, fields, form);
    expect(built.errors).toEqual([]);
    expect(built.overrides).toEqual(ov);
  });

  it('uses raw survey JSON when set and reports invalid JSON', () => {
    const form = initialStartForm(tpl, fields);
    form.surveyRaw = '{"pkg":"x"}';
    expect(buildStartRequest(tpl, fields, form).overrides.extra_vars).toEqual({ pkg: 'x' });
    form.surveyRaw = '[1]';
    expect(buildStartRequest(tpl, fields, form).errors).toHaveLength(1);
  });

  it('keeps unknown vars in raw mode when vars override is not allowed', () => {
    const t = { ...tpl, allow_vars_override: false } as Template;
    const form = startFormFromOverrides(t, fields, { extra_vars: { other: 1 } });
    expect(form.surveyRaw).not.toBeNull();
  });
});

describe('overridesFromRun', () => {
  const base = { ...tpl, extra_vars: { keep: 1 }, survey: null } as unknown as Template;
  const params = (p: Partial<RunParams>): RunParams =>
    ({
      limit: '',
      tags: '',
      skip_tags: '',
      check: false,
      diff: false,
      verbosity: 0,
      extra_vars: { keep: 1 },
      ...p,
    }) as RunParams;

  it('returns nothing for a run with default settings', () => {
    expect(overridesFromRun(base, fields, params({}))).toEqual({ overrides: {}, notCarried: [] });
  });
  it('carries allowed overrides', () => {
    const r = overridesFromRun(base, fields, params({ limit: 'web', check: true, tags: 'a' }));
    expect(r.overrides).toEqual({ limit: 'web', check: true, tags: 'a', skip_tags: '' });
    expect(r.notCarried).toEqual([]);
  });
  it('reports overrides the template no longer allows', () => {
    const r = overridesFromRun(base, fields, params({ diff: true, verbosity: 2 }));
    expect(r.overrides).toEqual({});
    expect(r.notCarried).toEqual(['diff mode', 'verbosity']);
  });
  it('never carries secret variables and ignores template-defined ones', () => {
    const r = overridesFromRun(
      base,
      fields,
      params({ extra_vars: { keep: 1, pkg: 'nginx', token: 'x' }, secret_vars: ['token'] }),
    );
    expect(r.overrides.extra_vars).toEqual({ pkg: 'nginx' });
    expect(r.notCarried).toEqual(['secret variables (enter them again)']);
  });
  it('limits variables to survey answers without the variable override', () => {
    const t = { ...base, allow_vars_override: false } as Template;
    const r = overridesFromRun(t, fields, params({ extra_vars: { keep: 1, pkg: 'a', other: 2 } }));
    expect(r.overrides.extra_vars).toEqual({ pkg: 'a' });
    expect(r.notCarried).toEqual(['variables']);
  });
});
