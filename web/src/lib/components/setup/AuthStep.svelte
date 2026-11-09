<script lang="ts">
  import type { SetupStatus } from '$lib/api';

  let { status, onnext }: { status: SetupStatus; onnext: () => void } = $props();
</script>

<div class="space-y-4">
  <dl class="grid max-w-xl grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
    <dt class="text-muted">Single sign-on (OIDC)</dt>
    <dd>{status.auth.oidc ? 'Configured' : 'Not configured'}</dd>
    {#if status.auth.oidc}
      <dt class="text-muted">Identity provider</dt>
      <dd class="font-mono">{status.auth.issuer_host}</dd>
    {/if}
    <dt class="text-muted">Break-glass login</dt>
    <dd>{status.auth.breakglass ? 'Enabled' : 'Disabled'}</dd>
  </dl>

  {#if status.auth.oidc}
    <div>
      <h3 class="text-sm font-medium">Role mapping</h3>
      <table class="mt-2 text-sm">
        <caption class="sr-only">IdP roles and the workspace roles they grant</caption>
        <thead>
          <tr class="text-left text-muted">
            <th scope="col" class="pr-8 font-normal">IdP role</th>
            <th scope="col" class="font-normal">Workspace role</th>
          </tr>
        </thead>
        <tbody>
          {#each status.auth.role_mappings as m (m.idp_role)}
            <tr>
              <td class="pr-8 font-mono">{m.idp_role}</td>
              <td>{m.role}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}

  {#if status.auth.breakglass}
    <p class="max-w-xl rounded-md border border-border bg-surface-2 p-3 text-sm">
      Keep the break-glass authenticator (TOTP) and its recovery material in a safe place. It is the
      only way in when the identity provider is unavailable.
    </p>
  {/if}

  <p class="text-sm text-muted">
    This step is read-only; authentication is configured on the server.
  </p>

  <button type="button" class="bg-brand min-h-11 rounded-lg px-4 py-2 font-medium" onclick={onnext}>
    Continue
  </button>
</div>
