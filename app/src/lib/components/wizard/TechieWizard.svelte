<!--
  TechieWizard Component
  
  4-step wizard for expert/techie mode stack creation:
  1. Server - Basic server info and naming
  2. Network - VPN, access, and networking options  
  3. Services - Select which services to deploy
  4. Review - Final review and deployment
  
  NOTE: Requirements analysis and Unifier processing happen AFTER the wizard,
  on the /stacks/creating page. This keeps the wizard focused on user input.
  
  Dispatches 'oncreate' event with StackConfig when user clicks Create.
-->
<script lang="ts">
  import {
    Stepper,
    RadioCard,
    Accordion,
    TipBox,
    ServerRegistryPanel,
    ServiceRegistrySelector,
  } from "./index";
  import {
    ACTIVE_STANDARD_BUNDLE,
    type StackConfig,
    type StandardBundleServiceKey,
    type VpnValue,
    createDefaultConfig,
    getTechieServiceQuestions,
    getVpnQuestions,
    TECHIE_STEPS,
  } from "$lib/wizard";
  import {
    hashPassphrase,
    MIN_RECOVERY_PASSPHRASE_LENGTH,
    scoreStrength,
  } from "$lib/wizard/argon2";

  interface Props {
    oncreate?: (config: StackConfig) => void;
    isDeploying?: boolean;
  }

  let { oncreate, isDeploying = false }: Props = $props();

  // Initialize config with defaults
  let config = $state<StackConfig>(createDefaultConfig());
  let step = $state(1);
  let recoveryPassphrase = $state("");
  let recoveryPassphraseConfirm = $state("");
  let recoveryHashError = $state<string | null>(null);
  let isHashingRecovery = $state(false);
  const standardBundle = ACTIVE_STANDARD_BUNDLE;
  const techieServiceQuestions = getTechieServiceQuestions();
  const vpnQuestions = getVpnQuestions();

  // Validation helpers
  const hasName = $derived(config.name.trim().length > 0);

  const serverProvisioningReady = $derived(
    config.serverProvisioning.mode !== "connect-remote" ||
      config.serverProvisioning.remote.host.trim().length > 0,
  );

  const hasServices = $derived(
    techieServiceQuestions.some(
      (service) => config.services[service.key] === true,
    ) || config.services.pocketId,
  );
  const recoveryPassphrasesMatch = $derived(
    recoveryPassphrase.length > 0 &&
      recoveryPassphrase === recoveryPassphraseConfirm,
  );
  const recoveryPassphraseLongEnough = $derived(
    recoveryPassphrase.length >= MIN_RECOVERY_PASSPHRASE_LENGTH,
  );
  const hasRecoveryHash = $derived(
    config.owner.recoveryPassphraseHash.length > 0,
  );
  const recoveryStrength = $derived(scoreStrength(recoveryPassphrase));
  const ownerDisplayNamePreview = $derived(
    config.owner.displayName.trim() || config.owner.username.trim(),
  );
  const ownerBootstrapReady = $derived(() => {
    if (config.owner.source !== "local") return false;
    if (!config.owner.username.trim()) return false;
    if (!config.owner.email.trim()) return false;
    if (!hasRecoveryHash) return false;
    return true;
  });

  const canGoNext = $derived(() => {
    if (step === 1) return hasName && serverProvisioningReady;
    if (step === 2) return true;
    if (step === 3) return hasServices;
    if (step === 4) return hasServices;
    return true;
  });

  const canDeploy = $derived(
    () =>
      step === TECHIE_STEPS.length &&
      hasServices &&
      hasName &&
      serverProvisioningReady &&
      ownerBootstrapReady(),
  );

  // Navigation
  function goNext() {
    if (step < TECHIE_STEPS.length && canGoNext()) step++;
  }

  function goPrev() {
    if (step > 1) step--;
  }

  // Handle create
  async function handleCreate() {
    const recoveryReady = await syncRecoveryHash();
    if (!recoveryReady || !ownerBootstrapReady()) {
      return;
    }

    config.wizardType = "techie";
    oncreate?.(config);
  }

  function resetRecoveryHash() {
    config.owner.recoveryPassphraseHash = "";
    recoveryHashError = null;
  }

  async function syncRecoveryHash() {
    if (!recoveryPassphrase || !recoveryPassphraseConfirm) {
      resetRecoveryHash();
      return false;
    }

    if (!recoveryPassphraseLongEnough || !recoveryPassphrasesMatch) {
      resetRecoveryHash();
      return false;
    }

    if (config.owner.recoveryPassphraseHash) {
      return true;
    }

    isHashingRecovery = true;
    recoveryHashError = null;

    try {
      config.owner.recoveryPassphraseHash =
        await hashPassphrase(recoveryPassphrase);
      return true;
    } catch (error) {
      config.owner.recoveryPassphraseHash = "";
      recoveryHashError =
        error instanceof Error
          ? error.message
          : "Could not hash the recovery passphrase.";
      return false;
    } finally {
      isHashingRecovery = false;
    }
  }

  function setVpn(value: string) {
    config.network.vpn = value as VpnValue;
  }

  function setServiceEnabled(key: StandardBundleServiceKey, enabled: boolean) {
    config.services[key] = enabled;
    if (key === "pocketbase") {
      config.identity.homelabProvider = enabled ? "pocketbase" : "pocket-id";
    }
  }

  const selectedServiceLabels = $derived(() => {
    return techieServiceQuestions
      .filter((service) => config.services[service.key])
      .map((service) => service.label);
  });
