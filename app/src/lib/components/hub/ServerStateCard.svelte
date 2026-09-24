<script lang="ts">
  import { Play, RefreshCw } from "@lucide/svelte";
  import { ServerCard } from "@kombiverselabs/ui/server";

  import type { CanonicalServer } from "#lib/api/registry.js";
  import Button from "#lib/components/ui/Button.svelte";
  import ServerAccessActions from "#lib/components/server-access/ServerAccessActions.svelte";
  import GuidancePanel from "#lib/components/hub/GuidancePanel.svelte";
  import { isManagedRuntimeServer } from "#lib/managed-runtime-server.js";
  import {
    actionableServerOutcome,
    canonicalServerCardStatus,
    canonicalServerMeta,
    canonicalServerStatusLabel,
    dashboardServerMeta,
    serverCardKit,
    serverCardMetrics,
    serverCardStatus,
    serverCardHostname,
    serverDomains,
    serverPrimaryAddress,
    type DashboardServer,
  } from "#lib/server-card-adapter.js";

  interface Props {
    server?: DashboardServer;
    canonical?: CanonicalServer;
    detailsHref: string;
    operationsEvidenceFresh?: boolean;
    assigning?: boolean;
    reconnecting?: boolean;
    canRollout?: boolean;
    rolloutLoading?: boolean;
    canReconnect?: boolean;
    onAssign?: () => void;
    onDeploy?: () => void;
    onReconnect?: () => void;
    onOpenDetails?: () => void;
    onOutcomeRetry?: () => void;
  }

  let {
    server,
    canonical,
    detailsHref,
    operationsEvidenceFresh = false,
    assigning = false,
    reconnecting = false,
    canRollout = false,
    rolloutLoading = false,
    canReconnect = false,
    onAssign,
    onDeploy,
    onReconnect,
    onOpenDetails,
    onOutcomeRetry,
  }: Props = $props();

  const outcome = $derived(actionableServerOutcome(server, canonical));
  const managedRuntime = $derived(
    server ? isManagedRuntimeServer(server) : false,
  );
  const showAssign = $derived(
    Boolean(
      server &&
      server.assignment === "unassigned" &&
      server.approved &&
      server.assignable !== false,
    ),
  );
  const resourceId = $derived(canonical?.id || server?.id);
  const resourceName = $derived(
    server ? serverCardHostname(server, canonical) : canonical?.name,
  );
</script>

<article class="space-y-2" data-testid="server-state-card">
  {#if server}
    {@const domains = serverDomains(server)}
    {@const cardProps = {
      "data-testid": "server-card",
      hostname: serverCardHostname(server, canonical),
      meta: dashboardServerMeta(server, canonical),
      status: canonical
        ? canonicalServerCardStatus(
            canonical.connection.state,
            canonical.health.state,
          )
        : serverCardStatus(server),
      statusLabel: canonical
        ? canonicalServerStatusLabel(canonical)
        : undefined,
      metrics: serverCardMetrics(server),
      address: serverPrimaryAddress(server),
      domain: domains[0],
      domainExtraCount: domains.length > 1 ? domains.length - 1 : undefined,
      kit:
        canonical?.node_role === "substrate"
          ? undefined
          : serverCardKit(server),
      note:
        server.precheck_state === "failed"
          ? "Node prechecks failed"
          : undefined,
      noteTone: "error" as const,
      detailsHref,
    }}
    {#if showAssign || managedRuntime}
      <ServerCard {...cardProps}>
        {#snippet actions()}
          <div class="flex w-full flex-wrap items-center gap-2">
            {#if showAssign}
              <Button
                variant="secondary"
                size="sm"
                class="w-full"
                testId="assign-server-button"
                disabled={!operationsEvidenceFresh || assigning}
                onclick={onAssign}
              >
                {assigning
                  ? "Assigning..."
                  : "Assign to this StackKit deployment"}
              </Button>
            {/if}
            {#if managedRuntime}
              <div
                class="flex flex-wrap gap-2"
                data-testid="managed-runtime-action-row"
              >
                <Button
                  variant="primary"
                  size="sm"
                  testId="deploy-stackkit-button"
                  onclick={onDeploy}
                  disabled={!canRollout || rolloutLoading}
                >
                  <Play class="h-4 w-4" />
                  {rolloutLoading ? "Starting..." : "Deploy StackKit"}
                </Button>
                <ServerAccessActions
                  serverId={canonical?.id || server.id}
                  serverName={serverCardHostname(server, canonical)}
                  unavailable={canonical?.connection.state === "offline" ||
                    canonical?.connection.state === "revoked"}
                  unavailableReason={canonical
                    ? `Node connection: ${canonicalServerStatusLabel(canonical)}`
                    : "Canonical managed access is not available yet."}
                />
                <Button
                  variant="secondary"
                  size="sm"
                  testId="open-server-details-link"
                  onclick={onOpenDetails}
                >
                  Open Node details
                </Button>
                {#if server.lease_id && canReconnect}
                  <Button
                    variant="secondary"
                    size="sm"
                    testId="reconnect-server-button"
                    onclick={onReconnect}
                    disabled={!operationsEvidenceFresh || reconnecting}
                  >
                    <RefreshCw class="h-4 w-4" />
                    {reconnecting ? "Reconnecting..." : "Reconnect"}
                  </Button>
                {/if}
              </div>
            {/if}
          </div>
        {/snippet}
      </ServerCard>
    {:else}
      <ServerCard {...cardProps} />
    {/if}
  {:else if canonical}
    <ServerCard
      data-testid="server-card"
      hostname={canonical.name}
      meta={canonicalServerMeta(canonical)}
      status={canonicalServerCardStatus(
        canonical.connection.state,
        canonical.health.state,
      )}
      statusLabel={canonicalServerStatusLabel(canonical)}
      note="Current telemetry is not available for this canonical Node."
      noteTone="warning"
      {detailsHref}
    >
      {#snippet actions()}
        <ServerAccessActions
          serverId={canonical.id}
          serverName={canonical.name}
          unavailable={canonical.connection.state === "offline" ||
            canonical.connection.state === "revoked"}
          unavailableReason={`Node connection: ${canonicalServerStatusLabel(canonical)}`}
        />
      {/snippet}
    </ServerCard>
  {/if}

  {#if outcome}
    <GuidancePanel
      {outcome}
      surface="stacks.hub.server"
      {resourceId}
      {resourceName}
      onRetry={onOutcomeRetry}
      retrying={reconnecting}
      compact
    />
  {/if}
</article>
