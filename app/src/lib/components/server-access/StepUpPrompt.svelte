<script lang="ts">
  import { onDestroy } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    openStepUpWindow,
    watchStepUpCompletion,
    type StepUpRequest,
  } from "#lib/auth/step-up.js";

  /**
   * Embedded step-up: the frame cannot navigate to the sign-in, so this asks
   * for a fresh confirmation in a new window and, once that window reports
   * back, runs `onRetry` once. A retry that still answers "fresh sign-in
   * required" leaves the prompt in place so the owner can try again; nothing
   * here retries on its own beyond one attempt per completion signal.
   */
  interface Props {
    /** What the fresh sign-in is for. */
    request: StepUpRequest;
    /** Why this action needs the confirmation. */
    body: string;
    /** Repeats the original action, or just clears the notice for a change the owner repeats by hand. The caller unmounts this prompt on success. */
    onRetry: () => Promise<void> | void;
  }

  let { request, body, onRetry }: Props = $props();

  let phase = $state<"idle" | "waiting" | "checking">("idle");
  let notConfirmed = $state(false);
  let blockedUrl = $state<string | null>(null);
  let stopWatching: (() => void) | null = null;

  function stop() {
    stopWatching?.();
    stopWatching = null;
  }

  async function settle() {
    if (phase === "checking") return;
    phase = "checking";
    try {
      await onRetry();
    } finally {
      phase = "waiting";
      notConfirmed = true;
    }
  }

  function watch(popup: Window | null) {
    stop();
    notConfirmed = false;
    phase = "waiting";
    stopWatching = watchStepUpCompletion(popup, (signal) => {
      // The completion page announces itself on two channels. Its first
      // announcement spends this confirmation; a new one needs a new click.
      // The weaker "window closed" signal keeps listening for it.
      if (signal === "complete") stop();
      void settle();
    });
  }

  function confirm() {
    const opened = openStepUpWindow(request);
    blockedUrl = opened.popup ? null : opened.url;
    watch(opened.popup);
  }

  onDestroy(stop);
</script>

<div
  class="flex max-w-md flex-col items-center gap-3 text-center"
  data-testid="step-up-prompt"
  data-phase={phase}
>
  <p class="font-semibold">{tr("ui.stepUp.title")}</p>
  <p class="text-sm opacity-80">{body}</p>
  <Button
    variant={phase === "idle" ? "primary" : "secondary"}
    size="sm"
    testId="step-up-confirm"
    disabled={phase === "checking"}
    onclick={confirm}
  >
    {phase === "idle"
      ? tr("ui.stepUp.confirm")
      : phase === "checking"
        ? tr("ui.stepUp.checking")
        : tr("ui.stepUp.reopen")}
  </Button>
  {#if blockedUrl}
    <p class="text-sm opacity-80">
      {tr("ui.stepUp.blocked")}
      <a
        class="underline"
        href={blockedUrl}
        target="_blank"
        data-testid="step-up-link"
        onclick={() => watch(null)}>{tr("ui.stepUp.blockedLink")}</a
      >
    </p>
  {/if}
  {#if phase === "waiting"}
    <p class="text-xs opacity-70" role="status">
      {notConfirmed ? tr("ui.stepUp.notConfirmed") : tr("ui.stepUp.waiting")}
    </p>
  {/if}
</div>
