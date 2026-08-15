<!--
  PreCheckResults Component
  
  Displays pre-check validation results with visual status indicators.
  Shows what checks passed, failed, or were skipped before deployment.
  
  Props:
  - results: PreCheckResult[] - Array of check results from backend
  - definitions: PreCheckDefinition[] - Optional definitions with blocking info
  - loading: boolean - Show loading state
  - compact: boolean - Use compact view without expandable details
-->
<script lang="ts">
  import { slide } from "svelte/transition";

  export interface PreCheckResult {
    type: string;
    status: "passed" | "failed" | "skipped" | "warning";
    details?: Record<string, unknown>;
    error?: string;
  }

  export interface PreCheckDefinition {
    type: string;
    description: string;
    minVersion?: string;
    blocking: boolean;
  }

  interface Props {
    results: PreCheckResult[];
    definitions?: PreCheckDefinition[];
    loading?: boolean;
    compact?: boolean;
  }

  let {
    results,
    definitions = [],
    loading = false,
    compact = false,
  }: Props = $props();

  // Track expanded states per check type
  let expandedChecks = $state<Set<string>>(new Set());

  // Compute summary counts
  let summary = $derived(() => {
    const counts = {
      passed: 0,
      failed: 0,
      skipped: 0,
      warning: 0,
      blockingFailed: 0,
    };
    for (const result of results) {
      counts[result.status]++;
      if (result.status === "failed" && isBlocking(result.type)) {
        counts.blockingFailed++;
      }
    }
    return counts;
  });

  // Check if a type is blocking
  function isBlocking(type: string): boolean {
    const def = definitions.find((d) => d.type === type);
    return def?.blocking ?? false;
  }

  // Get definition for a type
  function getDefinition(type: string): PreCheckDefinition | undefined {
    return definitions.find((d) => d.type === type);
  }

  // Toggle expansion of a check
  function toggleExpanded(type: string) {
    if (expandedChecks.has(type)) {
      expandedChecks.delete(type);
    } else {
      expandedChecks.add(type);
    }
    expandedChecks = new Set(expandedChecks);
  }

  // Format check type for display
  function formatCheckType(type: string): string {
    return type.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
  }

  // Get status styles
  function getStatusStyles(
    status: PreCheckResult["status"],
    blocking: boolean,
  ) {
    const styles = {
      passed: {
        icon: "✓",
        iconClass: "text-green-500",
        bgClass: "bg-green-50 dark:bg-green-900/20",
        borderClass: "border-green-200 dark:border-green-800",
        textClass: "text-green-700 dark:text-green-300",
      },
      failed: {
        icon: "✗",
        iconClass: blocking ? "text-red-600" : "text-red-500",
        bgClass: blocking
          ? "bg-red-100 dark:bg-red-900/30"
          : "bg-red-50 dark:bg-red-900/20",
        borderClass: blocking
          ? "border-red-400 dark:border-red-600"
          : "border-red-200 dark:border-red-800",
        textClass: "text-red-700 dark:text-red-300",
      },
      warning: {
        icon: "⚠",
        iconClass: "text-yellow-500",
        bgClass: "bg-yellow-50 dark:bg-yellow-900/20",
        borderClass: "border-yellow-200 dark:border-yellow-800",
        textClass: "text-yellow-700 dark:text-yellow-300",
      },
      skipped: {
        icon: "○",
        iconClass: "text-gray-400",
        bgClass: "bg-gray-50 dark:bg-gray-800/50",
        borderClass: "border-gray-200 dark:border-gray-700",
        textClass: "text-gray-600 dark:text-gray-400",
      },
    };
    return styles[status];
  }

  // Format details object for display
  function formatDetails(details: Record<string, unknown>): string[] {
    return Object.entries(details).map(([key, value]) => {
      const formattedKey = key
        .replace(/_/g, " ")
        .replace(/\b\w/g, (c) => c.toUpperCase());
      const formattedValue =
        typeof value === "object" ? JSON.stringify(value) : String(value);
      return `${formattedKey}: ${formattedValue}`;
    });
  }

  // Check if all blocking checks passed
  let canProceed = $derived(() => summary().blockingFailed === 0);
</script>

