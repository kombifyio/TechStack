<!--
  CreationPageFrame: the page frame of the creation flow.

  Five recognizable modules; each process visits only the ones it can change.
  The flow itself is in #lib/creation (core: session, processes, validation;
  frame: its state; modules: the steps). This frame adds what belongs to a page:
  the address, browser history and the prompt before leaving.

  Dispatches 'oncreate' event with StackConfig when the person creates the
  homelab on the last step.

  Answers survive a reload (draftKey): the non-secret part of the config is
  kept in sessionStorage for this tab and ends when the creation is accepted
  or the person chooses to leave. Passwords and passphrases are never stored.

  The step is part of the address (`?step=goals`), one history entry per step:
  the browser's back and forward buttons move between steps, and every step
  already reached opens from the stepper. Moving forward past a step that is
  not complete stops on that step and shows what is missing.
-->
<script lang="ts">
  import { tick, untrack, type Snippet } from "svelte";
  import {
    beforeNavigate,
    goto,
    pushState,
    replaceState,
  } from "$app/navigation";
  import { page } from "$app/state";
  import { Stepper } from "./index";
  import LayoutRecommendationPanel from "./LayoutRecommendationPanel.svelte";
  import {
    ACTIVE_STANDARD_BUNDLE,
    EASY_STEPS,
    type ManagedProviderID,
    type StackConfig,
  } from "#lib/wizard/index.js";
  import type {
    LayoutRecommendationAcceptance,
    LayoutRecommendationView,
  } from "#lib/api/layout.js";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    creationGoals,
    creationGoalContextFacts,
  } from "#lib/wizard/creationGoals.js";
  import { useViewContextOverlay } from "#lib/stores/viewContext.svelte.js";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import { provideWizardValidation } from "#lib/wizard/validation-context.js";
  import { appCreationContext } from "#lib/creation/app-context.svelte.js";
  import { provideCreationContext } from "#lib/creation/context.js";
  import {
    creationStepKey,
    creationStepNumber,
    creationStepOnly,
    type CreationProcess,
    type CreationStepKey,
  } from "#lib/creation/core/index.js";
  import { CreationFrame } from "#lib/creation/frame.svelte.js";
  import { CREATION_MODULES } from "#lib/creation/modules.js";
  import type { HomelabServerSelection } from "#lib/creation/homelab-servers.js";
  import {
    emitCreationEvent,
    guardCreationClose,
    useCreationEntry,
  } from "#lib/creation/host.js";
  import { onCreationCommand } from "#lib/stores/postMessageBridge.js";

  interface Props {
    /** Which creation this is: its steps, what it inherits, the run it ends in. */
    process: CreationProcess;
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
    /**
     * The deployment the wizard adds to. With a process that has a layout flow
     * and a selected use case, the wizard asks for the recommended distribution
     * of the selection and shows it on the Goals step.
     */
    layoutStackId?: string;
    /** A replacement a superseded run handed back; shown to be confirmed again. */
    layoutReplacement?: LayoutRecommendationView;
    /** Changes when the shown recommendation must be asked for again. */
    layoutRefresh?: number;
    onmanagedproviderselect?: (provider: ManagedProviderID) => void;
    nodeOptions?: Snippet<[StackConfig]>;
    nodeAlternative?: Snippet;
    /** The person's existing servers, offered on the Server step. */
    homelab?: HomelabServerSelection;
  }

  let {
    process: requestedProcess,
    oncreate,
    initialConfig,
    isDeploying = false,
    submitLabel,
    submittingLabel,
    draftKey,
    footerNotice,
    layoutStackId,
    layoutReplacement,
    layoutRefresh = 0,
    onmanagedproviderselect,
    nodeOptions,
    nodeAlternative,
    homelab,
  }: Props = $props();

  // Inside the embedded module the host owns the page around this frame: its
  // dialog has no place in the browser's history, and one step can stand alone.
  const entry = useCreationEntry();
  const singleStep = $derived(
    entry?.frame === "step" && entry.step && !requestedProcess.inactive[entry.step]
      ? entry.step
      : undefined,
  );
  const process = $derived(
    singleStep
      ? creationStepOnly(requestedProcess, singleStep, "wizard.step.inherited")
      : requestedProcess,
  );

  const creation = appCreationContext(() => layoutStackId);
  provideCreationContext(creation);
  const frame = new CreationFrame(creation, {
    get process() {
      return process;
    },
    get initialConfig() {
      return initialConfig;
    },
    get isDeploying() {
      return isDeploying;
    },
    get draftKey() {
      return draftKey;
    },
    get layoutStackId() {
      return layoutStackId;
    },
    get layoutReplacement() {
      return layoutReplacement;
    },
    get layoutRefresh() {
      return layoutRefresh;
    },
    onRecommendationOpen: openServerStep,
    get onmanagedproviderselect() {
      return onmanagedproviderselect;
    },
    get nodeOptions() {
      return nodeOptions;
    },
    get nodeAlternative() {
      return nodeAlternative;
    },
    get homelab() {
      return homelab;
    },
  });
  const session = frame.session;
  const standardBundle = ACTIVE_STANDARD_BUNDLE;
  const surfaceSteps = EASY_STEPS;

  // The app's own dialog replaces window.confirm. It answers later than the
  // navigation asks, so the navigation is cancelled first and repeated once
  // the person agreed to leave.
  let leaveConfirmed = false;
  beforeNavigate((navigation) => {
    if (!frame.hasUnsavedAnswers || navigation.willUnload || leaveConfirmed)
      return;
    const target = navigation.to;
    if (!target) return;
    // Back and forward between steps and targets stay on this page: the
    // history entries differ only in `?step=` and `?target=`. A link to this
    // page still asks, since it starts the flow over.
    if (
      navigation.type === "popstate" &&
      target.url.pathname === page.url.pathname
    )
      return;
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
      frame.forgetDraft();
      try {
        await goto(target.url);
      } finally {
        leaveConfirmed = false;
      }
    });
  });

  $effect(() => {
    if (!frame.hasUnsavedAnswers) return;
    const guard = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", guard);
    return () => window.removeEventListener("beforeunload", guard);
  });

  // The banner lists everything once the person tried to continue.
  provideWizardValidation({
    get attempted() {
      return frame.attempted;
    },
    get issues() {
      return frame.stepIssues;
    },
  });

  useViewContextOverlay(() => ({
    state: `wizard_${frame.current}`,
    facts: {
      wizardStep: frame.current,
      catalogState: frame.catalogState,
      ...creationGoalContextFacts(
        creationGoals(
          [...frame.primaryGoalQuestions, ...frame.advancedGoalQuestions],
          frame.catalogState === "ready" ? frame.useCaseCatalog : new Map(),
        ),
        frame.config,
        frame.useCaseCatalog,
      ),
    },
  }));

  const disabledStepReasons = $derived(
    Object.fromEntries(
      Object.entries(process.inactive).map(([key, reason]) => [
        creationStepNumber(key as CreationStepKey),
        tr(reason),
      ]),
    ),
  );
  const reachableSteps = $derived(frame.reachableSteps.map(creationStepNumber));
  const stepSummaries = $derived(
    Object.fromEntries(
      frame.reachableSteps.map((key) => [
        creationStepNumber(key),
        CREATION_MODULES[key].summary(frame.config, frame),
      ]),
    ),
  );
  const currentModule = $derived(CREATION_MODULES[frame.current]);

  // A blocked attempt moves to the list of what is missing, so a press on a
  // button at the foot of a long step never seems to do nothing.
  let validationSummary = $state<HTMLDivElement>();
  let stepContent = $state<HTMLDivElement>();
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

  // The step in the address: shallow history entries keep the page mounted,
  // so moving between steps is no navigation and never asks to leave.
  const currentUrl = $derived(page.shallow?.url ?? page.url);
  function writeStep(key: CreationStepKey, replace: boolean) {
    const url = new URL(currentUrl.href);
    url.searchParams.set("step", key);
    if (url.href === currentUrl.href) return;
    const write = replace || entry ? replaceState : pushState;
    write(`${url.pathname}${url.search}${url.hash}`, page.state);
  }

  const currentSummary = $derived(
    CREATION_MODULES[frame.current].summary(frame.config, frame),
  );
  $effect(() => {
    if (entry)
      emitCreationEvent({
        event: "step-changed",
        step: frame.current,
        summary:
          frame.current === "login" || frame.current === "server"
            ? ""
            : currentSummary,
      });
  });
  $effect(() => {
    if (!entry) return;
    const stopCommands = onCreationCommand((command) => {
      if (command.command === "go-to-step")
        void goTo(creationStepNumber(command.step));
    });
    const stopGuard = guardCreationClose(async () => {
      if (!frame.hasUnsavedAnswers) return true;
      const leave = await confirmInApp({
        title: tr("wizard.leave.title"),
        message: tr("wizard.leave.confirm"),
        confirmText: tr("wizard.leave.confirmAction"),
        cancelText: tr("wizard.leave.stay"),
        tone: "warning",
      });
      if (leave) frame.forgetDraft();
      return leave;
    });
    return () => {
      stopCommands();
      stopGuard();
    };
  });

  // Back and forward in the browser, a reload or a link that names a step:
  // a step already reached opens; anything else keeps the current step.
  let urlStepSynced = false;
  $effect(() => {
    const key = currentUrl.searchParams.get("step");
    untrack(() => {
      const requested = creationStepKey(key);
      const reachable =
        requested !== undefined &&
        (requested === frame.current ||
          frame.reachableSteps.includes(requested));
      if (!urlStepSynced) {
        urlStepSynced = true;
        if (reachable) session.open(requested);
        writeStep(frame.current, true);
        return;
      }
      if (requested === frame.current) return;
      if (!reachable) {
        writeStep(frame.current, true);
        return;
      }
      // Forward through history checks the steps on the way like a click.
      const move = session.goTo(requested);
      if (move.kind === "blocked") {
        writeStep(move.step, true);
        frame.attempted = true;
        void revealIssues();
        return;
      }
      frame.attempted = false;
      void revealStep();
    });
  });

  async function goNext() {
    frame.attempted = true;
    const move = session.next();
    if (move.kind === "moved") {
      writeStep(move.step, false);
      // The next step starts calm; its hints appear after its own attempt.
      frame.attempted = false;
      frame.draftRestored = false;
      await revealStep();
    } else if (move.kind === "blocked" && frame.stepIssues.length > 0) {
      await revealIssues();
    }
  }

  async function goPrev() {
    const move = session.prev();
    if (move.kind !== "moved") return;
    writeStep(move.step, false);
    frame.attempted = false;
    await revealStep();
  }

  // A click on a reached step. Forward, every step on the way must still be
  // complete; the first one that is not opens with what is missing.
  async function goTo(target: number) {
    const key = creationStepKey(target);
    if (!key) return;
    const before = frame.current;
    const move = session.goTo(key);
    if (move.kind === "stay") return;
    if (move.step !== before) writeStep(move.step, false);
    if (move.kind === "blocked") {
      frame.attempted = true;
      await revealIssues();
      return;
    }
    frame.attempted = false;
    await revealStep();
  }

  function openServerStep() {
    session.open("server");
    writeStep("server", false);
    frame.attempted = false;
  }

  // Handle create
  async function handleCreate() {
    frame.attempted = true;
    frame.enforceExistingOwnerReuse();
    // A step changed after it was passed opens again with what is missing.
    const incomplete = session.firstIncompleteBefore(frame.current);
    if (incomplete !== undefined) {
      session.open(incomplete);
      writeStep(incomplete, false);
      await revealIssues();
      return;
    }
    if (!frame.canDeploy) {
      if (frame.stepIssues.length > 0) await revealIssues();
      return;
    }

    const recoveryReady = await frame.owner.syncRecoveryHash();
    if (!recoveryReady || !frame.owner.adminIsValid()) {
      await revealIssues();
      return;
    }

    oncreate?.(frame.finalizedConfig(), {
      layoutRecommendation: frame.layoutAcceptance,
    });
  }

  /*
   * The Goals step emits raw disclosure observations (§3 rule 5), and nothing
   * here consumes them yet — deliberately. Wiring an observation sink is
   * kombify-Techstack-joei, not a counter invented here that nothing reads.
   */
