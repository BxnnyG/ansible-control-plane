import type { SurveyField } from './scan';

/** Parses `KEY=value` lines into a map; blank lines and `#` comments are skipped. */
export function parseEnvLines(text: string): {
  vars: Record<string, string>;
  error: string | null;
} {
  const vars: Record<string, string> = {};
  const lines = text.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i].trim();
    if (!line || line.startsWith('#')) continue;
    const eq = line.indexOf('=');
    const key = eq > 0 ? line.slice(0, eq).trim() : '';
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) {
      return { vars, error: `Line ${i + 1}: expected KEY=value with a valid variable name.` };
    }
    vars[key] = line.slice(eq + 1);
  }
  return { vars, error: null };
}

export function formatEnvLines(vars: Record<string, unknown> | null | undefined): string {
  return Object.entries(vars ?? {})
    .map(([k, v]) => `${k}=${typeof v === 'string' ? v : JSON.stringify(v)}`)
    .join('\n');
}

/** Parses a JSON object (extra vars); empty text is an empty object. */
export function parseJsonObject(text: string): {
  value: Record<string, unknown>;
  error: string | null;
} {
  if (!text.trim()) return { value: {}, error: null };
  try {
    const v: unknown = JSON.parse(text);
    if (v === null || typeof v !== 'object' || Array.isArray(v)) {
      return { value: {}, error: 'Expected a JSON object.' };
    }
    return { value: v as Record<string, unknown>, error: null };
  } catch {
    return { value: {}, error: 'Invalid JSON.' };
  }
}

/** Survey fields of a template survey (list or `{fields}`); empty when absent or malformed. */
export function surveyFields(survey: unknown): SurveyField[] {
  const list = Array.isArray(survey)
    ? survey
    : survey && typeof survey === 'object' && Array.isArray((survey as { fields?: unknown }).fields)
      ? (survey as { fields: unknown[] }).fields
      : [];
  return list.filter(
    (f): f is SurveyField =>
      !!f && typeof f === 'object' && typeof (f as SurveyField).name === 'string',
  );
}

/**
 * Parses survey JSON typed into the template form. Empty text means no survey
 * (null); otherwise it must be a list of fields or an object with `fields`.
 */
export function parseSurveyText(text: string): { value: unknown; error: string | null } {
  if (!text.trim()) return { value: null, error: null };
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return { value: null, error: 'Survey is not valid JSON.' };
  }
  const list = Array.isArray(v) ? v : (v as { fields?: unknown } | null)?.fields;
  if (!Array.isArray(list)) return { value: null, error: 'Survey must be a list of fields.' };
  for (const f of list) {
    const ok =
      f && typeof f === 'object' && typeof f.name === 'string' && typeof f.type === 'string';
    if (!ok) return { value: null, error: 'Every survey field needs a name and a type.' };
  }
  return { value: v, error: null };
}
