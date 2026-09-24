<!--
  EasyWizard Component

  Five recognizable modules; each operation visits only the ones it can change.

  Dispatches 'oncreate' event with StackConfig when user clicks Create.
-->
<script lang="ts">
  import { tick, type Snippet } from "svelte";
  import { Stepper } from "./index";
  import GoalsStep from "./steps/GoalsStep.svelte";
  import ServerStep from "./steps/ServerStep.svelte";
  import AccessStep from "./steps/AccessStep.svelte";
  import UsersStep from "./steps/UsersStep.svelte";
  import {
    ACTIVE_STANDARD_BUNDLE,
    isValidSubstrateGuest,
    type StackConfig,
    type AccessModeValue,
    type AudienceConfigKey,
    type BundleQuestionDefinition,
    type CanonicalUseCaseGoalKey,
    type GoalConfigKey,
    type ManagedProviderID,
    type WizardDeploymentLane,
    applyPasskeyFirstOwnerLogin,
    applyWizardDeploymentLane,
    createDefaultConfig,
    deriveServicesFromGoals,
    EASY_STEPS,
    getEasyAccessQuestions,
    getEasyAudienceQuestions,
    getEasyGoalQuestions,
    getVpnQuestions,
    selectedCanonicalUseCasesFromGoals,
    WizardPreviewController,
    type WizardPreviewState,
  } from "#lib/wizard/index.js";
  import type { WizardRecommendation } from "#lib/api/unifier.js";
  import { OwnerStepState } from "#lib/wizard/owner-state.svelte.js";
  import type {
    HouseholdPersonDraft,
    HouseholdProfile,
  } from "#lib/wizard/household-draft.js";
  import LoginStep from "./steps/LoginStep.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import {
    loadUseCaseCatalog,
    type UseCaseCatalogView,
  } from "#lib/api/useCaseCatalog.js";
  import { features, getDefaultEnabled } from "#lib/stores/features.js";

  interface Props {
    oncreate?: (config: StackConfig) => void;
    initialConfig?: StackConfig;
    isDeploying?: boolean;
    submitLabel?: string;
    submittingLabel?: string;
    showServerRole?: boolean;
    showServerServices?: boolean;
    applyDeploymentLaneDefaults?: boolean;
    reuseExistingOwner?: boolean;
    joinExistingDeployment?: boolean;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
    nodeOptions?: Snippet<[StackConfig]>;
    nodeAlternative?: Snippet;
  }

  let {
    oncreate,
    initialConfig,
    isDeploying = false,
    submitLabel = "Create",
    submittingLabel = "Creating...",
    showServerRole = false,
    showServerServices = false,
    applyDeploymentLaneDefaults = true,
    reuseExistingOwner = false,
    joinExistingDeployment = reuseExistingOwner,
    onmanagedproviderselect,
    nodeOptions,
    nodeAlternative,
  }: Props = $props();

  // Initialize config with defaults
  function initialWizardConfig() {
    const value = createDefaultConfig();
    if (value.goals) value.goals.files = true;
    return value;
  }
  let config = $state<StackConfig>(initialWizardConfig());
  let step = $state(1);
  let deploymentLaneApplied = $state(false);
  let initialConfigApplied = $state(false);
  let recommendationState = $state<WizardPreviewState>({ phase: "idle" });
  const recommendationController = new WizardPreviewController((state) => {
    recommendationState = state;
  });
  const standardBundle = ACTIVE_STANDARD_BUNDLE;
  const goalQuestions = getEasyGoalQuestions();
  const accessQuestions = getEasyAccessQuestions();
  const audienceQuestions = getEasyAudienceQuestions();
  const vpnQuestions = getVpnQuestions().filter(
    (option) => option.value !== "none",
  );
  const allFeatures = features.allFeatures;
  const primaryGoalQuestions = $derived.by(() =>
    goalQuestions.filter(
      (goal) =>
        isGoalEnabled(goal) && (goal.surface ?? "primary") === "primary",
    ),
  );
  const advancedGoalQuestions = $derived.by(() =>
    goalQuestions.filter(
      (goal) => isGoalEnabled(goal) && goal.surface === "advanced",
    ),
  );
  const availableCanonicalUseCaseGoals = $derived.by(() =>
    [...primaryGoalQuestions, ...advancedGoalQuestions]
      .map((goal) => goal.configKey)
      .filter(isCanonicalUseCaseGoal),
  );

  const surfaceSteps = EASY_STEPS;
  const disabledStepReasons = $derived.by(() => {
    const reasons: Record<number, string> = {};
    if (joinExistingDeployment) {
      reasons[3] = tr("wizard.step.inherited");
      reasons[4] = tr("wizard.step.inherited");
    }
    if (reuseExistingOwner) reasons[5] = tr("wizard.step.existingOwner");
    return reasons;
  });
  const activeStepIds = $derived(
    surfaceSteps
      .filter((item) => !disabledStepReasons[item.id])
      .map((item) => item.id),
  );
  const lastActiveStep = $derived(activeStepIds.at(-1) ?? 1);
  const currentStepKey = $derived(
    surfaceSteps.find((item) => item.id === step)?.key ?? "",
  );

  // Show validation only after interaction so initial rendering stays calm.
  let hasAttemptedNext = $state(false);

  // Step-5 owner/auth state (recovery passphrase, cloud link, validation)
  const ownerState = new OwnerStepState(() => config);
  const wizardDeploymentLane = $derived<WizardDeploymentLane>(
    authStore.deploymentMode === "saas" ? "saas" : "self-hosted",
  );
  const recommendationResult = $derived(
    recommendationState.phase === "resolved"
      ? recommendationState.result
      : recommendationState.phase === "loading" ||
          recommendationState.phase === "degraded"
        ? recommendationState.previous
        : undefined,
  );
  const primaryRecommendation = $derived(
    recommendationResult?.recommendations.find((item) => item.recommended) ??
      recommendationResult?.recommendations[0],
  );

  $effect(() => {
    if (!initialConfig || initialConfigApplied) return;
    config = cloneConfig(initialConfig) ?? createDefaultConfig();
    applyPasskeyFirstOwnerLogin(config);
    initialConfigApplied = true;
  });

  $effect(() => {
    if (reuseExistingOwner) return;
    recommendationController.update({
      smart_home_settings: config.useCaseSettings?.["smart-home"],
      smart_home_context: {
        existing:
          config.useCaseSettings?.["smart-home"]?.["instance-origin"] ===
          "existing",
        needs_radio:
          config.useCaseSettings?.["smart-home"]?.["device-passthrough"] ===
          true,
        ...(config.serverProvisioning.mode === "hypervisor" &&
        config.serverProvisioning.substrate?.advisoryInventory
          ? {
              proxmox_available: true,
              lan_reachable: false,
              cpu: config.serverProvisioning.substrate.advisoryInventory.cpu,
              memory_mib:
                config.serverProvisioning.substrate.advisoryInventory.memoryMiB,
              disk_gib:
                config.serverProvisioning.substrate.advisoryInventory.diskGiB,
            }
          : {}),
      },
      goals: selectedCanonicalUseCasesFromGoals(config.goals),
      services: [],
      deployment_lane: wizardDeploymentLane === "saas" ? "saas" : "self-hosted",
      ...(config.serverProvisioning.mode === "kombify-cloud"
        ? { provider_id: config.providerId }
        : {}),
      surface: "easy",
    });
  });

  $effect(() => () => recommendationController.destroy());

  $effect(() => {
    if (reuseExistingOwner) return;
    ownerState.setSignedInAccount({
      email: authStore.userEmail ?? "",
      displayName: authStore.userName ?? "",
      emailVerified: authStore.currentUser?.email_verified === true,
    });
  });

  function enforceExistingOwnerReuse() {
    if (!reuseExistingOwner) return;
    config.owner.bootstrapMode = "none";
    config.owner.source = "local";
    config.owner.username = "";
    config.owner.email = "";
    config.owner.displayName = "";
    config.owner.recoveryPassphraseHash = "";
    config.owner.recoveryMaterialRef = "";
    config.auth.requirePassword = false;
    config.auth.requireMfa = false;
    config.auth.allowPasswordless = false;
  }

  $effect(() => {
    if (
      !reuseExistingOwner ||
      (config.owner.bootstrapMode === "none" &&
        config.owner.source === "local" &&
        config.owner.username === "" &&
        config.owner.email === "" &&
        config.owner.displayName === "" &&
        config.owner.recoveryPassphraseHash === "" &&
        config.owner.recoveryMaterialRef === "" &&
        !config.auth.requirePassword &&
        !config.auth.requireMfa &&
        !config.auth.allowPasswordless)
    )
      return;
    enforceExistingOwnerReuse();
  });

  $effect(() => {
    if (
      deploymentLaneApplied ||
      !applyDeploymentLaneDefaults ||
      !authStore.modeDetected
    )
      return;
    applyWizardDeploymentLane(config, wizardDeploymentLane);
    deploymentLaneApplied = true;
  });

  $effect(() => {
    if (!config.goals) return;
    for (const goal of goalQuestions) {
      if (!isCanonicalUseCaseGoal(goal.configKey)) continue;
      if (!availableCanonicalUseCaseGoals.includes(goal.configKey)) {
        config.goals[goal.configKey] = false;
      }
    }
    const allAvailableSelected =
      availableCanonicalUseCaseGoals.length > 0 &&
      availableCanonicalUseCaseGoals.every((goal) => config.goals?.[goal]);
    if (config.goals.everything !== allAvailableSelected) {
      config.goals.everything = allAvailableSelected;
    }
  });

  function cloneConfig(value?: StackConfig): StackConfig | null {
    if (!value) return null;
    return JSON.parse(JSON.stringify(value)) as StackConfig;
  }

  function isGoalEnabled(
    goal: BundleQuestionDefinition<GoalConfigKey>,
  ): boolean {
    if (!goal.featureFlag) return true;
    return (
      $allFeatures.get(goal.featureFlag)?.enabled ??
      getDefaultEnabled(goal.featureFlag)
    );
  }

  function isCanonicalUseCaseGoal(
    goal: GoalConfigKey,
  ): goal is CanonicalUseCaseGoalKey {
    return goal !== "everything";
  }

  // Validation helpers
  const hasUsers = $derived(
    config.audience.onlyMe ||
      config.audience.familyFriends ||
      config.audience.public,
  );

  // Validation error messages for better feedback
  const validationErrors = $derived(() => {
    const errors: string[] = [];

    if (
      currentStepKey === "server" &&
      config.serverProvisioning.mode === "connect-remote" &&
      !config.serverProvisioning.remote.host.trim()
    ) {
      errors.push("Server host or IP is required for direct connection");
    }

    if (currentStepKey === "access" && !config.network.accessMode) {
      errors.push("Please select an access mode (Home only or Anywhere)");
    }
    if (
      currentStepKey === "access" &&
      config.serverProvisioning.mode === "kombify-cloud" &&
      config.network.accessMode === "home"
    ) {
      errors.push(tr("wizard.access.home.managedReason"));
    }

    if (currentStepKey === "users" && !hasUsers) {
      errors.push("Please select who will use your server");
    }

    if (currentStepKey === "login") {
      errors.push(...ownerState.ownerValidationErrors());
    }

    return errors;
  });

  // Robustness principle: defaults keep navigation available; validation errors
  // are hints unless a later step needs a concrete host/admin value.
  let serverSelectionReady = $state(true);
  let stepContent = $state<HTMLDivElement>();
  const canGoNext = $derived(() => {
    if (currentStepKey === "server") {
      if (!serverSelectionReady) return false;
      if (config.serverProvisioning.mode === "hypervisor") {
        return isValidSubstrateGuest(config.serverProvisioning.substrate);
      }
      if (config.serverProvisioning.mode !== "connect-remote") return true;
      return config.serverProvisioning.remote.host.trim().length > 0;
    }
    if (currentStepKey === "login") return ownerState.adminIsValid();
    if (currentStepKey === "users" && !joinExistingDeployment)
      return !config.network.publicAccess;
    if (currentStepKey === "access")
      return !(
        config.serverProvisioning.mode === "kombify-cloud" &&
        config.network.accessMode === "home"
      );
    return true;
  });

  const canDeploy = $derived(
    () =>
      step === lastActiveStep &&
      canGoNext() &&
      (config.serverProvisioning.mode !== "hypervisor" ||
        isValidSubstrateGuest(config.serverProvisioning.substrate)),
  );

  // Navigation records attempted progress so guidance appears at the right time.
  async function revealStep() {
    await tick();
    stepContent?.focus({ preventScroll: true });
    stepContent?.scrollIntoView({
      block: "start",
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches
        ? "instant"
        : "smooth",
    });
  }

  async function goNext() {
    hasAttemptedNext = true;
    const next = activeStepIds.find((id) => id > step);
    if (next && canGoNext()) {
      step = next;
      await revealStep();
    }
  }

  async function goPrev() {
    const previous = activeStepIds.findLast((id) => id < step);
    if (previous) {
      step = previous;
      await revealStep();
    }
  }

  // Derive services from goals
  function deriveServices() {
    const existingOverlay = config.services.headscale;
    deriveServicesFromGoals(config);
    // A joined Node inherits access from its Homelab; the hidden first-run
    // access default must not turn on a new overlay during goal derivation.
    if (joinExistingDeployment) config.services.headscale = existingOverlay;
  }

  // Handle create
  async function handleCreate() {
    hasAttemptedNext = true;
    enforceExistingOwnerReuse();
    if (!canDeploy()) return;

    const recoveryReady = await ownerState.syncRecoveryHash();
    if (!recoveryReady || !ownerState.adminIsValid()) {
      return;
    }

    config.wizardType = "easy";
    deriveServices();
    oncreate?.(config);
  }

  /**
   * Which products each use case is built from, straight from the StackKits
   * catalog. Loaded once and best-effort: the Goals step renders immediately
   * and the component strips appear when the catalog answers. A deployment
   * without a catalog keeps cards with no strip rather than an empty step.
   */
  let useCaseCatalog = $state<Map<string, UseCaseCatalogView>>(new Map());
  $effect(() => {
    let cancelled = false;
    void loadUseCaseCatalog().then((catalog) => {
      if (!cancelled) useCaseCatalog = catalog;
    });
    return () => {
      cancelled = true;
    };
  });

  /*
   * The Goals step emits raw disclosure observations (§3 rule 5), and nothing
   * here consumes them yet — deliberately. The only counter Techstack has,
   * `countAdvancedInteractions` in wizard/spec.ts, is derived from `config`,
   * and this toggle writes no config on purpose; and §4 retires the frontend
   * score it feeds. Wiring an observation sink is kombify-Techstack-joei,
   * not a counter invented here that nothing reads.
   */

  // Goal change handlers
  /**
   * A decision about a use case. Stored only when set explicitly; the catalog
   * keeps the defaults. Writing config directly keeps the same ownership as
   * setGoal: the wizard owns the config, the step only reports.
   */
  function setUseCaseSetting(
    goal: GoalConfigKey,
    settingId: string,
    value: string | boolean,
  ) {
    config.useCaseSettings ??= {};
    config.useCaseSettings[goal] = {
      ...(config.useCaseSettings[goal] ?? {}),
      [settingId]: value,
    };
  }

  function setGoal(goal: GoalConfigKey, value: boolean) {
    if (config.goals) {
      config.goals[goal] = value;
      // If "everything" is selected, toggle all currently available use cases.
      if (goal === "everything") {
        for (const useCase of availableCanonicalUseCaseGoals) {
          config.goals[useCase] = value;
        }
        config.goals.everything = value;
      } else if (
        isCanonicalUseCaseGoal(goal) &&
        !availableCanonicalUseCaseGoals.includes(goal)
      ) {
        config.goals[goal] = false;
        config.goals.everything = false;
      } else if (!value) {
        config.goals.everything = false;
      } else if (
        availableCanonicalUseCaseGoals.length > 0 &&
        availableCanonicalUseCaseGoals.every(
          (useCase) => config.goals?.[useCase],
        )
      ) {
        config.goals.everything = true;
      }
    }
  }

  function setAccessMode(value: AccessModeValue) {
    config.network.accessMode = value;
    config.network.accessModeSelected = true;
  }

  function setAudience(audience: AudienceConfigKey, value: boolean) {
    config.audience[audience] = value;
    if (audience === "public") config.network.publicAccess = value;
  }

  function setHouseholdProfile(profile: HouseholdProfile) {
    config.household = { profile, people: config.household?.people ?? [] };
    config.audience.onlyMe = profile === "solo";
    config.audience.familyFriends = profile !== "solo";
  }

  function setHouseholdPeople(people: HouseholdPersonDraft[]) {
    config.household = {
      profile:
        config.household?.profile ??
        (config.audience.onlyMe ? "solo" : "shared"),
      people,
    };
  }

  function openRecommendation(_recommendation: WizardRecommendation) {
    step = 2;
  }
