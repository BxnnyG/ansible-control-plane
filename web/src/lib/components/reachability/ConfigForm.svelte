<script lang="ts">
  import { api, type ReachabilityConfig, type SecretMeta } from '$lib/api';
  import { describeError } from '$lib/errors';
  import { intervalLabel, validInterval } from '$lib/reachability';
  import { toasts } from '$lib/toasts.svelte';
  import { btnPrimary, fmtDate, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    config,
    secrets,
    onsaved,
  }: {
    ws: string;
    config: ReachabilityConfig;
    secrets: SecretMeta[];
    onsaved: () => void;
  } = $props();

  let interval = $state(0);
  let secretId = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    interval = config.interval_seconds;
    secretId = config.ssh_secret_id ?? '';
  });

  const sshSecrets = $derived(secrets.filter((s) => s.type === 'ssh_key' || s.type === 'password'));

  async function save(e: Event) {
    e.preventDefault();
    if (!validInterval(interval)) {
      error = 'Interval must be 0 (off) or between 60 and 86400 seconds.';
      return;
    }
    busy = true;
    error = null;
    try {
      await api.reachability.setConfig(ws, {
        inventory_id: config.inventory_id,
        ssh_secret_id: secretId || null,
        interval_seconds: interval,
      });
      toasts.success('Reachability settings saved.');
      onsaved();
    } catch (err) {
      error = describeError(err);
    } finally {
      busy = false;
    }
  }
</script>

<form class="grid gap-4 p-5 md:grid-cols-2" onsubmit={save}>
  <div>
    <label class={label} for="reach-interval">Interval in seconds (0 = off)</label>
    <input
      id="reach-interval"
      class={input}
      type="number"
      min="0"
      max="86400"
      bind:value={interval}
    />
    <p class="mt-1 text-xs text-muted">
      Currently {intervalLabel(config.interval_seconds)}{config.next_run_at
        ? `, next run ${fmtDate(config.next_run_at)}`
        : ''}{config.last_run_at ? `, last run ${fmtDate(config.last_run_at)}` : ''}.
    </p>
  </div>
  <div>
    <label class={label} for="reach-secret">SSH credential</label>
    <select id="reach-secret" class={input} bind:value={secretId}>
      <option value="">None (use inventory variables)</option>
      {#each sshSecrets as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
    </select>
  </div>
  <div class="md:col-span-2">
    <FormError id="reach-config-error" message={error} />
    <button class={btnPrimary} type="submit" disabled={busy}>Save settings</button>
  </div>
</form>
