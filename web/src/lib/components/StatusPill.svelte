<script lang="ts">
  export type Tone = 'ok' | 'bad' | 'warn' | 'info' | 'running' | 'queued' | 'disabled';

  let { tone, text }: { tone: Tone; text: string } = $props();

  const cls: Record<Tone, string> = {
    ok: 'tone-ok',
    bad: 'tone-bad',
    warn: 'tone-warn',
    info: 'tone-info',
    running: 'tone-info tone-pulse',
    queued: 'tone-neutral',
    disabled: 'tone-neutral opacity-70',
  };
</script>

<span class={cls[tone]}>{text}</span>
