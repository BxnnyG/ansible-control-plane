import { describe, expect, it } from 'vitest';
import type { Template } from './api';
import { buildStartRequest, initialStartForm, startFormFromOverrides } from './runStart';
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
