import { describe, expect, it } from 'vitest';
import {
  canAck,
  conflictDetails,
  conflictHosts,
  conflictLabel,
  fieldRows,
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
