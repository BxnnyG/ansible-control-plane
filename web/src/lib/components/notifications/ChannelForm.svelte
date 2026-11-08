<script lang="ts">
  import {
    api,
    type NotificationChannel,
    type NotificationChannelDraft,
    type SecretMeta,
  } from '$lib/api';
  import { describeError } from '$lib/errors';
  import {
    CHANNEL_TYPES,
    buildChannelConfig,
    fieldsFromConfig,
    secretRequired,
    usableSecrets,
    validateChannel,
    type ChannelType,
  } from '$lib/notifications';
  import {
    FIELD_HELP,
    GUIDES,
    NTFY_DEFAULT_URL,
    credentialLabel,
    credentialSecretName,
    credentialSecretType,
    SMTP_DEFAULT_PORTS,
    smtpPortHint,
  } from '$lib/notificationGuides';
  import { btn, btnPrimary, input, label } from '$lib/ui';
  import FormError from '$lib/components/FormError.svelte';

  let {
    ws,
    channel = null,
    secrets,
    ondone,
    oncancel,
  }: {
    ws: string;
    /** Channel being edited; null creates a new one. */
    channel?: NotificationChannel | null;
    secrets: SecretMeta[];
    ondone: () => void;
    oncancel: () => void;
  } = $props();

  // Initial values are captured once; the parent re-mounts the form per channel.
  // svelte-ignore state_referenced_locally
  let type = $state<ChannelType>(channel?.type ?? 'webhook');
  // svelte-ignore state_referenced_locally
  let name = $state(channel?.name ?? '');
  // svelte-ignore state_referenced_locally
  let f = $state(fieldsFromConfig(channel?.type ?? 'webhook', channel?.config));
  // svelte-ignore state_referenced_locally
  let secretId = $state<string | null>(channel?.secret_id ?? null);
  // svelte-ignore state_referenced_locally
  let caPem = $state(channel?.tls_ca_pem ?? '');
  // svelte-ignore state_referenced_locally
  let skipVerify = $state(channel?.tls_skip_verify ?? false);
  // svelte-ignore state_referenced_locally
  let enabled = $state(channel?.enabled ?? true);
  type CredMode = 'none' | 'existing' | 'new';
  // svelte-ignore state_referenced_locally
  let credMode = $state<CredMode>(channel?.secret_id ? 'existing' : 'none');
  let newCred = $state('');
  let replaceCred = $state('');
  let error = $state<string | null>(null);
  let busy = $state(false);
  let testing = $state(false);
  let testResult = $state<{ ok: boolean; text: string } | null>(null);

  const options = $derived(usableSecrets(secrets));
  const needsSecret = $derived(secretRequired(type, f));
  const usesTls = $derived(type !== 'smtp' || f.security !== 'none');
  const guide = $derived(GUIDES[type]);
  const help = $derived(FIELD_HELP[type]);
  const credLabel = $derived(credentialLabel(type, type === 'ntfy' ? f.ntfyAuth : f.auth));
  const hasCred = $derived(
    credMode === 'new' ? newCred !== '' : credMode === 'existing' && secretId !== null,
  );

  function typeChanged() {
    testResult = null;
    if (type === 'ntfy' && !f.url.trim()) f.url = NTFY_DEFAULT_URL;
  }
  // svelte-ignore state_referenced_locally
  if (!channel && type === 'ntfy' && !f.url) f.url = NTFY_DEFAULT_URL;

  function draftBody(): NotificationChannelDraft {
    const d: NotificationChannelDraft = {
      type,
      config: buildChannelConfig(type, f),
      tls_ca_pem: caPem.trim(),
      tls_skip_verify: skipVerify,
    };
    if (credMode === 'new' && newCred !== '') d.secret_value = newCred;
    else if (credMode === 'existing' && secretId) {
      if (replaceCred !== '') d.secret_value = replaceCred;
      else {
        d.secret_id = secretId;
        if (channel) d.channel_id = channel.id;
      }
    }
    return d;
  }

  async function sendTest() {
    testResult = null;
    error = validateChannel(type, name.trim() || 'draft', f, hasCred);
    if (error) return;
    testing = true;
    try {
      const r = await api.notifications.channels.testDraft(ws, draftBody());
      testResult = r.ok
        ? { ok: true, text: 'Test message sent.' }
        : { ok: false, text: `Test failed: ${r.error}` };
    } catch (err) {
      testResult = { ok: false, text: describeError(err) };
    } finally {
      testing = false;
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    error = validateChannel(type, name, f, hasCred);
    if (error) return;
    busy = true;
    let createdSecret: string | null = null;
    try {
      let sid: string | null = credMode === 'existing' ? secretId : null;
      if (credMode === 'new') {
        const m = await api.secrets.create(ws, {
          name: credentialSecretName(name),
          type: credentialSecretType(type),
          value: newCred,
          description: `Credential of notification channel ${name.trim()}`,
        });
        createdSecret = m.id;
        sid = m.id;
      } else if (credMode === 'existing' && sid && replaceCred !== '') {
        await api.secrets.update(ws, sid, { value: replaceCred });
      }
      const common = {
        name: name.trim(),
        config: buildChannelConfig(type, f),
        secret_id: sid,
        tls_ca_pem: caPem.trim(),
        tls_skip_verify: skipVerify,
        enabled,
      };
      if (channel) await api.notifications.channels.update(ws, channel.id, common);
      else await api.notifications.channels.create(ws, { type, ...common });
      newCred = '';
      replaceCred = '';
      ondone();
    } catch (err) {
      error = describeError(err);
      if (createdSecret) {
        // Do not leave an orphaned credential behind when saving the channel failed.
        await api.secrets.remove(ws, createdSecret).catch(() => {});
      }
    } finally {
      busy = false;
    }
  }
</script>

<form onsubmit={submit} class="space-y-3" aria-label={channel ? 'Edit channel' : 'New channel'}>
  <div>
    <label class={label} for="nc-type">Type</label>
    <select
      id="nc-type"
      class={input}
      bind:value={type}
      onchange={typeChanged}
      disabled={!!channel}
    >
      {#each CHANNEL_TYPES as t (t.type)}<option value={t.type}>{t.label}</option>{/each}
    </select>
  </div>
  <div>
    <label class={label} for="nc-name">Name</label>
    <input id="nc-name" class={input} bind:value={name} required />
  </div>

  <details class="rounded border border-border bg-surface-2 p-3 text-sm" data-testid="setup-guide">
    <summary class="cursor-pointer font-medium">Setup guide: {guide.title}</summary>
    <p class="mt-2">{guide.intro}</p>
    <ol class="mt-2 list-decimal space-y-1 pl-5">
      {#each guide.steps as step (step)}<li>{step}</li>{/each}
    </ol>
    {#if guide.example}
      <p class="mt-2 text-xs text-muted">{guide.example.caption}</p>
      <pre class="overflow-x-auto rounded bg-surface p-2 text-xs">{guide.example.code}</pre>
    {/if}
    <ul class="mt-2 list-disc space-y-1 pl-5 text-muted">
      {#each guide.notes as note (note)}<li>{note}</li>{/each}
    </ul>
  </details>

  {#if type === 'webhook'}
    <div>
      <label class={label} for="nc-url">URL</label>
      <input id="nc-url" class={input} bind:value={f.url} placeholder="https://host/path" />
      <p class="mt-1 text-xs text-muted">{help.url}</p>
    </div>
    <div>
      <label class={label} for="nc-auth">Authentication</label>
      <select id="nc-auth" class={input} bind:value={f.auth}>
        <option value="hmac">HMAC signature (secret is the key)</option>
        <option value="bearer">Bearer token (secret is the token)</option>
      </select>
      <p class="mt-1 text-xs text-muted">{help.auth}</p>
    </div>
  {:else if type === 'matrix'}
    <div>
      <label class={label} for="nc-hs">Homeserver URL</label>
      <input
        id="nc-hs"
        class={input}
        bind:value={f.homeserver}
        placeholder="https://matrix.example"
      />
      <p class="mt-1 text-xs text-muted">{help.homeserver}</p>
    </div>
    <div>
      <label class={label} for="nc-room">Room ID</label>
      <input id="nc-room" class={input} bind:value={f.roomId} placeholder="!room:example" />
      <p class="mt-1 text-xs text-muted">{help.roomId}</p>
    </div>
  {:else if type === 'smtp'}
    <div>
      <label class={label} for="nc-host">Host</label>
      <input id="nc-host" class={input} bind:value={f.host} placeholder="smtp.example.org" />
      <p class="mt-1 text-xs text-muted">{help.host}</p>
    </div>
    <div>
      <label class={label} for="nc-port">Port (optional)</label>
      <input
        id="nc-port"
        class={input}
        inputmode="numeric"
        bind:value={f.port}
        placeholder={String(SMTP_DEFAULT_PORTS[f.security])}
      />
      <p class="mt-1 text-xs text-muted">{help.port} {smtpPortHint(f.security)}</p>
    </div>
    <div>
      <label class={label} for="nc-sec">Security</label>
      <select id="nc-sec" class={input} bind:value={f.security}>
        <option value="starttls">STARTTLS</option>
        <option value="tls">TLS</option>
        <option value="none">None (plaintext)</option>
      </select>
      <p class="mt-1 text-xs text-muted">{help.security}</p>
    </div>
    {#if f.security === 'none'}
      <label class="flex items-center gap-2 text-sm">
        <input type="checkbox" bind:checked={f.insecure} /> I understand that mail is sent unencrypted
      </label>
    {/if}
    <div>
      <label class={label} for="nc-user">Username (optional)</label>
      <input
        id="nc-user"
        class={input}
        bind:value={f.username}
        autocomplete="off"
        data-1p-ignore
        data-lpignore="true"
      />
      <p class="mt-1 text-xs text-muted">{help.username}</p>
    </div>
    <div>
      <label class={label} for="nc-from">From</label>
      <input id="nc-from" class={input} bind:value={f.from} placeholder="acp@example.org" />
      <p class="mt-1 text-xs text-muted">{help.from}</p>
    </div>
    <div>
      <label class={label} for="nc-to">To (comma separated)</label>
      <input id="nc-to" class={input} bind:value={f.to} placeholder="ops@example.org" />
      <p class="mt-1 text-xs text-muted">{help.to}</p>
    </div>
  {:else if type === 'gotify'}
    <div>
      <label class={label} for="nc-url">URL</label>
      <input id="nc-url" class={input} bind:value={f.url} placeholder="https://gotify.example" />
      <p class="mt-1 text-xs text-muted">{help.url}</p>
    </div>
    <div>
      <label class={label} for="nc-prio">Priority (optional)</label>
      <input
        id="nc-prio"
        class={input}
        inputmode="numeric"
        bind:value={f.priority}
        placeholder="5"
      />
      <p class="mt-1 text-xs text-muted">{help.priority}</p>
    </div>
  {:else if type === 'ntfy'}
    <div>
      <label class={label} for="nc-url">URL</label>
      <input id="nc-url" class={input} bind:value={f.url} placeholder={NTFY_DEFAULT_URL} />
      <p class="mt-1 text-xs text-muted">{help.url}</p>
    </div>
    <div>
      <label class={label} for="nc-nauth">Authentication</label>
      <select id="nc-nauth" class={input} bind:value={f.ntfyAuth}>
        <option value="token">Access token</option>
        <option value="basic">Username and password</option>
      </select>
      <p class="mt-1 text-xs text-muted">{help.auth}</p>
    </div>
    {#if f.ntfyAuth === 'basic'}
      <div>
        <label class={label} for="nc-nuser">Username</label>
        <input
          id="nc-nuser"
          class={input}
          bind:value={f.username}
          autocomplete="off"
          data-1p-ignore
          data-lpignore="true"
        />
        <p class="mt-1 text-xs text-muted">{help.username}</p>
      </div>
    {/if}
    <div>
      <label class={label} for="nc-topic">Topic</label>
      <input id="nc-topic" class={input} bind:value={f.topic} placeholder="acp-7f3k9x2m1q8d4v6z" />
      <p class="mt-1 text-xs text-muted">{help.topic}</p>
    </div>
    <div>
      <label class={label} for="nc-prio">Priority (optional, 1-5)</label>
      <input
        id="nc-prio"
        class={input}
        inputmode="numeric"
        bind:value={f.priority}
        placeholder="3"
      />
      <p class="mt-1 text-xs text-muted">{help.priority}</p>
    </div>
  {/if}

  <fieldset class="space-y-2">
    <legend class={label}>Credential{needsSecret ? '' : ' (optional)'}</legend>
    <div class="flex flex-wrap gap-4 text-sm">
      <label class="flex items-center gap-1">
        <input type="radio" bind:group={credMode} value="none" /> None
      </label>
      <label class="flex items-center gap-1">
        <input type="radio" bind:group={credMode} value="existing" /> Existing secret
      </label>
      <label class="flex items-center gap-1">
        <input type="radio" bind:group={credMode} value="new" /> Enter new credential
      </label>
    </div>
    {#if credMode === 'existing'}
      <select id="nc-secret" class={input} bind:value={secretId} aria-label="Existing secret">
        <option value={null}>Choose a secret</option>
        {#each options as s (s.id)}<option value={s.id}>{s.name} ({s.type})</option>{/each}
      </select>
      {#if secretId}
        <label class={label} for="nc-replace">Replace value (optional)</label>
        <input
          id="nc-replace"
          class={input}
          type="password"
          autocomplete="new-password"
          data-1p-ignore
          data-lpignore="true"
          bind:value={replaceCred}
          placeholder="Leave empty to keep the stored value"
        />
        <p class="text-xs text-muted">
          Replacing the value updates the shared secret, so every other user of it changes too.
        </p>
      {/if}
    {:else if credMode === 'new'}
      <label class={label} for="nc-newcred">{credLabel}</label>
      <input
        id="nc-newcred"
        class={input}
        type="password"
        autocomplete="new-password"
        bind:value={newCred}
        data-1p-ignore
        data-lpignore="true"
      />
      {#if type === 'smtp'}
        <p class="text-xs text-muted">{help.credential}</p>
      {/if}
      <p class="text-xs text-muted">
        Stored encrypted as secret "{credentialSecretName(name)}" ({credentialSecretType(type)});
        the value is never shown again.
      </p>
    {/if}
  </fieldset>

  {#if usesTls}
    <div>
      <label class={label} for="nc-ca">Custom CA certificate, PEM (optional)</label>
      <textarea id="nc-ca" class="{input} font-mono text-xs" rows="3" bind:value={caPem}></textarea>
    </div>
    <label class="flex items-center gap-2 text-sm">
      <input type="checkbox" bind:checked={skipVerify} /> Skip TLS verification (insecure)
    </label>
  {/if}
  <label class="flex items-center gap-2 text-sm">
    <input type="checkbox" bind:checked={enabled} /> Enabled
  </label>

  <FormError id="nc-error" message={error} />
  {#if testResult}
    <p
      role="status"
      class="text-sm {testResult.ok
        ? 'text-green-700 dark:text-green-400'
        : 'text-red-700 dark:text-red-400'}"
    >
      {testResult.text}
    </p>
  {/if}
  <div class="flex gap-2">
    <button class={btnPrimary} disabled={busy}>{channel ? 'Save' : 'Create'}</button>
    <button type="button" class={btn} disabled={testing || busy} onclick={sendTest}>
      {testing ? 'Sending...' : 'Send test'}
    </button>
    <button type="button" class={btn} onclick={oncancel}>Cancel</button>
  </div>
</form>
