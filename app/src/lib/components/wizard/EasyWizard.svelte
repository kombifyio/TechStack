<!--
  EasyWizard Component
  
  5-step wizard for easy mode stack creation:
  1. Goals - What do you want to do?
  2. Server - Where should the server come from?
  3. Access - Where do you need access?
  4. Users - Who will use it?
  5. Login - How will users authenticate?
  
  NOTE: Requirements analysis and Unifier processing happen AFTER the wizard,
  on the /stacks/creating page. This keeps the wizard focused on user input.
  
  Dispatches 'oncreate' event with StackConfig when user clicks Create.
-->
<script lang="ts">
  import { Stepper } from "./index";
  import GoalsStep from "./steps/GoalsStep.svelte";
  import ServerStep from "./steps/ServerStep.svelte";
  import AccessStep from "./steps/AccessStep.svelte";
  import UsersStep from "./steps/UsersStep.svelte";
  import {
    ACTIVE_STANDARD_BUNDLE,
    type StackConfig,
    type AccessModeValue,
    type AudienceConfigKey,
    type BundleQuestionDefinition,
    type CanonicalUseCaseGoalKey,
    type GoalConfigKey,
    type ManagedProviderID,
    type WizardDeploymentLane,
    applyWizardDeploymentLane,
    createDefaultConfig,
    deriveServicesFromGoals,
    EASY_STEPS,
    getEasyAccessQuestions,
    getEasyAudienceQuestions,
    getEasyGoalQuestions,
    getVpnQuestions,
  } from "$lib/wizard";
  import { OwnerStepState } from "$lib/wizard/owner-state.svelte";
  import LoginStep from "./steps/LoginStep.svelte";
  import { tr } from "$lib/i18n.svelte";
  import { authStore } from "$lib/stores/auth.svelte";
  import { features, getDefaultEnabled } from "$lib/stores/features";

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
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
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
    onmanagedproviderselect,
  }: Props = $props();

  // Initialize config with defaults
  let config = $state<StackConfig>(createDefaultConfig());
  let step = $state(1);
  let advancedOpen = $state(false);
  let deploymentLaneApplied = $state(false);
  let initialConfigApplied = $state(false);
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

  // Show validation only after interaction so initial rendering stays calm.
  let hasAttemptedNext = $state(false);

  // Step-5 owner/auth state (recovery passphrase, cloud link, validation)
  const ownerState = new OwnerStepState(() => config);
  const wizardDeploymentLane = $derived<WizardDeploymentLane>(
    authStore.deploymentMode === "saas" ? "saas" : "self-hosted",
  );

  $effect(() => {
    if (!initialConfig || initialConfigApplied) return;
    config = cloneConfig(initialConfig) ?? createDefaultConfig();
    initialConfigApplied = true;
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
    config.admin.password = "";
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
        !config.auth.allowPasswordless &&
        config.admin.password === "")
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
  const hasGoals = $derived(
    Boolean(
      config.goals?.everything ||
      availableCanonicalUseCaseGoals.some((goal) => config.goals?.[goal]),
    ),
  );

  const hasUsers = $derived(
    config.audience.onlyMe ||
      config.audience.familyFriends ||
      config.audience.public,
  );

  // Validation error messages for better feedback
  const validationErrors = $derived(() => {
    const errors: string[] = [];

    if (step === 1 && !hasGoals) {
      errors.push("Please select at least one goal for your server");
    }

    if (
      step === 2 &&
      config.serverProvisioning.mode === "connect-remote" &&
      !config.serverProvisioning.remote.host.trim()
    ) {
      errors.push("Server host or IP is required for direct connection");
    }

    if (step === 3 && !config.network.accessMode) {
      errors.push("Please select an access mode (Home only or Anywhere)");
    }

    if (step === 4 && !hasUsers) {
      errors.push("Please select who will use your server");
    }

    if (step === 5) {
      errors.push(...ownerState.ownerValidationErrors());
    }

    return errors;
  });

  // Robustness principle: defaults keep navigation available; validation errors
  // are hints unless a later step needs a concrete host/admin value.
  const canGoNext = $derived(() => {
    if (step === 1) return true;
    if (step === 2) {
      if (config.serverProvisioning.mode !== "connect-remote") return true;
      return config.serverProvisioning.remote.host.trim().length > 0;
    }
    if (step === 3) return true;
    if (step === 4) return true;
    if (step === 5) return ownerState.adminIsValid();
    return true;
  });

  // Deploy ist erlaubt wenn wir den letzten Schritt erreicht haben
  const canDeploy = $derived(() => step === EASY_STEPS.length);

  // Navigation records attempted progress so guidance appears at the right time.
  function goNext() {
    hasAttemptedNext = true;
    if (step < EASY_STEPS.length && canGoNext()) step++;
  }

  function goPrev() {
    if (step > 1) step--;
  }

  // Derive services from goals
  function deriveServices() {
    deriveServicesFromGoals(config);
  }

  // Handle create
  async function handleCreate() {
    hasAttemptedNext = true;
    enforceExistingOwnerReuse();

    const recoveryReady = await ownerState.syncRecoveryHash();
    if (!recoveryReady || !ownerState.adminIsValid()) {
      return;
    }

    config.wizardType = "easy";
    deriveServices();
    oncreate?.(config);
  }

  // Goal change handlers
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
  }

  function setAudience(audience: AudienceConfigKey, value: boolean) {
    config.audience[audience] = value;
  }
</script>

<div
  class="card w-full min-w-0 overflow-hidden p-4 sm:p-6 lg:p-8"
  data-testid="easy-wizard"
  data-standard-bundle-id={standardBundle.id}
  data-standard-bundle-version={standardBundle.version}
>
  <!-- Validation Hints Banner - nur bei Step 4 Auth-Problemen (sanft, informativ) -->
  {#if (step === 2 || step === 5) && validationErrors().length > 0 && hasAttemptedNext}
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
    steps={EASY_STEPS}
    currentStep={step}
    canGoNext={canGoNext()}
    canDeploy={canDeploy()}
    {isDeploying}
    deployLabel={submitLabel}
    deployingLabel={submittingLabel}
    onprev={goPrev}
    onnext={goNext}
    ondeploy={handleCreate}
  />

  <!-- Step 1: Goals -->
  {#if step === 1}
    <GoalsStep
      {config}
      primaryGoalQuestions={primaryGoalQuestions}
      advancedGoalQuestions={advancedGoalQuestions}
      {advancedOpen}
      onGoalChange={setGoal}
    />
  {/if}

  <!-- Step 2: Server -->
  {#if step === 2}
    <ServerStep
      bind:config
      showRole={showServerRole}
      showServices={showServerServices}
      {onmanagedproviderselect}
    />
  {/if}

  <!-- Step 3: Access -->
  {#if step === 3}
    <AccessStep
      {config}
      {accessQuestions}
      {vpnQuestions}
      onAccessModeSelect={setAccessMode}
    />
  {/if}

  <!-- Step 4: Users -->
  {#if step === 4}
    <UsersStep {config} {audienceQuestions} onAudienceChange={setAudience} />
  {/if}

  <!-- Step 5: Login -->
  {#if step === 5}
    <LoginStep {config} owner={ownerState} lane={wizardDeploymentLane} />
  {/if}
</div>
