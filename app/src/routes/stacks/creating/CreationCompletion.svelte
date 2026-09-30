<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import HostBaselineSummary from "#lib/components/HostBaselineSummary.svelte";
  import IdentitySetup from "#lib/components/wizard/owner/IdentitySetup.svelte";
  import { tr } from "#lib/i18n.svelte.js";
</script>

<div class="mb-6">
  <div class="flex items-center gap-3 mb-2">
    <div
      class="w-12 h-12 rounded-full bg-success/20 flex items-center justify-center"
    >
      <svg
        class="w-6 h-6 text-success"
        fill="none"
        viewBox="0 0 24 24"
        stroke="currentColor"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M5 13l4 4L19 7"
        />
      </svg>
    </div>
    <div>
      <h2 class="text-xl font-semibold text-foreground">
        {creation.completionTitle}
      </h2>
      <p class="text-muted-foreground text-sm">
        {creation.completionSubtitle}
      </p>
    </div>
  </div>
</div>

{#if creation.agentPairingRequired && creation.connectedServer}
  <section
    class="mb-6 rounded-xl border border-success/30 bg-success/10 p-5"
    data-testid="guard-connected-summary"
  >
    <p class="text-xs font-semibold uppercase tracking-wide text-success">
      {tr("ui.stacksCreatingCreationCompletion.guardHeartbeatVerified")}
    </p>
    <h3 class="mt-1 text-lg font-semibold text-foreground">
      {creation.connectedServer.name}
    </h3>
    <p class="mt-2 text-sm text-muted-foreground">
      {tr("ui.stacksCreatingCreationCompletion.techstackNowSeesThisNode")}
    </p>
    {#if creation.connectedServer.connection.last_heartbeat_at}
      <p class="mt-3 text-xs text-muted-foreground">
        {tr("ui.stacksCreatingCreationCompletion.lastGuardHeartbeat")}
        <span class="font-mono"
          >{creation.connectedServer.connection.last_heartbeat_at}</span
        >
      </p>
    {/if}
  </section>
{/if}

{#if creation.connectedServer}
  <HostBaselineSummary
    serverId={creation.connectedServer.id}
    inventoryHref={`/stacks/${encodeURIComponent(creation.stackId)}/servers/${encodeURIComponent(creation.connectedServer.id)}#port-inventory`}
  />
{/if}

{#if creation.stackId && (creation.ownerSeedExpected || creation.stackKitHandoff)}
  <IdentitySetup deploymentId={creation.stackId} />
{/if}
{#if creation.stackKitHandoff}
  <section
    class="mb-6 overflow-hidden rounded-2xl border border-success/30 bg-success/10"
    data-testid="homelab-ready-card"
  >
    <div class="p-5">
      <div
        class="flex flex-col gap-4 md:flex-row md:items-start md:justify-between"
      >
        <div>
          <p class="text-xs font-semibold uppercase tracking-wide text-success">
            {creation.serverProvisioningMode === "kombify-cloud"
              ? tr("ui.stacksCreatingCreationCompletion.managedProvisioningComplete")
              : tr("ui.stacksCreatingCreationCompletion.rolloutComplete")}
          </p>
          <h3 class="mt-1 text-2xl font-semibold text-foreground">
            {tr("wizard.identity.installationComplete")}
          </h3>
          <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
            {tr("wizard.identity.installationDescription")}
          </p>
        </div>
        {#if creation.stackKitHandoff.loginGatewayUrl}
          <a
            href={creation.stackKitHandoff.loginGatewayUrl}
            target="_blank"
            rel="noopener noreferrer"
            data-kx="control"
            data-variant="primary"
            class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
            data-testid="homelab-ready-login-link"
          >
            {tr("ui.stacksCreatingCreationCompletion.openHomelab")}
          </a>
        {/if}
      </div>

      <div class="mt-5 grid gap-3 md:grid-cols-3">
        <div data-kx="plate" class="p-3">
          <p class="text-xs text-muted-foreground">{tr("ui.stacksCreatingCreationCompletion.owner")}</p>
          <p class="mt-1 truncate text-sm font-semibold text-foreground">
            {creation.stackKitHandoff.ownerDisplayName ||
              creation.stackKitHandoff.ownerUsername ||
              tr("ui.stacksCreatingCreationCompletion.ownerReady")}
          </p>
          {#if creation.stackKitHandoff.ownerEmail}
            <p class="truncate text-xs text-muted-foreground">
              {creation.stackKitHandoff.ownerEmail}
            </p>
          {/if}
        </div>
        <div data-kx="plate" class="p-3">
          <p class="text-xs text-muted-foreground">{tr("ui.services.node")}</p>
          <p class="mt-1 truncate text-sm font-semibold text-foreground">
            {creation.lease?.host ||
              creation.lease?.publicIp ||
              creation.lease?.privateIp ||
              creation.remoteServerHost ||
              (creation.serverProvisioningMode === "kombify-cloud"
                ? tr("ui.homelabDashboardPage.managedRuntime")
                : tr("ui.stacksCreatingCreationCompletion.yourNode"))}
          </p>
          <p class="truncate text-xs text-muted-foreground">
            {#if creation.serverProvisioningMode === "kombify-cloud"}
              {creation.lease?.provider || "kombify Cloud"} · {creation.lease
                ?.offering || "standard"}
            {:else}
              {tr("ui.stacksCreatingCreationCompletion.selfHostedRollout")}
            {/if}
          </p>
        </div>
        <div data-kx="plate" class="p-3">
          <p class="text-xs text-muted-foreground">{tr("ui.stacksCreatingCreationCompletion.recovery")}</p>
          <p class="mt-1 truncate text-sm font-semibold text-foreground">
            {creation.stackKitHandoff.recoveryRef
              ? tr("ui.stacksCreatingCreationCompletion.materialLinked")
              : tr("ui.stacksCreatingCreationCompletion.hashPresent")}
          </p>
          {#if creation.stackKitHandoff.recoveryRef}
            <p class="truncate text-xs text-muted-foreground">
              {creation.stackKitHandoff.recoveryRef}
            </p>
          {/if}
        </div>
      </div>

      <div class="mt-5 flex flex-wrap gap-2">
        <a
          href={creation.dashboardHref()}
          data-kx="control"
          class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
        >
          {tr("ui.settings.dashboard")}
        </a>
        <a
          href={creation.monitoringHref()}
          data-kx="control"
          class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
        >
          {tr("ui.monitoring.monitoring")}
        </a>
        <a
          href={creation.servicesHref()}
          data-kx="control"
          class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
        >
          {tr("ui.services.services")}
        </a>
      </div>
    </div>
  </section>
{:else if creation.ownerSeedExpected && creation.creationOperation === "stack" && !creation.rolloutJobDetected}
  <!-- Create/provision finished for a self-hosted stack: the owner
                 seed is prepared, the login handoff arrives after rollout. -->
  <section
    class="mb-6 rounded-2xl border border-info/30 bg-info/10 p-5"
    data-testid="owner-prepared-card"
  >
    <p class="text-xs font-semibold uppercase tracking-wide text-info">
      {tr("ui.stacksCreatingCreationCompletion.ownerPrepared")}
    </p>
    <h3 class="mt-1 text-lg font-semibold text-foreground">
      {creation.ownerSeedSummary?.displayName ||
        creation.ownerSeedSummary?.username ||
        creation.ownerSeedSummary?.email ||
        tr("ui.stacksCreatingCreationCompletion.ownerSeedReady")}
    </h3>
    <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
      {#if creation.ownerSeedSummary?.source === "cloud-linked"}
        {tr("ui.stacksCreatingCreationCompletion.theOwnerIdentityDerivesFrom")}
      {:else if creation.ownerSeedSummary?.email}
        {tr("ui.creationCompletion.ownerSeedFor", { email: creation.ownerSeedSummary.email })}
      {:else}
        {tr("ui.stacksCreatingCreationCompletion.theOwnerSeedIsPrepared")}
      {/if}
      {tr("ui.stacksCreatingCreationCompletion.theFirstLoginGatewayAnd")}
    </p>
  </section>
{/if}
