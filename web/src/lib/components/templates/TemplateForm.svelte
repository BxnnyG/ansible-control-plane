<script lang="ts">
  import Combobox from '$lib/components/Combobox.svelte';
  import { playbookOptions } from '$lib/combobox';
  import {
    api,
    ApiError,
    type Inventory,
    type Project,
    type ConnectorType,
    type SecretMeta,
    type Template,
    type TemplateInput,
    type VaultIdentity,
  } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { normalizeScan } from '$lib/scan';
  import {
    formatEnvLines,
    parseEnvLines,
    parseJsonObject,
    parseSurveyText,
    surveyFields,
  } from '$lib/templates';
  import { tick } from 'svelte';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import { session } from '$lib/session.svelte';
  import FormError from '$lib/components/FormError.svelte';
  import SecretPicker from '$lib/components/connectors/SecretPicker.svelte';
  import {
    SURVEY_TYPES,
    builderToSurvey,
    builderToText,
    emptyRow,
    stripSecretDefaults,
    surveyToBuilder,
    textToBuilder,
    type BuilderState,
  } from '$lib/surveyBuilder';
  import {
    SECTIONS,
    cloneName,
    firstProblem,
    type FormProblem,
    type SectionId,
  } from '$lib/templateSections';
  import { beforeNavigate } from '$app/navigation';
  import { isDirty, snapshotOf } from '$lib/dirty';

  let {
    ws,
    template = null,
    clone = null,
    projects,
    inventories,
    identities,
    ondone,
    oncancel,
  }: {
    ws: string;
    template?: Template | null;
    /** Create a new template prefilled from this one (secret values are never copied). */
    clone?: Template | null;
    projects: Project[];
    inventories: Inventory[];
    /** Accepted for parents that pass all refs; the pickers load their own secrets. */
    secrets?: SecretMeta[];
    identities: VaultIdentity[];
    ondone: (t: Template) => void;
    oncancel?: () => void;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form when the template changes.
  // svelte-ignore state_referenced_locally
  const t = template ?? clone;
  // svelte-ignore state_referenced_locally
  let name = $state(clone ? cloneName(clone.name) : (t?.name ?? ''));
  let description = $state(t?.description ?? '');
  let projectId = $state(t?.project_id ?? '');
  let inventoryId = $state(t?.inventory_id ?? '');
  let playbook = $state(t?.playbook ?? '');
  let sshSecret = $state<string | null>(t?.ssh_secret_id ?? null);
  let becomeSecret = $state<string | null>(t?.become_secret_id ?? null);
  let vaultIds = $state<string[]>(t?.vault_identity_ids ?? []);
  let defaultLimit = $state(t?.default_limit ?? '');
  let defaultTags = $state(t?.default_tags ?? '');
  let defaultSkipTags = $state(t?.default_skip_tags ?? '');
  let verbosity = $state(t?.verbosity ?? 0);
  let checkMode = $state(t?.check_mode ?? false);
  let diffMode = $state(t?.diff_mode ?? false);
  let envText = $state(formatEnvLines(t?.env_vars));
  let extraVarsText = $state(
    t && Object.keys(t.extra_vars ?? {}).length > 0 ? JSON.stringify(t.extra_vars, null, 2) : '',
  );
  // svelte-ignore state_referenced_locally
  const initialSurvey: unknown = clone ? stripSecretDefaults(clone.survey) : t?.survey;
  let surveyText = $state(
    t && surveyFields(initialSurvey).length > 0 ? JSON.stringify(initialSurvey, null, 2) : '',
  );
  let builder = $state<BuilderState>(surveyToBuilder(initialSurvey));
  let surveyMode = $state<'builder' | 'json'>('builder');
  let surveyNote = $state<string | null>(null);
  let rowProblems = $state<Record<number, string>>({});
  let open = $state<Record<SectionId, boolean>>({
    basics: true,
    source: true,
    credentials: false,
    variables: false,
    survey: false,
    options: false,
  });
  let problem = $state<FormProblem | null>(null);

  // Secret pickers reuse the connector picker with a fixed accepted type.
  const pickerType = (secretType: 'ssh_key' | 'password'): ConnectorType => ({
    type: 'template',
    label: 'Template',
    secret_types: [secretType],
    secret_required: false,
    base_url: false,
    fields: [],
  });
  const sshType = pickerType('ssh_key');
  const passwordType = pickerType('password');
  const canCreateSecret = $derived(session.can('secrets.write'));
  let maxConcurrent = $state(t?.max_concurrent ?? 1);
  let allowLimit = $state(t?.allow_limit_override ?? false);
  let allowTags = $state(t?.allow_tags_override ?? false);
  let allowVars = $state(t?.allow_vars_override ?? false);
  let allowCheck = $state(t?.allow_check_override ?? false);
  let allowDiff = $state(t?.allow_diff_override ?? false);
  let allowVerbosity = $state(t?.allow_verbosity_override ?? false);

  let playbooks = $state<string[]>([]);
  let playbookNote = $state<string | null>(null);
  const projectOptions = $derived(projects.map((p) => ({ value: p.id, label: p.name })));
  const inventoryOptions = $derived(inventories.map((i) => ({ value: i.id, label: i.name })));
  // A stored playbook that the latest scan no longer lists stays selectable.
  const playbookChoices = $derived(
    playbookOptions(
      playbook && !playbooks.includes(playbook) ? [playbook, ...playbooks] : playbooks,
    ),
  );
  let error = $state<string | null>(null);
  let busy = $state(false);

  /** Survey value in a mode-independent form, so switching modes alone is not an edit. */
  function surveyNormalized(): string {
    if (surveyMode === 'builder') {
      const r = builderToSurvey(builder);
      return r.problems.length === 0
        ? JSON.stringify(r.value)
        : `invalid:${JSON.stringify(builder.rows)}`;
    }
    const r = parseSurveyText(surveyText);
    return r.error ? `invalid:${surveyText}` : JSON.stringify(r.value);
  }

  const snapshot = () =>
    snapshotOf({
      name,
      description,
      projectId,
      inventoryId,
      playbook,
      sshSecret,
      becomeSecret,
      vaultIds,
      defaultLimit,
      defaultTags,
      defaultSkipTags,
      verbosity,
      checkMode,
      diffMode,
      envText,
      extraVarsText,
      survey: surveyNormalized(),
      maxConcurrent,
      allowLimit,
      allowTags,
      allowVars,
      allowCheck,
      allowDiff,
      allowVerbosity,
    });
  const baseline = snapshot();
  let saved = false;

  // Leaving the page with edits (in-app navigation, reload or closing the tab) asks first.
  beforeNavigate((nav) => {
    if (!isDirty(baseline, snapshot(), saved)) return;
    if (nav.willUnload) nav.cancel();
    else if (!confirm('You have unsaved changes. Leave this page and discard them?')) nav.cancel();
  });

  const unselected = $derived(identities.filter((i) => !vaultIds.includes(i.id)));

  // Playbooks come from the latest scan of the selected project.
  $effect(() => {
    const pid = projectId;
    playbooks = [];
    playbookNote = null;
    if (!pid) return;
    let cancelled = false;
    api.projects
      .latestRevision(ws, pid)
      .then((rev) => {
        if (cancelled) return;
        playbooks = normalizeScan(rev.scan).playbooks.map((p) => p.path);
        if (playbooks.length === 0) playbookNote = 'The last scan found no playbooks.';
      })
      .catch((e) => {
        if (cancelled) return;
        playbookNote =
          e instanceof ApiError && e.status === 404
            ? 'This project has not been synced yet.'
            : describeError(e);
      });
    return () => {
      cancelled = true;
    };
  });

  function move(i: number, d: number) {
    const j = i + d;
    if (j < 0 || j >= vaultIds.length) return;
    const next = vaultIds.slice();
    [next[i], next[j]] = [next[j], next[i]];
    vaultIds = next;
  }

  function identityLabel(id: string): string {
    return identities.find((i) => i.id === id)?.label ?? id;
  }

  function toJsonMode() {
    const r = builderToText(builder);
    if (r.problems.length > 0) {
      surveyNote = r.problems[0].message;
      return;
    }
    surveyText = r.text;
    surveyNote = null;
    rowProblems = {};
    surveyMode = 'json';
  }

  function toBuilderMode() {
    const r = textToBuilder(surveyText);
    if (!r.state) {
      surveyNote = r.error;
      return;
    }
    builder = r.state;
    surveyNote = null;
    surveyMode = 'builder';
  }

  function moveRow(i: number, d: number) {
    const j = i + d;
    if (j < 0 || j >= builder.rows.length) return;
    const rows = builder.rows.slice();
    [rows[i], rows[j]] = [rows[j], rows[i]];
    builder.rows = rows;
  }

  /** Survey value from the active editor, with an error message for the first problem. */
  function currentSurvey(): { value: unknown; error: string | null } {
    if (surveyMode === 'json') return parseSurveyText(surveyText);
    const r = builderToSurvey(builder);
    rowProblems = Object.fromEntries(r.problems.map((p) => [p.row, p.message]));
    return { value: r.value, error: r.problems[0]?.message ?? null };
  }

  async function showProblem(p: FormProblem) {
    problem = p;
    error = p.message;
    open[p.section] = true;
    await tick();
    const el = document.getElementById(p.field);
    el?.scrollIntoView({ block: 'center' });
    el?.focus();
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    problem = null;
    const env = parseEnvLines(envText);
    const extra = parseJsonObject(extraVarsText);
    const survey = currentSurvey();
    const found = firstProblem({
      name,
      projectId,
      playbook,
      inventoryId,
      maxConcurrent,
      envError: env.error,
      extraVarsError: extra.error,
      surveyError: survey.error,
    });
    if (found) {
      await showProblem(found);
      return;
    }
    const body: TemplateInput = {
      name,
      description,
      project_id: projectId,
      inventory_id: inventoryId,
      playbook,
      ssh_secret_id: sshSecret || null,
      become_secret_id: becomeSecret || null,
      vault_identity_ids: vaultIds,
      default_limit: defaultLimit,
      default_tags: defaultTags,
      default_skip_tags: defaultSkipTags,
      verbosity,
      check_mode: checkMode,
      diff_mode: diffMode,
      env_vars: env.vars,
      extra_vars: extra.value,
      survey: survey.value,
      max_concurrent: maxConcurrent,
      allow_limit_override: allowLimit,
      allow_tags_override: allowTags,
      allow_vars_override: allowVars,
      allow_check_override: allowCheck,
      allow_diff_override: allowDiff,
      allow_verbosity_override: allowVerbosity,
    };
    busy = true;
    try {
      const result = template
        ? await api.templates.update(ws, template.id, body)
        : await api.templates.create(ws, body);
      saved = true; // no unsaved-changes prompt for the navigation that follows a save
      ondone(result);
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

{#snippet header(id: SectionId, title: string, hint: string)}
  <h2 class="m-0 text-sm font-semibold">
    <button
      type="button"
      class="flex w-full items-center gap-2 px-4 py-3 text-left max-md:min-h-11"
      aria-expanded={open[id]}
      aria-controls="sec-{id}"
      onclick={() => (open[id] = !open[id])}
    >
      <span aria-hidden="true" class="w-3 text-muted">{open[id] ? '-' : '+'}</span>
      <span>{title}</span>
      <span class="grow text-xs font-normal text-muted">{hint}</span>
      {#if problem?.section === id}<span class="text-xs font-medium text-bad">Needs attention</span
        >{/if}
    </button>
  </h2>
{/snippet}

<form
  onsubmit={submit}
  novalidate
  class="space-y-3"
  aria-label={template ? 'Template settings' : clone ? 'Clone template' : 'New template'}
>
  {#if clone}
    <p class="text-sm text-muted">
      Prefilled from <strong>{clone.name}</strong>. Credentials are copied as references only;
      secret values and secret survey defaults are not copied.
    </p>
  {/if}
  {#each SECTIONS as sec (sec.id)}
    <section class="rounded-xl border border-border" aria-label={sec.title}>
      {@render header(sec.id, sec.title, sec.hint)}
      <div id="sec-{sec.id}" class="border-t border-border p-4" hidden={!open[sec.id]}>
        {#if sec.id === 'basics'}
          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <label class={label} for="t-name">Name</label>
              <input id="t-name" class={input} bind:value={name} required />
            </div>
            <div>
              <label class={label} for="t-desc">Description</label>
              <input id="t-desc" class={input} bind:value={description} />
            </div>
          </div>
        {:else if sec.id === 'source'}
          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <label class={label} for="t-project">Project</label>
              <Combobox id="t-project" options={projectOptions} bind:value={projectId} required />
            </div>
            <div>
              <label class={label} for="t-playbook">Playbook</label>
              <Combobox id="t-playbook" options={playbookChoices} bind:value={playbook} required />
              {#if playbookNote}<p class="mt-1 text-xs text-muted">{playbookNote}</p>{/if}
            </div>
            <div>
              <label class={label} for="t-inv">Inventory</label>
              <Combobox id="t-inv" options={inventoryOptions} bind:value={inventoryId} required />
            </div>
          </div>
        {:else if sec.id === 'credentials'}
          <div class="grid gap-3 sm:grid-cols-2">
            <SecretPicker
              {ws}
              ctype={sshType}
              bind:secretId={sshSecret}
              canCreate={canCreateSecret}
              id="t-ssh"
              title="SSH key"
            />
            <SecretPicker
              {ws}
              ctype={passwordType}
              bind:secretId={becomeSecret}
              canCreate={canCreateSecret}
              id="t-become"
              title="Become password"
            />
          </div>
          <div class="mt-3">
            <span class={label}>Vault identities (in order)</span>
            <ol class="space-y-1">
              {#each vaultIds as id, i (id)}
                <li class="flex items-center gap-2 text-sm">
                  <span class="grow">{identityLabel(id)}</span>
                  <button type="button" class={btn} aria-label="Move up" onclick={() => move(i, -1)}
                    >Up</button
                  >
                  <button
                    type="button"
                    class={btn}
                    aria-label="Move down"
                    onclick={() => move(i, 1)}>Down</button
                  >
                  <button
                    type="button"
                    class={btn}
                    onclick={() => (vaultIds = vaultIds.filter((x) => x !== id))}>Remove</button
                  >
                </li>
              {/each}
            </ol>
            {#if unselected.length > 0}
              <select
                class="{input} mt-2 max-w-xs"
                aria-label="Add vault identity"
                value=""
                onchange={(e) => {
                  if (e.currentTarget.value) vaultIds = [...vaultIds, e.currentTarget.value];
                  e.currentTarget.value = '';
                }}
              >
                <option value="">Add identity...</option>
                {#each unselected as i (i.id)}<option value={i.id}>{i.label}</option>{/each}
              </select>
            {/if}
          </div>
        {:else if sec.id === 'variables'}
          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <label class={label} for="t-env">Environment variables (KEY=value per line)</label>
              <textarea id="t-env" class="{input} font-mono" rows="4" bind:value={envText}
              ></textarea>
              <p class="text-xs text-muted">Stored in plain text. Use secrets for credentials.</p>
            </div>
            <div>
              <label class={label} for="t-vars">Extra variables (JSON object)</label>
              <textarea id="t-vars" class="{input} font-mono" rows="4" bind:value={extraVarsText}
              ></textarea>
            </div>
          </div>
        {:else if sec.id === 'survey'}
          <div id="survey-section" tabindex="-1" class="space-y-3">
            <div class="flex flex-wrap items-center justify-between gap-2">
              <p class="text-xs text-muted">
                Fields asked at run time and type-checked. Without a survey, role argument specs
                type-check supplied variables.
              </p>
              {#if surveyMode === 'builder'}
                <button type="button" class={btn} onclick={toJsonMode}>Edit as JSON</button>
              {:else}
                <button type="button" class={btn} onclick={toBuilderMode}>Use field builder</button>
              {/if}
            </div>
            {#if surveyNote}<p class="text-sm text-bad" role="alert">{surveyNote}</p>{/if}
            {#if surveyMode === 'json'}
              <div>
                <label class={label} for="t-survey">Survey (JSON list of fields, optional)</label>
                <textarea
                  id="t-survey"
                  class="{input} font-mono"
                  rows="10"
                  bind:value={surveyText}
                  placeholder={'[{"name": "version", "type": "str", "required": true}]'}
                ></textarea>
                <p class="text-xs text-muted">
                  Fields: name, type (str, int, float, bool, list, dict, path, raw), required,
                  secret, default, choices, description.
                </p>
              </div>
            {:else}
              {#if builder.rows.length === 0}
                <p class="text-sm text-muted">No survey fields yet.</p>
              {/if}
              {#each builder.rows as row, i (row.uid)}
                <fieldset class="space-y-2 rounded-xl border border-border p-3">
                  <legend class="px-1 text-sm">Field {i + 1}</legend>
                  <div class="grid gap-3 sm:grid-cols-3">
                    <div>
                      <label class={label} for="sv-name-{row.uid}">Name</label>
                      <input
                        id="sv-name-{row.uid}"
                        class={input}
                        bind:value={row.name}
                        autocomplete="off"
                      />
                    </div>
                    <div>
                      <label class={label} for="sv-type-{row.uid}">Type</label>
                      <select id="sv-type-{row.uid}" class={input} bind:value={row.type}>
                        {#each SURVEY_TYPES as st (st)}<option value={st}>{st}</option>{/each}
                      </select>
                    </div>
                    <div class="flex items-end gap-4 pb-1.5 text-sm">
                      <label class="flex items-center gap-2"
                        ><input type="checkbox" bind:checked={row.required} /> Required</label
                      >
                      <label class="flex items-center gap-2"
                        ><input type="checkbox" bind:checked={row.secret} /> Secret</label
                      >
                    </div>
                    <div class="sm:col-span-3">
                      <label class={label} for="sv-desc-{row.uid}">Description</label>
                      <input id="sv-desc-{row.uid}" class={input} bind:value={row.description} />
                    </div>
                    <div>
                      <label class={label} for="sv-def-{row.uid}">Default</label>
                      {#if row.secret}
                        <input
                          id="sv-def-{row.uid}"
                          type="password"
                          class={input}
                          bind:value={row.defaultText}
                          autocomplete="new-password"
                        />
                      {:else}
                        <input
                          id="sv-def-{row.uid}"
                          class={input}
                          bind:value={row.defaultText}
                          placeholder={row.type === 'bool' ? 'true or false' : ''}
                        />
                      {/if}
                    </div>
                    <div class="sm:col-span-2">
                      <label class={label} for="sv-ch-{row.uid}">Choices (one per line)</label>
                      <textarea
                        id="sv-ch-{row.uid}"
                        class="{input} font-mono"
                        rows="2"
                        bind:value={row.choicesText}
                      ></textarea>
                    </div>
                  </div>
                  {#if Object.keys(row.rest).length > 0}
                    <p class="text-xs text-muted">
                      Also stored for this field and kept as is: {Object.keys(row.rest).join(', ')}.
                      Use the JSON editor to change them.
                    </p>
                  {/if}
                  {#if rowProblems[i]}<p class="text-sm text-bad">{rowProblems[i]}</p>{/if}
                  <div class="flex gap-2">
                    <button
                      type="button"
                      class={btn}
                      aria-label="Move field up"
                      onclick={() => moveRow(i, -1)}>Up</button
                    >
                    <button
                      type="button"
                      class={btn}
                      aria-label="Move field down"
                      onclick={() => moveRow(i, 1)}>Down</button
                    >
                    <button
                      type="button"
                      class={btn}
                      onclick={() => (builder.rows = builder.rows.filter((r) => r.uid !== row.uid))}
                      >Remove</button
                    >
                  </div>
                </fieldset>
              {/each}
              <button type="button" class={btn} onclick={() => builder.rows.push(emptyRow())}
                >Add field</button
              >
            {/if}
          </div>
        {:else if sec.id === 'options'}
          <div class="grid gap-3 sm:grid-cols-3">
            <div>
              <label class={label} for="t-max">Max. concurrent runs</label>
              <input id="t-max" type="number" min="1" class={input} bind:value={maxConcurrent} />
            </div>
            <div>
              <label class={label} for="t-limit">Default limit</label>
              <input
                id="t-limit"
                class={input}
                bind:value={defaultLimit}
                placeholder="e.g. web:!db1"
              />
            </div>
            <div>
              <label class={label} for="t-tags">Default tags</label>
              <input id="t-tags" class={input} bind:value={defaultTags} />
            </div>
            <div>
              <label class={label} for="t-skip">Default skip tags</label>
              <input id="t-skip" class={input} bind:value={defaultSkipTags} />
            </div>
            <div>
              <label class={label} for="t-verb">Verbosity (0-5)</label>
              <input
                id="t-verb"
                type="number"
                min="0"
                max="5"
                class={input}
                bind:value={verbosity}
              />
            </div>
            <div class="flex items-end gap-4 pb-1.5 text-sm">
              <label class="flex items-center gap-2"
                ><input type="checkbox" bind:checked={checkMode} /> Check mode</label
              >
              <label class="flex items-center gap-2"
                ><input type="checkbox" bind:checked={diffMode} /> Diff mode</label
              >
            </div>
          </div>
          <fieldset class="mt-4 rounded-xl border border-border p-3">
            <legend class="px-1 text-sm font-medium">Allow overrides at run time</legend>
            <div class="grid gap-2 sm:grid-cols-3">
              <label class="flex items-center gap-2 text-sm"
                ><input type="checkbox" bind:checked={allowLimit} /> Limit</label
              >
              <label class="flex items-center gap-2 text-sm"
                ><input type="checkbox" bind:checked={allowTags} /> Tags / skip tags</label
              >
              <label class="flex items-center gap-2 text-sm"
                ><input type="checkbox" bind:checked={allowVars} /> Extra variables</label
              >
              <label class="flex items-center gap-2 text-sm"
                ><input type="checkbox" bind:checked={allowCheck} /> Check mode</label
              >
              <label class="flex items-center gap-2 text-sm"
                ><input type="checkbox" bind:checked={allowDiff} /> Diff mode</label
              >
              <label class="flex items-center gap-2 text-sm"
                ><input type="checkbox" bind:checked={allowVerbosity} /> Verbosity</label
              >
            </div>
            {#if !allowVars}
              <p class="mt-2 text-xs text-muted">
                Without extra variables, only survey variables can be set.
              </p>
            {/if}
          </fieldset>
        {/if}
      </div>
    </section>
  {/each}

  <FormError id="template-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{template ? 'Save' : 'Create'}</button>
    {#if oncancel}<button type="button" class={btn} onclick={oncancel}>Cancel</button>{/if}
  </div>
</form>
