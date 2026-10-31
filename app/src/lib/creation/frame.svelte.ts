/**
 * The state one creation flow keeps while it is on screen, whichever frame shows
 * it: the answers and their draft, the catalog, the recommendations, the owner
 * step and the session. Created during a frame's initialization, because its
 * effects live as long as that frame.
 */
import { untrack, type Snippet } from "svelte";
import type { LayoutRecommendationView } from "#lib/api/layout.js";
import {
  loadUseCaseCatalog,
  type UseCaseCatalogView,
} from "#lib/api/useCaseCatalog.js";
import {
  LayoutPreviewController,
  acceptedLayoutFromPreview,
  type LayoutPreviewState,
} from "#lib/wizard/LayoutPreviewController.js";
import { creationGoals } from "#lib/wizard/creationGoals.js";
import {
  clearWizardDraft,
  loadWizardDraft,
  mergeOverDefaults,
  saveWizardDraft,
} from "#lib/wizard/draft-storage.js";
import type {
  HouseholdPersonDraft,
  HouseholdProfile,
} from "#lib/wizard/household-draft.js";
import {
  applyPasskeyFirstOwnerLogin,
  applyWizardDeploymentLane,
  buildLayoutSelection,
  createDefaultConfig,
  deriveServicesFromGoals,
  getEasyAudienceQuestions,
  getEasyAccessQuestions,
  getEasyGoalQuestions,
  getVpnQuestions,
  isValidSubstrateGuest,
  selectedCanonicalUseCasesFromGoals,
  WizardPreviewController,
  type AccessModeValue,
  type AudienceConfigKey,
  type BundleQuestionDefinition,
  type CanonicalUseCaseGoalKey,
  type GoalConfigKey,
  type ManagedProviderID,
  type StackConfig,
  type WizardPreviewState,
} from "#lib/wizard/index.js";
import { OwnerStepState } from "#lib/wizard/owner-state.svelte.js";
import type { WizardIssue } from "#lib/wizard/validation-context.js";
import { tr } from "#lib/i18n.svelte.js";
import type { CreationContext } from "./context";
import {
  createCreationSession,
  creationStepIssues,
  creationStepKey,
  creationStepNumber,
  isCreationStepComplete,
  type CreationFacts,
  type CreationIssueField,
  type CreationProcess,
  type CreationSession,
  type CreationSessionState,
  type CreationStepKey,
} from "./core/index";
import type { HomelabServerSelection } from "./homelab-servers";

export interface CreationFrameOptions {
  process: CreationProcess;
  initialConfig?: StackConfig;
  isDeploying: boolean;
  /**
   * Keeps the entered answers across a reload under this name. Without it
   * nothing is stored (surfaces that join an existing homelab).
   */
  draftKey?: string;
  /** The deployment the flow adds to; with a layout flow it asks for the distribution. */
  layoutStackId?: string;
  /** A replacement a superseded run handed back; shown to be confirmed again. */
  layoutReplacement?: LayoutRecommendationView;
  /** Changes when the shown recommendation must be asked for again. */
  layoutRefresh: number;
  /** A recommendation names a StackKit; the frame opens the step it applies to. */
  onRecommendationOpen?: () => void;
  onmanagedproviderselect?: (provider: ManagedProviderID) => void;
  nodeOptions?: Snippet<[StackConfig]>;
  nodeAlternative?: Snippet;
  /** The person's existing servers, offered on the Server step. */
  homelab?: HomelabServerSelection;
}

function cloneConfig(value?: StackConfig): StackConfig | null {
  if (!value) return null;
  return JSON.parse(JSON.stringify(value)) as StackConfig;
}

export class CreationFrame {
  readonly context: CreationContext;
  readonly session: CreationSession;
  readonly options: CreationFrameOptions;
  /** Where this process starts; having moved on counts as work to lose. */
  readonly #startStep: CreationStepKey;

