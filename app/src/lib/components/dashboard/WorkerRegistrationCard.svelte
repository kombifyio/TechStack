<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { Surface } from "@kombiverselabs/ui/primitives";
  import Button from "#lib/components/ui/Button.svelte";
  import type { Worker } from "#lib/api/workers.js";
  import type { DeploymentRequirements } from "#lib/wizard/index.js";

  // The legacy registration cards: shown only while a single deployment has a
  // registration token but no operations evidence yet.
  interface Props {
    /** "running": the stack runs, only the join material is shown. */
    variant: "running" | "connect";
    connectedWorkers: number;
    requirements: DeploymentRequirements | null;
    approvedWorkers: Worker[];
    serverUrl: string;
    registryMode: string;
    registryUrlError: string | null;
    installCommand: string;
    canRollout: boolean;
    rolloutLoading: boolean;
    onDeploy: () => void;
  }

  let {
    variant,
    connectedWorkers,
    requirements,
    approvedWorkers,
    serverUrl,
    registryMode,
    registryUrlError,
    installCommand,
    canRollout,
    rolloutLoading,
    onDeploy,
  }: Props = $props();

  let copiedInstallCommand = $state(false);
  let copiedRegistryUrl = $state(false);

  function copyInstallCommand() {
    navigator.clipboard.writeText(installCommand);
    copiedInstallCommand = true;
    setTimeout(() => (copiedInstallCommand = false), 2000);
  }

  function copyRegistryUrl() {
    if (!serverUrl) return;
    navigator.clipboard.writeText(serverUrl);
    copiedRegistryUrl = true;
    setTimeout(() => (copiedRegistryUrl = false), 2000);
  }
</script>

