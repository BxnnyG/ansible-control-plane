<script lang="ts">
  import BrandMark from '$lib/components/BrandMark.svelte';
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { api, oidcLoginUrl, type AuthConfig } from '$lib/api';
  import { loginErrorMessage } from '$lib/loginErrors';
  import { safeReturnTo } from '$lib/returnTo';
  import { session } from '$lib/session.svelte';
  import BreakGlassForm from '$lib/components/BreakGlassForm.svelte';

  let config = $state<AuthConfig | null>(null);
  let loadFailed = $state(false);
  let showBreakGlass = $state(false);

  const returnTo = $derived(safeReturnTo(page.url.searchParams.get('return_to')));
  const errorText = $derived(loginErrorMessage(page.url.searchParams.get('error')));

  onMount(async () => {
    try {
      config = await api.authConfig();
    } catch {
      loadFailed = true;
    }
  });

  // The layout redirects to return_to once the session is loaded.
  const onsuccess = () => session.load();
</script>

<svelte:head><title>Sign in - Ansible Control Plane</title></svelte:head>

<section
  class="rise w-full max-w-sm rounded-3xl border border-border bg-surface p-6 shadow-card sm:p-8"
>
  <div class="mb-6 flex flex-col items-center text-center">
    <span class="scale-125"><BrandMark /></span>
    <p class="mt-4 text-sm font-semibold tracking-wide text-muted uppercase">Control Plane</p>
    <h1 class="mt-1 text-2xl font-semibold">Sign in</h1>
  </div>

  <p role="alert" class="mt-3 min-h-5 text-sm text-bad">
    {errorText ?? ''}
  </p>

  {#if loadFailed}
    <p role="alert" class="mt-3 text-sm">Could not load the sign-in options. Reload the page.</p>
  {:else if !config}
    <p role="status" class="mt-3 text-sm text-muted">Loading...</p>
  {:else if !config.oidc && !config.breakglass}
    <p class="mt-3 text-sm">No sign-in method is enabled.</p>
  {:else}
    <div class="mt-3 space-y-4">
      {#if config.oidc}
        <a
          href={oidcLoginUrl(returnTo)}
          class="bg-brand flex min-h-11 w-full items-center justify-center rounded-lg px-4 py-2 text-center font-medium shadow-sm hover:brightness-110"
        >
          Sign in with SSO
        </a>
      {/if}

      {#if config.breakglass}
        {#if config.oidc}
          <div>
            <button
              type="button"
              class="text-sm underline"
              aria-expanded={showBreakGlass}
              aria-controls="breakglass-panel"
              onclick={() => (showBreakGlass = !showBreakGlass)}
            >
              Break-glass sign-in
            </button>
          </div>
        {/if}
        <div id="breakglass-panel" hidden={config.oidc && !showBreakGlass}>
          <BreakGlassForm {onsuccess} />
        </div>
      {/if}
    </div>
  {/if}
</section>
