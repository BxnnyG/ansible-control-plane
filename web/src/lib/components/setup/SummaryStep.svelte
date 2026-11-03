<script lang="ts">
  import { api } from '$lib/api';
  import type { SetupStatus } from '$lib/api';
  import FormError from '../FormError.svelte';

  let { slug, status, ondone }: { slug: string; status: SetupStatus; ondone: () => void } =
    $props();

  let busy = $state(false);
  let error = $state<string | null>(null);

  async function finish() {
    busy = true;
    error = null;
    try {
      await api.completeSetup(slug);
      ondone();
    } catch {
      error = 'Could not finish the setup. Please try again.';
    } finally {
      busy = false;
    }
  }
</script>

<div class="space-y-4">
  <dl class="grid max-w-xl grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
    <dt class="text-muted">Workspace name</dt>
    <dd>{status.name}</dd>
    <dt class="text-muted">Single sign-on</dt>
    <dd>{status.auth.oidc ? `Configured (${status.auth.issuer_host})` : 'Not configured'}</dd>
    <dt class="text-muted">Break-glass login</dt>
    <dd>{status.auth.breakglass ? 'Enabled' : 'Disabled'}</dd>
  </dl>
  <FormError id="finish-error" message={error} />
  <button
    type="button"
    disabled={busy}
    class="rounded-md bg-accent px-4 py-2 text-accent-fg disabled:opacity-60"
    onclick={finish}
  >
    {busy ? 'Finishing...' : 'Finish setup'}
  </button>
</div>
