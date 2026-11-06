import { describe, expect, it } from 'vitest';
import { ApiError, type GroupRulePlan } from './api';
import {
  buildParams,
  describeMatch,
  describeRuleError,
  paramValue,
  planHeadline,
  summarizePlan,
} from './groupRules';

const plan = (p: Partial<GroupRulePlan>): GroupRulePlan => ({
  group_create: false,
  matched: 0,
  add: [],
  remove: [],
  keep: 0,
  untrack: [],
  skipped: [],
  ...p,
});

describe('params', () => {
  it('builds and reads kind specific params', () => {
    expect(buildParams('subnet', ' 10.0.0.0/24 ')).toEqual({ cidr: '10.0.0.0/24' });
    expect(buildParams('proxmox_tag', 'web')).toEqual({ tag: 'web' });
    expect(buildParams('netbox_role', ' web ')).toEqual({ role: 'web' });
    expect(buildParams('netbox_site', 'a')).toEqual({ site: 'a' });
    expect(buildParams('subnet', '')).toEqual({});
    expect(paramValue({ kind: 'proxmox_tag', params: { tag: 'web' } })).toBe('web');
    expect(describeMatch({ kind: 'subnet', params: { cidr: '10.0.0.0/24' } })).toBe(
      'Subnet 10.0.0.0/24',
    );
  });
});

describe('plan summary', () => {
  it('counts additions, new hosts and removals', () => {
    const p = plan({
      add: [
        { name: 'a', create_host: true, fleet_host_id: '1' },
        { name: 'b', create_host: false, fleet_host_id: '2' },
      ],
      remove: ['c'],
    });
    expect(summarizePlan(p)).toMatchObject({ add: 2, newHosts: 1, remove: 1, empty: false });
    expect(planHeadline(p)).toBe('2 to add, 1 new host, 1 to remove');
  });

  it('reports an empty plan', () => {
    expect(summarizePlan(plan({})).empty).toBe(true);
    expect(planHeadline(plan({}))).toContain('No changes');
    expect(summarizePlan(plan({ group_create: true })).empty).toBe(false);
  });
});

describe('describeRuleError', () => {
  it('maps the 409 codes', () => {
    expect(describeRuleError(new ApiError(409, 'x', 'git_conflict', '', ['a.yml']))).toContain(
      'a.yml',
    );
    expect(describeRuleError(new ApiError(409, 'x', 'rule_changed'))).toContain('changed');
    expect(describeRuleError(new ApiError(409, 'x', 'rule_disabled'))).toContain('disabled');
    expect(describeRuleError(new ApiError(500, 'x', 'other'))).toBeNull();
    expect(describeRuleError(new Error('x'))).toBeNull();
  });
});
