import type { SurveyField } from './scan';

export type InputKind = 'text' | 'number' | 'checkbox' | 'select' | 'password' | 'textarea';

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
  if (f.choices && f.choices.length > 0) return 'select';
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
      depth,
      group: !!nested,
    });
    if (nested) out.push(...surveyToFormFields(f.fields ?? [], key, depth + 1));
  }
  return out;
}
