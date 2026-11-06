<script lang="ts">
  import { page } from '$app/state';

  const TABS = [
    { href: '/fleet', label: 'Overview', exact: true },
    { href: '/fleet/alerts', label: 'Alerts', exact: false },
    { href: '/fleet/unmanaged', label: 'Unmanaged', exact: false },
    { href: '/fleet/conflicts', label: 'Conflicts', exact: false },
    { href: '/fleet/group-rules', label: 'Group rules', exact: false },
    { href: '/fleet/remediation', label: 'Remediation', exact: false },
  ];
</script>

<nav aria-label="Fleet sections" class="mb-6 flex gap-1 border-b border-border">
  {#each TABS as t (t.href)}
    {@const current = t.exact ? page.url.pathname === t.href : page.url.pathname.startsWith(t.href)}
    <a
      href={t.href}
      aria-current={current ? 'page' : undefined}
      class="-mb-px border-b-2 px-3 py-2 text-sm {current
        ? 'border-accent font-medium text-accent'
        : 'border-transparent text-muted hover:text-fg'}">{t.label}</a
    >
  {/each}
</nav>
