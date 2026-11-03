<script lang="ts">
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

<section class="w-full max-w-sm rounded-xl border border-border bg-surface p-6">
  <h1 class="text-xl font-semibold">Sign in</h1>

  <p role="alert" class="mt-3 min-h-5 text-sm text-red-600 dark:text-red-400">
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
          class="block w-full rounded-md bg-accent px-4 py-2 text-center text-accent-fg"
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