</script>

<div
  class="w-full min-w-0"
  data-testid="easy-wizard"
  onpointerdowncapture={() => (frame.interacted = true)}
  onkeydowncapture={() => (frame.interacted = true)}
  onclickcapture={() => (frame.interacted = true)}
  oninput={() => (frame.userEdited = true)}
  onchange={() => (frame.userEdited = true)}
  data-standard-bundle-id={standardBundle.id}
  data-standard-bundle-version={standardBundle.version}
>
  {#if frame.draftRestored}
    <p
      class="mb-6 flex items-start justify-between gap-3 rounded-lg border border-info/30 bg-info/10 px-4 py-3 text-sm text-foreground"
      role="status"
      data-testid="wizard-draft-restored"
    >
      <span>{tr("wizard.draft.restored")}</span>
      <button
        type="button"
        class="shrink-0 text-sm underline underline-offset-2"
        onclick={() => (frame.draftRestored = false)}
      >
        {tr("ui.common.dismiss")}
      </button>
    </p>
  {/if}

  <!-- What is missing on this step, once the person tried to continue. -->
  {#if frame.stepIssues.length > 0 && frame.attempted}
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
            {#each frame.stepIssues as issue, index (index)}
              <li>• {issue.message}</li>
            {/each}
          </ul>
        </div>
      </div>
    </div>
  {/if}

  {#if !singleStep}
  <Stepper
    steps={surfaceSteps}
    {disabledStepReasons}
    currentStep={frame.stepNumber}
    canGoNext={!nodeAlternative && frame.canGoNext}
    canDeploy={!nodeAlternative && frame.canDeploy}
    showDeploy={!nodeAlternative}
    {isDeploying}
    deployLabel={submitLabel}
    deployingLabel={submittingLabel}
    blockedReason={nodeAlternative ? "" : frame.blockedReason}
    onprev={goPrev}
    onnext={goNext}
    ondeploy={handleCreate}
    {reachableSteps}
    {stepSummaries}
    onselect={goTo}
  />
  {/if}

  <div
    bind:this={stepContent}
    tabindex="-1"
    class="mx-auto w-full min-w-0 scroll-mt-6 outline-none {frame.current ===
      'goals' || frame.current === 'server'
      ? ''
      : 'max-w-4xl'}"
  >
    {#if !process.inactive[frame.current]}
      <currentModule.Component {...currentModule.props(frame)} />
    {/if}

    <!-- The accepted distribution, as a reminder where the run is created. -->
    {#if frame.showsLayoutSummary}
      <div class="mt-6" data-testid="layout-recommendation-summary-slot">
        <LayoutRecommendationPanel
          state={frame.layoutState}
          variant="summary"
          onretry={frame.retryLayout}
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
    currentStep={frame.stepNumber}
    canGoNext={!nodeAlternative && frame.canGoNext}
    canDeploy={!nodeAlternative && frame.canDeploy}
    showDeploy={!nodeAlternative}
    {isDeploying}
    deployLabel={submitLabel}
    deployingLabel={submittingLabel}
    blockedReason={nodeAlternative ? "" : frame.blockedReason}
    onprev={goPrev}
    onnext={goNext}
    ondeploy={handleCreate}
  />
</div>
