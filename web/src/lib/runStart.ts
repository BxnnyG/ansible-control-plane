import type { RunOverrides, Template } from './api';
import { surveyToVars, varsToSurvey, type FormField } from './survey';

export interface StartForm {
  limit: string;
  tags: string;
  skipTags: string;
  check: boolean;
  diff: boolean;
  verbosity: number;
  /** Raw extra vars JSON (only when the template allows var overrides). */
  extraVarsJson: string;
  /** Survey values by dotted key; checkboxes hold 'true'/'false'. */
  survey: Record<string, string>;
  /** Raw JSON object replacing the survey inputs ("advanced" mode); null when the form is used. */
  surveyRaw: string | null;
}

export function initialStartForm(t: Template, fields: readonly FormField[]): StartForm {
  const survey: Record<string, string> = {};
  for (const f of fields)
    if (!f.group) survey[f.key] = f.kind === 'checkbox' ? String(f.value === 'true') : f.value;
  return {
    limit: t.default_limit,
    tags: t.default_tags,
    skipTags: t.default_skip_tags,
    check: t.check_mode,
    diff: t.diff_mode,
    verbosity: t.verbosity,
    extraVarsJson: '',
    survey,
    surveyRaw: null,
  };
}

/** Form state for editing stored overrides (schedules) on top of the template defaults. */
export function startFormFromOverrides(
  t: Template,
  fields: readonly FormField[],
  ov: RunOverrides | null | undefined,
): StartForm {
  const form = initialStartForm(t, fields);
  if (!ov) return form;
  if (ov.limit !== undefined) form.limit = ov.limit;
  if (ov.tags !== undefined) form.tags = ov.tags;
  if (ov.skip_tags !== undefined) form.skipTags = ov.skip_tags;
  if (ov.check !== undefined) form.check = ov.check;
  if (ov.diff !== undefined) form.diff = ov.diff;
  if (ov.verbosity !== undefined) form.verbosity = ov.verbosity;
  if (ov.extra_vars && Object.keys(ov.extra_vars).length > 0) {
    const { survey, rest } = varsToSurvey(fields, ov.extra_vars);
    Object.assign(form.survey, survey);
    if (Object.keys(rest).length > 0) {
      if (t.allow_vars_override) form.extraVarsJson = JSON.stringify(rest, null, 2);
      else form.surveyRaw = JSON.stringify(ov.extra_vars, null, 2);
    }
  }
  return form;
}

/**
 * Builds the run request. Only overrides the template permits are sent, and only
 * where they differ from the template defaults. Returns validation errors instead
 * of a request when survey input is invalid.
 */
export function buildStartRequest(
  t: Template,
  fields: readonly FormField[],
  form: StartForm,
): { overrides: RunOverrides; errors: string[] } {
  const errors: string[] = [];
  const ov: RunOverrides = {};
  if (t.allow_limit_override && form.limit !== t.default_limit) ov.limit = form.limit;
  if (t.allow_tags_override) {
    if (form.tags !== t.default_tags) ov.tags = form.tags;
    if (form.skipTags !== t.default_skip_tags) ov.skip_tags = form.skipTags;
  }
  if (t.allow_check_override && form.check !== t.check_mode) ov.check = form.check;
  if (t.allow_diff_override && form.diff !== t.diff_mode) ov.diff = form.diff;
  if (t.allow_verbosity_override && form.verbosity !== t.verbosity) ov.verbosity = form.verbosity;

  const vars: Record<string, unknown> = {};
  if (t.allow_vars_override && form.extraVarsJson.trim()) {
    try {
      const v: unknown = JSON.parse(form.extraVarsJson);
      if (v === null || typeof v !== 'object' || Array.isArray(v)) throw new Error('not an object');
      Object.assign(vars, v);
    } catch {
      errors.push('Extra variables must be a JSON object.');
    }
  }
  if (form.surveyRaw !== null) {
    if (form.surveyRaw.trim()) {
      try {
        const v: unknown = JSON.parse(form.surveyRaw);
        if (v === null || typeof v !== 'object' || Array.isArray(v))
          throw new Error('not an object');
        Object.assign(vars, v);
      } catch {
        errors.push('Survey values must be a JSON object.');
      }
    }
  } else {
    const sv = surveyToVars(fields, form.survey);
    errors.push(...sv.errors);
    Object.assign(vars, sv.vars);
  }
  if (Object.keys(vars).length > 0) ov.extra_vars = vars;
  return { overrides: ov, errors };
}
