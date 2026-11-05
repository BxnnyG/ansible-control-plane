<script lang="ts">
  import { api, type TemplateWebhook } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { btn, btnDanger, btnPrimary, card, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let { ws, templateId }: { ws: string; templateId: string } = $props();

  let hook = $state<TemplateWebhook | null>(null);
  // Shown once after enable/rotate; dropped as soon as the user dismisses it or reloads.
  let revealed = $state<string | null>(null);
  let copied = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);

  const fullUrl = $derived(hook ? (hook.url ?? `${location.origin}${hook.path}`) : '');

  async function load() {
    try {
      hook = await api.templates.webhook(ws, templateId);
    } catch (e) {
      error = describeError(e);
    }
  }

  $effect(() => {
    void templateId;
    revealed = null;
    void load();
  });

  async function enableOrRotate() {
    busy = true;
    error = null;
    copied = false;
    try {
      const res = await api.templates.rotateWebhook(ws, templateId);
      revealed = res.secret ?? null;
      hook = { ...res, secret: undefined };
    } catch (e) {
      error = describeError(e);
    } finally {
      busy = false;
    }
  }

  async function disable() {
    busy = true;
    error = null;
    try {
      await api.templates.removeWebhook(ws, templateId);
      revealed = null;
      await load();
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

<section class={card} aria-labelledby="twh-h">
  <h2 id="twh-h" class="mb-2 text-lg font-medium">Webhook</h2>
  {#if hook}
    <p class="text-sm text-muted">
      {hook.enabled
        ? 'Enabled: a signed POST starts a run (HMAC-SHA256 of the body, header X-ACP-Signature: sha256=<hex>).'
        : 'Disabled.'}
    </p>
    <div class="mt-3 flex flex-wrap gap-2">
      <button class={btnPrimary} disabled={busy} onclick={enableOrRotate}>
        {hook.enabled ? 'Rotate secret' : 'Enable'}
      </button>
      {#if hook.enabled}
        <button class={btnDanger} disabled={busy} onclick={disable}>Disable</button>
      {/if}
    </div>
    {#if hook.enabled}
      <div class="mt-3">
        <span class={label}>URL</span>
        <code class="block text-xs break-all">{fullUrl}</code>
      </div>
    {/if}
    {#if revealed}
      <div
        role="alert"
        class="mt-3 rounded-md border border-amber-500/60 bg-amber-500/10 p-3 text-sm"
      >
        <p class="font-medium">Webhook secret - shown only once</p>
        <p class="text-muted">Copy it now. It cannot be shown again.</p>
        <code class="mt-2 block rounded bg-surface-2 p-2 text-xs break-all">{revealed}</code>
        <div class="mt-2 flex gap-2">
          <button class={btn} onclick={() => copy(revealed ?? '')}
            >{copied ? 'Copied' : 'Copy'}</button
          >
          <button class={btn} onclick={() => (revealed = null)}>Done, hide it</button>
        </div>
      </div>
    {/if}
  {/if}
  <FormError id="twh-error" message={error} />
</section>
