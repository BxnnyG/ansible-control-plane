<script lang="ts">
  import type { ProjectSyncStatus } from '$lib/api';

  let { status }: { status: ProjectSyncStatus } = $props();

  const styles: Record<ProjectSyncStatus, string> = {
    ok: 'tone-ok',
    error: 'tone-bad',
    never: 'tone-neutral',
  };
</script>

<span class="rounded-full px-2 py-0.5 text-xs {styles[status] ?? styles.never}">
  {status === 'never' ? 'never synced' : status}
</span>
