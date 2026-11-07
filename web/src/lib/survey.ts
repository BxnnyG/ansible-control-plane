import type { SurveyField } from './scan';

export type InputKind =
  'text' | 'number' | 'checkbox' | 'select' | 'multiselect' | 'password' | 'textarea';

export interface FormField {
  /** Dotted path, unique within the form. */
  key: string;
  label: string;
  kind: InputKind;
  /** Original survey type (str, int, bool, list, ...). */
  type: string;
  required: boolean;
  description: string;
  /** String form of the default value ('' if none). */
  value: string;
  choices: string[];
  /** Element type of list fields (str, int, ...); '' otherwise. */
  elements: string;
  depth: number;
  /** True for dict group headings (no input). */
  group: boolean;
}

function stringify(v: unknown): string {
  if (v === undefined || v === null) return '';
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}

function kindOf(f: SurveyField): InputKind {
  if (f.secret) return 'password';
  if (f.choices && f.choices.length > 0) return f.type === 'list' ? 'multiselect' : 'select';
  switch (f.type) {
    case 'int':
    case 'float':
      return 'number';
    case 'bool':
      return 'checkbox';
    case 'list':
    case 'dict':
    case 'raw':
      return 'textarea';
    default:
      return 'text';
  }
}

/** Flattens survey fields into render-ready descriptors; dicts with sub-fields become groups. */
export function surveyToFormFields(
  fields: readonly SurveyField[],
  prefix = '',
  depth = 0,
): FormField[] {
  const out: FormField[] = [];
  for (const f of fields) {
    const key = prefix ? `${prefix}.${f.name}` : f.name;
    const nested = f.type === 'dict' && f.fields && f.fields.length > 0;
    out.push({
      key,
      label: f.name,
      kind: nested ? 'text' : kindOf(f),
      type: f.type,
      required: f.required,
      description: f.description ?? '',
      // Secret defaults are never rendered.
      value: f.secret ? '' : stringify(f.default),
      choices: (f.choices ?? []).map(stringify),
      elements: f.elements ?? '',
      depth,
      group: !!nested,
    });
    if (nested) out.push(...surveyToFormFields(f.fields ?? [], key, depth + 1));
  }
  return out;
}

/** Parses a stored multi-select value (JSON array) into the selected choice strings. */
export function selectedChoices(raw: string): string[] {
  try {
    const v: unknown = JSON.parse(raw);
    return Array.isArray(v) ? v.map((x) => String(x)) : [];
  } catch {
    return [];
  }
}

/** Returns the stored multi-select value with one choice toggled, keeping choice order. */
export function toggleChoice(
  raw: string,
  choices: readonly string[],
  choice: string,
  on: boolean,
): string {
  const cur = new Set(selectedChoices(raw));
  if (on) cur.add(choice);
  else cur.delete(choice);
  return JSON.stringify(choices.filter((c) => cur.has(c)));
}

/** Converts the raw string held by the form into the typed value for the API. */
export function coerceField(f: FormField, raw: string): { value?: unknown; error?: string } {
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
        const v = JSON.parse(raw) as unknown;
        if (f.kind === 'multiselect' && Array.isArray(v)) {
          const num = f.elements === 'int' || f.elements === 'float';
          return { value: v.map((x) => (num ? Number(x) : String(x))) };
        }
        return { value: v };
      } catch {
        return { error: `${f.label} must be valid JSON.` };
      }
    default:
      return { value: raw };
  }
}

export function setPath(target: Record<string, unknown>, key: string, value: unknown): void {
  const parts = key.split('.');
  let cur = target;
  for (const p of parts.slice(0, -1)) {
    const next = cur[p];
    if (next === null || typeof next !== 'object' || Array.isArray(next)) cur[p] = {};
    cur = cur[p] as Record<string, unknown>;
  }
  cur[parts[parts.length - 1]] = value;
}

function isObject(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

/** Builds the survey variables from form values; collects validation errors. */
export function surveyToVars(
  fields: readonly FormField[],
  survey: Record<string, string>,
): { vars: Record<string, unknown>; errors: string[] } {
  const vars: Record<string, unknown> = {};
  const errors: string[] = [];
  for (const f of fields) {
    if (f.group) continue;
    const raw = String(survey[f.key] ?? '');
    if (raw === '' && f.kind !== 'checkbox') {
      if (f.required) errors.push(`${f.label} is required.`);
      continue;
    }
    const c = coerceField(f, raw);
    if (c.error) errors.push(c.error);
    else setPath(vars, f.key, c.value);
  }
  return { vars, errors };
}

/**
 * Splits stored variables into form values for the survey fields and the
 * remainder (keys the survey does not know).
 */
export function varsToSurvey(
  fields: readonly FormField[],
  vars: Record<string, unknown>,
): { survey: Record<string, string>; rest: Record<string, unknown> } {
  const rest = structuredClone(vars);
  const survey: Record<string, string> = {};
  for (const f of fields) {
    if (f.group) continue;
    const parts = f.key.split('.');
    let cur: unknown = vars;
    for (const p of parts) cur = isObject(cur) ? cur[p] : undefined;
    if (cur === undefined) continue;
    survey[f.key] = typeof cur === 'object' && cur !== null ? JSON.stringify(cur) : String(cur);
    // Remove the consumed key (and parents that become empty) from the remainder.
    const chain: Record<string, unknown>[] = [rest];
    for (const p of parts.slice(0, -1)) {
      const n = chain[chain.length - 1][p];
      if (!isObject(n)) break;
      chain.push(n);
    }
    if (chain.length === parts.length) {
      delete chain[chain.length - 1][parts[parts.length - 1]];
      for (let i = chain.length - 1; i > 0; i--)
        if (Object.keys(chain[i]).length === 0) delete chain[i - 1][parts[i - 1]];
    }
  }
  return { survey, rest };
}
