<script lang="ts">
  /**
   * Mounts the introduction and owns when it runs.
   *
   * It auto-starts once on a true first visit, and otherwise waits for the
   * user menu to ask. It never touches the journey: finishing the
   * introduction completes no task, and completing a task never closes the
   * introduction.
   *
   * The primary welcome action enters the existing creation flow. The optional
   * tour remains replayable; appearance stays on the settings surface.
   */
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { tr } from "#lib/i18n.svelte.js";
  import { goto } from "$app/navigation";
  import { Introduction } from "@kombiverselabs/ui/onboarding";

  import {
    INTRODUCTION_START_EVENT,
    TECHSTACK_INTRODUCTION,
    markIntroductionCompleted,
    markIntroductionDismissed,
    markIntroductionStarted,
    readIntroduction,
    shouldAutoStartIntroduction,
  } from "#lib/onboarding/introduction.js";
  import { resolveOnboardingMessage } from "#lib/onboarding/journey.js";
  import { onboardingStore } from "#lib/onboarding/store.svelte.js";
  import {
    hydrateStackIdentityFromBackend,
    saveStackIdentityToBackend,
    type StackIdentity,
  } from "#lib/stores/stackIdentity.js";
  import {
    defaultIdentity,
    normalizeStackIdentity,
  } from "#lib/components/open-core/index.js";
  import WelcomeDialog from "./WelcomeDialog.svelte";

  type Phase = "idle" | "welcome" | "tour";

  let phase = $state<Phase>("idle");
  let identity = $state<StackIdentity>(defaultIdentity);
  let identityEditable = $state(false);
  let identityNamed = $state(false);
  let saving = $state(false);
  let identityError = $state("");

  const open = $derived(phase === "tour");

  /**
   * The name field starts empty rather than pre-filled with the generated
   * default: an untouched identity has nothing worth defending, and an empty
   * field with a placeholder is faster to answer than one you must clear.
   */
  const welcomeName = $derived(identityNamed ? identity.name : "");

  async function loadIdentity(): Promise<void> {
    identityError = "";
    const response = await hydrateStackIdentityFromBackend();
    identityEditable = response?.editable ?? false;
    identityNamed = Boolean(response?.stack_identity?.name);
    identity = normalizeStackIdentity(response?.stack_identity);
  }

  function start(): void {
    markIntroductionStarted();
    phase = "welcome";
    // Fail-soft: an unreachable identity endpoint costs the name field, not
    // the introduction.
    void loadIdentity();
  }

  async function acceptName(name: string): Promise<void> {
    if (saving) return;
    saving = true;
    identityError = "";
    try {
      if (identityEditable && name !== welcomeName) {
        const response = await saveStackIdentityToBackend({
          ...identity,
          name,
        });
        identity = normalizeStackIdentity(
          response.stack_identity ?? { ...identity, name },
        );
        identityNamed = true;
      }
      await beginCreation();
    } catch {
      identityError = tr("onboarding.ui.start_failed");
    } finally {
      saving = false;
    }
  }

  async function beginCreation(): Promise<void> {
    await goto("/stacks/new");
    finish();
  }

  function dismiss(stopId: string): void {
    markIntroductionDismissed(stopId);
    phase = "idle";
  }

  function finish(): void {
    markIntroductionCompleted();
    phase = "idle";
  }

  onMount(() => {
    const onRequest = () => start();
    window.addEventListener(INTRODUCTION_START_EVENT, onRequest);
    return () =>
      window.removeEventListener(INTRODUCTION_START_EVENT, onRequest);
  });

  // Wait for the journey before deciding: "has this user done anything yet"
  // is a question only the loaded record can answer, and guessing it wrong
  // means interrupting somebody who is already at home here.
  let decided = false;
  $effect(() => {
    const journey = onboardingStore.resolved;
    if (decided || !journey || page.url.pathname !== "/dashboard") return;
    decided = true;
    if (
      shouldAutoStartIntroduction(readIntroduction(), {
        completedCount: journey.steps.filter((step) => step.done).length,
        dismissed: journey.status === "dismissed",
      })
    ) {
      start();
    }
  });
</script>

<WelcomeDialog
  open={phase === "welcome"}
  name={welcomeName}
  nameEditable={identityEditable}
  {saving}
  error={identityError}
  onStart={(name) => void acceptName(name)}
  onExplore={() => (phase = "tour")}
  onDismiss={() => dismiss("welcome")}
/>

<Introduction
  {open}
  stops={TECHSTACK_INTRODUCTION}
  resolveMessage={resolveOnboardingMessage}
  navigate={(route) => void goto(route)}
  onComplete={finish}
  onDismiss={(stopId) => dismiss(stopId)}
/>
