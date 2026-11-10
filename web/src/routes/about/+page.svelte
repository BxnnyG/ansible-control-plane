<script lang="ts">
  import { ATTRIBUTION } from '$lib/attribution';
</script>

<svelte:head><title>About - Ansible Control Plane</title></svelte:head>

<article class="max-w-2xl">
  <h1 class="text-xl font-semibold">About</h1>
  <p class="mt-3">
    Ansible Control Plane is a self-hosted web platform to run, organize and monitor Ansible.
  </p>
  <p class="mt-3 text-muted">{ATTRIBUTION}</p>
  <ul class="mt-4 list-disc space-y-1 pl-5">
    <li>
      License:
      <a
        class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
        href="https://www.gnu.org/licenses/agpl-3.0.html"
        rel="noopener noreferrer">GNU AGPL-3.0</a
      >
    </li>
    <li>
      Source:
      <a
        class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
        href="https://github.com/BxnnyG/ansible-control-plane"
        rel="noopener noreferrer">github.com/BxnnyG/ansible-control-plane</a
      >
    </li>
  </ul>
</article>
