<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { goto } from '$app/navigation';
  import { api, type SetupStatus } from '$lib/api';
  import { session } from '$lib/session.svelte';
  import { SETUP_STEPS, clampStep } from '$lib/setup/steps';
  import { SETUP_WORKSPACE } from '$lib/workspace';
  import ConnectorsStep from '$lib/components/setup/ConnectorsStep.svelte';
  import NetworksStep from '$lib/components/setup/NetworksStep.svelte';
  import AuthStep from '$lib/components/setup/AuthStep.svelte';
  import Stepper from '$lib/components/setup/Stepper.svelte';
  import SummaryStep from '$lib/components/setup/SummaryStep.svelte';
  import WorkspaceStep from '$lib/components/setup/WorkspaceStep.svelte';

  let status = $state<SetupStatus | null>(null);
  let step = $state(0);
  let failed = $state(false);
  let heading: HTMLHeadingElement | undefined = $state();

  const allowed = $derived(session.slug === SETUP_WORKSPACE && session.can('workspace.manage'));

  onMount(async () => {
    if (!allowed) return;
    try {
      status = await api.setupStatus(SETUP_WORKSPACE);
    } catch {
      failed = true;
    }
  });

  async function go(index: number) {
    step = clampStep(index);
    await tick();
    heading?.focus(); // move focus to the new step for keyboard and screen reader users
  }

  async function done() {
    session.markSetupDone();
    await session.loadWorkspace();
    await goto('/fleet');
  }
</script>

<svelte:head><title>Setup - Ansible Control Plane</title></svelte:head>

<div class="max-w-3xl space-y-6">
  <header>
    <h1 class="text-xl font-semibold">Welcome</h1>
    <p class="mt-1 text-muted">A few steps to get this workspace ready.</p>
  </header>

  {#if !allowed}
    <p role="alert">Only workspace administrators can run the setup.</p>
  {:else if failed}
    <p role="alert">Could not load the setup status. Reload the page.</p>
  {:else if status}
    <Stepper steps={SETUP_STEPS} current={step} />
    <section
      aria-labelledby="step-heading"
      class="rounded-2xl border border-border bg-surface p-5 shadow-card"
    >
      <h2 id="step-heading" tabindex="-1" bind:this={heading} class="mb-4 text-base font-semibold">
        {SETUP_STEPS[step].title}
      </h2>
      {#if SETUP_STEPS[step].id === 'workspace'}
        <WorkspaceStep
          slug={SETUP_WORKSPACE}
          name={status.name}
          onsaved={async (name) => {
            if (status) status.name = name;
            await session.loadWorkspace();
            await session.refreshMe();
            await go(step + 1);
          }}
        />
      {:else if SETUP_STEPS[step].id === 'authentication'}
        <AuthStep {status} onnext={() => go(step + 1)} />
      {:else if SETUP_STEPS[step].id === 'connectors'}
        <ConnectorsStep slug={SETUP_WORKSPACE} onnext={() => go(step + 1)} />
      {:else if SETUP_STEPS[step].id === 'networks'}
        <NetworksStep slug={SETUP_WORKSPACE} onnext={() => go(step + 1)} />
      {:else if SETUP_STEPS[step].id === 'summary'}
        <SummaryStep slug={SETUP_WORKSPACE} {status} ondone={done} />
      {/if}
      {#if step > 0}
        <button type="button" class="mt-4 text-sm underline" onclick={() => go(step - 1)}>
          Back
        </button>
      {/if}
    </section>
  {:else}
    <p role="status" class="text-muted">Loading...</p>
  {/if}
</div>
