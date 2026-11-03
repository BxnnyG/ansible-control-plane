import { describe, expect, it } from 'vitest';
import { SETUP_STEPS, clampStep } from './steps';

describe('clampStep', () => {
  it('keeps valid indices', () => {
    expect(clampStep(1)).toBe(1);
  });
  it('clamps out-of-range and invalid values', () => {
    expect(clampStep(-3)).toBe(0);
    expect(clampStep(99)).toBe(SETUP_STEPS.length - 1);
    expect(clampStep(Number.NaN)).toBe(0);
  });
});
