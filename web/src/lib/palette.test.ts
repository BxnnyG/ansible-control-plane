import { describe, expect, it } from 'vitest';
import { GROUP_CAP, buildRows, scoreItem, scoreText, type PaletteItem } from './palette';

const item = (group: PaletteItem['group'], label: string, detail?: string): PaletteItem => ({
  group,
  key: `${group}:${label}`,
  label,
  detail,
  href: '/x',
});

describe('scoreText', () => {
  it('orders exact, prefix, word prefix, substring', () => {
    const s = ['web', 'web-01', 'my web', 'cobweb', 'db'].map((t) => scoreText(t, 'web'));
    expect(s[0]).toBeGreaterThan(s[1]);
    expect(s[1]).toBeGreaterThan(s[2]);
    expect(s[2]).toBeGreaterThan(s[3]);
    expect(s[4]).toBe(0);
  });
});

describe('scoreItem', () => {
  it('weighs the label above the detail', () => {
    expect(scoreItem(item('Hosts', 'alpha', 'beta'), 'beta')).toBeLessThan(
      scoreItem(item('Hosts', 'beta'), 'beta'),
    );
    expect(scoreItem(item('Hosts', 'alpha', 'beta'), 'beta')).toBeGreaterThan(0);
  });
});

describe('buildRows', () => {
  const items = [
    item('Hosts', 'deploy-host'),
    item('Templates', 'Deploy app', 'site.yml'),
    item('Pages', 'Deployments'),
    item('Projects', 'infra'),
  ];
  it('shows only pages for an empty query', () => {
    expect(buildRows(items, '  ').map((r) => r.item.group)).toEqual(['Pages']);
  });
  it('puts pages first and marks group starts', () => {
    const rows = buildRows(items, 'deploy');
    expect(rows.map((r) => r.item.group)).toEqual(['Pages', 'Templates', 'Hosts']);
    expect(rows.every((r) => r.first)).toBe(true);
  });
  it('ranks within a group and caps it', () => {
    const many = Array.from({ length: 20 }, (_, i) => item('Hosts', `node-${i}`));
    many.push(item('Hosts', 'node'));
    const rows = buildRows(many, 'node');
    expect(rows).toHaveLength(GROUP_CAP.Hosts);
    expect(rows[0].item.label).toBe('node');
    expect(rows.filter((r) => r.first)).toHaveLength(1);
  });
  it('returns nothing without a match', () => {
    expect(buildRows(items, 'zzz')).toEqual([]);
  });
});
