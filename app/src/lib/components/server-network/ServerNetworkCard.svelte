<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { Network, Router } from "@lucide/svelte";
  import { tr, formatDateTime } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    getServerNetwork,
    rebindServerNetwork,
    type NetworkPoint,
    type NetworkRebindStatus,
    type ServerNetwork,
  } from "#lib/api/server-network.js";

  interface Props {
    serverId: string;
    /** True while the server is stale, offline or revoked. */
    unreachable?: boolean;
  }

  let { serverId, unreachable = false }: Props = $props();

  let network = $state<ServerNetwork | null>(null);
  let loadFailed = $state(false);
  let busy = $state(false);
  let notice = $state<string | null>(null);
  let error = $state<string | null>(null);
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;

  const move = $derived(network?.network_move ?? null);
  const rebindStatus = $derived<NetworkRebindStatus | null>(
    network?.rebind?.status ?? move?.rebind_status ?? null,
  );
  const inFlight = $derived(
    rebindStatus === "queued" || rebindStatus === "running",
  );
  const hasFacts = $derived(
    Boolean(
      network?.current &&
      (network.current.ip ||
        network.current.gateway ||
        network.current.ssid ||
        network.current.interface),
    ),
  );
  const showOfflineHint = $derived(
    unreachable || (network?.guidance ?? []).includes("offline_move_hint"),
  );

  function place(point: NetworkPoint | undefined): string {
    if (!point) return tr("network.unknownPlace");
    const parts = [point.ssid, point.ip, point.cidr].filter(Boolean);
    return parts.length > 0 ? parts.join(" · ") : tr("network.unknownPlace");
  }

  function statusLabel(status: NetworkRebindStatus): string {
    return tr(`network.rebind.${status}`);
  }

  async function load() {
    const mine = ++generation;
    clearTimeout(timer);
    try {
      const next = await getServerNetwork(serverId);
      if (mine !== generation) return;
      network = next;
      loadFailed = false;
    } catch {
      if (mine !== generation) return;
      loadFailed = network === null;
    }
    // A queued or running re-bind is followed until it settles.
    if (
      mine === generation &&
      (network?.rebind?.status === "queued" ||
        network?.rebind?.status === "running")
    ) {
      timer = setTimeout(() => void load(), 5000);
    }
  }

  async function rebind() {
    busy = true;
    error = null;
    notice = null;
    try {
      await rebindServerNetwork(serverId);
      notice = tr("network.rebind.started");
      await load();
    } catch (cause) {
      error = tr("network.rebind.error", {
        reason: parseApiError(cause).message,
      });
    } finally {
      busy = false;
    }
  }

  $effect(() => {
    void serverId;
    untrack(() => void load());
  });

  onDestroy(() => {
    generation++;
    clearTimeout(timer);
  });
</script>

<section data-kx="plate" class="p-5" data-testid="server-network-card">
  <div class="mb-1 flex items-center gap-2">
    <Network class="h-5 w-5 text-primary" />
    <h2 class="text-lg font-semibold text-foreground">{tr("network.title")}</h2>
  </div>
  <p class="mb-4 text-sm text-muted-foreground">{tr("network.intro")}</p>

  {#if move}
    <div
      class="mb-4 rounded-lg border border-primary/40 bg-primary/10 p-4"
      role="status"
      data-testid="server-network-moved"
    >
      <p class="font-medium text-foreground">{tr("network.moved.title")}</p>
      <p class="mt-1 text-sm text-foreground">
        {tr("network.moved.body", {
          from: place(move.previous),
          to: place(move.current),
        })}
      </p>
      <p class="mt-1 text-xs text-muted-foreground">
        {tr("network.moved.detected", {
          when: formatDateTime(move.detected_at),
        })}
      </p>
      <div class="mt-3 flex flex-wrap items-center gap-3">
        {#if rebindStatus}
          <span
            class="rounded-full border border-border px-2.5 py-0.5 text-xs font-medium"
            data-testid="server-network-rebind-status"
            data-status={rebindStatus}>{statusLabel(rebindStatus)}</span
          >
        {/if}
        {#if network?.can_rebind}
          <Button
            variant="secondary"
            size="sm"
            disabled={busy || inFlight}
            onclick={() => void rebind()}
            testId="server-network-rebind"
          >
            {busy ? tr("network.rebind.working") : tr("network.rebind.now")}
          </Button>
        {/if}
      </div>
    </div>
  {/if}

  {#if notice}
    <p class="mb-3 text-sm text-foreground" role="status">{notice}</p>
  {/if}
  {#if error}
    <p class="mb-3 text-sm text-destructive" role="alert">{error}</p>
  {/if}

  {#if loadFailed}
    <p class="text-sm text-muted-foreground">{tr("network.loadFailed")}</p>
  {:else if hasFacts && network}
    <dl class="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
      {#if network.current.ip}
        <div>
          <dt class="text-muted-foreground">{tr("network.address")}</dt>
          <dd class="font-mono text-foreground">{network.current.ip}</dd>
        </div>
      {/if}
      {#if network.current.gateway}
        <div>
          <dt class="text-muted-foreground">{tr("network.gateway")}</dt>
          <dd class="font-mono text-foreground">{network.current.gateway}</dd>
        </div>
      {/if}
      {#if network.current.cidr}
        <div>
          <dt class="text-muted-foreground">{tr("network.subnet")}</dt>
          <dd class="font-mono text-foreground">{network.current.cidr}</dd>
        </div>
      {/if}
      {#if network.current.interface}
        <div>
          <dt class="text-muted-foreground">{tr("network.interface")}</dt>
          <dd class="text-foreground">
            {network.current.interface}
            · {network.current.wireless
              ? tr("network.wifi")
              : tr("network.cable")}
          </dd>
        </div>
      {/if}
      {#if network.current.ssid}
        <div>
          <dt class="text-muted-foreground">{tr("network.wifi")}</dt>
          <dd class="text-foreground">{network.current.ssid}</dd>
        </div>
      {/if}
    </dl>
    {#if network.observed_at}
      <p class="mt-3 text-xs text-muted-foreground">
        {tr("network.observed", { when: formatDateTime(network.observed_at) })}
      </p>
    {/if}
  {:else if network}
    <p class="text-sm text-muted-foreground">{tr("network.notObserved")}</p>
  {/if}

  {#if showOfflineHint}
    <p
      class="mt-4 flex items-start gap-2 rounded-lg border border-border bg-muted/40 p-3 text-sm text-foreground"
      data-testid="server-network-offline-hint"
    >
      <Router class="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
      <span>{tr("network.offlineHint")}</span>
    </p>
  {/if}
</section>
