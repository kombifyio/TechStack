<!--
  Placement board: one column per Node with its capacity and the applications
  on it. Dragging a movable card onto another column asks the page to move it
  (`onmove`); the page resolves every card, so this stays presentation.
-->
<script lang="ts">
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import {
    AlertTriangle,
    ArrowRight,
    Cpu,
    HardDrive,
    Loader2,
    Server,
  } from "@lucide/svelte";
  import { ServiceCardCompact } from "@kombiverselabs/ui/service";
  import Button from "#lib/components/ui/Button.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import type { RegistryServer, RegistryService } from "#lib/api/registry.js";
  import type { PlacementCard, PlacementColumn } from "#lib/services/placement.js";
  import type { CockpitRefreshError } from "#lib/monitoring/cockpit-snapshot.js";

  interface Props {
    columns: PlacementColumn[];
    loading: boolean;
    registryError: string | null;
    migrationAvailable: boolean;
    migrationUnavailableReason?: string;
    cockpitStale: boolean;
    cockpitError: CockpitRefreshError | null;
    onretry: () => void;
    onmove: (service: RegistryService, target: RegistryServer) => void;
  }

  let {
    columns,
    loading,
    registryError,
    migrationAvailable,
    migrationUnavailableReason,
    cockpitStale,
    cockpitError,
    onretry,
    onmove,
  }: Props = $props();

  let activeDrag = $state<PlacementCard | null>(null);
  let activeDragServerId = $state<string | null>(null);

  function handleDragStart(event: DragEvent, card: PlacementCard) {
    if (!card.canMove) {
      event.preventDefault();
      return;
    }
    activeDrag = card;
    if (event.dataTransfer) {
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("text/plain", card.service.id || "");
    }
  }

  function handleDragOver(event: DragEvent, serverId: string) {
    event.preventDefault();
    if (activeDrag && activeDrag.service.server_id !== serverId) {
      activeDragServerId = serverId;
    }
  }

  function handleDragLeave() {
    activeDragServerId = null;
  }

  function handleDrop(event: DragEvent, target: RegistryServer) {
    event.preventDefault();
    activeDragServerId = null;
    const card = activeDrag;
    activeDrag = null;
    if (!card || card.service.server_id === target.id) return;
    onmove(card.service, target);
  }
</script>

