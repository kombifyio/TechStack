<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import {
    Check,
    LoaderCircle,
    ShieldAlert,
    Sparkles,
    TriangleAlert,
    Wrench,
  } from "@lucide/svelte";
  import {
    creation,
    type DegradedComponentView,
  } from "./creation-controller.svelte.js";
  import {
    degradedComponentMeaning,
    degradedComponentTitle,
    formatNextResumeAt,
  } from "./creation-results.js";

  // Lifecycle-only degradation (no component list) still gets one row so the
  // user sees what needs attention and can hand it to the Companion.
  let rows = $derived<DegradedComponentView[]>(
    creation.degradedComponents.length
      ? creation.degradedComponents
      : [
          {
            component: "",
            reasonCode: "",
            retryable: false,
            repairRequesting: false,
          },
        ],
  );

  function repairLabel(row: DegradedComponentView): string {
    switch (row.repairStatus) {
      case "scheduled":
        return row.repair?.nextAttemptAt
          ? tr("ui.stacksCreatingCreationDegraded.repairScheduledAt", {
              time: formatNextResumeAt(row.repair.nextAttemptAt),
            })
          : tr("ui.stacksCreatingCreationDegraded.repairScheduled");
      case "running":
        return tr("ui.stacksCreatingCreationDegraded.repairRunning");
      case "repaired":
        return tr("ui.stacksCreatingCreationDegraded.repairDone");
      case "failed":
        return tr("ui.stacksCreatingCreationDegraded.repairFailed");
      default:
        return "";
    }
  }

  function canRepair(row: DegradedComponentView): boolean {
    return (
      Boolean(row.component) &&
      (row.retryable || Boolean(row.repair)) &&
      row.repairStatus !== "running" &&
      row.repairStatus !== "repaired"
    );
  }
</script>

<ul class="mb-6 flex flex-col gap-3" data-testid="creation-degraded-components">
  {#each rows as row (row.component || "lifecycle")}
    {@const repaired = row.repairStatus === "repaired"}
    {@const status = repairLabel(row)}
    <li
      class="rounded-xl border bg-card p-4 {repaired
        ? 'border-success/30'
        : 'border-warning/40'}"
      data-testid="creation-degraded-component"
      data-component={row.component}
      data-repair-status={row.repairStatus ?? ""}
    >
      <div class="flex items-start gap-3">
        <div
          class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full {repaired
            ? 'bg-success/10 text-success'
            : 'bg-warning/15 text-warning'}"
          aria-hidden="true"
        >
          {#if repaired}
            <Check class="h-4 w-4" />
          {:else if row.component === "public_tls"}
            <ShieldAlert class="h-4 w-4" />
          {:else}
            <TriangleAlert class="h-4 w-4" />
          {/if}
        </div>
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
            <h3 class="text-sm font-semibold text-foreground">
              {degradedComponentTitle(row.component)}
            </h3>
            {#if status}
              <span
                class="inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-medium {repaired
                  ? 'bg-success/10 text-success'
                  : row.repairStatus === 'failed'
                    ? 'bg-destructive/10 text-destructive'
                    : 'bg-warning/10 text-warning'}"
                role="status"
              >
                {#if row.repairStatus === "running"}
                  <LoaderCircle class="h-3 w-3 animate-spin" aria-hidden="true" />
                {/if}
                {status}
              </span>
            {/if}
          </div>
          {#if !repaired}
            <p class="mt-1 text-sm text-muted-foreground">
              {degradedComponentMeaning(row)}
            </p>
          {/if}

          {#if row.repairMessage}
            <p class="mt-2 text-xs text-destructive" role="alert">
              {row.repairMessage}
            </p>
          {/if}

          {#if !repaired}
            <div class="mt-3 flex flex-wrap items-center gap-2">
              {#if canRepair(row)}
                <button
                  type="button"
                  data-kx="control"
                  data-variant="primary"
                  class="inline-flex items-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
                  data-testid="creation-degraded-repair"
                  disabled={row.repairRequesting}
                  aria-busy={row.repairRequesting}
                  onclick={() => creation.repairDegradedComponent(row.component)}
                >
                  {#if row.repairRequesting}
                    <LoaderCircle class="h-4 w-4 animate-spin" aria-hidden="true" />
                  {:else}
                    <Wrench class="h-4 w-4" aria-hidden="true" />
                  {/if}
                  {tr("ui.stacksCreatingCreationDegraded.repairNow")}
                </button>
              {/if}
              {#if creation.companionAvailable}
                <button
                  type="button"
                  data-kx="control"
                  class="inline-flex items-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                  data-testid="creation-degraded-ask-companion"
                  onclick={creation.askCompanionAboutDegraded}
                >
                  <Sparkles class="h-4 w-4" aria-hidden="true" />
                  {tr("ui.stacksCreatingCreationFailure.askCompanion")}
                </button>
              {/if}
            </div>
          {/if}
        </div>
      </div>
    </li>
  {/each}
</ul>
