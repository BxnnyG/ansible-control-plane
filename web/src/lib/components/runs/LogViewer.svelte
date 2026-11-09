<script lang="ts">
  import { untrack } from 'svelte';
  import { runStreamUrl, type RunStatusEvent } from '$lib/api';
  import { openRunLog } from '$lib/runStream';
  import { segmentStyle } from '$lib/ansi';
  import { LogModel, parseJsonEvent, parseLogEvent, searchLines, type LogLine } from '$lib/runlog';
  import { btn, input } from '$lib/ui';

  let {
    ws,
    runId,
    onstatus,
  }: { ws: string; runId: string; onstatus?: (e: RunStatusEvent) => void } = $props();

  let lines = $state.raw<LogLine[]>([]);
  let dropped = $state(0);
  let connection = $state<'connecting' | 'live' | 'reconnecting' | 'ended'>('connecting');
  let autoscroll = $state(true);
  let query = $state('');
  let cursor = $state(0);
  let box = $state<HTMLDivElement | null>(null);

  const hits = $derived(searchLines(lines, query));
  const hitSet = $derived(new Set(hits));
  const current = $derived(hits.length > 0 ? hits[Math.min(cursor, hits.length - 1)] : -1);

  // Props like `runId={run.id}` are getters over the parent's mutable state: reading them in an
  // effect would re-run it on every parent update (e.g. each status event). A derived value only
  // notifies when the key really changes.
  const streamUrl = $derived(runStreamUrl(ws, 'log', runId));

  $effect(() => {
    // Reconnect only when the stream URL (workspace or run) changes; everything else is untracked.
    const url = streamUrl;
    return untrack(() => connect(url));
  });

  function connect(url: string): () => void {
    const model = new LogModel();
    lines = [];
    dropped = 0;
    connection = 'connecting';
    let timer: ReturnType<typeof setTimeout> | undefined;
    const flush = () => {
      timer = undefined;
      lines = model.all();
      dropped = model.dropped;
    };
    const finish = () => {
      if (timer) clearTimeout(timer);
      flush();
      connection = 'ended';
    };
    const close = openRunLog(url, {
      onOpen: () => (connection = 'live'),
      onError: (closed) => {
        if (closed) finish();
        else if (connection !== 'ended') connection = 'reconnecting';
      },
      onLog: (data) => {
        connection = 'live';
        model.append(parseLogEvent(data));
        timer ??= setTimeout(flush, 60);
      },
      onStatus: (data) => {
        const st = parseJsonEvent<RunStatusEvent>(data);
        if (st) onstatus?.(st);
      },
      onEnd: finish,
    });
    return () => {
      close();
      if (timer) clearTimeout(timer);
    };
  }

  $effect(() => {
    void lines;
    if (autoscroll && box) box.scrollTop = box.scrollHeight;
  });

  $effect(() => {
    void query;
    cursor = 0;
  });

  function jump(delta: number) {
    if (hits.length === 0) return;
    cursor = (cursor + delta + hits.length) % hits.length;
    autoscroll = false;
    document.getElementById(`log-line-${hits[cursor]}`)?.scrollIntoView({ block: 'center' });
  }

  const label = $derived(
    {
      connecting: 'Connecting...',
      live: 'Live',
      reconnecting: 'Reconnecting...',
      ended: 'Log complete',
    }[connection],
  );
</script>

<div class="mb-2 flex flex-wrap items-center gap-2 text-sm">
  <span class="text-muted" aria-live="polite">{label}</span>
  <input
    class="{input} max-w-56"
    type="search"
    placeholder="Search log"
    aria-label="Search log"
    bind:value={query}
    onkeydown={(e) => e.key === 'Enter' && jump(e.shiftKey ? -1 : 1)}
  />
  {#if query}
    <span class="text-muted"
      >{hits.length === 0
        ? 'No matches'
        : `${Math.min(cursor, hits.length - 1) + 1} / ${hits.length}`}</span
    >
    <button class={btn} onclick={() => jump(-1)} aria-label="Previous match">Prev</button>
    <button class={btn} onclick={() => jump(1)} aria-label="Next match">Next</button>
  {/if}
  <label class="ml-auto flex items-center gap-2">
    <input type="checkbox" bind:checked={autoscroll} /> Auto-scroll
  </label>
  <a class={btn} href={runStreamUrl(ws, 'log.txt', runId)} download>Download</a>
</div>

<div
  bind:this={box}
  class="h-[32rem] overflow-auto rounded-lg border border-border bg-slate-950 p-3 font-mono text-xs leading-5 text-slate-100"
  role="log"
  aria-label="Run log"
>
  {#if dropped > 0}
    <div class="text-slate-400">
      ... {dropped} earlier line(s) hidden. Download the log for the full output.
    </div>
  {/if}
  {#each lines as line, i (line.id)}
    <div
      id="log-line-{i}"
      class="min-h-5 break-words whitespace-pre-wrap {i === current
        ? 'bg-yellow-500/40'
        : hitSet.has(i)
          ? 'bg-yellow-500/15'
          : ''}"
    >
      {#each line.segments as seg, k (k)}<span style={segmentStyle(seg)}>{seg.text}</span>{/each}
    </div>
  {/each}
  {#if lines.length === 0 && connection !== 'connecting'}
    <div class="text-slate-400">No output.</div>
  {/if}
</div>
