<script lang="ts">
  import { onDestroy } from "svelte";
  import { EntityDetailSheet } from "@kombiverselabs/ui/entity";
  import {
    getCanonicalServer,
    type CanonicalServer,
  } from "#lib/api/registry.js";
  import {
    getStackServerDetails,
    type StackServerDetailsPayload,
  } from "#lib/api/stacks.js";
  import GuardPanel from "#lib/components/guard/GuardPanel.svelte";
  import ServerMaintenanceCard from "#lib/components/server-maintenance/ServerMaintenanceCard.svelte";
  import StackKitOperatorPanel from "./StackKitOperatorPanel.svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import { operatorMessage as m } from "#lib/stackkit-operator-messages.js";
  import { parseApiError } from "#lib/api/errors.js";
  let {
    stackId,
    serverId,
    serverName,
  }: { stackId: string; serverId: string; serverName: string } = $props();
  let open = $state(false);
  let busy = $state(false);
  let server = $state<CanonicalServer | null>(null);
  let details = $state<StackServerDetailsPayload | null>(null);
  let loading = $state(false);
  let error = $state("");
  let generation = 0;
  async function load() {
    const attempt = ++generation;
    loading = true;
    error = "";
    try {
      const [canonical, detail] = await Promise.all([
        getCanonicalServer(serverId),
        getStackServerDetails(stackId, serverId),
      ]);
      if (attempt !== generation) return;
      if (canonical.id !== serverId) throw new Error(m("scopeMismatch"));
      server = canonical;
      details = detail;
    } catch (cause) {
      if (attempt === generation)
        error = parseApiError(cause).message || m("loadFailed");
    } finally {
      if (attempt === generation) loading = false;
    }
  }
  $effect(() => {
    const identity = `${stackId}:${serverId}`;
    void identity;
    generation++;
    server = null;
    details = null;
    error = "";
    open = false;
  });
  onDestroy(() => {
    generation++;
  });
</script>

<Button
  variant="secondary"
  class="mb-4"
  testId="server-operations-detail-open"
  onclick={() => {
    open = true;
    if (!server && !loading) void load();
  }}>{serverName} · {m("title")}</Button
>

<EntityDetailSheet
  {open}
  onclose={() => (open = false)}
  kind="server"
  name={serverName}
  meta={m("title")}
>
  <div data-testid="server-operations-detail">
    {#if error}<p role="alert" class="mt-3 text-sm text-destructive">{error}</p>
      <Button size="sm" disabled={loading} onclick={() => void load()}
        >{m("retry")}</Button
      >{/if}
    {#if server && details}
      <div class="mt-4 space-y-4">
        <StackKitOperatorPanel
          {server}
          {stackId}
          agentId={details.server.agent_id || ""}
          onBusy={(value) => (busy = value)}
          onChanged={load}
        />
        <ServerMaintenanceCard
          {serverId}
          {serverName}
          allowedActions={server.allowed_actions || []}
          disabled={busy}
          onChanged={() => void load()}
        />
        <GuardPanel
          {server}
          capabilities={details.server.capabilities}
          {serverName}
          {stackId}
          agentId={details.server.agent_id}
          onChanged={load}
        />
      </div>
    {/if}
  </div>
</EntityDetailSheet>
