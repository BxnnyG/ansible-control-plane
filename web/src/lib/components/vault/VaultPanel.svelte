<script lang="ts">
  import { api, type VaultIdentity, type VaultListing, type VaultWriteResult } from '$lib/api';
  import { btn, btnPrimary, card, input, label, td, th } from '$lib/ui';
  import ErrorBox from '$lib/components/ErrorBox.svelte';
  import DecryptModal from './DecryptModal.svelte';

  let {
    ws,
    projectId,
    canManage,
    canDecrypt,
  }: { ws: string; projectId: string; canManage: boolean; canDecrypt: boolean } = $props();

  let listing = $state<VaultListing | null>(null);
  let identities = $state<VaultIdentity[]>([]);
  let loadError = $state<unknown>(null);
  let decrypting = $state<{ path: string; keyPath: string } | null>(null);

  // encrypt form
  let encPath = $state('');
  let encKey = $state('');
  let encPlain = $state('');
  let encIdentity = $state('');
  let encBranch = $state('');
  let encMessage = $state('');
  let encError = $state<unknown>(null);
  let encResult = $state<VaultWriteResult | null>(null);

  // rekey form
  let rekPaths = $state<string[]>([]);
  let rekFrom = $state('');
  let rekTo = $state('');
  let rekBranch = $state('');
  let rekMessage = $state('');
  let rekError = $state<unknown>(null);
  let rekResult = $state<VaultWriteResult | null>(null);
  let busy = $state(false);

  async function load(slug: string, pid: string) {
    loadError = null;
    try {
      listing = await api.vault.list(slug, pid);
      identities = await api.vaultIdentities.list(slug);
    } catch (e) {
      loadError = e;
    }
  }

  $effect(() => {
    void load(ws, projectId);
  });

  function describeResult(r: VaultWriteResult): string {
    return r.no_op
      ? 'Nothing to commit.'
      : `Committed ${r.sha.slice(0, 8)} to ${r.branch}${r.branch_created ? ' (new branch)' : ''}.`;
  }

  async function encrypt(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    encError = null;
    encResult = null;
    try {
      encResult = await api.vault.encrypt(ws, projectId, {
        path: encPath.trim(),
        key_path: encKey.trim(),
        plaintext: encPlain === '' ? null : encPlain,
        identity: encIdentity,
        branch: encBranch.trim(),
        message: encMessage.trim(),
      });
      encPlain = '';
    } catch (err) {
      encError = err;
    } finally {
      busy = false;
    }
  }

  async function rekey(e: SubmitEvent) {
    e.preventDefault();
    busy = true;
    rekError = null;
    rekResult = null;
    try {
      rekResult = await api.vault.rekey(ws, projectId, {
        paths: rekPaths,
        from: rekFrom,
        to: rekTo,
        branch: rekBranch.trim(),
        message: rekMessage.trim(),
      });
    } catch (err) {
      rekError = err;
    } finally {
      busy = false;
    }
  }

  function toggle(p: string, on: boolean) {
    rekPaths = on ? [...rekPaths, p] : rekPaths.filter((x) => x !== p);
  }
</script>

