/**
 * Wizard steps are a list so later milestones can add steps (connectors,
 * projects, ...) by appending an entry and a component.
 */
export interface SetupStepDef {
  id: string;
  title: string;
}

export const SETUP_STEPS: readonly SetupStepDef[] = [
  { id: 'workspace', title: 'Workspace' },
  { id: 'authentication', title: 'Authentication' },
  { id: 'summary', title: 'Summary' },
];

/** Clamps a step index into the valid range. */
export function clampStep(index: number, steps: readonly SetupStepDef[] = SETUP_STEPS): number {
  if (!Number.isFinite(index)) return 0;
  return Math.min(Math.max(Math.trunc(index), 0), steps.length - 1);
}
