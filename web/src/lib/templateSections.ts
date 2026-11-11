/** Sections of the template form and the validation that points at the section with the problem. */

export type SectionId = 'basics' | 'source' | 'credentials' | 'variables' | 'survey' | 'options';

export const SECTIONS: { id: SectionId; title: string; hint: string }[] = [
  { id: 'basics', title: 'Basics', hint: 'Name and description' },
  { id: 'source', title: 'Source', hint: 'Project, playbook and inventory' },
  { id: 'credentials', title: 'Credentials', hint: 'SSH key, become password, vault identities' },
  { id: 'variables', title: 'Variables', hint: 'Environment and extra variables' },
  { id: 'survey', title: 'Survey', hint: 'Questions asked at run time' },
  { id: 'options', title: 'Options', hint: 'Defaults, concurrency and run-time overrides' },
];

export interface FormProblem {
  section: SectionId;
  /** Element id to focus. */
  field: string;
  message: string;
}

export interface SectionInput {
  name: string;
  projectId: string;
  playbook: string;
  inventoryId: string;
  maxConcurrent: number;
  envError: string | null;
  extraVarsError: string | null;
  surveyError: string | null;
}

/** First problem in form order, or null when the form can be submitted. */
export function firstProblem(v: SectionInput): FormProblem | null {
  if (!v.name.trim()) return { section: 'basics', field: 't-name', message: 'Name is required.' };
  if (!v.projectId) return { section: 'source', field: 't-project', message: 'Select a project.' };
  if (!v.playbook) return { section: 'source', field: 't-playbook', message: 'Select a playbook.' };
  if (!v.inventoryId) {
    return { section: 'source', field: 't-inv', message: 'Select an inventory.' };
  }
  if (v.envError) {
    return { section: 'variables', field: 't-env', message: `Environment: ${v.envError}` };
  }
  if (v.extraVarsError) {
    return {
      section: 'variables',
      field: 't-vars',
      message: `Extra variables: ${v.extraVarsError}`,
    };
  }
  if (v.surveyError) return { section: 'survey', field: 'survey-section', message: v.surveyError };
  if (!Number.isInteger(v.maxConcurrent) || v.maxConcurrent < 1) {
    return {
      section: 'options',
      field: 't-max',
      message: 'Max. concurrent runs must be at least 1.',
    };
  }
  return null;
}

/** Sections that currently hold a problem; used to mark section headers. */
export function sectionsWithProblems(problem: FormProblem | null): Set<SectionId> {
  return new Set(problem ? [problem.section] : []);
}

/** Name proposed for a clone. */
export function cloneName(name: string): string {
  return `Copy of ${name}`;
}