</script>

<div
  class="w-full min-w-0"
  data-testid="easy-wizard"
  data-standard-bundle-id={standardBundle.id}
  data-standard-bundle-version={standardBundle.version}
>
  <!-- Validation Hints Banner - nur bei Step 4 Auth-Problemen (sanft, informativ) -->
  {#if (currentStepKey === "server" || currentStepKey === "login") && validationErrors().length > 0 && hasAttemptedNext}
    <div class="mb-6 p-4 rounded-lg border border-warning/30 bg-warning/10">
      <div class="flex items-start gap-3">
        <svg
          class="w-5 h-5 flex-shrink-0 mt-0.5 text-warning"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
          />
        </svg>
        <div>
          <p class="font-medium text-foreground">
            {tr("wizard.validation.completeFields")}
          </p>
          <ul class="mt-1 text-sm text-muted-foreground space-y-1">
            {#each validationErrors() as error, index (index)}
              <li>• {error}</li>
            {/each}
          </ul>
        </div>
      </div>
    </div>
  {/if}

  <Stepper
    steps={surfaceSteps}
    {disabledStepReasons}
    currentStep={step}
    canGoNext={!nodeAlternative && canGoNext()}
    canDeploy={!nodeAlternative && canDeploy()}
    showDeploy={!nodeAlternative}
    {isDeploying}
    deployLabel={submitLabel}
    deployingLabel={submittingLabel}
    onprev={goPrev}
    onnext={goNext}
    ondeploy={handleCreate}
  />

  <div
    bind:this={stepContent}
    tabindex="-1"
    class="mx-auto w-full min-w-0 scroll-mt-6 outline-none {currentStepKey ===
      'goals' || currentStepKey === 'server'
      ? ''
      : 'max-w-4xl'}"
  >
    {#if currentStepKey === "goals"}
      <GoalsStep
        bind:config
        {primaryGoalQuestions}
        {advancedGoalQuestions}
        onGoalChange={setGoal}
        onSettingChange={setUseCaseSetting}
        {recommendationState}
        onRecommendationOpen={openRecommendation}
        {useCaseCatalog}
      />
    {/if}

    {#if currentStepKey === "server"}
      <ServerStep
        bind:config
        bind:selectionReady={serverSelectionReady}
        {nodeOptions}
        {nodeAlternative}
        showRole={showServerRole}
        showServices={showServerServices}
        lockFoundation={joinExistingDeployment}
        joinSurface={joinExistingDeployment}
        {onmanagedproviderselect}
        recommendedStackKit={primaryRecommendation?.stackkit}
      />
    {/if}

    {#if currentStepKey === "access" && !joinExistingDeployment}
      <AccessStep
        {config}
        {accessQuestions}
        {vpnQuestions}
        onAccessModeSelect={setAccessMode}
        onDomainBaseChange={(domain) => (config.network.domainBase = domain)}
      />
    {/if}

    {#if currentStepKey === "users" && !joinExistingDeployment}
      <UsersStep
        {config}
        {audienceQuestions}
        onAudienceChange={setAudience}
        householdProfile={config.household?.profile}
        peopleDrafts={config.household?.people ?? []}
        onHouseholdProfileChange={setHouseholdProfile}
        onPeopleDraftsChange={setHouseholdPeople}
      />
    {/if}

    {#if currentStepKey === "login" && !reuseExistingOwner}
      <LoginStep {config} owner={ownerState} lane={wizardDeploymentLane} />
    {/if}
  </div>
  <Stepper
    navigationOnly
    steps={surfaceSteps}
    {disabledStepReasons}
    currentStep={step}
    canGoNext={!nodeAlternative && canGoNext()}
    canDeploy={!nodeAlternative && canDeploy()}
    showDeploy={!nodeAlternative}
    {isDeploying}
    deployLabel={submitLabel}
    deployingLabel={submittingLabel}
    onprev={goPrev}
    onnext={goNext}
    ondeploy={handleCreate}
  />
</div>
