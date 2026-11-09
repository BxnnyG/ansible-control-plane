<script lang="ts">
  import { api, type ProjectWebhook, type WebhookProvider } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, btnDanger, btnPrimary, card, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let { ws, projectId, canWrite }: { ws: string; projectId: string; canWrite: boolean } = $props();

  const PROVIDERS: WebhookProvider[] = ['gitea', 'github', 'gitlab', 'generic'];

  let hook = $state<ProjectWebhook | null>(null);
  let provider = $state<WebhookProvider>('gitea');
  // Shown once after enable/rotate; dropped as soon as the user dismisses it or reloads.
  let revealed = $state<string | null>(null);
  let copied = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);

  const fullUrl = $derived(hook ? (hook.url ?? `${location.origin}${hook.path}`) : '');

  async function load() {
    try {
      hook = await api.projects.webhook(ws, projectId);
      provider = hook.provider;
    } catch (e) {
      error = describeError(e);
    }
  }

  $effect(() => {
    void projectId;
    void load();
  });

  async function run(fn: () => Promise<ProjectWebhook | void>) {
    busy = true;
    error = null;
    copied = false;
    try {
      const res = await fn();
      if (res) {
        revealed = res.secret ?? null;
        // Do not keep the secret inside the long-lived state object.
        hook = { ...res, secret: undefined };
      } else {
        await load();
        revealed = null;
      }
    } catch (e) {
      error = describeError(e);
    } finally {
      busy = false;
    }
  }

  async function copy(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = true;
    } catch {
      error = 'Copy failed. Select the text and copy it manually.';
    }
  }
</script>

<section class={card} aria-labelledby="webhook-h">
  <h2 id="webhook-h" class="mb-2 text-sm font-semibold">Webhook</h2>
  {#if hook}
    <p class="text-sm text-muted">
      {hook.enabled ? 'Enabled: pushes trigger a sync.' : 'Disabled.'}
    </p>

    {#if canWrite}
      <div class="mt-3 flex flex-wrap items-end gap-2">
        <div>
          <label class={label} for="wh-provider">Provider</label>
          <select id="wh-provider" class={input} bind:value={provider}>
            {#each PROVIDERS as p (p)}<option value={p}>{p}</option>{/each}
          </select>
        </div>
        <button
          class={btnPrimary}
          disabled={busy}
          onclick={() => run(() => api.projects.setWebhook(ws, projectId, true, provider))}
        >
          {hook.enabled ? 'Update' : 'Enable'}
        </button>
        {#if hook.enabled}
          <button
            class={btn}
            disabled={busy}
            onclick={() => run(() => api.projects.rotateWebhook(ws, projectId))}
          >
            Rotate secret
          </button>
          <button
            class={btnDanger}
            disabled={busy}
            onclick={() => run(() => api.projects.removeWebhook(ws, projectId))}
          >
            Remove
          </button>
        {/if}
      </div>
    {/if}

    {#if hook.enabled}
      <div class="mt-3">
        <span class={label}>Payload URL</span>
        <code class="block text-xs break-all">{fullUrl}</code>
      </div>
    {/if}

    {#if revealed}
      <div role="alert" class="mt-3 rounded-xl border border-warn/40 bg-warn/10 p-3 text-sm">
        <p class="font-medium">Webhook secret - shown only once</p>
        <p class="text-muted">
          Copy it now and store it in your Git provider. It cannot be shown again.
        </p>
        <code class="mt-2 block rounded bg-surface-2 p-2 text-xs break-all">{revealed}</code>
        <div class="mt-2 flex gap-2">
          <button class={btn} onclick={() => copy(revealed ?? '')}>
            {copied ? 'Copied' : 'Copy'}
          </button>
          <button class={btn} onclick={() => (revealed = null)}>Done, hide it</button>
        </div>
      </div>
    {/if}
  {/if}
  <FormError id="webhook-error" message={error} />
</section>
