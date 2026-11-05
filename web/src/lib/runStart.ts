import type { RunOverrides, Template } from './api';
import type { FormField } from './survey';

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
  };
}

function coerce(f: FormField, raw: string): { value?: unknown; error?: string } {
  switch (f.type) {
    case 'int': {
      const n = Number(raw);
      return Number.isInteger(n) ? { value: n } : { error: `${f.label} must be an integer.` };
    }
    case 'float': {
      const n = Number(raw);
      return Number.isFinite(n) ? { value: n } : { error: `${f.label} must be a number.` };
    }
    case 'bool':
      return { value: raw === 'true' };
    case 'list':
    case 'dict':
    case 'raw':
      try {
        return { value: JSON.parse(raw) as unknown };
      } catch {
        return { error: `${f.label} must be valid JSON.` };
      }
    default:
      return { value: raw };
  }
}

function setPath(target: Record<string, unknown>, key: string, value: unknown): void {
  const parts = key.split('.');
  let cur = target;
  for (const p of parts.slice(0, -1)) {
    const next = cur[p];
    if (next === null || typeof next !== 'object' || Array.isArray(next)) cur[p] = {};
    cur = cur[p] as Record<string, unknown>;
  }
  cur[parts[parts.length - 1]] = value;
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
  for (const f of fields) {
    if (f.group) continue;
    const raw = String(form.survey[f.key] ?? '');
    if (raw === '' && f.kind !== 'checkbox') {
      if (f.required) errors.push(`${f.label} is required.`);
      continue;
    }
    const c = coerce(f, raw);
    if (c.error) errors.push(c.error);
    else setPath(vars, f.key, c.value);
  }
  if (Object.keys(vars).length > 0) ov.extra_vars = vars;
  return { overrides: ov, errors };
}
