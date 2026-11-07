<script lang="ts">
  import { api, type NotificationChannel, type NotificationRule, type Template } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { EVENT_KINDS, toggleEvent, type EventKind } from '$lib/notifications';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    rule = null,
    channels,
    templates,
    ondone,
    oncancel,
  }: {
    ws: string;
    rule?: NotificationRule | null;
    channels: NotificationChannel[];
    templates: Template[];
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  // svelte-ignore state_referenced_locally
  let channelId = $state(rule?.channel_id ?? channels[0]?.id ?? '');
  // svelte-ignore state_referenced_locally
  let events = $state<EventKind[]>(rule?.events ?? ['run.failed']);
  // svelte-ignore state_referenced_locally
  let templateId = $state<string | null>(rule?.template_id ?? null);
  // svelte-ignore state_referenced_locally
  let enabled = $state(rule?.enabled ?? true);
  let error = $state<string | null>(null);
  let busy = $state(false);

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!channelId) error = 'Choose a channel.';
    else if (events.length === 0) error = 'Select at least one event.';
    else error = null;
    if (error) return;
    busy = true;
    try {
      const body = { channel_id: channelId, events, template_id: templateId, enabled };
      if (rule) await api.notifications.rules.update(ws, rule.id, body);
      else await api.notifications.rules.create(ws, body);
      ondone();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-3" aria-label={rule ? 'Edit rule' : 'New rule'}>
  <div>
    <label class={label} for="nr-channel">Channel</label>
    <select id="nr-channel" class={input} bind:value={channelId}>
      {#each channels as c (c.id)}<option value={c.id}>{c.name}</option>{/each}
    </select>
  </div>
  <fieldset>
    <legend class={label}>Events</legend>
    <div class="space-y-1">
      {#each EVENT_KINDS as k (k.kind)}
        <label class="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={events.includes(k.kind)}
            onchange={() => (events = toggleEvent(events, k.kind))}
          />
          {k.label}
        </label>
      {/each}
    </div>
  </fieldset>
  <div>
    <label class={label} for="nr-template">Template</label>
    <select id="nr-template" class={input} bind:value={templateId}>
      <option value={null}>All templates</option>
      {#each templates as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
    </select>
  </div>
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={enabled} /> Enabled
  </label>
  <FormError id="nr-error" message={error} />
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{rule ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
