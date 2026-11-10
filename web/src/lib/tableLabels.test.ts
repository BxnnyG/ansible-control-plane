import { describe, expect, it } from 'vitest';
import { rowLabels } from './tableLabels';

describe('rowLabels', () => {
  it('returns the headers when the cell count matches', () => {
    expect(rowLabels(['Name', 'Status'], 2)).toEqual(['Name', 'Status']);
  });

  it('returns null for colspan or conditional rows', () => {
    expect(rowLabels(['Name', 'Status'], 1)).toBeNull();
  });

  it('returns null without a header', () => {
    expect(rowLabels([], 0)).toBeNull();
  });
});
