<script lang="ts">
  import { page } from '$app/state';

  const TABS = [
    { href: '/fleet', label: 'All hosts', exact: true },
    { href: '/fleet/alerts', label: 'Alerts', exact: false },
    { href: '/fleet/unmanaged', label: 'Unmanaged', exact: false },
    { href: '/fleet/conflicts', label: 'Conflicts', exact: false },
    { href: '/fleet/group-rules', label: 'Group rules', exact: false },
    { href: '/fleet/remediation', label: 'Remediation', exact: false },
  ];
</script>

<nav
  aria-label="Host sections"
  class="mb-6 flex gap-1 overflow-x-auto border-b border-border whitespace-nowrap"
>
  {#each TABS as t (t.href)}
    {@const current = t.exact ? page.url.pathname === t.href : page.url.pathname.startsWith(t.href)}
    <a
      href={t.href}
      aria-current={current ? 'page' : undefined}
      class="-mb-px inline-flex min-h-11 items-center border-b-2 px-4 py-2 text-sm {current
        ? 'border-accent font-semibold text-accent'
        : 'border-transparent text-muted hover:text-fg'}">{t.label}</a
    >
  {/each}
</nav>
