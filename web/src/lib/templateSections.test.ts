import { describe, expect, it } from 'vitest';
import {
  cloneName,
  firstProblem,
  sectionsWithProblems,
  type SectionInput,
} from './templateSections';

const ok: SectionInput = {
  name: 'Deploy',
  projectId: 'p',
  playbook: 'site.yml',
  inventoryId: 'i',
  maxConcurrent: 1,
  envError: null,
  extraVarsError: null,
  surveyError: null,
};

describe('firstProblem', () => {
  it('accepts a complete form', () => {
    expect(firstProblem(ok)).toBeNull();
  });
  it('points at the section of the first problem in form order', () => {
    expect(firstProblem({ ...ok, name: ' ' })?.section).toBe('basics');
    expect(firstProblem({ ...ok, playbook: '', name: 'x' })).toMatchObject({
      section: 'source',
      field: 't-playbook',
    });
    expect(firstProblem({ ...ok, envError: 'Line 1' })?.section).toBe('variables');
    expect(firstProblem({ ...ok, surveyError: 'bad' })?.section).toBe('survey');
    expect(firstProblem({ ...ok, maxConcurrent: 0 })?.section).toBe('options');
  });
  it('marks only the failing section', () => {
    const p = firstProblem({ ...ok, inventoryId: '' });
    expect([...sectionsWithProblems(p)]).toEqual(['source']);
    expect(sectionsWithProblems(null).size).toBe(0);
  });
});

describe('cloneName', () => {
  it('prefixes the name', () => {
    expect(cloneName('Deploy')).toBe('Copy of Deploy');
  });
});
