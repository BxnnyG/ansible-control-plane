import { ApiError, type RemediationRef, type RemediationRule, type RemediationTarget } from './api';

export const TARGETS: readonly { value: RemediationTarget; label: string }[] = [
  { value: 'host', label: 'The alert host' },
  { value: 'group', label: 'An inventory group (must contain the host)' },
  { value: 'network', label: 'All hosts of the same network' },
];

type Target = Pick<RemediationRule | RemediationRef, 'target' | 'group'>;

/** Short description of the limit a rule uses, e.g. "group web". */
export function describeTarget(r: Target): string {
  switch (r.target) {
    case 'group':
      return `group ${r.group}`;
    case 'network':
      return 'network of the host';
    default:
      return 'host';
  }
}

/** Button caption of a rule: check-first rules start with a dry run. */
export function fixLabel(r: Pick<RemediationRef, 'name' | 'check_first'>): string {
  return r.check_first ? `Check: ${r.name}` : `Fix: ${r.name}`;
}

/** Error text for the 4xx answers of the remediation endpoints. */
export function describeRemediationError(e: unknown): string | null {
  if (!(e instanceof ApiError)) return null;
  switch (e.code) {
    case 'rule_disabled':
      return 'This rule is disabled.';
    case 'not_applicable':
      return 'The rule no longer applies to this alert (resolved or changed).';
    case 'hosts_not_in_inventory':
      return e.message;
  }
  if (e.status === 403) {
    return 'You may not run this on all affected hosts (outside your scope).';
  }
  if (e.status === 422 && e.message) return e.message;
  return null;
}

/** Pure helper: rules applicable to an alert, from the batch endpoint answer. */
export function rulesFor(
  map: Record<string, RemediationRef[]> | null | undefined,
  alertId: string,
): RemediationRef[] {
  return map?.[alertId] ?? [];
}
