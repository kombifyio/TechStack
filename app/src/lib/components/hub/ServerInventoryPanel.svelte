<script lang="ts">
  import { HardDrive } from "@lucide/svelte";

  import type { CanonicalServer } from "#lib/api/registry.js";
  import ServerStateCard from "#lib/components/hub/ServerStateCard.svelte";
  import {
    canonicalServerFor,
    type DashboardServer,
  } from "#lib/server-card-adapter.js";

  interface Props {
    servers: DashboardServer[];
    canonicalServers: CanonicalServer[];
    canonicalOnlyServers: CanonicalServer[];
    canonicalInventoryUnavailable?: boolean;
    operationsEvidenceFresh?: boolean;
    assigningWorkerId?: string | null;
    reconnectingLeaseId?: string | null;
    canRollout?: boolean;
    rolloutLoading?: boolean;
    serverDetailsHref: (serverId: string, kitDeploymentId?: string) => string;
    canReconnect: (server: DashboardServer) => boolean;
    onAssign: (kitDeploymentId: string, serverId: string) => void;
    onDeploy: (kitDeploymentId: string) => void;
    onReconnect: (leaseId: string) => void;
    onNavigate: (href: string) => void;
  }

  let {
    servers,
    canonicalServers,
    canonicalOnlyServers,
    canonicalInventoryUnavailable = false,
    operationsEvidenceFresh = false,
    assigningWorkerId = null,
    reconnectingLeaseId = null,
    canRollout = false,
    rolloutLoading = false,
    serverDetailsHref,
    canReconnect,
    onAssign,
    onDeploy,
    onReconnect,
    onNavigate,
  }: Props = $props();

  const count = $derived(servers.length + canonicalOnlyServers.length);
</script>

<section
  aria-labelledby="home-hub-nodes-title"
  data-testid="server-inventory-panel"
>
  <div class="mb-4 flex items-center justify-between gap-3">
    <div class="flex items-center gap-2">
      <HardDrive class="h-5 w-5 text-primary" />
      <h3
        id="home-hub-nodes-title"
        class="text-xl font-semibold text-foreground"
      >
        Nodes
      </h3>
    </div>
    <span class="text-sm text-muted-foreground">
      {count} Node{count === 1 ? "" : "s"}
    </span>
  </div>

  {#if canonicalInventoryUnavailable}
    <p
      class="mb-3 rounded-lg border border-dashed border-warning/40 bg-warning/10 p-3 text-sm text-warning"
      role="status"
      data-testid="dashboard-inventory-unavailable"
    >
      Canonical Node and service state are unavailable. The retained telemetry
      below is diagnostic and is not treated as a zero inventory.
    </p>
  {/if}

  {#if count === 0}
    <p class="text-sm text-muted-foreground">
      No nodes are registered for this StackKit deployment yet.
    </p>
  {:else}
    <div class="grid gap-3 lg:grid-cols-2">
      {#each servers as server (`${server.kit_deployment_id}:${server.id}`)}
        {@const canonical = canonicalServerFor(server, canonicalServers)}
        {@const reconnectable = canReconnect(server)}
        {@const detailsHref = serverDetailsHref(
          server.id,
          server.kit_deployment_id,
        )}
        <ServerStateCard
          {server}
          {canonical}
          {detailsHref}
          {operationsEvidenceFresh}
          assigning={assigningWorkerId === server.id}
          reconnecting={Boolean(
            server.lease_id && reconnectingLeaseId === server.lease_id,
          )}
          {canRollout}
          {rolloutLoading}
          canReconnect={reconnectable}
          onAssign={() => onAssign(server.kit_deployment_id, server.id)}
          onDeploy={() => onDeploy(server.kit_deployment_id)}
          onReconnect={server.lease_id
            ? () => onReconnect(server.lease_id as string)
            : undefined}
          onOpenDetails={() => onNavigate(detailsHref)}
          onOutcomeRetry={server.lease_id && reconnectable
            ? () => onReconnect(server.lease_id as string)
            : undefined}
        />
      {/each}
      {#each canonicalOnlyServers as server (server.id)}
        {@const detailsHref = serverDetailsHref(server.id)}
        <ServerStateCard
          canonical={server}
          {detailsHref}
          onOpenDetails={() => onNavigate(detailsHref)}
        />
      {/each}
    </div>
  {/if}
</section>