</script>

<div
  class="card w-full min-w-0 overflow-hidden p-4 sm:p-6 lg:p-8"
  data-testid="techie-wizard"
  data-standard-bundle-id={standardBundle.id}
  data-standard-bundle-version={standardBundle.version}
>
  <Stepper
    steps={TECHIE_STEPS}
    currentStep={step}
    canGoNext={canGoNext()}
    canDeploy={canDeploy()}
    {isDeploying}
    onprev={goPrev}
    onnext={goNext}
    ondeploy={handleCreate}
  />

  <!-- Step 1: Server -->
  {#if step === 1}
    <div class="space-y-8" data-testid="techie-step-1">
      <div class="text-center mb-4">
        <h2 class="text-2xl font-semibold text-white mb-3">
          Step 1: Server Configuration
        </h2>
        <p class="text-muted-foreground max-w-lg mx-auto">
          Name your stack and configure basic settings.
        </p>
      </div>

      <div class="max-w-md mx-auto space-y-4">
        <div>
          <label
            for="techie-stack-name"
            class="block text-sm text-muted-foreground mb-1">Stack Name</label
          >
          <input
            id="techie-stack-name"
            type="text"
            bind:value={config.name}
            placeholder="my-homelab"
            class="w-full px-4 py-2 bg-input border border-border rounded-lg text-white placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
            data-testid="techie-name"
          />
          <p class="text-xs text-muted-foreground mt-1">
            Used to identify your stack in the dashboard
          </p>
        </div>
      </div>

      <ServerRegistryPanel bind:config />

      <TipBox>
        <strong>Tip:</strong> The default SaaS path generates a one-liner for your
        rollout target. Managed kombify servers remain an optional target.
      </TipBox>
    </div>
  {/if}

  <!-- Step 2: Network -->
  {#if step === 2}
    <div class="space-y-8" data-testid="techie-step-2">
      <div class="text-center mb-4">
        <h2 class="text-2xl font-semibold text-white mb-3">
          Step 2: Network & Access
        </h2>
        <p class="text-muted-foreground max-w-lg mx-auto">
          Configure how you'll connect to your services.
        </p>
      </div>

      <div class="space-y-6">
        <fieldset>
          <legend class="block text-sm text-muted-foreground mb-2">
            Private access profile
          </legend>
          <div class="grid gap-3 md:grid-cols-3">
            {#each vpnQuestions as vpn (vpn.value)}
              <RadioCard
                name="vpn"
                value={vpn.value}
                selected={config.network.vpn === vpn.value}
                title={vpn.label || vpn.value}
                description={vpn.description}
                testId={vpn.testId}
                onselect={setVpn}
              />
            {/each}
          </div>
        </fieldset>

        <div class="grid gap-4 md:grid-cols-2">
          <label
            class="flex items-center gap-3 p-4 bg-gray-800/50 rounded-lg border border-border cursor-pointer hover:border-border"
          >
            <input
              type="checkbox"
              bind:checked={config.network.enableCloudflare}
              class="rounded border-border text-primary"
            />
            <div>
              <p class="text-white font-medium">Cloudflare Zero Trust</p>
              <p class="text-sm text-muted-foreground">
                Additional security layer via Cloudflare
              </p>
            </div>
          </label>

          <label
            class="flex items-center gap-3 p-4 bg-gray-800/50 rounded-lg border border-border cursor-pointer hover:border-border"
          >
            <input
              type="checkbox"
              bind:checked={config.network.publicAccess}
              class="rounded border-border text-primary"
            />
            <div>
              <p class="text-white font-medium">Public Access</p>
              <p class="text-sm text-muted-foreground">
                Expose some services to the internet
              </p>
            </div>
          </label>
        </div>
      </div>

      <TipBox>
        <strong>Tip:</strong> Headscale is the standard private mesh profile. Public
        access should be enabled only for services that need it.
      </TipBox>
    </div>
  {/if}

  <!-- Step 3: Services -->
  {#if step === 3}
    <div class="space-y-8" data-testid="techie-step-3">
      <div class="text-center mb-4">
        <h2 class="text-2xl font-semibold text-white mb-3">
          Step 3: Select Services
        </h2>
        <p class="text-muted-foreground max-w-lg mx-auto">
          Choose which services to deploy on your stack.
        </p>
      </div>

      <ServiceRegistrySelector
        services={config.services}
        onchange={setServiceEnabled}
      />

      <TipBox>
        <strong>Tip:</strong> Pocket ID is the default identity head. PocketBase is
        optional backend compatibility for stacks that explicitly need it.
      </TipBox>
    </div>
  {/if}

  <!-- Step 4: Review -->
  {#if step === 4}
    <div class="space-y-8" data-testid="techie-step-4">
      <div class="text-center mb-4">
        <h2 class="text-2xl font-semibold text-white mb-3">
          Step 4: Review & Deploy
        </h2>
        <p class="text-muted-foreground max-w-lg mx-auto">
          Review your configuration before creating the stack.
        </p>
      </div>

      <div class="grid gap-5 md:grid-cols-2">
        <!-- Stack Info -->
        <div class="p-5 bg-gray-800/50 rounded-xl border border-border">
          <h3 class="text-lg font-semibold text-white mb-4">Stack Info</h3>
          <dl class="space-y-2 text-sm">
            <div class="flex justify-between">
              <dt class="text-muted-foreground">Name</dt>
              <dd class="text-white">{config.name}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-muted-foreground">Server</dt>
              <dd class="text-white">
                {config.serverProvisioning.mode === "kombify-cloud"
                  ? "kombify Cloud"
                  : config.serverProvisioning.mode === "connect-remote"
                    ? "Remote SSH"
                    : "Install command"}
              </dd>
            </div>
          </dl>
        </div>

        <!-- Network -->
        <div class="p-4 bg-gray-800/50 rounded-lg border border-border">
          <h3 class="text-lg font-semibold text-white mb-3">Network</h3>
          <dl class="space-y-2 text-sm">
            <div class="flex justify-between">
              <dt class="text-muted-foreground">VPN</dt>
              <dd class="text-white capitalize">{config.network.vpn}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-muted-foreground">Cloudflare</dt>
              <dd class="text-white">
                {config.network.enableCloudflare ? "Enabled" : "Disabled"}
              </dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-muted-foreground">Public Access</dt>
              <dd class="text-white">
                {config.network.publicAccess ? "Enabled" : "Disabled"}
              </dd>
            </div>
          </dl>
        </div>

        <!-- Services -->
        <div
          class="p-4 bg-gray-800/50 rounded-lg border border-border md:col-span-2"
        >
          <h3 class="text-lg font-semibold text-white mb-3">Services</h3>
          <div class="flex flex-wrap gap-2">
            {#each selectedServiceLabels() as label (label)}
              <span
                class="px-3 py-1 bg-primary/10 text-primary rounded-full text-sm"
                >{label}</span
              >
            {/each}
          </div>
        </div>

        <!-- Owner Bootstrap -->
        <div
          class="p-4 bg-gray-800/50 rounded-lg border border-border md:col-span-2"
          data-testid="techie-owner-bootstrap"
        >
          <h3 class="text-lg font-semibold text-white mb-3">Owner bootstrap</h3>
          <div class="grid gap-3 md:grid-cols-2">
            <div>
              <label
                for="techie-owner-username"
                class="block text-sm text-muted-foreground mb-1"
                >Owner username</label
              >
              <input
                id="techie-owner-username"
                type="text"
                bind:value={config.owner.username}
                placeholder="owner"
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-white placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
                data-testid="techie-owner-username"
              />
            </div>
            <div>
              <label
                for="techie-owner-email"
                class="block text-sm text-muted-foreground mb-1"
                >Owner email</label
              >
              <input
                id="techie-owner-email"
                type="email"
                bind:value={config.owner.email}
                placeholder="owner@example.com"
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-white placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
                data-testid="techie-owner-email"
              />
            </div>
          </div>
          <div class="mt-3">
            <label
              for="techie-owner-display-name"
              class="block text-sm text-muted-foreground mb-1"
              >Display name</label
            >
            <input
              id="techie-owner-display-name"
              type="text"
              bind:value={config.owner.displayName}
              placeholder={config.owner.username || "Owner"}
              class="w-full px-3 py-2 bg-input border border-border rounded-lg text-white placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
              data-testid="techie-owner-display-name"
            />
            {#if ownerDisplayNamePreview}
              <p class="text-xs text-muted-foreground mt-1">
                Owner preview: {ownerDisplayNamePreview}
              </p>
            {/if}
          </div>
        </div>

        <!-- Recovery -->
        <div
          class="p-4 bg-gray-800/50 rounded-lg border border-border md:col-span-2"
          data-testid="techie-recovery-card"
        >
          <h3 class="text-lg font-semibold text-white mb-3">
            Recovery passphrase
          </h3>
          <div class="grid gap-3 md:grid-cols-2">
            <div>
              <label
                for="techie-recovery-passphrase"
                class="block text-sm text-muted-foreground mb-1"
                >Recovery passphrase</label
              >
              <input
                id="techie-recovery-passphrase"
                type="password"
                bind:value={recoveryPassphrase}
                oninput={resetRecoveryHash}
                autocomplete="new-password"
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-white focus:border-primary focus:ring-1 focus:ring-primary"
                data-testid="techie-recovery-passphrase"
              />
            </div>
            <div>
              <label
                for="techie-recovery-passphrase-confirm"
                class="block text-sm text-muted-foreground mb-1"
                >Confirm recovery passphrase</label
              >
              <input
                id="techie-recovery-passphrase-confirm"
                type="password"
                bind:value={recoveryPassphraseConfirm}
                oninput={resetRecoveryHash}
                onblur={syncRecoveryHash}
                autocomplete="new-password"
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-white focus:border-primary focus:ring-1 focus:ring-primary"
                data-testid="techie-recovery-passphrase-confirm"
              />
            </div>
          </div>
          <p
            class="text-xs text-muted-foreground mt-2"
            data-testid="techie-recovery-strength"
          >
            Strength: {recoveryStrength.score}/4
          </p>
          {#if recoveryPassphraseConfirm && !recoveryPassphrasesMatch}
            <p class="text-xs text-error mt-1">
              Recovery passphrases do not match.
            </p>
          {/if}
          {#if isHashingRecovery}
            <p
              class="text-xs text-muted-foreground mt-1"
              data-testid="techie-recovery-status"
            >
              Hashing recovery passphrase...
            </p>
          {:else if hasRecoveryHash}
            <p
              class="text-xs text-success mt-1"
              data-testid="techie-recovery-status"
            >
              Recovery passphrase ready.
            </p>
          {:else if recoveryHashError}
            <p
              class="text-xs text-error mt-1"
              data-testid="techie-recovery-status"
            >
              {recoveryHashError}
            </p>
          {/if}
        </div>
      </div>

      <!-- Final Options -->
      <Accordion title="Deployment Options">
        <div class="space-y-3">
          <label class="flex items-center gap-2 text-foreground/80">
            <input
              type="checkbox"
              bind:checked={config.advanced.autoUpdates}
              class="rounded border-border text-primary"
            />
            Enable automatic updates
          </label>
          <label class="flex items-center gap-2 text-foreground/80">
            <input
              type="checkbox"
              bind:checked={config.advanced.backupsEnabled}
              class="rounded border-border text-primary"
            />
            Enable automatic backups
          </label>
        </div>
      </Accordion>

      <TipBox variant="info">
        After creation, you'll receive a registration token to connect workers
        to this stack.
      </TipBox>
    </div>
  {/if}
</div>
