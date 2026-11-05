<script lang="ts">
  import type { HostResult } from '$lib/api';
  import { td, th } from '$lib/ui';

  let { results }: { results: HostResult[] | null } = $props();

  const rows = $derived(results ?? []);

  function tone(r: HostResult): string {
    if (r.failed > 0 || r.unreachable > 0) return 'text-red-600 dark:text-red-400';
    if (r.changed > 0) return 'text-amber-600 dark:text-amber-400';
    return '';
  }
</script>

{#if rows.length === 0}
  <p class="text-sm text-muted">No host results yet.</p>
{:else}
  <div class="overflow-x-auto rounded-lg border border-border">
    <table class="w-full">
      <thead class="bg-surface-2">
        <tr>
          <th class={th}>Host</th><th class={th}>ok</th><th class={th}>changed</th>
          <th class={th}>failed</th><th class={th}>unreachable</th><th class={th}>skipped</th>
          <th class={th}>rescued</th><th class={th}>ignored</th>
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each rows as r (r.host)}
          <tr>
            <td class="{td} {tone(r)}">
              <span class="font-mono text-xs">{r.host}</span>
              {#if (r.changed_tasks ?? []).length > 0}
                <details class="mt-1">
                  <summary class="cursor-pointer text-xs text-muted">
                    {(r.changed_tasks ?? []).length} changed task(s)
                  </summary>
                  <ul class="mt-1 list-disc pl-5 text-xs">
                    {#each r.changed_tasks ?? [] as task, i (i)}<li>{task}</li>{/each}
                  </ul>
                </details>
              {/if}
            </td>
            <td class={td}>{r.ok}</td><td class={td}>{r.changed}</td><td class={td}>{r.failed}</td>
            <td class={td}>{r.unreachable}</td><td class={td}>{r.skipped}</td>
            <td class={td}>{r.rescued}</td><td class={td}>{r.ignored}</td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/if}
