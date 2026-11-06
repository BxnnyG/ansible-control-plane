import { describe, expect, it } from 'vitest';
import {
  alertBadge,
  alertHost,
  alertLabels,
  ansibleClass,
  ansibleLabel,
  driftLabel,
  networkCountsLabel,
  onlineClass,
  onlineLabel,
  canAck,
  conflictDetails,
  conflictHosts,
  conflictLabel,
  fieldRows,
  severityClass,
  severityLabel,
  stateParam,
} from './fleet';

describe('fieldRows', () => {
  it('lists present fields with their sources and skips empty ones', () => {
    const rows = fieldRows({
      fields: {
        hostname: 'web1',
        ips: ['10.0.0.5', '10.0.0.6'],
        tags: [],
        sources: {
          hostname: [{ connector_id: 'a', connector: 'pve', type: 'proxmox' }],
          ips: [
            { connector_id: 'a', connector: 'pve', type: 'proxmox' },
            { connector_id: 'b', connector: 'fw', type: 'opnsense' },
            { connector_id: 'b', connector: 'fw', type: 'opnsense' },
          ],
        },
      },
    });
    expect(rows.map((r) => r.key)).toEqual(['hostname', 'ips']);
    expect(rows[1].values).toHaveLength(2);
    expect(rows[1].sources).toEqual(['pve', 'fw']);
  });

  it('returns no rows for empty fields', () => {
    expect(fieldRows({ fields: {} })).toEqual([]);
  });
});

describe('conflicts', () => {
  it('labels known and unknown types', () => {
    expect(conflictLabel('duplicate_ip')).toBe('Duplicate IP address');
    expect(conflictLabel('some_new_type')).toBe('some new type');
  });

  it('renders details as key/value lines', () => {
    expect(conflictDetails({ details: { ip: '10.0.0.1', hosts: ['a', 'b'] } })).toEqual([
      'ip: 10.0.0.1',
      'hosts: ["a","b"]',
    ]);
  });

  it('maps filter to API state and ack availability', () => {
    expect(stateParam('all')).toBeUndefined();
    expect(stateParam('open')).toBe('open');
    expect(canAck({ state: 'open' })).toBe(true);
    expect(canAck({ state: 'resolved' })).toBe(false);
  });
});

describe('conflictHosts', () => {
  it('uses host names and falls back to numbered labels', () => {
    expect(conflictHosts({ host_ids: ['a', 'b'], hosts: [{ id: 'a', name: 'web1' }] })).toEqual([
      { id: 'a', label: 'web1' },
      { id: 'b', label: 'host 2' },
    ]);
  });
});

describe('alert helpers', () => {
  it('formats the host badge', () => {
    expect(alertBadge({ open_alerts: 0, worst_severity: '' })).toBe('');
    expect(alertBadge({ open_alerts: 3, worst_severity: 'critical' })).toBe('3 critical');
    expect(alertBadge({})).toBe('');
  });

  it('labels and colours severities', () => {
    expect(severityLabel('warning')).toBe('Warning');
    expect(severityLabel('custom')).toBe('custom');
    expect(severityClass('critical')).toContain('red');
    expect(severityClass('warning')).toContain('amber');
    expect(severityClass('info')).toContain('muted');
  });

  it('picks the host cell text and sorts labels', () => {
    expect(alertHost({ host: 'web1', host_name: 'x', host_ip: 'y' })).toBe('web1');
    expect(alertHost({ host: '', host_name: 'x', host_ip: 'y' })).toBe('x');
    expect(alertHost({ host: '', host_name: '', host_ip: '192.0.2.1' })).toBe('192.0.2.1');
    expect(alertHost({ host: '', host_name: '', host_ip: '' })).toBe('-');
    expect(alertLabels({ labels: { b: '2', a: '1' } })).toEqual(['a=1', 'b=2']);
  });
});

describe('host status helpers', () => {
  it('labels and styles the online state', () => {
    expect(onlineLabel('online')).toBe('Online');
    expect(onlineLabel('weird')).toBe('weird');
    expect(onlineClass('online')).toContain('green');
    expect(onlineClass('offline')).toContain('red');
    expect(onlineClass('unknown')).toContain('text-muted');
  });

  it('describes the Ansible state', () => {
    const run = (result: 'ok' | 'failed', check = false) => ({
      run_id: 'r',
      template: 't',
      finished_at: '',
      result,
      check,
    });
    expect(ansibleLabel({ managed: false, last_run: null })).toBe('not managed');
    expect(ansibleLabel({ managed: true, last_run: null })).toBe('never run');
    expect(ansibleLabel({ managed: true, last_run: run('ok') })).toBe('ok');
    expect(ansibleLabel({ managed: true, last_run: run('ok', true) })).toBe('ok (check)');
    expect(ansibleClass({ managed: true, last_run: run('failed') })).toContain('red');
    expect(ansibleClass({ managed: true, last_run: run('ok') })).toContain('green');
    expect(ansibleClass({ managed: true, last_run: null })).toContain('text-muted');
  });

  it('labels drift and network counters', () => {
    expect(driftLabel({ drift: null })).toBe('');
    expect(driftLabel({ drift: { run_id: 'r', template: 't', finished_at: '', changed: 1 } })).toBe(
      'drift (1 change)',
    );
    expect(driftLabel({ drift: { run_id: 'r', template: 't', finished_at: '', changed: 3 } })).toBe(
      'drift (3 changes)',
    );
    expect(networkCountsLabel(undefined)).toBe('');
    expect(
      networkCountsLabel({ online: 4, ansible_ok: 3, failed: 1, never_managed: 2, open_alerts: 0 }),
    ).toBe('4 online, 3 ok, 1 failed, 2 never managed');
  });
});
