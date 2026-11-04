<script lang="ts">
  import type { ProjectSyncStatus } from '$lib/api';

  let { status }: { status: ProjectSyncStatus } = $props();

  const styles: Record<ProjectSyncStatus, string> = {
    ok: 'bg-green-600/15 text-green-700 dark:text-green-400',
    error: 'bg-red-600/15 text-red-700 dark:text-red-400',
    never: 'bg-surface-2 text-muted',
  };
</script>

<span class="rounded-full px-2 py-0.5 text-xs {styles[status] ?? styles.never}">
  {status === 'never' ? 'never synced' : status}
</span>
