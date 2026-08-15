<script lang="ts">
  import { onMount } from "svelte";
  import RadioCard from "./RadioCard.svelte";
  import TipBox from "./TipBox.svelte";
  import {
    applyServerProvisioningMode,
    getIonosDatacenterChoices,
    getServerProvisioningModeChoices,
    normalizeIonosDatacenter,
    type IonosDatacenter,
    type ServerProvisioningMode,
    type StackConfig,
    type ManagedProviderID,
  } from "$lib/wizard";
  import { tr } from "$lib/i18n.svelte";
  import { features, loadFeatures } from "$lib/stores/features";
  import { authStore } from "$lib/stores/auth.svelte";
  import { shouldDowngradeManagedRuntimeSelection } from "$lib/wizard/managed-runtime-selection";

  interface Props {
    config: StackConfig;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
  }

  let {
    config = $bindable(),
    onmanagedproviderselect,
  }: Props = $props();
  const provisioningModes = getServerProvisioningModeChoices();
  const ionosDatacenters = getIonosDatacenterChoices();
  const allFeatures = features.allFeatures;
  const featuresReady = features.ready;
  const featuresLoading = features.loading;
  const featuresErrorKind = features.errorKind;
  // Verification failure (retryable) vs genuine not-entitled state.
  const entitlementVerificationFailed = $derived(Boolean($featuresErrorKind));

  onMount(() => {
    void loadFeatures();
  });

  function retryEntitlementCheck() {
    void loadFeatures(true);
  }
  const managedRuntimeBaseRequirements = [
    { key: "monthly_runtime", label: "Monthly Runtime" },
    { key: "monthly_runtime_cloudkit", label: "Cloud Kit rollout" },
  ];
  const monthlyRuntimeAvailable = $derived.by(
    () =>
      (($allFeatures.get("monthly_runtime")?.enabled ?? false) &&
        ($allFeatures.get("monthly_runtime_cloudkit")?.enabled ?? false)),
  );
  const selectedMode = $derived(
    provisioningModes.find(
      (mode) => mode.value === config.serverProvisioning.mode,
    ),
  );

  function selectMode(mode: string) {
    if (mode === "kombify-cloud" && !managedRuntimeSelectable) return;
    const provisioningMode = mode as ServerProvisioningMode;
    applyServerProvisioningMode(config, provisioningMode);
  }

  const managedProviders: Array<{
    value: ManagedProviderID;
    label: string;
    description: string;
    featureKey: string;
  }> = [
    {
      value: "centron",
      label: "Centron",
      description: "Centron Managed VPS",
      featureKey: "monthly_runtime_centron",
    },
    {
      value: "ionos",
      label: "IONOS",
      description: "IONOS Managed VPS",
      featureKey: "monthly_runtime_ionos",
    },
  ];
  const missingManagedRuntimeBaseFeatures = $derived.by(() =>
    managedRuntimeBaseRequirements.filter(
      (requirement) => $allFeatures.get(requirement.key)?.enabled !== true,
    ),
  );
  const visibleManagedProviders = $derived.by(() =>
    monthlyRuntimeAvailable
      ? managedProviders.filter(
          (provider) =>
            $allFeatures.get(provider.featureKey)?.enabled === true,
        )
      : [],
  );
  const managedRuntimeSelectable = $derived(
    monthlyRuntimeAvailable && visibleManagedProviders.length > 0,
  );
  const missingManagedProviderFeatures = $derived.by(() =>
    monthlyRuntimeAvailable
      ? managedProviders.filter(
          (provider) => $allFeatures.get(provider.featureKey)?.enabled !== true,
        )
      : [],
  );
  const managedRuntimeUnavailableReason = $derived.by(() => {
    if ($featuresErrorKind === "auth") {
      return "TechStack couldn't verify your account yet because the embedded session isn't fully established. This usually clears on its own once sign-in completes — retry to check again, or use a user-owned server in the meantime.";
    }
    if ($featuresErrorKind === "network" || $featuresErrorKind === "server") {
      return "Entitlement verification is temporarily unavailable, so kombify-managed servers stay disabled fail-closed. Retry, or use a user-owned server until verification succeeds.";
    }
    if (!$featuresReady || $featuresLoading) {
      return "TechStack is checking account entitlements. Kombify-managed provider provisioning stays disabled until Monthly Runtime, Cloud Kit access, and provider access are positively verified.";
    }
    if (missingManagedRuntimeBaseFeatures.length > 0) {
      const missing = missingManagedRuntimeBaseFeatures
        .map((feature) => feature.label)
        .join(", ");
      return `Kombify-managed servers are not active because this account is missing: ${missing}. Use a user-owned server or ask an admin/support to enable Monthly Runtime with Cloud Kit access.`;
    }
    if (visibleManagedProviders.length === 0) {
      const missing = missingManagedProviderFeatures
        .map((provider) => provider.label)
        .join(", ");
      return `Kombify-managed servers are not active because no managed VPS provider entitlement is enabled for this account. Missing provider access: ${missing}. Use a user-owned server or ask an admin/support to enable a provider entitlement.`;
    }
    return undefined;
  });
  const managedRuntimeDisabledSummary = $derived.by(() => {
    if ($featuresErrorKind === "auth")
      return "Couldn't verify your account — retry.";
    if ($featuresErrorKind === "network" || $featuresErrorKind === "server")
      return "Entitlement verification is temporarily unavailable.";
    if (!$featuresReady || $featuresLoading) return "Checking entitlements.";
    if (missingManagedRuntimeBaseFeatures.length > 0) {
      return "Monthly Runtime or StackKit entitlement is missing.";
    }
    if (visibleManagedProviders.length === 0) {
      return "No managed VPS provider entitlement is enabled.";
    }
    return undefined;
  });

  function selectManagedProvider(provider: ManagedProviderID) {
    if (!visibleManagedProviders.some((item) => item.value === provider))
      return;
    config.providerId = provider;
    onmanagedproviderselect?.(provider);
    if (provider === "ionos") {
      config.ionosDatacenter = normalizeIonosDatacenter(config.ionosDatacenter);
    }
  }

  function selectIonosDatacenter(datacenter: IonosDatacenter) {
    config.ionosDatacenter = datacenter;
  }

  $effect(() => {
    if (
      config.serverProvisioning.mode === "kombify-cloud" &&
      shouldDowngradeManagedRuntimeSelection({
        authenticated: authStore.isAuthenticated,
        featuresReady: $featuresReady,
        featuresLoading: $featuresLoading,
        verificationFailed: entitlementVerificationFailed,
        selectable: managedRuntimeSelectable,
      })
    ) {
      applyServerProvisioningMode(config, "install-command");
    }
    if (
      config.serverProvisioning.mode === "kombify-cloud" &&
      managedRuntimeSelectable &&
      !visibleManagedProviders.some((item) => item.value === config.providerId)
    ) {
      config.providerId = visibleManagedProviders[0].value;
    }
    if (
      config.serverProvisioning.mode === "kombify-cloud" &&
      config.providerId === "ionos"
    ) {
      config.ionosDatacenter = normalizeIonosDatacenter(config.ionosDatacenter);
    }
  });
