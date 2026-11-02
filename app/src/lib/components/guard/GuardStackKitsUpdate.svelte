<script lang="ts">
  import { tr, trn } from "#lib/i18n.svelte.js";
  import { ExternalLink, ShieldAlert } from "@lucide/svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    getStackKitsUpdate,
    setStackKitsUpdateBehavior,
    type StackKitsUpdateBehavior,
    type StackKitsUpdateChange,
    type StackKitsUpdateView,
  } from "#lib/api/guard.js";
  import { runStackKitLifecycleOperation } from "#lib/api/stacks.js";
  import { getJob, streamJobProgress } from "#lib/api/jobs.js";

  // Techstack manages StackKits updates (D-2026-11-02-03): it proposes the
  // pending release with highlights for this server's use cases and starts it
  // by the owner's setting. The Guard runs the governed upgrade, verifies it
  // and rolls it back when it fails.

  interface Props {
    workerId: string;
    stackId?: string;
    agentId?: string;
    onChanged?: () => void | Promise<void>;
  }

  let { workerId, stackId, agentId, onChanged }: Props = $props();

  const TERMINAL = ["completed", "failed", "canceled", "cancelled"];
  const BEHAVIORS: StackKitsUpdateBehavior[] = ["approve", "automatic"];

  let view = $state<StackKitsUpdateView | null>(null);
  let loadError = $state<string | null>(null);
  let error = $state<string | null>(null);
  let savingBehavior = $state(false);
  let approving = $state(false);
  let job = $state<{ id: string; state: string; progress: number } | null>(
    null,
  );
  let stopTracking: (() => void) | null = null;

  const proposal = $derived(view?.proposal);
  const started = $derived(
    view?.started && view.started.version === view.available
      ? view.started
      : undefined,
  );
  const securityNotice = $derived(
    view?.started?.trigger === "security" ? view.started : undefined,
  );
  const lastUpgrade = $derived(view?.last_upgrade);
  const running = $derived(job !== null && !TERMINAL.includes(job.state));
  // A job that ended before the Guard recorded an outcome never ran on the
  // server: nothing changed there.
  const notRun = $derived(
    job !== null &&
      TERMINAL.includes(job.state) &&
      job.state !== "completed" &&
      lastUpgrade?.job_id !== job.id,
  );
  const upgraded = $derived(
    lastUpgrade?.outcome === "succeeded" &&
      lastUpgrade.version === proposal?.available,
  );
  const canApprove = $derived(
    Boolean(proposal && stackId && agentId) && !running && !approving,
  );
  const changelogUrl = $derived(
    proposal?.changelog_url || view?.changelog_url || "",
  );

  async function load() {
    try {
      view = await getStackKitsUpdate(workerId);
      loadError = null;
      // Follow the update Techstack started only while nothing else is
      // tracked; an owner's later Approve job is not replaced by it.
      const jobId = started?.job_id;
      if (jobId && job === null) track(jobId);
    } catch (cause) {
      loadError = parseApiError(cause).message;
    }
  }

  $effect(() => {
    void workerId;
    void load();
    return () => stopTracking?.();
  });

  function track(id: string) {
    stopTracking?.();
    job = { id, state: "pending", progress: 0 };
    let poll: ReturnType<typeof setInterval> | null = null;
    let closed = false;
    const close = () => {
      closed = true;
      closeStream();
      if (poll) clearInterval(poll);
    };
    const read = async () => {
      if (closed) return;
      try {
        const detail = await getJob(id);
        if (closed) return;
        job = { id, state: detail.state, progress: detail.progress ?? 0 };
        if (TERMINAL.includes(detail.state)) {
          close();
          await load();
          await onChanged?.();
        }
      } catch (cause) {
        error = parseApiError(cause).message;
      }
    };
    const closeStream = streamJobProgress(id, {
      onEvent: () => void read(),
      onFallback: () => {
        if (!closed && !poll) poll = setInterval(() => void read(), 3_000);
      },
    });
    stopTracking = close;
    void read();
  }

  async function chooseBehavior(behavior: StackKitsUpdateBehavior) {
    if (!view || view.behavior === behavior || savingBehavior) return;
    savingBehavior = true;
    error = null;
    try {
      view = await setStackKitsUpdateBehavior(workerId, behavior);
      const jobId = started?.job_id;
      if (jobId && job === null) track(jobId);
    } catch (cause) {
      error = parseApiError(cause).message;
    } finally {
      savingBehavior = false;
    }
  }

  async function approve() {
    if (!proposal || !stackId || !agentId || !canApprove) return;
    approving = true;
    error = null;
    try {
      // The existing typed lifecycle operation: the governed upgrade to the
      // published release, run by the Guard on the server's installed CLI.
      const accepted = await runStackKitLifecycleOperation(stackId, {
        operation: "upgrade",
        agent_id: agentId,
        owner_approved: true,
        target_release: proposal.available,
      });
      track(accepted.job_id);
    } catch (cause) {
      error = parseApiError(cause).message;
    } finally {
      approving = false;
    }
  }

  function kindLabel(change: StackKitsUpdateChange): string {
    const key = `guard.updates.kind.${change.kind}`;
    const label = tr(key);
    return label === key ? change.kind : label;
  }