  readonly accessQuestions = getEasyAccessQuestions();
  readonly audienceQuestions = getEasyAudienceQuestions();
  readonly vpnQuestions = getVpnQuestions().filter(
    (option) => option.value !== "none",
  );
  readonly #goalQuestions = getEasyGoalQuestions();

  config = $state<StackConfig>(createDefaultConfig());
  nav = $state<CreationSessionState>(null as unknown as CreationSessionState);
  draftRestored = $state(false);
  /** Show validation only after interaction so initial rendering stays calm. */
  attempted = $state(false);
  /** Set by anything the person changes; ends once the submission is accepted. */
  userEdited = $state(false);
  /**
   * A card applying handed-over answers reports them like a choice; only what
   * follows the person's own first touch counts as something they could lose.
   */
  interacted = false;
  /** The Server step reports its own readiness; the value stays when it is left. */
  serverSelectionReady = $state(true);

  recommendationState = $state<WizardPreviewState>({ phase: "idle" });
  layoutState = $state<LayoutPreviewState>({ phase: "idle" });
  /**
   * Which products each use case is built from, straight from the StackKits
   * catalog. Cards render immediately, but only become selectable when this
   * pinned catalog advertises their applications.
   */
  useCaseCatalog = $state<Map<string, UseCaseCatalogView>>(new Map());
  /**
   * Loading, failed and ready are different answers. A failed read is never
   * shown as "Coming soon" and never lets the person continue without it.
   */
  catalogState = $state<"loading" | "ready" | "error">("loading");
  #catalogAttempt = $state(0);

  readonly owner: OwnerStepState;
  readonly #recommendations: WizardPreviewController;
  readonly #layouts: LayoutPreviewController;