<div class="pre-check-results space-y-4">
  {#if loading}
    <!-- Loading State -->
    <div class="flex flex-col items-center justify-center py-8">
      <div
        class="animate-spin rounded-full h-10 w-10 border-b-2 border-primary"
      ></div>
      <p class="mt-3 text-sm text-gray-400">Running pre-checks...</p>
    </div>
  {:else if results.length === 0}
    <!-- Empty State -->
    <div class="text-center py-8 text-gray-400">
      <svg
        class="w-12 h-12 mx-auto mb-3 opacity-50"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2"
        />
      </svg>
      <p>No pre-check results available</p>
    </div>
  {:else}
    <!-- Summary Header -->
    <div
      class="flex flex-wrap items-center gap-3 p-4 rounded-lg bg-gray-800/50 border border-gray-700"
    >
      <span class="text-sm font-medium text-gray-300">Summary:</span>

      {#if summary().passed > 0}
        <span
          class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium bg-green-900/30 text-green-400 border border-green-700/50"
        >
          <span class="text-green-500">✓</span>
          {summary().passed} Passed
        </span>
      {/if}

      {#if summary().failed > 0}
        <span
          class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium bg-red-900/30 text-red-400 border border-red-700/50"
        >
          <span class="text-red-500">✗</span>
          {summary().failed} Failed
          {#if summary().blockingFailed > 0}
            <span class="text-red-300"
              >({summary().blockingFailed} blocking)</span
            >
          {/if}
        </span>
      {/if}

      {#if summary().warning > 0}
        <span
          class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium bg-yellow-900/30 text-yellow-400 border border-yellow-700/50"
        >
          <span class="text-yellow-500">⚠</span>
          {summary().warning} Warnings
        </span>
      {/if}

      {#if summary().skipped > 0}
        <span
          class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium bg-gray-700/50 text-gray-400 border border-gray-600"
        >
          <span>○</span>
          {summary().skipped} Skipped
        </span>
      {/if}

      <!-- Proceed indicator -->
      <div class="ml-auto">
        {#if canProceed()}
          <span class="inline-flex items-center gap-1.5 text-xs text-green-400">
            <svg class="w-4 h-4" fill="currentColor" viewBox="0 0 20 20">
              <path
                fill-rule="evenodd"
                d="M10 18a8 8 0 100-16 8 8 0 000 16zm3.707-9.293a1 1 0 00-1.414-1.414L9 10.586 7.707 9.293a1 1 0 00-1.414 1.414l2 2a1 1 0 001.414 0l4-4z"
                clip-rule="evenodd"
              />
            </svg>
            Ready to proceed
          </span>
        {:else}
          <span class="inline-flex items-center gap-1.5 text-xs text-red-400">
            <svg class="w-4 h-4" fill="currentColor" viewBox="0 0 20 20">
              <path
                fill-rule="evenodd"
                d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z"
                clip-rule="evenodd"
              />
            </svg>
            Fix blocking errors
          </span>
        {/if}
      </div>
    </div>

    <!-- Results List -->
    <div class="space-y-2">
      {#each results as result (result.type)}
        {@const blocking = isBlocking(result.type)}
        {@const definition = getDefinition(result.type)}
        {@const styles = getStatusStyles(result.status, blocking)}
        {@const hasDetails =
          result.details && Object.keys(result.details).length > 0}
        {@const hasError = !!result.error}
        {@const isExpanded = expandedChecks.has(result.type)}
        {@const canExpand = !compact && (hasDetails || hasError)}

        <div
          class="rounded-lg border transition-all {styles.bgClass} {styles.borderClass} {blocking &&
          result.status === 'failed'
            ? 'ring-2 ring-red-500/50'
            : ''}"
        >
          <!-- Check Header -->
          <button
            type="button"
            onclick={() => canExpand && toggleExpanded(result.type)}
            class="w-full flex items-center gap-3 px-4 py-3 text-left {canExpand
              ? 'cursor-pointer hover:bg-white/5'
              : 'cursor-default'}"
            disabled={!canExpand}
          >
            <!-- Status Icon -->
            <span class="text-lg {styles.iconClass}">{styles.icon}</span>

            <!-- Check Info -->
            <div class="flex-1 min-w-0">
              <div class="flex items-center gap-2">
                <span class="font-medium text-gray-100">
                  {formatCheckType(result.type)}
                </span>
                {#if blocking}
                  <span
                    class="text-xs px-1.5 py-0.5 rounded bg-red-900/50 text-red-300 border border-red-700/50"
                  >
                    Blocking
                  </span>
                {/if}
                {#if definition?.minVersion}
                  <span class="text-xs text-gray-500">
                    (min: {definition.minVersion})
                  </span>
                {/if}
              </div>
              {#if definition?.description}
                <p class="text-sm text-gray-400 truncate">
                  {definition.description}
                </p>
              {/if}
            </div>

            <!-- Status Badge -->
            <span
              class="text-xs px-2 py-1 rounded-full {styles.bgClass} {styles.textClass} border {styles.borderClass}"
            >
              {#if result.status === "passed"}
                Passed
              {:else if result.status === "failed"}
                Failed
              {:else if result.status === "warning"}
                Warning
              {:else}
                Skipped
              {/if}
            </span>

            <!-- Expand Icon -->
            {#if canExpand}
              <svg
                class="w-5 h-5 text-gray-400 transition-transform {isExpanded
                  ? 'rotate-180'
                  : ''}"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M19 9l-7 7-7-7"
                />
              </svg>
            {/if}
          </button>

          <!-- Expanded Details -->
          {#if isExpanded && canExpand}
            <div
              transition:slide={{ duration: 200 }}
              class="px-4 pb-4 pt-1 border-t border-gray-700/50"
            >
              {#if hasError}
                <div
                  class="mb-3 p-3 rounded bg-red-900/30 border border-red-700/50"
                >
                  <p class="text-sm font-medium text-red-300 mb-1">Error:</p>
                  <p class="text-sm text-red-200 font-mono">{result.error}</p>
                </div>
              {/if}

              {#if hasDetails}
                <div class="space-y-1">
                  <p
                    class="text-xs font-medium text-gray-400 uppercase tracking-wide mb-2"
                  >
                    Details
                  </p>
                  <div class="grid gap-1">
                    {#each formatDetails(result.details!) as detail}
                      <div
                        class="text-sm text-gray-300 font-mono bg-gray-800/50 px-2 py-1 rounded"
                      >
                        {detail}
                      </div>
                    {/each}
                  </div>
                </div>
              {/if}
            </div>
          {/if}
        </div>
      {/each}
    </div>
  {/if}
</div>
