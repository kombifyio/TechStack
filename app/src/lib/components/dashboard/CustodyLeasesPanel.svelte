<script lang="ts">
  import { tr, trn } from "#lib/i18n.svelte.js";
  import { AlertTriangle } from "@lucide/svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import type { StackCustodyLease } from "#lib/api/stacks.js";
  import { statusLabel } from "#lib/server-card-adapter.js";

  // Leases without a machine. They stay visible because they can still cost
  // money, but they are never presented as servers.
  interface Props {
    custodyLeases: StackCustodyLease[];
    cleaningCustodyLeaseId: string | null;
    resolvingCustodyLeaseId: string | null;
    decommissionError: string | null;
    decommissionCustodyLease: (leaseId: string) => void;
    resolveCustodyLease: (lease: StackCustodyLease) => void;
  }

  let {
    custodyLeases,
    cleaningCustodyLeaseId,
    resolvingCustodyLeaseId,
    decommissionError,
    decommissionCustodyLease,
    resolveCustodyLease,
  }: Props = $props();

  // The label explains why a lease has no server, so an operator can tell an
  // expired lease from one whose VM was deleted at the provider.
  const custodyReasonLabels: Record<string, string> = $derived({
    provider_reports_absent: tr("ui.custodyLeasesPanel.vmNoLongerExistsAt"),
    lease_cancelled: tr("ui.custodyLeasesPanel.leaseCancelled"),
    lease_archived: tr("ui.custodyLeasesPanel.leaseArchived"),
    enrollment_failed: tr("ui.custodyLeasesPanel.nodeNeverFinishedEnrolling"),
    no_execution_authority: tr("ui.custodyLeasesPanel.noExecutionAuthorityLegacyOr"),
    never_observed: tr("ui.custodyLeasesPanel.neverObserved"),
  });
  function custodyReasonLabel(reason: string): string {
    return custodyReasonLabels[reason] ?? statusLabel(reason);
  }

  function custodyAllows(
    lease: StackCustodyLease,
    action: "decommission" | "resolve_custody",
  ): boolean {
    return lease.allowed_actions?.includes(action) === true;
  }
</script>

<div
  class="rounded-lg border border-warning/40 bg-warning/5 p-4"
  data-testid="custody-leases"
>
  <div class="mb-3 flex items-center gap-2">
    <AlertTriangle class="h-4 w-4 shrink-0 text-warning" />
    <h3 class="text-sm font-semibold text-foreground">
      {trn("ui.custody.leasesWithoutNode", custodyLeases.length)}
    </h3>
  </div>
  <p class="mb-3 text-xs text-muted-foreground">
    {tr("ui.custody.explainer")}
  </p>
  <ul class="space-y-2">
    {#each custodyLeases as lease (lease.lease_id)}
      <li
        data-kx="plate"
        class="flex flex-wrap items-center gap-x-2 gap-y-1 px-3 py-2 text-sm"
        data-testid="custody-lease"
      >
        <span class="font-medium text-foreground">{lease.label}</span>
        {#if lease.provider}
          <span class="font-mono text-xs text-muted-foreground"
            >{lease.provider}</span
          >
        {/if}
        <span
          data-kx="status"
          data-status="warn"
          class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
          >{custodyReasonLabel(lease.reason)}</span
        >
        {#if lease.last_known_ip}
          <span
            class="font-mono text-xs text-muted-foreground"
            title={tr("ui.custody.lastKnownAddress")}
            >{tr("ui.custody.wasAddress", { ip: lease.last_known_ip })}</span
          >
        {/if}
        <span
          class="ml-auto font-mono text-[11px] text-muted-foreground"
          >{lease.lease_id}</span
        >
        {#if custodyAllows(lease, "decommission")}
          <Button
            variant="destructive"
            size="sm"
            testId="decommission-custody-lease"
            onclick={() => decommissionCustodyLease(lease.lease_id)}
            disabled={cleaningCustodyLeaseId === lease.lease_id}
          >
            {cleaningCustodyLeaseId === lease.lease_id
              ? tr("ui.custodyLeasesPanel.decommissioning")
              : tr("ui.stacksId.decommission")}
          </Button>
        {/if}
        {#if custodyAllows(lease, "resolve_custody")}
          <Button
            variant="secondary"
            size="sm"
            testId="resolve-custody-lease"
            onclick={() => resolveCustodyLease(lease)}
            disabled={resolvingCustodyLeaseId === lease.lease_id}
          >
            {resolvingCustodyLeaseId === lease.lease_id
              ? tr("ui.custodyLeasesPanel.resolving")
              : tr("ui.homelabDashboardPage.resolveRecord")}
          </Button>
        {/if}
      </li>
    {/each}
  </ul>
  {#if decommissionError}
    <p class="mt-3 text-sm text-destructive">{decommissionError}</p>
  {/if}
</div>
