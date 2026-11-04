/** Scan result shape (ADR-0013), mirrors internal/scan; stored as ProjectRevision.scan. */

export type SurveyFieldType = 'str' | 'int' | 'float' | 'bool' | 'list' | 'dict' | 'path' | 'raw';

export interface SurveyField {
  name: string;
  type: SurveyFieldType | (string & {});
  required: boolean;
  secret?: boolean;
  default?: unknown;
  choices?: unknown[];
  description?: string;
  elements?: string;
  fields?: SurveyField[];
}

export interface Survey {
  role: string;
  entry_point: string;
  title?: string;
  description?: string;
  fields: SurveyField[];
}

export interface ScanResult {
  playbooks: { path: string; name?: string; plays: number; hosts: string[]; roles: string[] }[];
  roles: { name: string; path: string; entry_points: string[]; surveys: Survey[] }[];
  requirements: { file: string; kind: string; name: string; version?: string; src?: string }[];
  vars_files: { path: string; scope: string; name: string; vaulted: boolean }[];
  inventories: { path: string; format: string; source: string }[];
  vault_files: { path: string; version: string; vault_id?: string }[];
  inline_vault: { file: string; paths: string[] }[];
  warnings: { path: string; message: string }[];
}

/** Fills missing lists so the UI can iterate without null checks. */
export function normalizeScan(raw: Record<string, unknown> | null | undefined): ScanResult {
  const r = (raw ?? {}) as Partial<ScanResult>;
  return {
    playbooks: (r.playbooks ?? []).map((p) => ({
      ...p,
      hosts: p.hosts ?? [],
      roles: p.roles ?? [],
    })),
    roles: (r.roles ?? []).map((x) => ({
      ...x,
      entry_points: x.entry_points ?? [],
      surveys: (x.surveys ?? []).map((s) => ({ ...s, fields: s.fields ?? [] })),
    })),
    requirements: r.requirements ?? [],
    vars_files: r.vars_files ?? [],
    inventories: r.inventories ?? [],
    vault_files: r.vault_files ?? [],
    inline_vault: r.inline_vault ?? [],
    warnings: r.warnings ?? [],
  };
}
