import { ApiError, type GroupRule, type GroupRuleKind, type GroupRulePlan } from './api';

export interface KindDef {
  kind: GroupRuleKind;
  label: string;
  /** Key in `params`. */
  param: string;
  paramLabel: string;
  placeholder: string;
}

export const RULE_KINDS: readonly KindDef[] = [
  {
    kind: 'subnet',
    label: 'Subnet',
    param: 'cidr',
    paramLabel: 'CIDR',
    placeholder: '10.0.0.0/24',
  },
  {
    kind: 'proxmox_tag',
    label: 'Proxmox tag',
    param: 'tag',
    paramLabel: 'Tag',
    placeholder: 'webserver',
  },
  {
    kind: 'netbox_role',
    label: 'NetBox role',
    param: 'role',
    paramLabel: 'Role slug',
    placeholder: 'web-server',
  },
  {
    kind: 'netbox_site',
    label: 'NetBox site',
    param: 'site',
    paramLabel: 'Site slug',
    placeholder: 'site-a',
  },
  {
    kind: 'netbox_tag',
    label: 'NetBox tag',
    param: 'tag',
    paramLabel: 'Tag',
    placeholder: 'prod',
  },
];

export function kindDef(kind: GroupRuleKind): KindDef | undefined {
  return RULE_KINDS.find((k) => k.kind === kind);
}

export function kindLabel(kind: GroupRuleKind): string {
  return kindDef(kind)?.label ?? kind;
}

/** Params object for a kind and its single value; empty value yields no params. */
export function buildParams(kind: GroupRuleKind, value: string): Record<string, unknown> {
  const def = kindDef(kind);
  const v = value.trim();
  if (!def || !v) return {};
  return { [def.param]: v };
}

/** Reads the single parameter value of a rule back for the edit form. */
export function paramValue(rule: Pick<GroupRule, 'kind' | 'params'>): string {
  const def = kindDef(rule.kind);
  const v = def ? rule.params?.[def.param] : undefined;
  return typeof v === 'string' ? v : '';
}

/** Short description of a rule match, e.g. "Subnet 10.0.0.0/24". */
export function describeMatch(rule: Pick<GroupRule, 'kind' | 'params'>): string {
  const v = paramValue(rule);
  return v ? `${kindLabel(rule.kind)} ${v}` : kindLabel(rule.kind);
}

export interface PlanSummary {
  add: number;
  newHosts: number;
  remove: number;
  untrack: number;
  skipped: number;
  keep: number;
  /** True when applying would change nothing. */
  empty: boolean;
}

export function summarizePlan(plan: GroupRulePlan): PlanSummary {
  const add = plan.add?.length ?? 0;
  const newHosts = (plan.add ?? []).filter((a) => a.create_host).length;
  const remove = plan.remove?.length ?? 0;
  const untrack = plan.untrack?.length ?? 0;
  return {
    add,
    newHosts,
    remove,
    untrack,
    skipped: plan.skipped?.length ?? 0,
    keep: plan.keep ?? 0,
    empty: add === 0 && remove === 0 && !plan.group_create,
  };
}

/** One-line summary shown above the diff. */
export function planHeadline(plan: GroupRulePlan): string {
  const s = summarizePlan(plan);
  if (s.empty) return 'No changes: the group already matches the rule.';
  const parts = [`${s.add} to add`];
  if (s.newHosts > 0) parts.push(`${s.newHosts} new host${s.newHosts === 1 ? '' : 's'}`);
  if (s.remove > 0) parts.push(`${s.remove} to remove`);
  if (plan.group_create) parts.push('group will be created');
  return parts.join(', ');
}

/** User-facing text for the 409 errors of preview/apply. */
export function describeRuleError(e: unknown): string | null {
  if (!(e instanceof ApiError)) return null;
  switch (e.code) {
    case 'git_conflict':
      return `The inventory branch moved${e.paths.length ? ` (${e.paths.join(', ')})` : ''}. Re-import the inventory or resolve it upstream, then preview again.`;
    case 'rule_changed':
      return 'The rule was changed while applying. Reload and preview again.';
    case 'rule_disabled':
      return 'The rule is disabled. Enable it before applying.';
  }
  return null;
}
