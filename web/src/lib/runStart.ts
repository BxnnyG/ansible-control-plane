import type { RunOverrides, RunParams, Template } from './api';
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

/**
 * Overrides for "Run again": the old run's parameters, restricted to what the
 * template currently lets a caller override. `notCarried` names the settings that
 * could not be reused (override no longer allowed, or a secret value).
 */
export function overridesFromRun(
  t: Template,
  fields: readonly FormField[],
  params: RunParams,
): { overrides: RunOverrides; notCarried: string[] } {
  const ov: RunOverrides = {};
  const lost: string[] = [];
  const differs = (a: unknown, b: unknown) => a !== b;
  if (differs(params.limit, t.default_limit)) {
    if (t.allow_limit_override) ov.limit = params.limit;
    else lost.push('limit');
  }
  if (differs(params.tags, t.default_tags) || differs(params.skip_tags, t.default_skip_tags)) {
    if (t.allow_tags_override) {
      ov.tags = params.tags;
      ov.skip_tags = params.skip_tags;
    } else lost.push('tags');
  }
  if (differs(params.check, t.check_mode)) {
    if (t.allow_check_override) ov.check = params.check;
    else lost.push('check mode');
  }
  if (differs(params.diff, t.diff_mode)) {
    if (t.allow_diff_override) ov.diff = params.diff;
    else lost.push('diff mode');
  }
  if (differs(params.verbosity, t.verbosity)) {
    if (t.allow_verbosity_override) ov.verbosity = params.verbosity;
    else lost.push('verbosity');
  }
  // Variables: drop secret keys and everything the template already defines itself.
  const secretKeys = new Set(params.secret_vars ?? []);
  const own = t.extra_vars ?? {};
  const vars: Record<string, unknown> = {};
  let hadSecret = false;
  let hadVars = false;
  for (const [k, v] of Object.entries(params.extra_vars ?? {})) {
    if (secretKeys.has(k)) {
      hadSecret = true;
      continue;
    }
    if (JSON.stringify(own[k]) === JSON.stringify(v)) continue;
    hadVars = true;
    vars[k] = v;
  }
  if (hadVars) {
    if (t.allow_vars_override) ov.extra_vars = vars;
    else {
      // Only survey answers may be sent without the variable override.
      const { rest } = varsToSurvey(fields, vars);
      const kept: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(vars))
        if (JSON.stringify(rest[k]) !== JSON.stringify(v)) kept[k] = v;
      if (Object.keys(kept).length > 0) ov.extra_vars = kept;
      if (Object.keys(rest).length > 0) lost.push('variables');
    }
  }
  if (hadSecret) lost.push('secret variables (enter them again)');
  return { overrides: ov, notCarried: lost };
}
