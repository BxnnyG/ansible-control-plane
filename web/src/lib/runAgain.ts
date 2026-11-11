import { api, type Run, type RunOverrides, type Template } from './api';
import { overridesFromRun } from './runStart';
import { surveyToFormFields } from './survey';
import { surveyFields } from './templates';

export interface RunAgain {
  template: Template;
  prefill: RunOverrides;
  notCarried: string[];
}

/** Loads the run's template and derives the prefill for the run dialog. Throws on API errors. */
export async function prepareRunAgain(ws: string, run: Run): Promise<RunAgain> {
  if (!run.template_id) throw new Error('The template of this run no longer exists.');
  const template = await api.templates.get(ws, run.template_id);
  const fields = surveyToFormFields(surveyFields(template.survey));
  const { overrides, notCarried } = overridesFromRun(template, fields, run.params);
  return { template, prefill: overrides, notCarried };
}
