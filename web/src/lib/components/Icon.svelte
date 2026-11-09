<script lang="ts">
  /** Stroke icon from 24x24 path data; decorative unless `label` is set. */
  let {
    d,
    class: cls = 'h-5 w-5',
    label,
  }: { d: string; class?: string; label?: string } = $props();
</script>

<svg
  viewBox="0 0 24 24"
  class={cls}
  fill="none"
  stroke="currentColor"
  stroke-width="1.8"
  stroke-linecap="round"
  stroke-linejoin="round"
  role={label ? 'img' : undefined}
  aria-label={label}
  aria-hidden={label ? undefined : 'true'}><path {d} /></svg
>
