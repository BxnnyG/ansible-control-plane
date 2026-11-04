<script lang="ts">
  import { input, label } from '$lib/ui';

  let {
    id,
    value = $bindable(''),
    disabled = false,
  }: { id: string; value: string; disabled?: boolean } = $props();
</script>

<!-- Light editor: plain textarea; YAML validity is reported by the API on save.
     Vault values stay in their encrypted "!vault" form and must be kept as is. -->
<div>
  <label class={label} for={id}>Variables (YAML)</label>
  <textarea
    {id}
    class="{input} font-mono"
    rows="6"
    bind:value
    {disabled}
    spellcheck="false"
    autocomplete="off"
    placeholder="ansible_user: deploy"
  ></textarea>
  {#if value.includes('!vault')}
    <p class="mt-1 text-xs text-muted">
      Contains vault-encrypted values. Keep the <code>!vault</code> blocks unchanged.
    </p>
  {/if}
</div>
