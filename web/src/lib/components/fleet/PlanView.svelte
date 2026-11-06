<script lang="ts">
  import type { GroupRulePlan } from '$lib/api';
  import { planHeadline } from '$lib/groupRules';

  let { plan, group }: { plan: GroupRulePlan; group: string } = $props();
</script>

<div class="space-y-2 text-sm">
  <p class="font-medium">{planHeadline(plan)}</p>
  <p class="text-xs text-muted">
    {plan.matched} matched, {plan.keep} unchanged{plan.untrack.length
      ? `, ${plan.untrack.length} manual member${plan.untrack.length === 1 ? '' : 's'} left alone`
      : ''}.
  </p>
  {#if plan.group_create}
    <p>Group <code>{group}</code> will be created.</p>
  {/if}
  {#if plan.add.length}
    <ul aria-label="Additions" class="font-mono text-xs">
      {#each plan.add as a (a.fleet_host_id)}
        <li class="text-green-700 dark:text-green-400">
          + {a.name}{a.create_host
            ? ` (new host${a.ansible_host ? `, ansible_host ${a.ansible_host}` : ''})`
            : ''}
        </li>
      {/each}
    </ul>
  {/if}
  {#if plan.remove.length}
    <ul aria-label="Removals" class="font-mono text-xs">
      {#each plan.remove as r (r)}
        <li class="text-red-600 dark:text-red-400">- {r}</li>
      {/each}
    </ul>
  {/if}
  {#if plan.skipped.length}
    <div>
      <p class="text-xs text-muted">Skipped:</p>
      <ul aria-label="Skipped" class="text-xs text-muted">
        {#each plan.skipped as s (s.fleet_host_id)}
          <li>{s.name}: {s.reason}</li>
        {/each}
      </ul>
    </div>
  {/if}
</div>
