<script lang="ts">
  let { id, message }: { id: string; message: string | null } = $props();
</script>

<!-- Always rendered so screen readers announce the text when it changes. -->
<p {id} role="alert" class="min-h-5 text-sm text-red-600 dark:text-red-400">{message ?? ''}</p>
