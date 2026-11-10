<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import FormError from '../FormError.svelte';

  let { slug, name, onsaved }: { slug: string; name: string; onsaved: (name: string) => void } =
    $props();

  let value = $derived(name);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let input: HTMLInputElement | undefined = $state();

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    const trimmed = value.trim();
    if (trimmed.length === 0 || trimmed.length > 80) {
      error = 'Enter a name with 1 to 80 characters.';
      input?.focus();
      return;
    }
    busy = true;
    error = null;
    try {
      const status = await api.setWorkspaceName(slug, trimmed);
      onsaved(status.name);
    } catch (err) {
      error =
        err instanceof ApiError && err.status === 403
          ? 'You are not allowed to change the workspace.'
          : 'Could not save the name. Please try again.';
      input?.focus();
    } finally {
      busy = false;
    }
  }
</script>

<form class="space-y-3" onsubmit={submit} aria-describedby="ws-name-error">
  <div>
    <label class="block text-sm font-medium" for="ws-name">Workspace display name</label>
    <input
      id="ws-name"
      type="text"
      maxlength="80"
      required
      bind:value
      bind:this={input}
      aria-invalid={error ? 'true' : undefined}
      class="mt-1 w-full max-w-md rounded-md border border-border bg-surface px-3 py-2"
    />
  </div>
  <FormError id="ws-name-error" message={error} />
  <button
    type="submit"
    disabled={busy}
    class="bg-brand min-h-11 rounded-lg px-4 py-2 font-medium disabled:opacity-60"
  >
    {busy ? 'Saving...' : 'Save and continue'}
  </button>
</form>
