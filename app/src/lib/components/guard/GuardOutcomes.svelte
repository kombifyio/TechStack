<script lang="ts">
  import { tr, formatDateTime } from "#lib/i18n.svelte.js";
  import { untrack } from "svelte";
  import { TriangleAlert } from "@lucide/svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    denialReason,
    listGuardOutcomes,
    resolveGuardOutcome,
    type GuardOutcomeDisposition,
    type GuardOutcomeResolution,
    type GuardUnresolvedOutcome,
  } from "#lib/api/guard.js";
  import GuardStepUpNotice from "./GuardStepUpNotice.svelte";

  interface Props {
    workerId: string;
    /** Count the heartbeat reported; decides whether a failed read is worth a notice. */
    reportedUnresolved?: number;
    /** Called after a resolution committed so the page can reread the facts. */
    onResolved?: () => void | Promise<void>;
  }

  let { workerId, reportedUnresolved = 0, onResolved }: Props = $props();

  const DISPOSITIONS: GuardOutcomeDisposition[] = [
    "verified_applied",
    "verified_not_applied",
    "accept_unknown",
  ];
  const NOTE_MAX = 400;

  let outcomes = $state<GuardUnresolvedOutcome[]>([]);
  let loadFailed = $state(false);
  let activeId = $state<string | null>(null);
  let disposition = $state<GuardOutcomeDisposition>("verified_not_applied");
  let note = $state("");
  let confirming = $state(false);
  let submitting = $state(false);
  let error = $state<string | null>(null);
  let stepUpRequired = $state(false);
  let receipt = $state<GuardOutcomeResolution | null>(null);

  async function load() {
    try {
      outcomes = await listGuardOutcomes(workerId);
      loadFailed = false;
    } catch {
      loadFailed = true;
    }
  }

  $effect(() => {
    if (workerId) {
      untrack(() => {
        void load();
      });
    }
  });

  // A changed heartbeat count means the list may have changed too.
  let lastReported: number | undefined;
  $effect(() => {
    const next = reportedUnresolved;
    const changed = lastReported !== undefined && next !== lastReported;
    lastReported = next;
    if (changed) {
      untrack(() => {
        void load();
      });
    }
  });

  function open(outcome: GuardUnresolvedOutcome) {
    activeId = outcome.command_id;
    disposition = "verified_not_applied";
    note = "";
    confirming = false;
    error = null;
    stepUpRequired = false;
    receipt = null;
  }

  function close() {
    activeId = null;
    confirming = false;
    error = null;
    stepUpRequired = false;
  }

  const active = $derived(
    outcomes.find((outcome) => outcome.command_id === activeId) ?? null,
  );
  const noteValid = $derived(
    note.trim().length > 0 && note.trim().length <= NOTE_MAX,
  );

  async function submit() {
    if (!active || !noteValid || submitting) return;
    submitting = true;
    error = null;
    stepUpRequired = false;
    try {
      receipt = await resolveGuardOutcome(workerId, active.command_id, {
        disposition,
        expected_revision: active.revision,
        note: note.trim(),
      });
      close();
      await load();
      await onResolved?.();
    } catch (cause) {
      const parsed = parseApiError(cause);
      const reason = denialReason(parsed.details);
      confirming = false;
      if (parsed.isForbidden && reason === "outcome_resolution_step_up_required") {
        stepUpRequired = true;
      } else if (reason === "outcome_revision_stale") {
        // The list moved under us: show the current revision and ask again.
        error = tr("guard.outcomes.stale");
        await load();
      } else {
        error = parsed.message;
      }
    } finally {
      submitting = false;
    }
  }

  function since(iso: string): string {
    return formatDateTime(iso, { dateStyle: "medium", timeStyle: "short" });
  }
</script>