<section class="{card} space-y-4" aria-labelledby="vault-h">
  <div>
    <h2 id="vault-h" class="text-sm font-semibold">Vault</h2>
    <p class="text-sm text-muted">
      Passwords come from <a
        class="underline decoration-border-strong underline-offset-2 hover:decoration-current"
        href="/vault">vault identities</a
      >. Encrypt and rekey commit to a separate branch of the project.
    </p>
  </div>
  <ErrorBox error={loadError} />

  {#if listing}
    {#if listing.vault_files.length === 0 && listing.inline_vault.length === 0}
      <p class="text-sm text-muted">No vault-encrypted content found in the latest revision.</p>
    {:else}
      <table class="w-full">
        <thead
          ><tr
            ><th class={th}>Location</th><th class={th}>Kind</th><th class={th}
              ><span class="sr-only">Actions</span></th
            ></tr
          ></thead
        >
        <tbody class="divide-y divide-border">
          {#each listing.vault_files as f (f.path)}
            <tr>
              <td class={td}><code>{f.path}</code></td>
              <td class={td}>
                file <span class="text-muted"
                  >{f.version}{f.vault_id ? `, id ${f.vault_id}` : ''}</span
                >
              </td>
              <td class="{td} text-right">
                {#if canDecrypt}
                  <button class={btn} onclick={() => (decrypting = { path: f.path, keyPath: '' })}>
                    Decrypt...
                  </button>
                {/if}
              </td>
            </tr>
          {/each}
          {#each listing.inline_vault as v (v.file)}
            {#each v.paths as kp (kp)}
              <tr>
                <td class={td}><code>{v.file}</code> <code class="text-muted">{kp}</code></td>
                <td class={td}>inline</td>
                <td class="{td} text-right">
                  {#if canDecrypt}
                    <button
                      class={btn}
                      onclick={() => (decrypting = { path: v.file, keyPath: kp })}
                    >
                      Decrypt...
                    </button>
                  {/if}
                </td>
              </tr>
            {/each}
          {/each}
        </tbody>
      </table>
    {/if}
  {/if}

  {#if decrypting}
    <DecryptModal
      {ws}
      {projectId}
      path={decrypting.path}
      keyPath={decrypting.keyPath}
      onclose={() => (decrypting = null)}
    />
  {/if}

  {#if canManage && listing}
    <div class="grid gap-6 lg:grid-cols-2">
      <form onsubmit={encrypt} class="space-y-3" aria-label="Encrypt">
        <h3 class="font-medium">Encrypt</h3>
        <div>
          <label class={label} for="enc-path">File path</label>
          <input id="enc-path" class="{input} font-mono" bind:value={encPath} required />
        </div>
        <div>
          <label class={label} for="enc-key">YAML key path (inline value, optional)</label>
          <input id="enc-key" class="{input} font-mono" bind:value={encKey} />
        </div>
        <div>
          <label class={label} for="enc-plain"
            >New plaintext (empty: encrypt existing content)</label
          >
          <textarea
            id="enc-plain"
            class="{input} font-mono"
            rows="4"
            bind:value={encPlain}
            autocomplete="off"
            spellcheck="false"
          ></textarea>
        </div>
        <div>
          <label class={label} for="enc-id">Identity</label>
          <select id="enc-id" class={input} bind:value={encIdentity}>
            <option value="">Default</option>
            {#each identities as i (i.id)}<option value={i.label}>{i.label}</option>{/each}
          </select>
        </div>
        <div>
          <label class={label} for="enc-branch">Branch</label>
          <input id="enc-branch" class={input} bind:value={encBranch} placeholder="acp/vault" />
        </div>
        <div>
          <label class={label} for="enc-msg">Commit message</label>
          <input id="enc-msg" class={input} bind:value={encMessage} required />
        </div>
        <button class={btnPrimary} disabled={busy}>Encrypt and commit</button>
        {#if encResult}
          <p role="status" class="text-sm text-ok">
            {describeResult(encResult)}
          </p>
        {/if}
        <ErrorBox error={encError} />
      </form>

      <form onsubmit={rekey} class="space-y-3" aria-label="Rekey">
        <h3 class="font-medium">Rekey</h3>
        <fieldset>
          <legend class={label}>Files</legend>
          {#each listing.vault_files as f (f.path)}
            <label class="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={rekPaths.includes(f.path)}
                onchange={(e) => toggle(f.path, e.currentTarget.checked)}
              />
              <code>{f.path}</code>
            </label>
          {:else}
            <p class="text-sm text-muted">No vault files.</p>
          {/each}
        </fieldset>
        <div>
          <label class={label} for="rek-from">From identity</label>
          <select id="rek-from" class={input} bind:value={rekFrom} required>
            <option value="">Select...</option>
            {#each identities as i (i.id)}<option value={i.label}>{i.label}</option>{/each}
          </select>
        </div>
        <div>
          <label class={label} for="rek-to">To identity</label>
          <select id="rek-to" class={input} bind:value={rekTo} required>
            <option value="">Select...</option>
            {#each identities as i (i.id)}<option value={i.label}>{i.label}</option>{/each}
          </select>
        </div>
        <div>
          <label class={label} for="rek-branch">Branch</label>
          <input id="rek-branch" class={input} bind:value={rekBranch} placeholder="acp/vault" />
        </div>
        <div>
          <label class={label} for="rek-msg">Commit message</label>
          <input id="rek-msg" class={input} bind:value={rekMessage} required />
        </div>
        <button class={btnPrimary} disabled={busy || rekPaths.length === 0}>
          Rekey and commit
        </button>
        {#if rekResult}
          <p role="status" class="text-sm text-ok">
            {describeResult(rekResult)}
          </p>
        {/if}
        <ErrorBox error={rekError} />
      </form>
    </div>
  {/if}
</section>
