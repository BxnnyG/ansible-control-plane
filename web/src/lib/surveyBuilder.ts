/**
 * Survey field builder: converts between the stored survey shape (list of fields, or an
 * object with `fields`) and flat editable rows. Keys the builder does not edit (nested
 * `fields`, `elements`, unknown keys) are kept in `rest`, so a round trip loses nothing.
 */
import { surveyFields } from './templates';

export const SURVEY_TYPES = ['str', 'int', 'float', 'bool', 'list', 'dict', 'path', 'raw'] as const;

export interface BuilderRow {
  /** Stable key for keyed each-blocks; not persisted. */
  uid: number;
  name: string;
  type: string;
  required: boolean;
  secret: boolean;
  description: string;
  /** Default value as typed text ('' means none). */
  defaultText: string;
  /** One choice per line. */
  choicesText: string;
  rest: Record<string, unknown>;
}

export interface BuilderState {
  rows: BuilderRow[];
  /** Keys of an object-shaped survey besides `fields`; null for the list shape. */
  wrapper: Record<string, unknown> | null;
}

const HANDLED = new Set([
  'name',
  'type',
  'required',
  'secret',
  'description',
  'default',
  'choices',
]);
let counter = 0;

export function emptyRow(): BuilderRow {
  return {
    uid: ++counter,
    name: '',
    type: 'str',
    required: false,
    secret: false,
    description: '',
    defaultText: '',
    choicesText: '',
    rest: {},
  };
}

function valueText(v: unknown): string {
  if (v === undefined || v === null) return '';
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}

function textValue(type: string, text: string): { value?: unknown; error?: string } {
  switch (type) {
    case 'int': {
      const n = Number(text);
      return Number.isInteger(n) ? { value: n } : { error: 'must be an integer' };
    }
    case 'float': {
      const n = Number(text);
      return Number.isFinite(n) ? { value: n } : { error: 'must be a number' };
    }
    case 'bool':
      return text === 'true' || text === 'false'
        ? { value: text === 'true' }
        : { error: 'must be true or false' };
    case 'list':
    case 'dict':
    case 'raw':
      try {
        return { value: JSON.parse(text) as unknown };
      } catch {
        return { error: 'must be valid JSON' };
      }
    default:
      return { value: text };
  }
}

function choiceValue(type: string, line: string): unknown {
  if (type === 'int' || type === 'float') {
    const n = Number(line);
    return Number.isFinite(n) ? n : line;
  }
  if (type === 'str' || type === 'path') return line;
  try {
    return JSON.parse(line) as unknown;
  } catch {
    return line;
  }
}

/** Builds editable rows from a stored survey value (null, list or `{fields}` object). */
export function surveyToBuilder(survey: unknown): BuilderState {
  const isObj = !!survey && typeof survey === 'object' && !Array.isArray(survey);
  let wrapper: Record<string, unknown> | null = null;
  if (isObj) {
    const { fields: _fields, ...others } = survey as Record<string, unknown>;
    void _fields;
    wrapper = others;
  }
  const rows = surveyFields(survey).map((f): BuilderRow => {
    const rest: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(f)) if (!HANDLED.has(k)) rest[k] = v;
    return {
      uid: ++counter,
      name: f.name,
      type: f.type,
      required: !!f.required,
      secret: !!f.secret,
      description: f.description ?? '',
      defaultText: valueText(f.default),
      choicesText: (f.choices ?? []).map(valueText).join('\n'),
      rest,
    };
  });
  return { rows, wrapper };
}

export interface RowProblem {
  /** Index into the rows array. */
  row: number;
  message: string;
}

/** Converts rows back to the stored value; null when there are no rows. */
export function builderToSurvey(state: BuilderState): { value: unknown; problems: RowProblem[] } {
  const problems: RowProblem[] = [];
  const seen = new Set<string>();
  const fields = state.rows.map((r, i) => {
    const name = r.name.trim();
    if (!name) problems.push({ row: i, message: 'Field name is required.' });
    else if (seen.has(name)) problems.push({ row: i, message: `Duplicate field name ${name}.` });
    seen.add(name);
    if (!(SURVEY_TYPES as readonly string[]).includes(r.type)) {
      problems.push({ row: i, message: `${name || 'Field'} has an unknown type.` });
    }
    const f: Record<string, unknown> = { ...r.rest, name, type: r.type, required: r.required };
    if (r.secret) f.secret = true;
    if (r.description) f.description = r.description;
    if (r.defaultText !== '') {
      const d = textValue(r.type, r.defaultText);
      if (d.error) problems.push({ row: i, message: `${name || 'Field'} default ${d.error}.` });
      else f.default = d.value;
    }
    const lines = r.choicesText.split('\n').filter((l) => l.trim() !== '');
    if (lines.length > 0) f.choices = lines.map((l) => choiceValue(r.type, l));
    return f;
  });
  if (fields.length === 0) return { value: null, problems };
  return { value: state.wrapper ? { ...state.wrapper, fields } : fields, problems };
}

/** Pretty-printed JSON for the advanced editor; empty string for no survey. */
export function builderToText(state: BuilderState): { text: string; problems: RowProblem[] } {
  const { value, problems } = builderToSurvey(state);
  return { text: value === null ? '' : JSON.stringify(value, null, 2), problems };
}

/** Parses advanced-editor text into builder rows; error when it is not a valid survey. */
export function textToBuilder(text: string): { state: BuilderState | null; error: string | null } {
  if (!text.trim()) return { state: { rows: [], wrapper: null }, error: null };
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    return { state: null, error: 'Survey is not valid JSON.' };
  }
  const list = Array.isArray(v) ? v : (v as { fields?: unknown } | null)?.fields;
  if (!Array.isArray(list)) return { state: null, error: 'Survey must be a list of fields.' };
  if (
    list.some(
      (f) => !f || typeof f !== 'object' || typeof (f as { name?: unknown }).name !== 'string',
    )
  ) {
    return { state: null, error: 'Every survey field needs a name.' };
  }
  return { state: surveyToBuilder(v), error: null };
}

/** Removes default values of secret fields, for cloning a template without secret material. */
export function stripSecretDefaults(survey: unknown): unknown {
  const strip = (f: unknown): unknown => {
    if (!f || typeof f !== 'object') return f;
    const o = { ...(f as Record<string, unknown>) };
    if (o.secret) delete o.default;
    if (Array.isArray(o.fields)) o.fields = o.fields.map(strip);
    return o;
  };
  if (Array.isArray(survey)) return survey.map(strip);
  if (survey && typeof survey === 'object') {
    const o = { ...(survey as Record<string, unknown>) };
    if (Array.isArray(o.fields)) o.fields = o.fields.map(strip);
    return o;
  }
  return survey;
}