{#if !loading && !registryError && !migrationAvailable}
  <div
    class="mb-5 flex gap-3 rounded-lg border border-warning/30 bg-warning/10 p-4 text-sm text-warning"
    role="status"
    data-testid="migration-unavailable"
  >
    <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-warning" />
    <div>
      <p class="font-semibold">
        {tr("ui.services.runtimeMigrationIsNotEnabled")}
      </p>
      <p class="mt-1 text-warning/80">
        {migrationUnavailableReason ||
          tr("ui.services.dragAndDropStaysDisabled")}
      </p>
    </div>
  </div>
{/if}
{#if cockpitStale || cockpitError}
  <div
    class="mb-5 flex gap-3 rounded-lg border border-warning/30 bg-warning/10 p-4 text-sm text-warning"
    role={cockpitError ? "alert" : "status"}
    data-testid="placement-cockpit-refresh-state"
  >
    <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-warning" />
    <div class="min-w-0">
      <p class="font-semibold">
        {cockpitError
          ? tr("ui.services.capacityTelemetryRefreshFailed")
          : tr("ui.services.showingLastVerifiedCapacityTelemetry")}
      </p>
      <p class="mt-1 text-warning/80">
        {tr("ui.services.theLastSuccessfulSnapshotRemains")}
      </p>
      {#if cockpitError}
        <p
          class="mt-2 break-words font-mono text-xs text-warning/70"
          data-testid="placement-cockpit-refresh-error"
        >
          {cockpitError.message}
        </p>
      {/if}
    </div>
  </div>
{/if}
{#if loading}
  <LoadingNote label={tr("common.loading")} />
{:else if registryError}
  <div
    class="flex gap-3 rounded-lg border border-warning/30 bg-warning/10 p-4 text-sm text-warning"
    role="status"
    data-testid="registry-placement-warning"
  >
    <AlertTriangle class="mt-0.5 h-5 w-5 shrink-0 text-warning" />
    <div class="min-w-0">
      <p class="font-semibold">
        {tr("ui.services.applicationPlacementIsTemporarilyUnavailable")}
      </p>
      <p class="mt-1 text-warning/80">
        {tr("ui.services.placementAndMigrationControlsStay")}
      </p>
      <p class="mt-2 break-words font-mono text-xs text-warning/70">
        {registryError}
      </p>
      <Button
        variant="secondary"
        size="sm"
        class="mt-3"
        onclick={onretry}
      >
        {tr("ui.services.retryManagementData")}
      </Button>
    </div>
  </div>
{:else if columns.length === 0}
  <div class="rounded-lg border border-border bg-card/60 p-12 text-center">
    <p class="text-lg text-foreground">
      {tr("ui.services.noNodesRegistered")}
    </p>
    <p class="mt-1 text-sm text-muted-foreground">
      {tr("ui.services.connectNodesToYourHomelab")}
    </p>
  </div>
{:else}
  <div
    class="flex gap-6 overflow-x-auto pb-6 scrollbar-thin select-none animate-in fade-in duration-300"
  >
    {#each columns as column (column.server.id)}
      {@const server = column.server}
      {@const cap = column.capacity}

      <!-- Server Column -->
      <div
        class="w-80 shrink-0 flex flex-col bg-card/35 backdrop-blur-sm border rounded-xl p-4 min-h-[550px] transition-all duration-300
               {activeDragServerId === server.id
          ? 'border-primary/60 bg-primary/5 shadow-md shadow-primary/5'
          : 'border-border/80'}"
        ondragover={(e) => handleDragOver(e, server.id)}
        ondragleave={handleDragLeave}
        ondrop={(e) => handleDrop(e, server)}
        role="list"
        aria-label={tr("ui.services.nodeColumn", { name: server.name })}
      >
        <!-- Server Headline Card -->
        <div
          class="bg-muted/30 border border-border/75 rounded-lg p-3.5 mb-4 shadow-sm hover:border-primary/30 transition-colors duration-300"
        >
          <div class="flex items-start justify-between gap-2">
            <h3 class="font-bold text-sm text-foreground truncate">
              {server.name}
            </h3>
            <span
              class="text-[9px] px-1.5 py-0.5 rounded-md bg-muted border border-border font-medium text-muted-foreground shrink-0 uppercase tracking-wider"
            >
              {server.role_label}
            </span>
          </div>
          <p
            class="text-[10px] text-muted-foreground/80 truncate font-mono mt-1"
          >
            {server.hostname || tr("ui.services.noHostname")}
          </p>

          <div
            class="mt-3 pt-2.5 border-t border-border/50 flex items-center justify-between text-xs"
          >
            <span class="text-muted-foreground font-medium"
              >{tr("ui.services.type")}</span
            >
            <span class="font-semibold text-foreground capitalize">
              {column.cloud
                ? tr("ui.services.cloudVps")
                : tr("ui.services.localNode")}
            </span>
          </div>

          <!-- Capacity Meters -->
          <div class="mt-4 space-y-3">
            <div>
              <div
                class="flex justify-between text-[10px] text-muted-foreground mb-1"
              >
                <span class="flex items-center gap-1 font-medium"
                  ><HardDrive class="w-3.5 h-3.5 text-primary/80" />
                  {tr("ui.services.storage")}</span
                >
                <span class="font-medium">
                  {#if cap.storageTotal}
                    {tr("ui.services.storageFreeOf", {
                      free: cap.storageFree ?? "?",
                      total: cap.storageTotal,
                    })}
                  {:else}
                    {tr("ui.services.telemetryPending")}
                  {/if}
                </span>
              </div>
              <div
                class="w-full bg-muted/65 rounded-full h-1.5 overflow-hidden"
              >
                <div
                  class="bg-primary h-1.5 rounded-full transition-all duration-300"
                  style="width: {cap.storagePercent ?? 0}%"
                ></div>
              </div>
            </div>
            <div>
              <div
                class="flex justify-between text-[10px] text-muted-foreground mb-1"
              >
                <span class="flex items-center gap-1 font-medium"
                  ><Cpu class="w-3.5 h-3.5 text-info/80" /> RAM</span
                >
                <span class="font-medium">
                  {#if cap.ramTotal}
                    {tr("ui.services.ramUsedOf", {
                      used: cap.ramUsed ?? "?",
                      total: cap.ramTotal,
                    })}
                  {:else}
                    {tr("ui.services.telemetryPending")}
                  {/if}
                </span>
              </div>
              <div
                class="w-full bg-muted/65 rounded-full h-1.5 overflow-hidden"
              >
                <div
                  class="bg-info h-1.5 rounded-full transition-all duration-300"
                  style="width: {cap.ramPercent ?? 0}%"
                ></div>
              </div>
            </div>
            <p
              class="text-[9px] uppercase tracking-wide text-muted-foreground/70"
            >
              {cap.source === "telemetry"
                ? cockpitStale
                  ? "last verified telemetry"
                  : "live telemetry"
                : cap.source === "capabilities"
                  ? tr("ui.services.nodeCapabilities")
                  : "no capacity data"}
            </p>
          </div>
        </div>

        <!-- Application Cards Stack -->
        <div class="flex-1 space-y-3 flex flex-col">
          {#each column.cards as card (card.key)}
            {@const service = card.service}
            {@const isMigrating = card.migrating}
            {@const canMove = card.canMove}

            <!-- Application Card -->
            <div
              draggable={canMove}
              ondragstart={(e) => handleDragStart(e, card)}
              ondragend={() => (activeDrag = null)}
              role="listitem"
              class="space-y-2 {canMove
                ? 'cursor-grab active:cursor-grabbing'
                : 'opacity-75'} {service.status === 'migrating' ||
              service.status === 'deploying'
                ? 'animate-pulse'
                : ''} {service.status === 'archived' ? 'opacity-55' : ''}"
            >
              <ServiceCardCompact
                name={card.name}
                meta={card.meta}
                placement={card.placement}
                status={card.status}
                migration={card.migration}
                showGrip={canMove}
                actions={card.actions}
              />

              <!-- Relocating status messages -->
              {#if isMigrating}
                <div
                  class="mt-2.5 pt-2 border-t border-border/40 flex items-center gap-1.5 text-[11px] text-warning font-medium"
                >
                  <Loader2 class="w-3 h-3 animate-spin shrink-0" />
                  <span class="truncate"
                    >{card.migrationMessage}</span
                  >
                </div>
              {/if}
              <!-- Verify / Move / Remove moved into the detail sheet's
                   Operations tab (card family v2, 2026-08-27). -->
            </div>
          {/each}

          {#if activeDragServerId === server.id && activeDrag && activeDrag.service.server_id !== server.id}
            <div
              class="border-2 border-dashed border-primary/50 bg-primary/5 rounded-lg p-4 h-24 flex flex-col items-center justify-center text-xs text-muted-foreground transition-all gap-1.5"
            >
              <ArrowRight class="w-5 h-5 text-primary animate-pulse" />
              <span class="font-medium"
                >{tr("ui.services.moveHere", {
                  name: activeDrag.name,
                })}</span
              >
            </div>
          {/if}

          {#if column.cards.length === 0}
            <div
              class="flex-1 border border-dashed border-border/40 rounded-lg p-6 flex flex-col items-center justify-center text-center text-xs text-muted-foreground min-h-[140px] bg-card/20"
            >
              <Server class="w-5 h-5 text-muted-foreground/50 mb-1.5" />
              <span class="font-medium text-foreground/80"
                >{tr("ui.services.noApplicationsDeployed")}</span
              >
              <span class="text-[10px] mt-1 text-muted-foreground/80"
                >{migrationAvailable
                  ? tr("ui.services.dropManagedApplicationsHere")
                  : tr("ui.services.runtimeMigrationUnavailable")}</span
              >
            </div>
          {/if}
        </div>
      </div>
    {/each}
  </div>
{/if}
