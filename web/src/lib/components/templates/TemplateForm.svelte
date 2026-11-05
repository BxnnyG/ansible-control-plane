<script lang="ts">
  import {
    api,
    ApiError,
    type Inventory,
    type Project,
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
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    template = null,
    projects,
    inventories,
    secrets,
    identities,
    ondone,
    oncancel,
  }: {
    ws: string;
    template?: Template | null;
    projects: Project[];
    inventories: Inventory[];
    secrets: SecretMeta[];
    identities: VaultIdentity[];
    ondone: (t: Template) => void;
    oncancel?: () => void;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form when the template changes.
  // svelte-ignore state_referenced_locally
  const t = template;
  let name = $state(t?.name ?? '');
  let description = $state(t?.description ?? '');
  let projectId = $state(t?.project_id ?? '');
  let inventoryId = $state(t?.inventory_id ?? '');
  let playbook = $state(t?.playbook ?? '');
  let sshSecret = $state(t?.ssh_secret_id ?? '');
  let becomeSecret = $state(t?.become_secret_id ?? '');
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
  let surveyText = $state(
    t && surveyFields(t.survey).length > 0 ? JSON.stringify(t.survey, null, 2) : '',
  );
  let maxConcurrent = $state(t?.max_concurrent ?? 1);
  let allowLimit = $state(t?.allow_limit_override ?? false);
  let allowTags = $state(t?.allow_tags_override ?? false);
  let allowVars = $state(t?.allow_vars_override ?? false);
  let allowCheck = $state(t?.allow_check_override ?? false);
  let allowDiff = $state(t?.allow_diff_override ?? false);
  let allowVerbosity = $state(t?.allow_verbosity_override ?? false);

  let playbooks = $state<string[]>([]);
  let playbookNote = $state<string | null>(null);
  let error = $state<string | null>(null);
  let busy = $state(false);

  const sshChoices = $derived(secrets.filter((s) => s.type === 'ssh_key'));
  const becomeChoices = $derived(secrets.filter((s) => s.type === 'password'));
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

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = null;
    const env = parseEnvLines(envText);
    const extra = parseJsonObject(extraVarsText);
    const survey = parseSurveyText(surveyText);
    const problem = env.error
      ? `Environment: ${env.error}`
      : extra.error
        ? `Extra variables: ${extra.error}`
        : survey.error;
    if (problem) {
      error = problem;
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
      ondone(
        template
          ? await api.templates.update(ws, template.id, body)
          : await api.templates.create(ws, body),
      );
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form
  onsubmit={submit}
  class="space-y-4"
  aria-label={template ? 'Template settings' : 'New template'}
>
  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class={label} for="t-name">Name</label>
      <input id="t-name" class={input} bind:value={name} required />
    </div>
    <div>
      <label class={label} for="t-desc">Description</label>
      <input id="t-desc" class={input} bind:value={description} />
    </div>
    <div>
      <label class={label} for="t-project">Project</label>
      <select id="t-project" class={input} bind:value={projectId} required>
        <option value="">Select...</option>
        {#each projects as p (p.id)}<option value={p.id}>{p.name}</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="t-playbook">Playbook</label>
      <select id="t-playbook" class={input} bind:value={playbook} required>
        <option value="">Select...</option>
        {#if playbook && !playbooks.includes(playbook)}<option value={playbook}>{playbook}</option
          >{/if}
        {#each playbooks as p (p)}<option value={p}>{p}</option>{/each}
      </select>
      {#if playbookNote}<p class="mt-1 text-xs text-muted">{playbookNote}</p>{/if}
    </div>
    <div>
      <label class={label} for="t-inv">Inventory</label>
      <select id="t-inv" class={input} bind:value={inventoryId} required>
        <option value="">Select...</option>
        {#each inventories as i (i.id)}<option value={i.id}>{i.name}</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="t-max">Max. concurrent runs</label>
      <input id="t-max" type="number" min="1" class={input} bind:value={maxConcurrent} />
    </div>
  </div>

  <fieldset class="rounded-md border border-border p-3">
    <legend class="px-1 text-sm font-medium">Credentials</legend>
    <div class="grid gap-3 sm:grid-cols-2">
      <div>
        <label class={label} for="t-ssh">SSH key</label>
        <select id="t-ssh" class={input} bind:value={sshSecret}>
          <option value="">None</option>
          {#each sshChoices as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
        </select>
      </div>
      <div>
        <label class={label} for="t-become">Become password</label>
        <select id="t-become" class={input} bind:value={becomeSecret}>
          <option value="">None</option>
          {#each becomeChoices as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
        </select>
      </div>
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
            <button type="button" class={btn} aria-label="Move down" onclick={() => move(i, 1)}
              >Down</button
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
  </fieldset>

  <fieldset class="rounded-md border border-border p-3">
    <legend class="px-1 text-sm font-medium">Defaults</legend>
    <div class="grid gap-3 sm:grid-cols-3">
      <div>
        <label class={label} for="t-limit">Limit</label>
        <input id="t-limit" class={input} bind:value={defaultLimit} placeholder="e.g. web:!db1" />
      </div>
      <div>
        <label class={label} for="t-tags">Tags</label>
        <input id="t-tags" class={input} bind:value={defaultTags} />
      </div>
      <div>
        <label class={label} for="t-skip">Skip tags</label>
        <input id="t-skip" class={input} bind:value={defaultSkipTags} />
      </div>
      <div>
        <label class={label} for="t-verb">Verbosity (0-5)</label>
        <input id="t-verb" type="number" min="0" max="5" class={input} bind:value={verbosity} />
      </div>
      <label class="flex items-center gap-2 self-end text-sm">
        <input type="checkbox" bind:checked={checkMode} /> Check mode
      </label>
      <label class="flex items-center gap-2 self-end text-sm">
        <input type="checkbox" bind:checked={diffMode} /> Diff mode
      </label>
    </div>
  </fieldset>

  <fieldset class="rounded-md border border-border p-3">
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

  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class={label} for="t-env">Environment variables (KEY=value per line)</label>
      <textarea id="t-env" class="{input} font-mono" rows="4" bind:value={envText}></textarea>
      <p class="text-xs text-muted">Stored in plain text. Use secrets for credentials.</p>
    </div>
    <div>
      <label class={label} for="t-vars">Extra variables (JSON object)</label>
      <textarea id="t-vars" class="{input} font-mono" rows="4" bind:value={extraVarsText}
      ></textarea>
    </div>
  </div>
  <div>
    <label class={label} for="t-survey">Survey override (JSON list of fields, optional)</label>
    <textarea
      id="t-survey"
      class="{input} font-mono"
      rows="6"
      bind:value={surveyText}
      placeholder={'[{"name": "version", "type": "str", "required": true}]'}
    ></textarea>
    <p class="text-xs text-muted">
      Fields: name, type (str, int, float, bool, list, dict), required, secret, default, choices,
      description. Without a survey, role argument specs type-check supplied variables.
    </p>
  </div>

  <FormError id="template-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{template ? 'Save' : 'Create'}</button>
    {#if oncancel}<button type="button" class={btn} onclick={oncancel}>Cancel</button>{/if}
  </div>
</form>
