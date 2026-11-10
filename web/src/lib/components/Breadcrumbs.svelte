<script lang="ts">
  /** Parent trail for sub-pages; the current page title is rendered by the page header. */
  let { trail, current = '' }: { trail: { label: string; href: string }[]; current?: string } =
    $props();
</script>

<nav aria-label="Breadcrumb" class="mb-2 text-sm text-muted">
  <ol class="flex flex-wrap items-center gap-1.5">
    {#each trail as t, i (t.href)}
      <li class="flex items-center gap-1.5">
        <a class="hover:text-fg hover:underline max-lg:inline-block max-lg:py-2" href={t.href}
          >{t.label}</a
        >
        {#if current || i < trail.length - 1}<span aria-hidden="true">/</span>{/if}
      </li>
    {/each}
    {#if current}<li aria-current="page" class="max-w-[60vw] truncate font-mono text-fg">
        {current}
      </li>{/if}
  </ol>
</nav>