</script>

{#snippet changeList(changes: StackKitsUpdateChange[], testId: string)}
  <ul class="mt-1.5 flex flex-col gap-1.5" data-testid={testId}>
    {#each changes as change, index (change.version + index)}
      <li class="flex items-start gap-2 text-xs leading-relaxed">
        <span
          class="mt-px shrink-0 rounded px-1.5 py-px text-[10px] font-semibold uppercase tracking-wide {change.kind ===
          'security'
            ? 'bg-warning/15 text-warning'
            : 'bg-muted text-muted-foreground'}">{kindLabel(change)}</span
        >
        <span class="min-w-0 text-foreground">{change.text}</span>
      </li>
    {/each}
  </ul>
{/snippet}

{#if view || loadError}
  <section
    class="rounded-lg border border-border bg-muted/20 p-4"
    aria-labelledby="guard-updates-title"
    data-testid="guard-stackkits-update"
    data-behavior={view?.behavior ?? ""}
    data-available={view?.available ?? ""}
  >
    <div class="flex flex-wrap items-baseline justify-between gap-2">
      <h3
        id="guard-updates-title"
        class="text-sm font-semibold text-foreground"
      >
        {tr("guard.updates.title")}
      </h3>
      {#if view?.installed}
        <span
          class="font-mono text-xs text-muted-foreground"
          data-testid="guard-updates-installed"
        >
          {tr("guard.updates.runs", { version: view.installed })}
        </span>
      {/if}
    </div>

    {#if loadError}
      <p class="mt-2 text-xs text-destructive" role="alert">
        {tr("guard.updates.loadFailed")}
      </p>
    {/if}

    {#if view}
      <div
        class="mt-3 grid gap-x-6 gap-y-2 border-t border-border pt-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center"
      >
        <div class="min-w-0">
          <p class="text-sm font-medium text-foreground">
            {tr("guard.updates.behavior.label")}
          </p>
          <p class="text-xs text-muted-foreground">
            {tr("guard.updates.behavior.hint")}
          </p>
        </div>
        <div
          class="inline-flex rounded-md border border-border bg-background p-0.5"
          role="radiogroup"
          aria-label={tr("guard.updates.behavior.label")}
          data-testid="guard-updates-behavior"
        >
          {#each BEHAVIORS as behavior (behavior)}
            <button
              type="button"
              role="radio"
              aria-checked={view.behavior === behavior}
              disabled={savingBehavior}
              class="rounded px-3 py-1.5 text-xs font-medium transition-colors {view.behavior ===
              behavior
                ? 'bg-primary text-primary-foreground'
                : 'text-muted-foreground hover:text-foreground'}"
              data-testid="guard-updates-behavior-{behavior}"
              onclick={() => void chooseBehavior(behavior)}
            >
              {tr(`guard.updates.behavior.${behavior}`)}
            </button>
          {/each}
        </div>
      </div>

      {#if securityNotice}
        <div
          class="mt-3 flex gap-2 rounded-md border border-warning/40 bg-warning/5 p-3"
          role="status"
          data-testid="guard-updates-security-notice"
        >
          <ShieldAlert
            class="mt-0.5 h-4 w-4 shrink-0 text-warning"
            aria-hidden="true"
          />
          <div class="min-w-0 text-xs">
            <p class="font-medium text-foreground">
              {tr("guard.updates.security.applied", {
                version: securityNotice.version,
              })}
            </p>
            {#if securityNotice.patched?.length}
              <ul class="mt-1 list-disc ps-4 text-muted-foreground">
                {#each securityNotice.patched as note, index (index)}
                  <li>
                    {#if note.app}<span class="font-mono text-foreground"
                        >{note.app}</span
                      >:{" "}
                    {/if}{note.text}
                  </li>
                {/each}
              </ul>
            {/if}
            {#if changelogUrl}
              <a
                class="mt-1 inline-flex items-center gap-1 text-primary hover:underline"
                href={changelogUrl}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="guard-updates-security-changelog"
              >
                {tr("guard.updates.changelog")}
                <ExternalLink class="h-3 w-3" aria-hidden="true" />
              </a>
            {/if}
          </div>
        </div>
      {/if}

      {#if proposal}
        <div
          class="mt-3 rounded-md border border-border bg-background p-3"
          data-testid="guard-updates-proposal"
          data-security={proposal.security?.affects_node ? "true" : "false"}
        >
          <div class="flex flex-wrap items-center gap-2">
            <p class="text-sm font-semibold text-foreground">
              {tr("guard.updates.available", { version: proposal.available })}
            </p>
            {#if proposal.security?.affects_node}
              <span
                class="rounded bg-warning/15 px-1.5 py-px text-[10px] font-semibold uppercase tracking-wide text-warning"
                data-testid="guard-updates-security-badge"
                >{tr("guard.updates.security.badge")}</span
              >
            {/if}
          </div>

          {#if proposal.highlights.length > 0}
            <div class="mt-2 grid gap-3 md:grid-cols-2">
              {#each proposal.highlights as highlight (highlight.use_case_id)}
                <div
                  class="min-w-0"
                  data-testid="guard-updates-highlight"
                  data-use-case={highlight.use_case_id}
                >
                  <p class="text-xs font-semibold text-foreground">
                    {tr("guard.updates.forUseCase", {
                      name: highlight.use_case_title,
                    })}
                  </p>
                  {@render changeList(
                    highlight.changes,
                    "guard-updates-highlight-changes",
                  )}
                </div>
              {/each}
            </div>
          {/if}

          {#if proposal.security && proposal.security.changes.length > 0 && proposal.highlights.length === 0}
            {@render changeList(
              proposal.security.changes,
              "guard-updates-security-changes",
            )}
          {/if}

          {#if proposal.other_changes.length > 0}
            <p class="mt-3 text-xs font-semibold text-muted-foreground">
              {tr("guard.updates.other")}
            </p>
            {@render changeList(proposal.other_changes, "guard-updates-other")}
            {#if proposal.other_changes_total > proposal.other_changes.length}
              <p class="mt-1 text-xs text-muted-foreground">
                {trn(
                  "guard.updates.more",
                  proposal.other_changes_total - proposal.other_changes.length,
                )}
              </p>
            {/if}
          {:else if proposal.highlights.length === 0 && !proposal.security}
            <p class="mt-2 text-xs text-muted-foreground">
              {tr("guard.updates.noNotes")}
            </p>
          {/if}

          <div
            class="mt-3 flex flex-wrap items-center justify-between gap-3 border-t border-border pt-3"
          >
            {#if changelogUrl}
              <a
                class="inline-flex items-center gap-1 text-xs font-medium text-primary hover:underline"
                href={changelogUrl}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="guard-updates-changelog"
              >
                {tr("guard.updates.changelog")}
                <ExternalLink class="h-3 w-3" aria-hidden="true" />
              </a>
            {/if}
            {#if running && job}
              <p
                class="text-xs text-foreground"
                role="status"
                data-testid="guard-updates-progress"
                data-job-id={job.id}
              >
                {tr("guard.updates.running", {
                  version: proposal.available,
                  progress: Math.round(job.progress),
                })}
              </p>
            {:else if started?.job_id && !job}
              <p class="text-xs text-muted-foreground">
                {tr(`guard.updates.started.${started.trigger}`)}
              </p>
            {:else if !upgraded}
              <div class="flex items-center gap-3">
                <p class="hidden max-w-xs text-xs text-muted-foreground md:block">
                  {tr("guard.updates.approveHint")}
                </p>
                <Button
                  variant="primary"
                  size="sm"
                  testId="guard-updates-approve"
                  disabled={!canApprove}
                  onclick={() => void approve()}
                >
                  {tr("guard.updates.approve")}
                </Button>
              </div>
            {/if}
          </div>
        </div>
      {:else if lastUpgrade?.outcome !== "failed"}
        <p class="mt-3 text-xs text-muted-foreground">
          {tr("guard.updates.current")}
        </p>
      {/if}

      {#if notRun && !running}
        <p
          class="mt-2 text-xs text-destructive"
          role="status"
          data-testid="guard-updates-not-run"
        >
          {tr("guard.updates.notRun", { installed: view.installed ?? "" })}
        </p>
      {/if}

      {#if lastUpgrade && !running}
        <p
          class="mt-2 text-xs {lastUpgrade.outcome === 'failed'
            ? 'text-destructive'
            : 'text-foreground'}"
          data-testid="guard-updates-outcome"
          data-outcome={lastUpgrade.outcome}
        >
          {#if lastUpgrade.outcome === "failed"}
            {tr("guard.updates.failed", {
              version: lastUpgrade.version,
              installed: view.installed ?? "",
            })}
          {:else if view.installed !== lastUpgrade.version}
            {tr("guard.updates.succeeded", { version: lastUpgrade.version })}
          {/if}
        </p>
      {/if}

      {#if error}
        <p class="mt-2 text-xs text-destructive" role="alert">{error}</p>
      {/if}
    {/if}
  </section>
{/if}
