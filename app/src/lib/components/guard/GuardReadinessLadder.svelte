<script lang="ts">
  import { tr, formatDateTime } from "#lib/i18n.svelte.js";
  import { CircleCheck, CircleDashed, Clock, TriangleAlert } from "@lucide/svelte";
  import { getCanonicalServer, type CanonicalServer } from "#lib/api/registry.js";
  import { collectGuardEvidence } from "#lib/api/guard.js";
  import { getStackServerDetails } from "#lib/api/stacks.js";
  import {
    assessGuard,
    pairingLadder,
    type LadderStepState,
  } from "#lib/guard/facts.js";
  import { renderGuardMessage } from "#lib/guard/render.js";

  interface Props {
    /** The server the pairing is expected to produce; null until one is seen. */
    server: CanonicalServer | null;
    stackId: string;
    /** Why pairing could not start or continue, when the flow knows. */
    pairingError?: string;
  }

  let { server, stackId, pairingError = "" }: Props = $props();

  const POLL_MS = 5000;

  let live = $state<CanonicalServer | null>(null);
  let capabilities = $state<unknown>(undefined);
  let now = $state(Date.now());

  // The ladder follows the persisted read model, not the pairing flow's own
  // belief: every rung is a fact the control plane recorded.
  $effect(() => {
    const serverId = server?.id;
    if (!serverId) return;
    let active = true;
    const tick = async () => {
      now = Date.now();
      const [canonical, details] = await Promise.allSettled([
        getCanonicalServer(serverId),
        getStackServerDetails(stackId, serverId),
      ]);
      if (!active) return;
      if (canonical.status === "fulfilled") live = canonical.value;
      if (details.status === "fulfilled") {
        capabilities = details.value.server.capabilities;
      }
    };
    void tick();
    const timer = setInterval(() => void tick(), POLL_MS);
    return () => {
      active = false;
      clearInterval(timer);
    };
  });

  const current = $derived(live ?? server);
  const ladder = $derived(
    pairingLadder(assessGuard(collectGuardEvidence(current, capabilities), now)),
  );

  const formatTime = (iso: string) =>
    formatDateTime(iso, { dateStyle: "medium", timeStyle: "short" });
  const say = (message: Parameters<typeof renderGuardMessage>[0]) =>
    renderGuardMessage(message, tr, formatTime);

  const tone: Record<LadderStepState, string> = {
    done: "text-success",
    failed: "text-warning",
    waiting: "text-primary",
    blocked: "text-muted-foreground",
  };
</script>

<section
  class="mt-5 rounded-xl border border-border bg-card p-4"
  aria-labelledby="guard-ladder-title"
  data-testid="guard-ladder"
  data-complete={ladder.complete}
>
  <h3 id="guard-ladder-title" class="text-sm font-semibold text-foreground">
    {tr("guard.ladder.title")}
  </h3>
  <p class="mt-1 text-xs text-muted-foreground">{tr("guard.ladder.intro")}</p>

  <ol class="mt-3 flex flex-col gap-3">
    {#each ladder.steps as step, index (step.key)}
      <li
        class="grid grid-cols-[1.75rem_minmax(0,1fr)] gap-x-2"
        data-testid="guard-ladder-step"
        data-step={step.key}
        data-state={step.state}
        aria-current={step.state === "waiting" || step.state === "failed"
          ? "step"
          : undefined}
      >
        <span class="mt-0.5 {tone[step.state]}">
          {#if step.state === "done"}
            <CircleCheck class="h-5 w-5" aria-hidden="true" />
          {:else if step.state === "failed"}
            <TriangleAlert class="h-5 w-5" aria-hidden="true" />
          {:else if step.state === "waiting"}
            <Clock class="h-5 w-5" aria-hidden="true" />
          {:else}
            <CircleDashed class="h-5 w-5" aria-hidden="true" />
          {/if}
        </span>
        <div class="min-w-0 text-sm">
          <p class="flex flex-wrap items-baseline gap-x-2">
            <span class="font-medium text-foreground"
              >{index + 1}. {tr(`guard.ladder.${step.key}`)}</span
            >
            <span class="text-xs font-semibold {tone[step.state]}"
              >{tr(`guard.ladder.state.${step.state}`)}</span
            >
          </p>
          {#if step.state !== "blocked"}
            {#if step.key === "bound" && !current && step.state !== "done"}
              <p class="mt-0.5 text-xs text-foreground">{tr("guard.ladder.bound.awaiting")}</p>
              <p class="mt-0.5 text-xs text-muted-foreground">
                <span class="font-semibold">{tr("guard.next")}</span>
                {tr("guard.ladder.bound.awaitingNext")}
              </p>
            {:else}
              <p class="mt-0.5 text-xs text-foreground">{say(step.fact.reason)}</p>
              {#each step.fact.details as detail, detailIndex (detailIndex)}
                <p class="text-[11px] text-muted-foreground">{say(detail)}</p>
              {/each}
              {#if step.state !== "done" && step.fact.next}
                <p class="mt-0.5 text-xs text-muted-foreground">
                  <span class="font-semibold">{tr("guard.next")}</span>
                  {say(step.fact.next)}
                </p>
              {/if}
            {/if}
            {#if step.key === "bound" && pairingError}
              <p class="mt-1 text-xs text-destructive" role="alert" data-testid="guard-ladder-error">
                {pairingError}
              </p>
            {/if}
          {:else}
            <p class="mt-0.5 text-xs text-muted-foreground">{tr("guard.ladder.blockedHint")}</p>
          {/if}
        </div>
      </li>
    {/each}
  </ol>

  {#if ladder.complete}
    <p class="mt-3 text-xs font-semibold text-success" role="status">
      {tr("guard.ladder.complete")}
    </p>
  {/if}
</section>
