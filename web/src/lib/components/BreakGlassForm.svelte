<script lang="ts">
  import { api, ApiError } from '$lib/api';
  import FormError from './FormError.svelte';

  let { onsuccess }: { onsuccess: () => void } = $props();

  let username = $state('');
  let password = $state('');
  let totp = $state('');
  let busy = $state(false);
  let error = $state<string | null>(null);
  let totpInput: HTMLInputElement | undefined = $state();

  const totpValid = $derived(/^\d{6}$/.test(totp));

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (!totpValid) {
      error = 'Enter the 6-digit code from your authenticator app.';
      totpInput?.focus();
      return;
    }
    busy = true;
    error = null;
    try {
      await api.breakGlassLogin({ username: username.trim(), password, totp });
      password = '';
      totp = '';
      onsuccess();
    } catch (err) {
      password = '';
      totp = '';
      if (err instanceof ApiError && err.status === 429) {
        error = 'Too many attempts. Please wait and try again later.';
      } else if (err instanceof ApiError && err.status === 401) {
        error = 'Invalid credentials.';
      } else {
        error = 'Sign-in failed. Please try again.';
      }
    } finally {
      busy = false;
    }
  }
</script>

<form class="space-y-3" onsubmit={submit} aria-describedby="bg-error">
  <div>
    <label class="block text-sm font-medium" for="bg-username">Username</label>
    <input
      id="bg-username"
      name="username"
      type="text"
      autocomplete="username"
      required
      bind:value={username}
      class="mt-1 w-full rounded-md border border-border bg-surface px-3 py-2"
    />
  </div>
  <div>
    <label class="block text-sm font-medium" for="bg-password">Password</label>
    <input
      id="bg-password"
      name="password"
      type="password"
      autocomplete="current-password"
      required
      bind:value={password}
      class="mt-1 w-full rounded-md border border-border bg-surface px-3 py-2"
    />
  </div>
  <div>
    <label class="block text-sm font-medium" for="bg-totp">Authenticator code</label>
    <input
      id="bg-totp"
      name="totp"
      type="text"
      inputmode="numeric"
      autocomplete="one-time-code"
      pattern={'[0-9]{6}'}
      maxlength="6"
      required
      bind:value={totp}
      bind:this={totpInput}
      class="mt-1 w-full rounded-md border border-border bg-surface px-3 py-2 font-mono tracking-widest"
    />
  </div>
  <FormError id="bg-error" message={error} />
  <button
    type="submit"
    disabled={busy}
    class="w-full rounded-md border border-border px-4 py-2 hover:bg-surface-2 disabled:opacity-60"
  >
    {busy ? 'Signing in...' : 'Sign in'}
  </button>
</form>
