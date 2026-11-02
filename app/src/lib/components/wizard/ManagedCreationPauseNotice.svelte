<!--
  Why new managed servers at one provider are held for this account, what led
  to it and when creation reopens. Only creating is held; existing servers stay
  readable, stoppable and deletable.
-->
<script lang="ts">
  import { CirclePause, RefreshCw } from "@lucide/svelte";
  import type { ManagedCreationPause } from "#lib/api/stacks.js";
  import { formatDateTime, tr } from "#lib/i18n.svelte.js";

  let {
    pause,
    provider,
    onrefresh,
  }: {
    pause: ManagedCreationPause;
    provider: string;
    onrefresh: () => Promise<void>;
  } = $props();

  const TITLES: Record<string, string> = {
    paused: "wizard.server.managed.pause.title.paused",
    probe_in_flight: "wizard.server.managed.pause.title.probeInFlight",
    outcome_unknown: "wizard.server.managed.pause.title.outcomeUnknown",
  };
  const FAILURES: Record<string, string> = {
    "provider.auth": "wizard.server.managed.pause.failure.auth",
    "provider.permission": "wizard.server.managed.pause.failure.permission",
    "provider.quota": "wizard.server.managed.pause.failure.quota",
    "provider.rate_limit": "wizard.server.managed.pause.failure.rateLimit",
    "provider.transient": "wizard.server.managed.pause.failure.transient",
    "provider.timeout": "wizard.server.managed.pause.failure.timeout",
    "provider.partial_create": "wizard.server.managed.pause.failure.partialCreate",
    "provider.cleanup_required":
      "wizard.server.managed.pause.failure.cleanupRequired",
  };
  const time = (value: string) =>
    formatDateTime(value, { timeStyle: "short" });

  let refreshing = $state(false);
  async function refresh() {
    refreshing = true;
    try {
      await onrefresh();
    } finally {
      refreshing = false;
    }
  }
</script>

<section
  class="pause"
  role="status"
  data-testid="managed-creation-pause"
  data-state={pause.state}
>
  <header>
    <CirclePause size={18} aria-hidden="true" />
    <h4>
      {tr(TITLES[pause.state] ?? TITLES.paused, { provider })}
    </h4>
    <button
      type="button"
      onclick={() => void refresh()}
      disabled={refreshing}
      data-testid="managed-creation-pause-refresh"
      ><RefreshCw size={13} aria-hidden="true" />{tr(
        "wizard.server.managed.pause.refresh",
      )}</button
    >
  </header>
  <p>{tr("wizard.server.managed.pause.lead")}</p>
  <dl>
    {#if pause.consecutive_failures > 0}
      <div data-testid="managed-creation-pause-failures">
        <dt>{tr("wizard.server.managed.pause.failures")}</dt>
        <dd>{pause.consecutive_failures}</dd>
        <small
          >{tr("wizard.server.managed.pause.failuresCaption", {
            threshold: pause.policy.threshold,
            minutes: Math.round(pause.policy.window_seconds / 60),
          })}</small
        >
      </div>
    {/if}
    {#if pause.last_failure}
      <div data-testid="managed-creation-pause-last-failure">
        <dt>{tr("wizard.server.managed.pause.lastFailure")}</dt>
        <dd>
          {tr(
            FAILURES[pause.last_failure.code] ??
              "wizard.server.managed.pause.failure.other",
          )}
        </dd>
        <small>{time(pause.last_failure.at)}</small>
      </div>
    {/if}
    <div data-testid="managed-creation-pause-recovery">
      <dt>{tr("wizard.server.managed.pause.resumes")}</dt>
      {#if pause.state === "paused" && pause.retry_at}
        <dd>{time(pause.retry_at)}</dd>
        <small>{tr("wizard.server.managed.pause.resumesCaption")}</small>
      {:else if pause.recovery === "reconcile_existing_create"}
        <dd>{tr("wizard.server.managed.pause.recovery.reconcile")}</dd>
      {:else}
        <dd>{tr("wizard.server.managed.pause.recovery.wait")}</dd>
      {/if}
    </div>
  </dl>
</section>

<style>
  .pause {
    display: grid;
    gap: 10px;
    border-left: 2px solid var(--warning);
    padding-left: 16px;
  }
  header {
    display: flex;
    align-items: center;
    gap: 9px;
    flex-wrap: wrap;
  }
  header > :global(svg) {
    color: var(--warning);
  }
  h4 {
    font-size: 15px;
    font-weight: 550;
    letter-spacing: -0.01em;
  }
  header button {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    margin-inline-start: auto;
    font-size: 12px;
    color: var(--primary);
  }
  header button:disabled {
    opacity: 0.6;
  }
  header button:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  p {
    font-size: 13px;
    line-height: 1.6;
    color: var(--muted-foreground);
    max-width: 70ch;
  }
  dl {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
    gap: 4px 20px;
  }
  dl div {
    display: grid;
    gap: 4px;
    padding-block: 6px;
  }
  dt,
  small {
    font-size: 11px;
    color: var(--muted-foreground);
  }
  dd {
    font-size: 15px;
    font-weight: 550;
  }
</style>
