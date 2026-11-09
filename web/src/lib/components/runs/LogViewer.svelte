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

<div class="overflow-clip rounded-md border border-border-strong bg-console">
  <div
    class="sticky top-12 z-[5] flex flex-wrap items-center gap-2 border-b border-white/10 bg-console px-3 py-1.5 text-xs text-console-fg"
  >
    <span
      class="inline-flex items-center gap-1.5 font-medium {connection === 'live'
        ? 'tone-info tone-pulse'
        : connection === 'ended'
          ? 'tone-neutral'
          : 'tone-warn'}"
      aria-live="polite">{label}</span
    >
    <input
      class="{input} !max-w-52 !border-white/20 !bg-transparent !py-0.5 !text-console-fg"
      type="search"
      placeholder="Search log"
      aria-label="Search log"
      bind:value={query}
      onkeydown={(e) => e.key === 'Enter' && jump(e.shiftKey ? -1 : 1)}
    />
    {#if query}
      <span class="opacity-70"
        >{hits.length === 0
          ? 'No matches'
          : `${Math.min(cursor, hits.length - 1) + 1} / ${hits.length}`}</span
      >
      <button
        class="{btn} !border-white/20 !bg-transparent !text-console-fg"
        onclick={() => jump(-1)}
        aria-label="Previous match">Prev</button
      >
      <button
        class="{btn} !border-white/20 !bg-transparent !text-console-fg"
        onclick={() => jump(1)}
        aria-label="Next match">Next</button
      >
    {/if}
    <label class="ml-auto flex items-center gap-2 max-md:min-h-11">
      <input type="checkbox" bind:checked={autoscroll} /> Auto-scroll
    </label>
    <a
      class="{btn} !border-white/20 !bg-transparent !text-console-fg"
      href={runStreamUrl(ws, 'log.txt', runId)}
      download>Download</a
    >
  </div>

  <div
    bind:this={box}
    class="h-[32rem] overflow-auto py-2 font-mono text-xs leading-5 text-console-fg"
    role="log"
    aria-label="Run log"
  >
    {#if dropped > 0}
      <div class="px-3 opacity-60">
        ... {dropped} earlier line(s) hidden. Download the log for the full output.
      </div>
    {/if}
    {#each lines as line, i (line.id)}
      <div
        id="log-line-{i}"
        class="flex min-h-5 {i === current
          ? 'bg-amber-400/30'
          : hitSet.has(i)
            ? 'bg-amber-400/10'
            : ''}"
      >
        <span
          class="w-10 shrink-0 pr-2 text-right text-console-fg/35 select-none max-sm:hidden"
          aria-hidden="true">{i + 1 + dropped}</span
        >
        <span class="min-w-0 flex-1 px-3 break-words whitespace-pre-wrap sm:pl-1"
          >{#each line.segments as seg, k (k)}<span style={segmentStyle(seg)}>{seg.text}</span
            >{/each}</span
        >
      </div>
    {/each}
    {#if lines.length === 0 && connection !== 'connecting'}
      <div class="px-3 opacity-60">No output.</div>
    {/if}
  </div>
</div>
