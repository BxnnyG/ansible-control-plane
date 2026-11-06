<script lang="ts">
  import { api, type Connector, type ConnectorType } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { session } from '$lib/session.svelte';
  import { btn, btnPrimary } from '$lib/ui';
  import ConnectorForm from '$lib/components/connectors/ConnectorForm.svelte';
  import FormError from '$lib/components/FormError.svelte';

  let { slug, onnext }: { slug: string; onnext: () => void } = $props();

  let types = $state<ConnectorType[]>([]);
  let added = $state<Connector[]>([]);
  let adding = $state(false);
  let loading = $state(true);
  let error = $state<string | null>(null);

  const allowed = $derived(session.can('connector.write'));

  $effect(() => {
    api
      .connectorTypes(slug)
      .then((t) => (types = t))
      .catch((e) => (error = describeError(e)))
      .finally(() => (loading = false));
  });
</script>

<div class="space-y-4">
  <p class="text-sm text-muted">
    Connectors read hosts and networks from OPNsense, Proxmox and similar systems. This step is
    optional; you can add connectors later.
  </p>
  <FormError id="setup-conn-error" message={error} />
  {#if added.length}
    <ul class="text-sm" aria-label="Added connectors">
      {#each added as c (c.id)}<li>{c.name} ({c.type}) added</li>{/each}
    </ul>
  {/if}
  {#if loading}
    <p class="text-muted">Loading...</p>
  {:else if !allowed}
    <p class="text-sm">You are not allowed to manage connectors. Skip this step.</p>
  {:else if adding && types.length > 0}
    <div class="max-w-xl">
      <ConnectorForm
        ws={slug}
        {types}
        submitLabel="Save connector"
        ondone={(c) => {
          added = [...added, c];
          adding = false;
        }}
        oncancel={() => (adding = false)}
      />
    </div>
  {:else if types.length > 0}
    <button type="button" class={btn} onclick={() => (adding = true)}>Add connector</button>
  {/if}
  {#if !adding}
    <div>
      <button type="button" class={btnPrimary} onclick={onnext}>
        {added.length ? 'Continue' : 'Skip'}
      </button>
    </div>
  {/if}
</div>
