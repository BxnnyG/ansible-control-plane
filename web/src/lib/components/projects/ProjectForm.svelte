<script lang="ts">
  import { api, type Project, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    project = null,
    secrets,
    ondone,
    oncancel,
  }: {
    ws: string;
    project?: Project | null;
    secrets: SecretMeta[];
    ondone: (p: Project) => void;
    oncancel?: () => void;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form when the project changes.
  // svelte-ignore state_referenced_locally
  let name = $state(project?.name ?? '');
  // svelte-ignore state_referenced_locally
  let url = $state(project?.url ?? '');
  // svelte-ignore state_referenced_locally
  let ref = $state(project?.default_ref ?? 'main');
  // svelte-ignore state_referenced_locally
  let credential = $state(project?.credential_secret_id ?? '');
  // svelte-ignore state_referenced_locally
  let httpsUser = $state(project?.https_username ?? '');
  // svelte-ignore state_referenced_locally
  let intervalMin = $state(
    project?.sync_interval_seconds ? String(Math.round(project.sync_interval_seconds / 60)) : '',
  );
  let error = $state<string | null>(null);
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    error = null;
    const body = {
      name,
      url,
      default_ref: ref,
      credential_secret_id: credential || null,
      https_username: httpsUser,
      sync_interval_seconds: intervalMin ? Number(intervalMin) * 60 : null,
    };
    try {
      const saved = project
        ? await api.projects.update(ws, project.id, body)
        : await api.projects.create(ws, body);
      ondone(saved);
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-3" aria-label={project ? 'Project settings' : 'New project'}>
  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class={label} for="p-name">Name</label>
      <input id="p-name" class={input} bind:value={name} required />
    </div>
    <div>
      <label class={label} for="p-ref">Default ref</label>
      <input id="p-ref" class={input} bind:value={ref} placeholder="main" />
    </div>
  </div>
  <div>
    <label class={label} for="p-url">Repository URL</label>
    <input
      id="p-url"
      class={input}
      bind:value={url}
      required
      placeholder="ssh://git@git.example.test/team/playbooks.git"
    />
  </div>
  <div class="grid gap-3 sm:grid-cols-2">
    <div>
      <label class={label} for="p-cred">Credential</label>
      <select id="p-cred" class={input} bind:value={credential}>
        <option value="">None (anonymous)</option>
        {#each secrets as s (s.id)}<option value={s.id}>{s.name} ({s.type})</option>{/each}
      </select>
    </div>
    <div>
      <label class={label} for="p-user">HTTPS username</label>
      <input id="p-user" class={input} bind:value={httpsUser} autocomplete="off" />
    </div>
  </div>
  <div class="max-w-xs">
    <label class={label} for="p-int">Sync interval (minutes, empty = manual)</label>
    <input id="p-int" type="number" min="1" class={input} bind:value={intervalMin} />
  </div>
  <FormError id="project-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{project ? 'Save' : 'Create'}</button>
    {#if oncancel}<button type="button" class={btn} onclick={oncancel}>Cancel</button>{/if}
  </div>
</form>
