<script lang="ts">
  import { ariaSort, nextSort, type SortState } from '$lib/sort';
  import { th } from '$lib/ui';

  let {
    label,
    key,
    sort = $bindable(null),
  }: { label: string; key: string; sort: SortState | null } = $props();
</script>

<th class={th} aria-sort={ariaSort(sort, key)}>
  <button
    type="button"
    class="inline-flex items-center gap-1 font-semibold hover:underline"
    onclick={() => (sort = nextSort(sort, key))}
  >
    {label}
    <span aria-hidden="true" class="text-xs text-muted"
      >{sort?.key === key ? (sort.dir === 'asc' ? '▲' : '▼') : ''}</span
    >
  </button>
</th>