{#if variant === "running"}
  <Surface class="mb-6 p-5">
    <div data-testid="worker-management-card">
      <div class="flex items-start justify-between mb-4">
        <div class="flex items-center gap-3">
          <div
            class="w-10 h-10 rounded-lg bg-success/10 flex items-center justify-center"
          >
            <svg
              class="w-5 h-5 text-success"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M5 13l4 4L19 7"
              />
            </svg>
          </div>
          <div>
            <h2 class="text-lg font-semibold text-foreground">
              {tr("ui.worker.statusUnavailable")}
            </h2>
            <p class="text-sm text-muted-foreground">
              {tr("ui.workerRegistrationCard.operationsEvidenceIsRequiredBefore")}
            </p>
          </div>
        </div>
        <span
          data-kx="status"
          data-status="warn"
          class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
          data-testid="worker-connected-count"
          >{tr("ui.worker.verifiedConnected", { count: connectedWorkers })}</span
        >
      </div>

      <!-- Worker registry URL for running stack -->
      {#if serverUrl}
        <div class="rounded-lg bg-muted/50 p-4 mb-4">
          <div class="flex items-center justify-between gap-3 mb-2">
            <span class="text-sm text-muted-foreground">
              {tr("ui.worker.registryLink")}{registryMode ? ` (${registryMode})` : ""}:
            </span>
            <Button variant="ghost" size="sm" onclick={copyRegistryUrl}>
              {copiedRegistryUrl ? tr("ui.common.copiedCheck") : tr("ui.common.copy")}
            </Button>
          </div>
          <a
            href={serverUrl}
            target="_blank"
            rel="noopener noreferrer"
            data-testid="worker-registry-url"
            class="text-sm font-mono text-primary break-all hover:underline"
          >
            {serverUrl}
          </a>
          {#if registryUrlError}
            <div class="mt-2 text-xs text-warning">
              {tr("ui.worker.registryUrlError", { error: registryUrlError })}
            </div>
          {/if}
        </div>
      {/if}

      <!-- Install command for running stack -->
      {#if installCommand}
        <div class="rounded-lg bg-muted/50 p-4">
          <div class="flex items-center justify-between mb-2">
            <span class="text-sm text-muted-foreground"
              >{tr("ui.workerRegistrationCard.installCommandForNewWorkers")}</span
            >
            <Button variant="ghost" size="sm" onclick={copyInstallCommand}>
              {copiedInstallCommand ? tr("ui.common.copiedCheck") : tr("ui.common.copy")}
            </Button>
          </div>
          <pre
            class="text-sm font-mono text-primary overflow-x-auto whitespace-pre-wrap break-all">{installCommand}</pre>
        </div>
      {/if}
    </div>
  </Surface>
{:else}
  <Surface class="mb-8 p-5">
    <div data-testid="worker-management-card">
      <div class="flex items-start justify-between mb-4">
        <div class="flex items-center gap-3">
          <div
            class="w-10 h-10 rounded-lg bg-primary/10 flex items-center justify-center"
          >
            <svg
              class="w-5 h-5 text-primary"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M5 12h14M12 5l7 7-7 7"
              />
            </svg>
          </div>
          <div>
            <h2 class="text-lg font-semibold text-foreground">
              {tr("ui.worker.connect")}
            </h2>
            <p class="text-sm text-muted-foreground">
              {#if requirements}
                {requirements.description}
              {:else}
                {tr("ui.worker.connectNodes")}
              {/if}
            </p>
          </div>
        </div>
        <div class="text-right">
          <div class="text-2xl font-bold text-primary">
            <span data-testid="worker-connected-count"
              >{connectedWorkers}/{requirements?.minTotalServers || 1}</span
            >
          </div>
          <div class="text-xs text-muted-foreground">
            {tr("ui.workerRegistrationCard.workersConnectedVerified")}
          </div>
        </div>
      </div>

      <!-- Progress bar -->
      {#if requirements}
        <div class="mb-4">
          <div class="h-2 bg-muted rounded-full overflow-hidden">
            <div
              class="h-full rounded-full transition-all duration-500 {canRollout
                ? 'bg-success'
                : 'bg-primary'}"
              style="width: {Math.min(
                100,
                (connectedWorkers / requirements.minTotalServers) * 100,
              )}%"
            ></div>
          </div>
        </div>
      {/if}

      <!-- Approved workers remain visible, but approval is not connection evidence. -->
      {#if approvedWorkers.length > 0}
        <div class="mb-4 space-y-2">
          <div class="text-sm text-muted-foreground mb-2">
            {tr("ui.worker.approved")}
          </div>
          {#each approvedWorkers as w}
            <div
              data-testid="approved-worker-row"
              class="flex items-center justify-between rounded-lg bg-muted/50 px-3 py-2"
            >
              <div class="flex items-center gap-3">
                <div
                  class="h-2.5 w-2.5 rounded-full bg-muted-foreground"
                ></div>
                <div class="min-w-0">
                  <div class="text-sm text-foreground truncate">
                    {w.hostname || w.id}
                  </div>
                  <div class="text-xs text-muted-foreground truncate">
                    {w.ip || "-"}
                  </div>
                </div>
              </div>
              <span
                data-kx="status"
                data-status="off"
                class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
              >
                {tr("ui.worker.unverified")}
              </span>
            </div>
          {/each}
        </div>
      {/if}

      <!-- Worker registry URL (must be reachable from worker machines) -->
      {#if serverUrl}
        <div class="rounded-lg bg-muted/50 p-4 mb-4">
          <div class="flex items-center justify-between gap-3 mb-2">
            <span class="text-sm text-muted-foreground">
              {tr("ui.worker.registryLink")}{registryMode ? ` (${registryMode})` : ""}:
            </span>
            <Button variant="ghost" size="sm" onclick={copyRegistryUrl}>
              {copiedRegistryUrl ? tr("ui.common.copiedCheck") : tr("ui.common.copy")}
            </Button>
          </div>
          <a
            href={serverUrl}
            target="_blank"
            rel="noopener noreferrer"
            data-testid="worker-registry-url"
            class="text-sm font-mono text-primary break-all hover:underline"
          >
            {serverUrl}
          </a>
          {#if registryUrlError}
            <div class="mt-2 text-xs text-warning">
              {tr("ui.worker.registryUrlError", { error: registryUrlError })}
            </div>
          {/if}
        </div>
      {/if}

      <!-- Install command -->
      {#if installCommand}
        <div class="rounded-lg bg-muted/50 p-4 mb-4">
          <div class="flex items-center justify-between mb-2">
            <span class="text-sm text-muted-foreground">{tr("ui.workerRegistrationCard.installCommand")}</span
            >
            <Button variant="ghost" size="sm" onclick={copyInstallCommand}>
              {copiedInstallCommand ? tr("ui.common.copiedCheck") : tr("ui.common.copy")}
            </Button>
          </div>
          <pre
            class="text-sm font-mono text-primary overflow-x-auto whitespace-pre-wrap break-all">{installCommand}</pre>
        </div>
      {/if}

      <!-- Actions -->
      <div class="flex flex-wrap gap-3">
        <Button
          testId="deploy-homelab-button"
          onclick={() => onDeploy()}
          variant={canRollout && !rolloutLoading ? "primary" : "secondary"}
          disabled={!canRollout || rolloutLoading}
        >
          {#if rolloutLoading}
            {tr("ui.worker.startingRollout")}
          {:else if canRollout}
            {tr("ui.worker.deployHomelab")}
          {:else}
            {tr("ui.worker.waitingVerified", { connected: connectedWorkers, required: requirements?.minTotalServers || 1 })}
          {/if}
        </Button>
      </div>

      <!-- Requirements details - prominently displayed -->
      {#if requirements?.details?.length && requirements.details.length > 0}
        <div class="mt-6 pt-4 border-t border-border">
          <h3
            class="text-sm font-medium text-foreground mb-3 flex items-center gap-2"
          >
            <svg
              class="w-4 h-4 text-info"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4"
              />
            </svg>
            {tr("ui.worker.requirements")}
          </h3>
          <div class="grid gap-2 sm:grid-cols-2">
            {#each requirements.details as detail, i}
              <div
                class="flex items-start gap-2 text-sm text-muted-foreground bg-muted/30 rounded-lg px-3 py-2"
              >
                <svg
                  class="w-4 h-4 text-info shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                <span>{detail}</span>
              </div>
            {/each}
          </div>

          <!-- Next steps hint -->
          {#if !canRollout}
            <div
              class="mt-4 p-3 bg-warning/10 border border-warning/30 rounded-lg"
            >
              <p class="text-sm text-foreground flex items-start gap-2">
                <svg
                  class="w-4 h-4 text-warning shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
                  />
                </svg>
                <span>
                  <strong>{tr("ui.workerRegistrationCard.nextStep")}</strong>
                  {requirements.minTotalServers > 1
                    ? tr("ui.worker.runInstallMany", { count: requirements.minTotalServers })
                    : tr("ui.worker.runInstallOne")}
                </span>
              </p>
            </div>
          {:else}
            <div
              class="mt-4 p-3 bg-success/10 border border-success/30 rounded-lg"
            >
              <p class="text-sm text-foreground flex items-start gap-2">
                <svg
                  class="w-4 h-4 text-success shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M5 13l4 4L19 7"
                  />
                </svg>
                <span>
                  <strong>{tr("ui.worker.readyLead")}</strong> {tr("ui.worker.readyBody")}
                </span>
              </p>
            </div>
          {/if}
        </div>
      {/if}
    </div>
  </Surface>
{/if}
