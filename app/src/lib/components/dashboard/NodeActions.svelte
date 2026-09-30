<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { Play, RefreshCw } from "@lucide/svelte";

  import Button from "#lib/components/ui/Button.svelte";
  import ServerAccessActions from "#lib/components/server-access/ServerAccessActions.svelte";
  import GuidancePanel from "#lib/components/hub/GuidancePanel.svelte";
  import { isManagedRuntimeServer } from "#lib/managed-runtime-server.js";
  import {
    actionableServerOutcome,
    canonicalServerStatusLabel,
    type DashboardServer,
  } from "#lib/server-card-adapter.js";
  import type { NodeView } from "#lib/dashboard/homelab-model.js";

  interface Props {
    node: NodeView;
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
  }

  let {
    node,
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
  }: Props = $props();

  const server = $derived<DashboardServer | undefined>(node.server);
  const canonical = $derived(node.canonical);
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
  const accessUnavailable = $derived(
    canonical?.connection.state === "offline" ||
      canonical?.connection.state === "revoked",
  );
</script>

{#if server?.precheck_state === "failed"}
  <p class="text-xs text-destructive">{tr("ui.node.precheckFailed")}</p>
{/if}
{#if !server && canonical}
  <p class="text-xs text-warning">
    {tr("ui.node.noTelemetry")}
  </p>
{/if}

{#if showAssign}
  <Button
    variant="secondary"
    size="sm"
    class="w-full"
    testId="assign-server-button"
    disabled={!operationsEvidenceFresh || assigning}
    onclick={onAssign}
  >
    {assigning ? tr("ui.nodeActions.assigning") : tr("ui.nodeActions.assignToThisStackkitDeployment")}
  </Button>
{/if}

{#if managedRuntime && server}
  <div class="flex flex-wrap gap-2" data-testid="managed-runtime-action-row">
    <Button
      variant="primary"
      size="sm"
      testId="deploy-stackkit-button"
      onclick={onDeploy}
      disabled={!canRollout || rolloutLoading}
    >
      <Play class="h-4 w-4" />
      {rolloutLoading ? tr("ui.homelabDashboardPage.starting") : tr("ui.homelabDashboardPage.deployStackkit")}
    </Button>
    <ServerAccessActions
      serverId={canonical?.id || server.id}
      serverName={node.name}
      unavailable={accessUnavailable}
      unavailableReason={canonical
        ? tr("ui.nodeActions.connection", {
            state: canonicalServerStatusLabel(canonical),
          })
        : tr("ui.nodeActions.canonicalManagedAccessIsNot")}
    />
    <Button
      variant="secondary"
      size="sm"
      testId="open-server-details-link"
      onclick={onOpenDetails}
    >
      {tr("ui.node.openDetails")}
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
        {reconnecting ? tr("ui.homelabDashboardPage.reconnecting") : tr("ui.homelabDashboardPage.reconnect")}
      </Button>
    {/if}
  </div>
{:else if !server && canonical}
  <ServerAccessActions
    serverId={canonical.id}
    serverName={canonical.name}
    unavailable={accessUnavailable}
    unavailableReason={tr("ui.nodeActions.connection", {
      state: canonicalServerStatusLabel(canonical),
    })}
  />
{/if}

{#if outcome}
  <GuidancePanel
    {outcome}
    surface="stacks.hub.server"
    resourceId={canonical?.id || server?.id}
    resourceName={node.name}
    onRetry={server?.lease_id && canReconnect ? onReconnect : undefined}
    retrying={reconnecting}
    compact
  />
{/if}
