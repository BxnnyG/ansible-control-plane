<script lang="ts">
  import type { HostResult } from '$lib/api';
  import type { HostFailure } from '$lib/hostFailures';
  import { td, th } from '$lib/ui';

  let {
    results,
    failures = new Map(),
    selected = null,
    onselect,
  }: {
    results: HostResult[] | null;
    /** First failure message per host, extracted from the log. */
    failures?: Map<string, HostFailure>;
    /** Host the log is currently filtered to. */
    selected?: string | null;
    /** Called with the host on click; the parent toggles the filter. */
    onselect?: (host: string) => void;
  } = $props();

  const failed = (r: HostResult) => r.failed > 0 || r.unreachable > 0;

  const rows = $derived(results ?? []);

  function tone(r: HostResult): string {
    if (r.failed > 0 || r.unreachable > 0) return 'text-bad';
    if (r.changed > 0) return 'text-warn';
    return '';
  }
</script>

{#if rows.length === 0}
  <p class="text-sm text-muted">No host results yet.</p>
{:else}
  <div class="tbl-wrap">
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
          {@const fail = failures.get(r.host)}
          <tr
            class="group cursor-pointer hover:bg-surface-2 focus-visible:bg-surface-2 {selected ===
            r.host
              ? 'bg-surface-2'
              : ''}"
            tabindex="0"
            title={failed(r)
              ? (fail?.message ?? 'Failed (no message found in the log)')
              : undefined}
            onclick={(e) => {
              if (!(e.target as HTMLElement).closest('summary, details')) onselect?.(r.host);
            }}
            onkeydown={(e) => {
              if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) {
                e.preventDefault();
                onselect?.(r.host);
              }
            }}
          >
            <td class="{td} {tone(r)}">
              <span class="font-mono text-xs">{r.host}</span>
              {#if failed(r) && fail}
                <p
                  class="mt-0.5 hidden max-w-xl group-focus-within:block group-hover:block text-xs font-normal break-words text-muted"
                >
                  {fail.message}
                </p>
              {/if}
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
