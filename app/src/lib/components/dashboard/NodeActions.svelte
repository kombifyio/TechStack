<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { Play, RefreshCw } from "@lucide/svelte";

  import Button from "#lib/components/ui/Button.svelte";
  import ServerAccessActions from "#lib/components/server-access/ServerAccessActions.svelte";
  import { isManagedRuntimeServer } from "#lib/managed-runtime-server.js";
  import {
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
    /**
     * Label of the restart that recovers this Node's failed rollout. When set,
     * the restart replaces Deploy as the primary action: Deploy cannot work for
     * a stack in error.
     */
    retryLabel?: string;
    retrying?: boolean;
    canReconnect?: boolean;
    onAssign?: () => void;
    onDeploy?: () => void;
    onRetry?: () => void;
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
    retryLabel,
    retrying = false,
    canReconnect = false,
    onAssign,
    onDeploy,
    onRetry,
    onReconnect,
    onOpenDetails,
  }: Props = $props();

  const server = $derived<DashboardServer | undefined>(node.server);
  const canonical = $derived(node.canonical);
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
  <div class="flex flex-col gap-2" data-testid="managed-runtime-action-row">
    {#if retryLabel && onRetry}
      <Button
        variant="primary"
        size="sm"
        class="w-full"
        testId="retry-rollout-button"
        onclick={onRetry}
        disabled={retrying}
      >
        <RefreshCw class="h-4 w-4 {retrying ? 'animate-spin' : ''}" />
        {retrying ? tr("ui.guidance.retrying") : retryLabel}
      </Button>
    {:else}
      <Button
        variant="primary"
        size="sm"
        class="w-full"
        testId="deploy-stackkit-button"
        onclick={onDeploy}
        disabled={!canRollout || rolloutLoading}
      >
        <Play class="h-4 w-4" />
        {rolloutLoading ? tr("ui.homelabDashboardPage.starting") : tr("ui.homelabDashboardPage.deployStackkit")}
      </Button>
    {/if}
    <ServerAccessActions
      layout="stack"
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
      class="w-full"
      testId="open-server-details-link"
      onclick={onOpenDetails}
    >
      {tr("ui.node.openDetails")}
    </Button>
    {#if server.lease_id && canReconnect}
      <Button
        variant="secondary"
        size="sm"
        class="w-full"
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
    layout="stack"
    serverId={canonical.id}
    serverName={canonical.name}
    unavailable={accessUnavailable}
    unavailableReason={tr("ui.nodeActions.connection", {
      state: canonicalServerStatusLabel(canonical),
    })}
  />
{/if}

{#if !(managedRuntime && server)}
  <Button
    variant="secondary"
    size="sm"
    class="w-full"
    testId="open-server-details-link"
    onclick={onOpenDetails}
  >
    {tr("ui.node.openDetails")}
  </Button>
{/if}
