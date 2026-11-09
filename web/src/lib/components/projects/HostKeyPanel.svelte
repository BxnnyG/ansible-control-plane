<script lang="ts">
  import { api, type HostKeyInfo, type Project } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, btnPrimary, card } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    project,
    canWrite,
    onchange,
  }: {
    ws: string;
    project: Project;
    canWrite: boolean;
    onchange: (p: Project) => void;
  } = $props();

  let keys = $state<HostKeyInfo[]>([]);
  let chosen = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);

  async function scan() {
    busy = true;
    error = null;
    chosen = '';
    try {
      keys = (await api.projects.scanHostKeys(ws, project.id)).keys;
    } catch (e) {
      error = describeError(e);
    } finally {
      busy = false;
    }
  }

  async function confirm() {
    busy = true;
    error = null;
    try {
      onchange(await api.projects.confirmHostKey(ws, project.id, chosen));
      keys = [];
      chosen = '';
    } catch (e) {
      error = describeError(e);
    } finally {
      busy = false;
    }
  }
</script>

<section class={card} aria-labelledby="hostkey-h">
  <h2 id="hostkey-h" class="mb-2 text-sm font-semibold">SSH host key</h2>
  {#if project.host_key_pinned}
    <p class="text-sm">Pinned fingerprints:</p>
    <ul class="mt-1 space-y-1">
      {#each project.host_key_fingerprints as fp (fp)}
        <li><code class="text-xs break-all">{fp}</code></li>
      {/each}
    </ul>
  {:else}
    <p class="text-sm text-muted">
      No host key pinned. Scan the server, compare the fingerprint with one you trust, then confirm
      exactly one key. Sync and test stay blocked until then.
    </p>
  {/if}

  {#if canWrite}
    <div class="mt-3 flex gap-2">
      <button class={btn} onclick={scan} disabled={busy}>
        {project.host_key_pinned ? 'Re-scan' : 'Scan host key'}
      </button>
    </div>
  {/if}

  {#if keys.length > 0}
    <fieldset class="mt-3 space-y-2">
      <legend class="text-sm font-medium">Select the key you trust</legend>
      {#each keys as k (k.fingerprint)}
        <label class="flex items-start gap-2 text-sm">
          <input type="radio" name="hostkey" value={k.fingerprint} bind:group={chosen} />
          <span>
            <span class="text-muted">{k.type}</span>
            <code class="block text-xs break-all">{k.fingerprint}</code>
          </span>
        </label>
      {/each}
      <button class={btnPrimary} onclick={confirm} disabled={busy || !chosen}>
        Pin selected key
      </button>
    </fieldset>
  {/if}
  <FormError id="hostkey-error" message={error} />
</section>