</script>

<div class="space-y-6" data-testid="server-provisioning-step">
  <div class="grid min-w-0 gap-4 lg:grid-cols-3">
    {#each provisioningModes as mode (mode.value)}
      <RadioCard
        name="server-provisioning"
        value={mode.value}
        selected={config.serverProvisioning.mode === mode.value}
        title={tr(mode.titleKey)}
        description={tr(mode.descriptionKey)}
        helpText={mode.helpKey ? tr(mode.helpKey) : undefined}
        badge={mode.badgeKey ? tr(mode.badgeKey) : undefined}
        disabled={mode.value === "kombify-cloud" && !managedRuntimeSelectable}
        disabledReason={mode.value === "kombify-cloud" &&
        !managedRuntimeSelectable
          ? managedRuntimeDisabledSummary
          : undefined}
        testId={mode.testId}
        onselect={selectMode}
      />
    {/each}
  </div>

  {#if !managedRuntimeSelectable && managedRuntimeUnavailableReason}
    <div
      class="rounded-lg border border-warning/30 bg-warning/10 p-4 text-sm text-foreground"
      data-testid="managed-runtime-unavailable"
    >
      <p class="font-medium">Kombify-managed servers are disabled</p>
      <p class="mt-1 text-muted-foreground">
        {managedRuntimeUnavailableReason}
      </p>
      {#if entitlementVerificationFailed}
        <button
          type="button"
          class="mt-3 inline-flex items-center rounded-md border border-warning/40 bg-warning/10 px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-warning/20 disabled:cursor-not-allowed disabled:opacity-60"
          data-testid="retry-entitlement-check"
          onclick={retryEntitlementCheck}
          disabled={$featuresLoading}
        >
          {$featuresLoading ? "Checking…" : "Retry verification"}
        </button>
      {/if}
    </div>
  {/if}

  {#if config.serverProvisioning.mode === "connect-remote"}
    <div
      class="rounded-xl border border-border bg-muted/20 p-5"
      data-testid="remote-server-config"
    >
      <div class="grid gap-4 md:grid-cols-[1fr_120px]">
        <div>
          <label
            for="remote-server-host"
            class="block text-sm text-muted-foreground mb-1"
          >
            {tr("wizard.server.remote.host")}
          </label>
          <input
            id="remote-server-host"
            type="text"
            bind:value={config.serverProvisioning.remote.host}
            placeholder="server.example.com"
            class="w-full px-4 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground"
            data-testid="remote-server-host"
          />
        </div>
        <div>
          <label
            for="remote-server-port"
            class="block text-sm text-muted-foreground mb-1"
          >
            {tr("wizard.server.remote.port")}
          </label>
          <input
            id="remote-server-port"
            type="number"
            min="1"
            max="65535"
            bind:value={config.serverProvisioning.remote.sshPort}
            class="w-full px-4 py-2 bg-input border border-border rounded-lg text-foreground"
            data-testid="remote-server-port"
          />
        </div>
      </div>

      <div class="grid gap-4 md:grid-cols-2 mt-4">
        <div>
          <label
            for="remote-server-user"
            class="block text-sm text-muted-foreground mb-1"
          >
            {tr("wizard.server.remote.user")}
          </label>
          <input
            id="remote-server-user"
            type="text"
            bind:value={config.serverProvisioning.remote.sshUser}
            placeholder="root"
            class="w-full px-4 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground"
            data-testid="remote-server-user"
          />
        </div>
        <div>
          <label
            for="remote-server-auth"
            class="block text-sm text-muted-foreground mb-1"
          >
            {tr("wizard.server.remote.auth")}
          </label>
          <select
            id="remote-server-auth"
            bind:value={config.serverProvisioning.remote.authMethod}
            class="w-full px-4 py-2 bg-input border border-border rounded-lg text-foreground"
            data-testid="remote-server-auth"
          >
            <option value="ssh-key"
              >{tr("wizard.server.remote.auth.sshKey")}</option
            >
            <option value="password"
              >{tr("wizard.server.remote.auth.password")}</option
            >
          </select>
        </div>
      </div>

      <div class="grid gap-4 md:grid-cols-2 mt-4">
        <div>
          <label
            for="remote-server-key"
            class="block text-sm text-muted-foreground mb-1"
          >
            {tr("wizard.server.remote.keyLabel")}
          </label>
          <input
            id="remote-server-key"
            type="text"
            bind:value={config.serverProvisioning.remote.sshKeyLabel}
            placeholder="root@server"
            class="w-full px-4 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground"
            data-testid="remote-server-key-label"
          />
        </div>
        <label
          class="flex items-center gap-3 rounded-lg border border-border bg-card px-4 py-3 text-foreground"
        >
          <input
            type="checkbox"
            bind:checked={config.serverProvisioning.remote.useSudo}
            class="rounded border-border text-primary"
          />
          {tr("wizard.server.remote.sudo")}
        </label>
      </div>
    </div>
  {:else if selectedMode?.tipKey}
    <TipBox variant="info">
      {tr(selectedMode.tipKey)}
    </TipBox>
  {/if}

  {#if config.serverProvisioning.mode === "kombify-cloud"}
    <div
      class="rounded-xl border border-border bg-muted/20 p-5"
      data-testid="managed-provider-selector"
    >
      <div class="mb-3">
        <h3 class="text-sm font-medium text-foreground">Managed VPS</h3>
        <p class="text-sm text-muted-foreground">
          Cloud Kit prepares the selected provider foundation. Application
          deployment follows the StackKit plan and its available deployment
          adapters.
        </p>
      </div>
      <div class="grid gap-3 sm:grid-cols-2">
        {#each visibleManagedProviders as provider (provider.value)}
          <button
            type="button"
            class="rounded-lg border px-4 py-3 text-left transition-colors {config.providerId ===
            provider.value
              ? 'border-primary bg-primary/10 text-foreground'
              : 'border-border bg-card text-muted-foreground hover:text-foreground'}"
            aria-pressed={config.providerId === provider.value}
            data-testid={`managed-provider-${provider.value}`}
            onclick={() => selectManagedProvider(provider.value)}
          >
            <span class="block text-sm font-medium">{provider.label}</span>
            <span class="mt-1 block text-xs">{provider.description}</span>
          </button>
        {/each}
      </div>
      {#if config.providerId === "ionos"}
        <div class="mt-5" data-testid="ionos-datacenter-selector">
          <div class="mb-3">
            <h4 class="text-sm font-medium text-foreground">
              IONOS Datacenter
            </h4>
            <p class="text-sm text-muted-foreground">
              Select the location for this managed VPS.
            </p>
          </div>
          <div class="grid gap-3 sm:grid-cols-3">
            {#each ionosDatacenters as datacenter (datacenter.value)}
              <button
                type="button"
                class="rounded-lg border px-4 py-3 text-left transition-colors {config.ionosDatacenter ===
                datacenter.value
                  ? 'border-primary bg-primary/10 text-foreground'
                  : 'border-border bg-card text-muted-foreground hover:text-foreground'}"
                aria-pressed={config.ionosDatacenter === datacenter.value}
                data-testid={datacenter.testId}
                onclick={() => selectIonosDatacenter(datacenter.value)}
              >
                <span class="block text-sm font-medium">{datacenter.label}</span
                >
                <span class="mt-1 block text-xs">{datacenter.value}</span>
                <span class="mt-1 block text-xs">{datacenter.description}</span>
              </button>
            {/each}
          </div>
        </div>
      {/if}
    </div>
  {/if}
</div>
