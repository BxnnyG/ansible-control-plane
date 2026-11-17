<script lang="ts">
  import type { ReachabilityMatrix } from '$lib/api';
  import {
    cellView,
    indexResults,
    runHref,
    targetSummary,
    visibleTargets,
  } from '$lib/reachability';
  import { fmtDate, tblWrap, th } from '$lib/ui';

  let { matrix, now }: { matrix: ReachabilityMatrix; now: number } = $props();

  const index = $derived(indexResults(matrix.results));
  const targets = $derived(visibleTargets(matrix));
  let selected = $state<string | null>(null);

  const detail = $derived.by(() => {
    if (!selected) return null;
    const [sid, tid] = selected.split(':');
    const r = index.get(selected);
    const s = matrix.sources.find((x) => x.id === sid);
    const t = matrix.targets.find((x) => x.id === tid);
    if (!s || !t) return null;
    return { s, t, r, view: cellView(r, matrix.config.interval_seconds, now) };
  });
</script>

{#if matrix.sources.length === 0 || targets.length === 0}
  <p class="p-5 text-sm text-muted">
    Add at least one source host and one enabled target to see the matrix.
  </p>
{:else}
  <div class={tblWrap}>
    <table class="w-full border-collapse text-sm">
      <thead>
        <tr>
          <th class="{th} sticky left-0 bg-surface">Source \ Target</th>
          {#each targets as t (t.id)}
            <th class={th} title={targetSummary(t)}>{t.name}</th>
          {/each}
        </tr>
      </thead>
      <tbody class="divide-y divide-border">
        {#each matrix.sources as s (s.id)}
          <tr>
            <th class="sticky left-0 bg-surface px-3 py-1.5 text-left font-medium">{s.host_name}</th
            >
            {#each targets as t (t.id)}
              {@const key = `${s.id}:${t.id}`}
              {@const v = cellView(index.get(key), matrix.config.interval_seconds, now)}
              <td class="px-1.5 py-1.5 text-center">
                <button
                  type="button"
                  class="min-h-11 min-w-11 rounded-lg border border-border px-2 text-sm {v.tone} {v.stale
                    ? 'border-dashed opacity-60'
                    : ''}"
                  title={v.title}
                  aria-label="{s.host_name} to {t.name}: {v.label}{v.stale ? ' (stale)' : ''}"
                  aria-pressed={selected === key}
                  data-state={v.state}
                  onclick={() => (selected = selected === key ? null : key)}
                >
                  <span aria-hidden="true">{v.icon || '·'}</span>{#if v.stale}<span
                      class="ml-0.5 text-xs">stale</span
                    >{/if}
                </button>
              </td>
            {/each}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
  {#if detail}
    <div class="border-t border-border p-4 text-sm" data-testid="cell-detail">
      <p class="font-medium">{detail.s.host_name} to {detail.t.name} ({targetSummary(detail.t)})</p>
      <p class="{detail.view.tone} mt-1 inline-block">{detail.view.label}</p>
      {#if detail.view.stale}<p class="text-muted">Stale: older than twice the interval.</p>{/if}
      {#if detail.r}
        <dl class="mt-2 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
          <dt class="text-muted">Latency</dt>
          <dd>{detail.r.latency_ms != null ? `${detail.r.latency_ms} ms` : 'n/a'}</dd>
          <dt class="text-muted">Checked</dt>
          <dd>{fmtDate(detail.r.checked_at)}</dd>
          {#if detail.r.detail}
            <dt class="text-muted">Detail</dt>
            <dd class="break-words">{detail.r.detail}</dd>
          {/if}
          {#if runHref(detail.r)}
            <dt class="text-muted">Run</dt>
            <dd><a class="underline" href={runHref(detail.r)}>Open run</a></dd>
          {/if}
        </dl>
      {/if}
    </div>
  {/if}
{/if}
