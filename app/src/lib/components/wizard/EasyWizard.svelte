<!--
  EasyWizard Component

  Five recognizable modules; each operation visits only the ones it can change.

  Dispatches 'oncreate' event with StackConfig when the person creates the
  homelab on the last step.

  Answers survive a reload (draftKey): the non-secret part of the config is
  kept in sessionStorage for this tab and ends when the creation is accepted
  or the person chooses to leave. Passwords and passphrases are never stored.
-->
<script lang="ts">
  import { onDestroy, tick, untrack, type Snippet } from "svelte";
  import { beforeNavigate, goto } from "$app/navigation";
  import { Stepper } from "./index";
  import GoalsStep from "./steps/GoalsStep.svelte";
  import LayoutRecommendationPanel from "./LayoutRecommendationPanel.svelte";
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
    buildLayoutSelection,
    selectedCanonicalUseCasesFromGoals,
    WizardPreviewController,
    type WizardPreviewState,
  } from "#lib/wizard/index.js";
  import type { WizardRecommendation } from "#lib/api/unifier.js";
  import type {
    LayoutFlow,
    LayoutRecommendationAcceptance,
    LayoutRecommendationView,
  } from "#lib/api/layout.js";
  import {
    LayoutPreviewController,
    acceptedLayoutFromPreview,
    type LayoutPreviewState,
  } from "#lib/wizard/LayoutPreviewController.js";
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
  import { creationGoals } from "#lib/wizard/creationGoals.js";
  import { features, getDefaultEnabled } from "#lib/stores/features.js";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import {
    clearWizardDraft,
    loadWizardDraft,
    mergeOverDefaults,
    saveWizardDraft,
  } from "#lib/wizard/draft-storage.js";
  import {
    provideWizardValidation,
    type WizardIssue,
  } from "#lib/wizard/validation-context.js";

  interface Props {
    /**
     * Called when the person creates on the last step. `layoutRecommendation`
     * is the recommended distribution the person was shown, present only when
     * it is recorded, open and verified against their hardware; the run request
     * carries it, and finishing the run accepts it.
     */
    oncreate?: (
      config: StackConfig,
      extras?: { layoutRecommendation?: LayoutRecommendationAcceptance },
    ) => void;
    initialConfig?: StackConfig;
    isDeploying?: boolean;
    /** Text of the final button; the catalog's "Create homelab" when omitted. */
    submitLabel?: string;
    submittingLabel?: string;
    /**
     * Keeps the entered answers across a reload under this name. Without it
     * nothing is stored (surfaces that join an existing homelab).
     */
    draftKey?: string;
    /**
     * Feedback about the submission (progress, errors). It renders directly
     * above the footer navigation, where the person pressed the button.
     */
    footerNotice?: Snippet;
    showServerRole?: boolean;
    showServerServices?: boolean;
    applyDeploymentLaneDefaults?: boolean;
    reuseExistingOwner?: boolean;
    joinExistingDeployment?: boolean;
    /**
     * The deployment the wizard adds to. With joinExistingDeployment and a
     * selected use case, the wizard asks for the recommended distribution of
     * the selection and shows it on the Goals step.
     */
    layoutStackId?: string;
    layoutFlow?: LayoutFlow;
    /** A replacement a superseded run handed back; shown to be confirmed again. */
    layoutReplacement?: LayoutRecommendationView;
    /** Changes when the shown recommendation must be asked for again. */
    layoutRefresh?: number;
    /** The server step is inherited from the deployment (use cases only). */
    inheritServer?: boolean;
    /** The run needs at least one selected use case (adding use cases). */
    requireGoal?: boolean;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
    nodeOptions?: Snippet<[StackConfig]>;
    nodeAlternative?: Snippet;
  }

  let {
    oncreate,
    initialConfig,
    isDeploying = false,
    submitLabel,
    submittingLabel,
    draftKey,
    footerNotice,
    showServerRole = false,
    showServerServices = false,
    applyDeploymentLaneDefaults = true,
    reuseExistingOwner = false,
    joinExistingDeployment = reuseExistingOwner,
    layoutStackId,
    layoutFlow = "add-server",
    layoutReplacement,
    layoutRefresh = 0,
    inheritServer = false,
    requireGoal = false,
    onmanagedproviderselect,
    nodeOptions,
    nodeAlternative,
  }: Props = $props();

  // A reload restores the answers of this tab (secrets excluded).
  const restoredDraft = untrack(() =>
    draftKey ? loadWizardDraft(draftKey) : null,
  );

  // Initialize config with defaults
  function initialWizardConfig() {
    if (restoredDraft) {
      const restored = mergeOverDefaults(
        createDefaultConfig(),
        restoredDraft.config,
      );
      applyPasskeyFirstOwnerLogin(restored);
      return restored;
    }
    const stored = cloneConfig(initialConfig);
    if (stored) {
      applyPasskeyFirstOwnerLogin(stored);
      return stored;
    }
    const value = createDefaultConfig();
    if (value.goals) value.goals.files = true;
    return value;
  }
  let config = $state<StackConfig>(untrack(initialWizardConfig));
  let step = $state(restoredDraft?.step ?? 1);
  let draftRestored = $state(Boolean(restoredDraft));
  // Protect entered answers against accidental departure (reload, tab close,
  // in-app navigation). The config carries SSH passwords and recovery
  // material, so only its non-secret part is kept (draftKey); a reload brings
  // that back and asks for the secrets again. Protection ends once the
  // submission is accepted (isDeploying) and stays in place after a failed one.
  let userEdited = $state(false);
  const hasUnsavedAnswers = $derived((userEdited || step > 1) && !isDeploying);

  // The app's own dialog replaces window.confirm. It answers later than the
  // navigation asks, so the navigation is cancelled first and repeated once
  // the person agreed to leave.
  let leaveConfirmed = false;
  beforeNavigate((navigation) => {
    if (!hasUnsavedAnswers || navigation.willUnload || leaveConfirmed) return;
    const target = navigation.to;
    if (!target) return;
    navigation.cancel();
    void confirmInApp({
      title: tr("wizard.leave.title"),
      message: tr("wizard.leave.confirm"),
      confirmText: tr("wizard.leave.confirmAction"),
      cancelText: tr("wizard.leave.stay"),
      tone: "warning",
    }).then(async (leave) => {
      if (!leave) return;
      leaveConfirmed = true;
      if (draftKey) clearWizardDraft(draftKey);
      try {
        await goto(target.url);
      } finally {
        leaveConfirmed = false;
      }
    });
  });

  // Keep the non-secret answers for this tab while the person works.
  $effect(() => {
    if (!draftKey || isDeploying) return;
    if (!userEdited && step === 1 && !restoredDraft) return;
    const key = draftKey;
    const answers = $state.snapshot(config) as StackConfig;
    const position = step;
    const timer = setTimeout(
      () => saveWizardDraft(key, { step: position, config: answers }),
      300,
    );
    return () => clearTimeout(timer);
  });

  // An accepted submission ends the draft; a failed one keeps it.
  onDestroy(() => {
    if (draftKey && isDeploying) clearWizardDraft(draftKey);
  });

  $effect(() => {
    if (!hasUnsavedAnswers) return;
    const guard = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  });
  let deploymentLaneApplied = $state(Boolean(restoredDraft));
  let initialConfigApplied = $state(
    Boolean(untrack(() => initialConfig)) || Boolean(restoredDraft),
  );
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
    creationGoals(
      [...primaryGoalQuestions, ...advancedGoalQuestions],
      useCaseCatalog,
    )
      .filter((card) => card.availability === "available")
      .flatMap((card) => (card.question ? [card.question.configKey] : []))
      .filter(isCanonicalUseCaseGoal),
  );

  const surfaceSteps = EASY_STEPS;
  const disabledStepReasons = $derived.by(() => {
    const reasons: Record<number, string> = {};
    if (inheritServer) reasons[2] = tr("wizard.step.inherited");
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
      use_case_settings: config.useCaseSettings,
      use_case_capabilities: config.useCaseCapabilities,
      services: [],
      deployment_lane: wizardDeploymentLane === "saas" ? "saas" : "self-hosted",
      ...(config.serverProvisioning.mode === "kombify-cloud"
        ? { provider_id: config.providerId }
        : {}),
      surface: "easy",
    });
  });

  $effect(() => () => recommendationController.destroy());

  // Recommended distribution: only for a run against an existing deployment
  // with something selected. The preview follows the selection (debounced,
  // stale answers dropped); the run request carries the acceptable one.
  let layoutState = $state<LayoutPreviewState>({ phase: "idle" });
  const layoutController = new LayoutPreviewController((state) => {
    layoutState = state;
  });
  const layoutEnabled = $derived(
    Boolean(layoutStackId) && joinExistingDeployment && !nodeAlternative,
  );
  const layoutSelection = $derived(
    layoutEnabled && layoutStackId
      ? buildLayoutSelection(config, layoutStackId, layoutFlow)
      : null,
  );
  const layoutAcceptance = $derived(
    layoutEnabled
      ? acceptedLayoutFromPreview(layoutState, layoutSelection)
      : undefined,
  );
  $effect(() => {
    const selection = layoutSelection;
    untrack(() => layoutController.update(selection));
  });
  $effect(() => {
    const replacement = layoutReplacement;
    if (!replacement) return;
    untrack(() => {
      if (layoutSelection) layoutController.adopt(layoutSelection, replacement);
    });
  });
  let layoutRefreshSeen = untrack(() => layoutRefresh);
  $effect(() => {
    const requested = layoutRefresh;
    if (requested === layoutRefreshSeen) return;
    layoutRefreshSeen = requested;
    untrack(() => layoutController.retry(layoutSelection));
  });
  $effect(() => () => layoutController.destroy());

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
    // Catalog loading or a temporarily unavailable release must not erase
    // saved wishes. Runtime admission still validates every submitted goal.
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
  const hasSelectedGoal = $derived(
    selectedCanonicalUseCasesFromGoals(config.goals).length > 0,
  );
  const hasUsers = $derived(
    config.audience.onlyMe ||
      config.audience.familyFriends ||
      config.audience.public,
  );

  // What stands in the way on the current step, blockers first. Each sentence
  // says what to do, not what is wrong in code; a blocked "Next" shows the
  // first one right beside it, and the field it belongs to is marked invalid.
  const ownerFieldByMessage = $derived(
    new Map<string, WizardIssue["field"]>([
      [tr("wizard.login.profile.emailRequired"), "owner-email"],
      [tr("ui.ownerState.emailIsRequiredForPasswordless"), "owner-email"],
      [tr("ui.ownerState.recoveryPassphrasesDoNotMatch"), "owner-recovery"],
    ]),
  );
  const stepIssues = $derived.by((): WizardIssue[] => {
    const blockers: WizardIssue[] = [];
    const hints: WizardIssue[] = [];
    const server = config.serverProvisioning;

    if (currentStepKey === "goals") {
      if (requireGoal && !hasSelectedGoal) {
        blockers.push({
          field: "step",
          message: tr("wizard.validation.goalsRequired"),
        });
      }
      if (useCaseCatalogState === "loading") {
        blockers.push({
          field: "catalog",
          message: tr("wizard.catalog.blockedLoading"),
        });
      } else if (useCaseCatalogState === "error") {
        blockers.push({
          field: "catalog",
          message: tr("wizard.catalog.blockedError"),
        });
      }
    }

    if (currentStepKey === "server") {
      if (!serverSelectionReady) {
        blockers.push({
          field: "server-selection",
          message: tr("wizard.validation.serverSelection"),
        });
      }
      if (
        server.mode === "hypervisor" &&
        !isValidSubstrateGuest(server.substrate)
      ) {
        blockers.push({
          field: "hypervisor-guest",
          message: tr("wizard.validation.hypervisorGuest"),
        });
      }
      if (server.mode === "connect-remote" && !server.remote.host.trim()) {
        blockers.push({
          field: "server-host",
          message: tr("ui.easyWizard.serverHostOrIpIs"),
        });
      }
      if (
        server.mode === "connect-remote" &&
        authStore.walletSource === "kombify" &&
        !server.remote.sshWalletItemId
      ) {
        blockers.push({
          field: "server-wallet",
          message: tr("kwallet.wizard.item_required"),
        });
      }
    }

    if (currentStepKey === "access") {
      if (!config.network.accessMode) {
        hints.push({
          field: "access-mode",
          message: tr("ui.easyWizard.pleaseSelectAnAccessMode"),
        });
      }
      if (
        server.mode === "kombify-cloud" &&
        config.network.accessMode === "home"
      ) {
        blockers.push({
          field: "access-mode",
          message: tr("wizard.access.home.managedReason"),
        });
      }
    }

    if (currentStepKey === "users") {
      if (!joinExistingDeployment && config.network.publicAccess) {
        blockers.push({
          field: "public-access",
          message: tr("wizard.validation.publicUsers"),
        });
      }
      if (!hasUsers) {
        hints.push({
          field: "audience",
          message: tr("ui.easyWizard.pleaseSelectWhoWillUse"),
        });
      }
    }

    if (currentStepKey === "login") {
      const target = ownerState.adminIsValid() ? hints : blockers;
      for (const message of ownerState.ownerValidationErrors()) {
        target.push({
          field: ownerFieldByMessage.get(message) ?? "step",
          message,
        });
      }
    }

    return [...blockers, ...hints];
  });

  // The banner lists everything once the person tried to continue.
  provideWizardValidation({
    get attempted() {
      return hasAttemptedNext;
    },
    get issues() {
      return stepIssues;
    },
  });

  // Robustness principle: defaults keep navigation available; validation errors
  // are hints unless a later step needs a concrete host/admin value.
  let serverSelectionReady = $state(true);
  let stepContent = $state<HTMLDivElement>();
  const canGoNext = $derived(() => {
    // A failed or unfinished catalog read must not read as "nothing to install".
    if (currentStepKey === "goals")
      return useCaseCatalogState === "ready" && (!requireGoal || hasSelectedGoal);
    if (currentStepKey === "server") {
      if (!serverSelectionReady) return false;
      if (config.serverProvisioning.mode === "hypervisor") {
        return isValidSubstrateGuest(config.serverProvisioning.substrate);
      }
      if (config.serverProvisioning.mode !== "connect-remote") return true;
      // Hosted Techstack reaches the server with a Wallet item, so a join or
      // found run without one would only be refused (`wallet_item_required`).
      if (
        authStore.walletSource === "kombify" &&
        !config.serverProvisioning.remote.sshWalletItemId
      )
        return false;
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

  // The first thing in the way, shown beside the blocked forward button.
  const blockedReason = $derived(
    (step === lastActiveStep ? !canDeploy() : !canGoNext())
      ? (stepIssues[0]?.message ?? tr("wizard.validation.stepIncomplete"))
      : "",
  );

  // A blocked attempt moves to the list of what is missing, so a press on a
  // button at the foot of a long step never seems to do nothing.
  let validationSummary = $state<HTMLDivElement>();
  async function revealIssues() {
    await tick();
    validationSummary?.focus({ preventScroll: true });
    validationSummary?.scrollIntoView({
      block: "center",
      behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches
        ? "instant"
        : "smooth",
    });
  }

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
      // The next step starts calm; its hints appear after its own attempt.
      hasAttemptedNext = false;
      draftRestored = false;
      await revealStep();
    } else if (next && stepIssues.length > 0) {
      await revealIssues();
    }
  }

  async function goPrev() {
    const previous = activeStepIds.findLast((id) => id < step);
    if (previous) {
      step = previous;
      hasAttemptedNext = false;
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
    if (!canDeploy()) {
      if (stepIssues.length > 0) await revealIssues();
      return;
    }

    const recoveryReady = await ownerState.syncRecoveryHash();
    if (!recoveryReady || !ownerState.adminIsValid()) {
      await revealIssues();
      return;
    }

    config.wizardType = "easy";
    deriveServices();
    oncreate?.(config, { layoutRecommendation: layoutAcceptance });
  }

  /**
   * Which products each use case is built from, straight from the StackKits
   * catalog. Cards render immediately, but only become selectable when
   * this pinned catalog advertises their applications.
   */
  let useCaseCatalog = $state<Map<string, UseCaseCatalogView>>(new Map());
  // Loading, failed and ready are different answers. A failed read is never
  // shown as "Coming soon" and never lets the person continue without it.
  let useCaseCatalogState = $state<"loading" | "ready" | "error">("loading");
  let catalogAttempt = $state(0);
  $effect(() => {
    void catalogAttempt;
    let cancelled = false;
    useCaseCatalogState = "loading";
    loadUseCaseCatalog().then(
      (catalog) => {
        if (cancelled) return;
        useCaseCatalog = catalog;
        useCaseCatalogState = "ready";
      },
      () => {
        if (!cancelled) useCaseCatalogState = "error";
      },
    );
    return () => {
      cancelled = true;
    };
  });
  function reloadUseCaseCatalog() {
    catalogAttempt += 1;
  }

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
    userEdited = true;
    config.useCaseSettings ??= {};
    config.useCaseSettings[goal] = {
      ...(config.useCaseSettings[goal] ?? {}),
      [settingId]: value,
    };
  }

  function setGoal(goal: GoalConfigKey, value: boolean) {
    userEdited = true;
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
    userEdited = true;
    config.network.accessMode = value;
    config.network.accessModeSelected = true;
  }

  function setAudience(audience: AudienceConfigKey, value: boolean) {
    userEdited = true;
    config.audience[audience] = value;
    if (audience === "public") config.network.publicAccess = value;
  }

  function setHouseholdProfile(profile: HouseholdProfile) {
    userEdited = true;
    config.household = { profile, people: config.household?.people ?? [] };
    config.audience.onlyMe = profile === "solo";
    config.audience.familyFriends = profile !== "solo";
  }

  function setHouseholdPeople(people: HouseholdPersonDraft[]) {
    userEdited = true;
    config.household = {
      profile:
        config.household?.profile ??
        (config.audience.onlyMe ? "solo" : "shared"),
      people,
    };
  }

  function openRecommendation(_recommendation: WizardRecommendation) {
    step = 2;
    hasAttemptedNext = false;
  }
</script>

<div
  class="w-full min-w-0"
  data-testid="easy-wizard"
  oninput={() => (userEdited = true)}
  onchange={() => (userEdited = true)}
  data-standard-bundle-id={standardBundle.id}
  data-standard-bundle-version={standardBundle.version}
>
  {#if draftRestored}
    <p
      class="mb-6 flex items-start justify-between gap-3 rounded-lg border border-info/30 bg-info/10 px-4 py-3 text-sm text-foreground"
      role="status"
      data-testid="wizard-draft-restored"
    >
      <span>{tr("wizard.draft.restored")}</span>
      <button
        type="button"
        class="shrink-0 text-sm underline underline-offset-2"
        onclick={() => (draftRestored = false)}
      >
        {tr("ui.common.dismiss")}
      </button>
    </p>
  {/if}

  <!-- What is missing on this step, once the person tried to continue. -->
  {#if stepIssues.length > 0 && hasAttemptedNext}
    <div
      bind:this={validationSummary}
      id="wizard-validation-summary"
      tabindex="-1"
      role="alert"
      class="mb-6 scroll-mt-6 rounded-lg border border-warning/30 bg-warning/10 p-4 outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid="wizard-validation-summary"
    >
      <div class="flex items-start gap-3">
        <svg
          class="mt-0.5 h-5 w-5 shrink-0 text-warning"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
          aria-hidden="true"
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
          <ul class="mt-1 space-y-1 text-sm text-muted-foreground">
            {#each stepIssues as issue, index (index)}
              <li>• {issue.message}</li>
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
    blockedReason={nodeAlternative ? "" : blockedReason}
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
        catalogState={useCaseCatalogState}
        onCatalogReload={reloadUseCaseCatalog}
        {layoutState}
        onLayoutRetry={() => layoutController.retry(layoutSelection)}
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

    <!-- The accepted distribution, as a reminder where the run is created. -->
    {#if layoutEnabled && step === lastActiveStep && currentStepKey !== "goals" && layoutState.phase !== "idle"}
      <div class="mt-6" data-testid="layout-recommendation-summary-slot">
        <LayoutRecommendationPanel
          state={layoutState}
          variant="summary"
          onretry={() => layoutController.retry(layoutSelection)}
        />
      </div>
    {/if}
  </div>
  {#if footerNotice}
    <!-- Takes no room while the notice has nothing to show. -->
    <div
      class="mx-auto mt-8 hidden w-full min-w-0 max-w-4xl space-y-4 has-[*]:block"
    >
      {@render footerNotice()}
    </div>
  {/if}
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
    blockedReason={nodeAlternative ? "" : blockedReason}
    onprev={goPrev}
    onnext={goNext}
    ondeploy={handleCreate}
  />
</div>
