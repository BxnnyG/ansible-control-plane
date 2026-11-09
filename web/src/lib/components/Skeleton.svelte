<script lang="ts">
  /** Shimmering placeholder block; size it with Tailwind classes. */
  let { class: cls = 'h-4 w-full' }: { class?: string } = $props();
</script>

<div class="skeleton {cls}" aria-hidden="true"></div>