{#if outcomes.length > 0 || receipt || (loadFailed && reportedUnresolved > 0)}
  <section
    class="rounded-lg border border-border bg-muted/20 p-4"
    aria-labelledby="guard-outcomes-title"
    data-testid="guard-outcomes"
  >
    <h3 id="guard-outcomes-title" class="text-sm font-semibold text-foreground">
      {tr("guard.outcomes.title")}
    </h3>
    <p class="mt-1 text-xs text-muted-foreground">
      {tr("guard.outcomes.intro")}
    </p>

    {#if receipt}
      <p
        class="mt-3 rounded-md border border-success/40 bg-success/10 p-3 text-xs text-foreground"
        role="status"
        data-testid="guard-outcome-receipt"
      >
        {tr("guard.outcomes.receipt", {
          command: receipt.command_id,
          revision: receipt.revision,
          disposition: tr(`guard.outcomes.disposition.${receipt.disposition}`),
          actor: receipt.actor,
        })}
        {receipt.fence_released ? tr("guard.outcomes.fenceReleased") : ""}
      </p>
    {/if}

    {#if loadFailed && reportedUnresolved > 0}
      <p class="mt-3 text-xs text-warning" role="alert">
        {tr("guard.outcomes.loadFailed")}
      </p>
    {/if}

    <ul class="mt-3 flex flex-col gap-3">
      {#each outcomes as outcome (outcome.command_id)}
        <li
          class="rounded-md border border-border bg-background p-3"
          data-testid="guard-outcome"
          data-command-id={outcome.command_id}
        >
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div class="min-w-0">
              <p class="text-sm font-medium text-foreground">
                {outcome.operation}
              </p>
              <p class="mt-0.5 font-mono text-[11px] text-muted-foreground">
                {outcome.command_id}
              </p>
              <p class="mt-1 flex items-center gap-1.5 text-xs text-warning">
                <TriangleAlert class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                {outcome.state === "needs_operator"
                  ? tr("guard.outcomes.needsOperator")
                  : tr("guard.outcomes.reconciling")}
                <span class="text-muted-foreground"
                  >· {tr("guard.outcomes.since", { at: since(outcome.unresolved_since) })}</span
                >
              </p>
              {#if outcome.reason}
                <p class="mt-1 text-xs text-muted-foreground">
                  {tr("guard.detail.reasonCode", { code: outcome.reason })}
                </p>
              {/if}
            </div>
            {#if activeId !== outcome.command_id}
              <Button
                variant="secondary"
                size="sm"
                testId="guard-outcome-resolve"
                onclick={() => open(outcome)}
              >
                {tr("guard.outcomes.resolve")}
              </Button>
            {/if}
          </div>

          {#if activeId === outcome.command_id}
            <div class="mt-3 flex flex-col gap-3 border-t border-border pt-3">
              <fieldset class="flex flex-col gap-2">
                <legend class="text-xs font-medium text-foreground">
                  {tr("guard.outcomes.dispositionLabel")}
                </legend>
                {#each DISPOSITIONS as option (option)}
                  <label class="flex items-start gap-2 text-xs text-foreground">
                    <input
                      type="radio"
                      name="guard-disposition-{outcome.command_id}"
                      value={option}
                      bind:group={disposition}
                      disabled={submitting || confirming}
                      class="mt-0.5"
                    />
                    <span>
                      <span class="font-medium"
                        >{tr(`guard.outcomes.disposition.${option}`)}</span
                      >
                      <span class="block text-muted-foreground"
                        >{tr(`guard.outcomes.disposition.${option}.hint`)}</span
                      >
                    </span>
                  </label>
                {/each}
              </fieldset>

              <div>
                <label
                  class="text-xs font-medium text-foreground"
                  for="guard-note-{outcome.command_id}"
                  >{tr("guard.outcomes.noteLabel")}</label
                >
                <textarea
                  id="guard-note-{outcome.command_id}"
                  class="mt-1 w-full rounded-md border border-border bg-background px-3 py-2 text-sm"
                  rows="3"
                  maxlength={NOTE_MAX}
                  bind:value={note}
                  disabled={submitting || confirming}
                  data-testid="guard-outcome-note"
                ></textarea>
                <p class="mt-0.5 text-[11px] text-muted-foreground">
                  {tr("guard.outcomes.noteHint", { max: NOTE_MAX })}
                </p>
              </div>

              {#if confirming}
                <div
                  class="rounded-md border border-warning/50 bg-warning/10 p-3 text-xs text-foreground"
                  role="alertdialog"
                  aria-label={tr("guard.outcomes.confirmTitle")}
                >
                  <p class="font-medium">{tr("guard.outcomes.confirmTitle")}</p>
                  <p class="mt-1">{tr("guard.outcomes.confirmBody")}</p>
                </div>
              {/if}

              {#if stepUpRequired}
                <GuardStepUpNotice
                  testId="guard-outcome-stepup"
                  body={tr("guard.outcomes.stepUp")}
                  onConfirmed={() => (stepUpRequired = false)}
                />
              {/if}

              {#if error}
                <p class="text-xs text-destructive" role="alert" data-testid="guard-outcome-error">
                  {error}
                </p>
              {/if}

              <div class="flex flex-wrap gap-2">
                {#if confirming}
                  <Button
                    variant="destructive"
                    size="sm"
                    testId="guard-outcome-confirm"
                    disabled={submitting}
                    onclick={() => void submit()}
                  >
                    {submitting
                      ? tr("guard.outcomes.releasing")
                      : tr("guard.outcomes.confirm")}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    disabled={submitting}
                    onclick={() => (confirming = false)}
                  >
                    {tr("guard.outcomes.back")}
                  </Button>
                {:else}
                  <Button
                    variant="primary"
                    size="sm"
                    testId="guard-outcome-continue"
                    disabled={!noteValid}
                    onclick={() => {
                      error = null;
                      stepUpRequired = false;
                      confirming = true;
                    }}
                  >
                    {tr("guard.outcomes.review")}
                  </Button>
                  <Button variant="ghost" size="sm" onclick={close}>
                    {tr("guard.outcomes.cancel")}
                  </Button>
                {/if}
              </div>
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  </section>
{/if}
