<script lang="ts">
  import type { ScanResult } from '$lib/scan';
  import { card, td, th } from '$lib/ui';
  import SurveyPreview from './SurveyPreview.svelte';

  let { scan }: { scan: ScanResult } = $props();
</script>

<section class="space-y-4" aria-labelledby="scan-h">
  <h2 id="scan-h" class="text-lg font-medium">Latest scan</h2>

  {#if scan.warnings.length > 0}
    <ul class="{card} space-y-1 text-sm text-amber-700 dark:text-amber-400">
      {#each scan.warnings as w, i (i)}<li><code>{w.path}</code> {w.message}</li>{/each}
    </ul>
  {/if}

  <details class={card} open>
    <summary class="cursor-pointer font-medium">Playbooks ({scan.playbooks.length})</summary>
    <table class="mt-2 w-full">
      <thead
        ><tr><th class={th}>Path</th><th class={th}>Hosts</th><th class={th}>Roles</th></tr></thead
      >
      <tbody class="divide-y divide-border">
        {#each scan.playbooks as p (p.path)}
          <tr>
            <td class={td}>
              <code>{p.path}</code>{#if p.name}<span class="text-muted"> - {p.name}</span>{/if}
            </td>
            <td class={td}>{p.hosts.join(', ')}</td>
            <td class={td}>{p.roles.join(', ')}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </details>

  <details class={card}>
    <summary class="cursor-pointer font-medium">Roles ({scan.roles.length})</summary>
    <div class="mt-2 space-y-3">
      {#each scan.roles as r (r.path)}
        <div>
          <p class="text-sm"><strong>{r.name}</strong> <code class="text-muted">{r.path}</code></p>
          {#each r.surveys as s (s.entry_point)}
            <details class="mt-1 ml-3">
              <summary class="cursor-pointer text-sm">Survey: {s.entry_point}</summary>
              <div class="mt-2 max-w-md"><SurveyPreview survey={s} /></div>
            </details>
          {/each}
        </div>
      {/each}
    </div>
  </details>

  <details class={card}>
    <summary class="cursor-pointer font-medium">Requirements ({scan.requirements.length})</summary>
    <ul class="mt-2 text-sm">
      {#each scan.requirements as r, i (i)}
        <li>
          {r.kind}: {r.name}{r.version ? ` ${r.version}` : ''}
          <code class="text-muted">{r.file}</code>
        </li>
      {/each}
    </ul>
  </details>

  <details class={card}>
    <summary class="cursor-pointer font-medium">Vars files ({scan.vars_files.length})</summary>
    <ul class="mt-2 text-sm">
      {#each scan.vars_files as v (v.path)}
        <li>
          <code>{v.path}</code> <span class="text-muted">{v.scope} {v.name}</span>
          {#if v.vaulted}<span class="text-amber-700 dark:text-amber-400"> (vault)</span>{/if}
        </li>
      {/each}
    </ul>
  </details>

  <details class={card}>
    <summary class="cursor-pointer font-medium">Inventories ({scan.inventories.length})</summary>
    <ul class="mt-2 text-sm">
      {#each scan.inventories as i (i.path)}
        <li><code>{i.path}</code> <span class="text-muted">{i.format}, via {i.source}</span></li>
      {/each}
    </ul>
  </details>

  <details class={card} open={scan.vault_files.length + scan.inline_vault.length > 0}>
    <summary class="cursor-pointer font-medium">
      Vault content ({scan.vault_files.length + scan.inline_vault.length})
    </summary>
    <ul class="mt-2 text-sm">
      {#each scan.vault_files as v (v.path)}
        <li>
          <span class="rounded bg-amber-500/15 px-1 text-xs text-amber-700 dark:text-amber-400"
            >vault</span
          >
          <code>{v.path}</code>
          <span class="text-muted">{v.version}{v.vault_id ? `, id ${v.vault_id}` : ''}</span>
        </li>
      {/each}
      {#each scan.inline_vault as v (v.file)}
        <li>
          <span class="rounded bg-amber-500/15 px-1 text-xs text-amber-700 dark:text-amber-400"
            >inline</span
          >
          <code>{v.file}</code> <span class="text-muted">{v.paths.join(', ')}</span>
        </li>
      {/each}
    </ul>
    <p class="mt-2 text-xs text-muted">
      Vault passwords are managed as <a class="text-accent hover:underline" href="/vault"
        >vault identities</a
      >. Decrypt, encrypt and rekey are available in the vault panel above.
    </p>
  </details>
</section>
