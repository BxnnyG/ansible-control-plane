<script lang="ts">
  import { btn } from '$lib/ui';
  import NetworkTrackingPanel from '$lib/components/fleet/NetworkTrackingPanel.svelte';

  let { slug, onnext }: { slug: string; onnext: () => void } = $props();
</script>

<div class="space-y-4">
  <NetworkTrackingPanel ws={slug} saveLabel="Save and continue" allowUnchanged onsaved={onnext} />
  <div>
    <button type="button" class={btn} onclick={onnext}>Skip</button>
  </div>
</div>