  /** `options` reads live: pass getters, so an effect tracks only what it uses. */
  constructor(context: CreationContext, options: CreationFrameOptions) {
    this.context = context;
    this.options = options;

    const initial = untrack(() => ({
      process: options.process,
      draftKey: options.draftKey,
      initialConfig: options.initialConfig,
    }));
    // A reload restores the answers of this tab (secrets excluded).
    const restored = initial.draftKey
      ? loadWizardDraft(initial.draftKey)
      : null;
    this.draftRestored = Boolean(restored);
    this.config = this.#initialConfig(restored?.config, initial.initialConfig);

    this.session = createCreationSession({
      process: initial.process,
      current: creationStepKey(restored?.step),
      furthest: creationStepKey(restored?.furthest ?? restored?.step),
      isComplete: (key) => isCreationStepComplete(key, this.facts),
    });
    this.#startStep = this.session.activeSteps()[0] ?? "goals";
    const stopSession = this.session.subscribe((state) => {
      this.nav = state;
    });
    $effect(() => stopSession);

    this.owner = new OwnerStepState(() => this.config);
    this.#recommendations = new WizardPreviewController((state) => {
      this.recommendationState = state;
    });
    this.#layouts = new LayoutPreviewController((state) => {
      this.layoutState = state;
    });
    $effect(() => () => this.#recommendations.destroy());
    $effect(() => () => this.#layouts.destroy());

    $effect(() => {
      const process = options.process;
      untrack(() => this.session.setProcess(process));
    });

    this.#keepDraft(Boolean(restored));
    this.#adoptLateInitialConfig(Boolean(restored));
    this.#followRecommendations();
    this.#followLayout();
    this.#followOwner();
    this.#applyLaneDefaults(Boolean(restored));
    this.#followEverythingGoal();
    this.#loadCatalog();
  }

  #initialConfig(restored?: StackConfig, stored?: StackConfig): StackConfig {
    if (restored) {
      const value = mergeOverDefaults(createDefaultConfig(), restored);
      applyPasskeyFirstOwnerLogin(value);
      return value;
    }
    const copy = cloneConfig(stored);
    if (copy) {
      applyPasskeyFirstOwnerLogin(copy);
      return copy;
    }
    const value = createDefaultConfig();
    if (value.goals) value.goals.files = true;
    return value;
  }

  get process(): CreationProcess {
    return this.options.process;
  }
  get current(): CreationStepKey {
    return this.nav.current;
  }
  get stepNumber(): number {
    return creationStepNumber(this.nav.current);
  }
  get lastStep(): CreationStepKey {
    void this.nav;
    return this.session.lastStep();
  }
  get reachableSteps(): CreationStepKey[] {
    void this.nav;
    return this.session.reachableSteps();
  }

  readonly primaryGoalQuestions = $derived.by(() =>
    this.#goalQuestions.filter(
      (goal) =>
        this.#isGoalEnabled(goal) && (goal.surface ?? "primary") === "primary",
    ),
  );
  readonly advancedGoalQuestions = $derived.by(() =>
    this.#goalQuestions.filter(
      (goal) => this.#isGoalEnabled(goal) && goal.surface === "advanced",
    ),
  );
  readonly #availableGoals = $derived.by(() =>
    creationGoals(
      [...this.primaryGoalQuestions, ...this.advancedGoalQuestions],
      this.useCaseCatalog,
    )
      .filter((card) => card.availability === "available")
      .flatMap((card) => (card.question ? [card.question.configKey] : []))
      .filter((goal): goal is CanonicalUseCaseGoalKey => goal !== "everything"),
  );

  #isGoalEnabled(goal: BundleQuestionDefinition<GoalConfigKey>): boolean {
    return (
      !goal.featureFlag || this.context.features.isEnabled(goal.featureFlag)
    );
  }

  // Read at the moment a step or a move asks, so each fact tracks only itself.
  readonly facts: CreationFacts = (() => {
    const frame = this;
    return {
      get requireGoal() {
        return frame.process.requireGoal;
      },
      get hasSelectedGoal() {
        return (
          selectedCanonicalUseCasesFromGoals(frame.config.goals).length > 0
        );
      },
      get catalogState() {
        return frame.catalogState;
      },
      get serverSelectionReady() {
        return frame.serverSelectionReady;
      },
      get existingServer() {
        return Boolean(frame.options.homelab?.selected);
      },
      get serverMode() {
        return frame.config.serverProvisioning.mode;
      },
      get hypervisorGuestValid() {
        return isValidSubstrateGuest(frame.config.serverProvisioning.substrate);
      },
      get remoteHost() {
        return frame.config.serverProvisioning.remote.host;
      },
      get usesWallet() {
        return frame.context.auth.walletSource === "kombify";
      },
      get walletItemChosen() {
        return Boolean(frame.config.serverProvisioning.remote.sshWalletItemId);
      },
      get accessMode() {
        return frame.config.network.accessMode;
      },
      get publicAccess() {
        return frame.config.network.publicAccess;
      },
      get inheritsAccess() {
        return frame.process.inheritsAccess;
      },
      get hasUsers() {
        const { onlyMe, familyFriends, public: open } = frame.config.audience;
        return onlyMe || familyFriends || open;
      },
      get ownerValid() {
        return frame.owner.adminIsValid();
      },
      get ownerIssues() {
        return frame.owner
          .ownerValidationErrors()
          .map((text) => ({ field: frame.#ownerField(text), text }));
      },
    };
  })();

  #ownerField(message: string): CreationIssueField {
    if (
      message === tr("wizard.login.profile.emailRequired") ||
      message === tr("ui.ownerState.emailIsRequiredForPasswordless")
    )
      return "owner-email";
    if (message === tr("ui.ownerState.recoveryPassphrasesDoNotMatch"))
      return "owner-recovery";
    return "step";
  }

  /** What stands in the way on the current step, blockers first. */
  readonly stepIssues: WizardIssue[] = $derived.by(() =>
    creationStepIssues(this.nav.current, this.facts).map((issue) => ({
      field: issue.field,
      message: issue.text ?? tr(issue.messageKey ?? ""),
    })),
  );
  readonly canGoNext = $derived.by(() =>
    isCreationStepComplete(this.nav.current, this.facts),
  );
  readonly earlierStepIncomplete = $derived.by(() => {
    void this.nav;
    return this.session.firstIncompleteBefore(this.nav.current) !== undefined;
  });
  readonly canDeploy = $derived.by(() => {
    const server = this.config.serverProvisioning;
    return (
      this.nav.current === this.lastStep &&
      !this.earlierStepIncomplete &&
      this.canGoNext &&
      (this.facts.existingServer ||
        server.mode !== "hypervisor" ||
        isValidSubstrateGuest(server.substrate))
    );
  });
  /** The first thing in the way, shown beside the blocked forward button. */
  readonly blockedReason = $derived.by(() => {
    const onLast = this.nav.current === this.lastStep;
    if (onLast ? this.canDeploy : this.canGoNext) return "";
    if (onLast && this.earlierStepIncomplete)
      return tr("wizard.stepper.locked");
    return (
      this.stepIssues[0]?.message ?? tr("wizard.validation.stepIncomplete")
    );
  });
  /**
   * Protect entered answers against accidental departure. The config carries
   * SSH passwords and recovery material, so only its non-secret part is kept
   * (draftKey); protection ends once the submission is accepted and stays in
   * place after a failed one.
   */
  readonly hasUnsavedAnswers = $derived.by(
    () =>
      (this.userEdited || this.nav.current !== this.#startStep) &&
      !this.options.isDeploying,
  );

  // Recommended distribution: only for a run against an existing deployment
  // with something selected. The preview follows the selection (debounced,
  // stale answers dropped); the run request carries the acceptable one.
  readonly #layoutEnabled = $derived.by(
    () =>
      Boolean(this.options.layoutStackId) &&
      this.process.inheritsAccess &&
      !this.options.nodeAlternative,
  );
  readonly layoutSelection = $derived.by(() =>
    this.#layoutEnabled && this.options.layoutStackId && this.process.layoutFlow
      ? buildLayoutSelection(
          this.config,
          this.options.layoutStackId,
          this.process.layoutFlow,
        )
      : null,
  );
  readonly layoutAcceptance = $derived.by(() =>
    this.#layoutEnabled
      ? acceptedLayoutFromPreview(this.layoutState, this.layoutSelection)
      : undefined,
  );
  get showsLayoutSummary(): boolean {
    return (
      this.#layoutEnabled &&
      this.nav.current === this.lastStep &&
      this.nav.current !== "goals" &&
      this.layoutState.phase !== "idle"
    );
  }

  retryLayout = () => this.#layouts.retry(this.layoutSelection);
  reloadCatalog = () => {
    this.#catalogAttempt += 1;
  };

  // -- what the person changes -----------------------------------------------

  /**
   * A decision about a use case. Stored only when set explicitly; the catalog
   * keeps the defaults. The frame owns the answers, the step only reports.
   */
  setUseCaseSetting = (
    goal: GoalConfigKey,
    settingId: string,
    value: string | boolean,
  ) => {
    if (this.interacted) this.userEdited = true;
    this.config.useCaseSettings ??= {};
    this.config.useCaseSettings[goal] = {
      ...(this.config.useCaseSettings[goal] ?? {}),
      [settingId]: value,
    };
  };

  setGoal = (goal: GoalConfigKey, value: boolean) => {
    if (this.interacted) this.userEdited = true;
    const goals = this.config.goals;
    if (!goals) return;
    const available = this.#availableGoals;
    goals[goal] = value;
    // If "everything" is selected, toggle all currently available use cases.
    if (goal === "everything") {
      for (const useCase of available) goals[useCase] = value;
      goals.everything = value;
    } else if (!available.includes(goal)) {
      goals[goal] = false;
      goals.everything = false;
    } else if (!value) {
      goals.everything = false;
    } else if (
      available.length > 0 &&
      available.every((useCase) => goals[useCase])
    ) {
      goals.everything = true;
    }
  };

  setAccessMode = (value: AccessModeValue) => {
    if (this.interacted) this.userEdited = true;
    this.config.network.accessMode = value;
    this.config.network.accessModeSelected = true;
  };

  setAudience = (audience: AudienceConfigKey, value: boolean) => {
    if (this.interacted) this.userEdited = true;
    this.config.audience[audience] = value;
    if (audience === "public") this.config.network.publicAccess = value;
  };

  setHouseholdProfile = (profile: HouseholdProfile) => {
    if (this.interacted) this.userEdited = true;
    this.config.household = {
      profile,
      people: this.config.household?.people ?? [],
    };
    this.config.audience.onlyMe = profile === "solo";
    this.config.audience.familyFriends = profile !== "solo";
  };

  setHouseholdPeople = (people: HouseholdPersonDraft[]) => {
    if (this.interacted) this.userEdited = true;
    this.config.household = {
      profile:
        this.config.household?.profile ??
        (this.config.audience.onlyMe ? "solo" : "shared"),
      people,
    };
  };

  // -- creating --------------------------------------------------------------

  enforceExistingOwnerReuse() {
    if (!this.process.reusesOwner) return;
    const { owner, auth } = this.config;
    owner.bootstrapMode = "none";
    owner.source = "local";
    owner.username = "";
    owner.email = "";
    owner.displayName = "";
    owner.recoveryPassphraseHash = "";
    owner.recoveryMaterialRef = "";
    auth.requirePassword = false;
    auth.requireMfa = false;
    auth.allowPasswordless = false;
  }

  /** The answers as they go into the run; the owner's secrets are ready by now. */
  finalizedConfig(): StackConfig {
    const config = this.config;
    config.wizardType = "easy";
    const existingOverlay = config.services.headscale;
    deriveServicesFromGoals(config);
    // A joined Node inherits access from its Homelab; the hidden first-run
    // access default must not turn on a new overlay during goal derivation.
    if (this.process.inheritsAccess)
      config.services.headscale = existingOverlay;
    return config;
  }

  forgetDraft() {
    const key = this.options.draftKey;
    if (key) clearWizardDraft(key);
  }

  // -- what follows the answers ----------------------------------------------

  #keepDraft(restored: boolean) {
    // Keep the non-secret answers for this tab while the person works.
    $effect(() => {
      const { draftKey, isDeploying } = this.options;
      if (!draftKey || isDeploying) return;
      if (!this.userEdited && this.nav.current === "goals" && !restored) return;
      const answers = $state.snapshot(this.config) as StackConfig;
      const step = creationStepNumber(this.nav.current);
      const furthest = creationStepNumber(this.nav.furthest);
      const timer = setTimeout(
        () => saveWizardDraft(draftKey, { step, furthest, config: answers }),
        300,
      );
      return () => clearTimeout(timer);
    });

    // A step change is kept at once, so a reload right after it opens the
    // same step; typed answers follow with the debounce above.
    $effect(() => {
      const step = creationStepNumber(this.nav.current);
      const furthest = creationStepNumber(this.nav.furthest);
      const { draftKey, isDeploying } = this.options;
      if (!draftKey || isDeploying) return;
      if (step === 1 && furthest === 1 && !restored) return;
      saveWizardDraft(draftKey, {
        step,
        furthest,
        config: untrack(() => $state.snapshot(this.config)) as StackConfig,
      });
    });

    // An accepted submission ends the draft; a failed one keeps it.
    $effect(() => () => {
      if (untrack(() => this.options.isDeploying)) this.forgetDraft();
    });
  }

  #adoptLateInitialConfig(restored: boolean) {
    let applied =
      restored || Boolean(untrack(() => this.options.initialConfig));
    $effect(() => {
      const initial = this.options.initialConfig;
      if (!initial || applied) return;
      applied = true;
      this.config = cloneConfig(initial) ?? createDefaultConfig();
      applyPasskeyFirstOwnerLogin(this.config);
    });
  }

  #followRecommendations() {
    $effect(() => {
      if (this.process.reusesOwner) return;
      const config = this.config;
      const server = config.serverProvisioning;
      const smartHome = config.useCaseSettings?.["smart-home"];
      this.#recommendations.update({
        smart_home_settings: smartHome,
        smart_home_context: {
          existing: smartHome?.["instance-origin"] === "existing",
          needs_radio: smartHome?.["device-passthrough"] === true,
          ...(server.mode === "hypervisor" &&
          server.substrate?.advisoryInventory
            ? {
                proxmox_available: true,
                lan_reachable: false,
                cpu: server.substrate.advisoryInventory.cpu,
                memory_mib: server.substrate.advisoryInventory.memoryMiB,
                disk_gib: server.substrate.advisoryInventory.diskGiB,
              }
            : {}),
        },
        goals: selectedCanonicalUseCasesFromGoals(config.goals),
        use_case_settings: config.useCaseSettings,
        use_case_capabilities: config.useCaseCapabilities,
        services: [],
        deployment_lane:
          this.context.auth.lane === "saas" ? "saas" : "self-hosted",
        ...(server.mode === "kombify-cloud"
          ? { provider_id: config.providerId }
          : {}),
        surface: "easy",
      });
    });
  }

  get recommendedStackKit(): string | undefined {
    const state = this.recommendationState;
    const result =
      state.phase === "resolved"
        ? state.result
        : state.phase === "loading" || state.phase === "degraded"
          ? state.previous
          : undefined;
    return (
      result?.recommendations.find((item) => item.recommended) ??
      result?.recommendations[0]
    )?.stackkit;
  }

  #followLayout() {
    $effect(() => {
      const selection = this.layoutSelection;
      untrack(() => this.#layouts.update(selection));
    });
    $effect(() => {
      const replacement = this.options.layoutReplacement;
      if (!replacement) return;
      untrack(() => {
        if (this.layoutSelection)
          this.#layouts.adopt(this.layoutSelection, replacement);
      });
    });
    let refreshSeen = untrack(() => this.options.layoutRefresh);
    $effect(() => {
      const requested = this.options.layoutRefresh;
      if (requested === refreshSeen) return;
      refreshSeen = requested;
      untrack(() => this.#layouts.retry(this.layoutSelection));
    });
  }

  #followOwner() {
    $effect(() => {
      if (this.process.reusesOwner) return;
      this.owner.setSignedInAccount(this.context.auth.signedInAccount);
    });
    $effect(() => {
      if (!this.process.reusesOwner) return;
      const { owner, auth } = this.config;
      if (
        owner.bootstrapMode === "none" &&
        owner.source === "local" &&
        owner.username === "" &&
        owner.email === "" &&
        owner.displayName === "" &&
        owner.recoveryPassphraseHash === "" &&
        owner.recoveryMaterialRef === "" &&
        !auth.requirePassword &&
        !auth.requireMfa &&
        !auth.allowPasswordless
      )
        return;
      this.enforceExistingOwnerReuse();
    });
  }

  #applyLaneDefaults(restored: boolean) {
    let applied = restored;
    $effect(() => {
      if (
        applied ||
        !this.process.appliesLaneDefaults ||
        !this.context.auth.modeDetected
      )
        return;
      applyWizardDeploymentLane(this.config, this.context.auth.lane);
      applied = true;
    });
  }

  #followEverythingGoal() {
    $effect(() => {
      const goals = this.config.goals;
      if (!goals) return;
      // Catalog loading or a temporarily unavailable release must not erase
      // saved wishes. Runtime admission still validates every submitted goal.
      const available = this.#availableGoals;
      const allSelected =
        available.length > 0 && available.every((goal) => goals[goal]);
      if (goals.everything !== allSelected) goals.everything = allSelected;
    });
  }

  #loadCatalog() {
    $effect(() => {
      void this.#catalogAttempt;
      let cancelled = false;
      this.catalogState = "loading";
      loadUseCaseCatalog().then(
        (catalog) => {
          if (cancelled) return;
          this.useCaseCatalog = catalog;
          this.catalogState = "ready";
        },
        () => {
          if (!cancelled) this.catalogState = "error";
        },
      );
      return () => {
        cancelled = true;
      };
    });
  }
}
