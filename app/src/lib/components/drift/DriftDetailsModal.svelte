<script lang="ts">
  import Modal from "../Modal.svelte";
  import DriftBadge from "./DriftBadge.svelte";
  import DriftDiff from "./DriftDiff.svelte";
  import type { DriftResult } from "$lib/api/drift";
  import { formatDuration, triggerDriftResolve } from "$lib/api/drift";

  interface Props {
    result: DriftResult;
    onClose: () => void;
    onResolve?: (jobId: string) => void;
  }

  let { result, onClose, onResolve }: Props = $props();

  let resolving = $state(false);
  let error = $state<string | null>(null);

  async function handleResolve() {
    if (!result || result.status !== "drifted") return;

    resolving = true;
    error = null;

    try {
      const response = await triggerDriftResolve(result.stack_id);
      onResolve?.(response.job_id);
      onClose();
    } catch (err) {
      error = err instanceof Error ? err.message : "Failed to resolve drift";
    } finally {
      resolving = false;
    }
  }

  const formattedDate = $derived(
    result?.checked_at
      ? new Date(result.checked_at).toLocaleString()
      : "Unknown"
  );

  const summary = $derived(result?.plan_summary);
  const changes = $derived(result?.affected_resources ?? []);
  const hasDrift = $derived(result?.status === "drifted" && changes.length > 0);
</script>

<Modal title="Drift Detection Details" {onClose} maxWidth="2xl">
  {#snippet children()}
    <div class="p-6 space-y-6">
      <!-- Stack Info Header -->
      <div class="flex items-start justify-between">
        <div>
          <h3 class="text-lg font-semibold text-white">
            {result.stack_name || result.stack_id}
          </h3>
          <p class="text-sm text-gray-400 mt-1">
            Checked: {formattedDate}
            {#if result.duration_ms}
              · Duration: {formatDuration(result.duration_ms)}
            {/if}
          </p>
        </div>
        <DriftBadge status={result.status} size="lg" />
      </div>

      <!-- Error Message (if any) -->
      {#if result.error_message}
        <div
          class="bg-red-900/20 border border-red-700 rounded-lg p-4 text-red-300"
        >
          <div class="font-medium mb-1">Error</div>
          <div class="text-sm">{result.error_message}</div>
          {#if result.error_details}
            <pre
              class="mt-2 text-xs text-red-400 bg-red-950/50 p-2 rounded overflow-x-auto">{result.error_details}</pre>
          {/if}
        </div>
      {/if}

      <!-- Plan Summary -->
      {#if summary}
        <div class="grid grid-cols-4 gap-4">
          <div
            class="bg-gray-800 rounded-lg p-4 text-center border border-gray-700"
          >
            <div
              class="text-2xl font-bold {summary.to_create > 0
                ? 'text-green-400'
                : 'text-gray-500'}"
            >
              {summary.to_create}
            </div>
            <div class="text-xs text-gray-400 mt-1">To Create</div>
          </div>
          <div
            class="bg-gray-800 rounded-lg p-4 text-center border border-gray-700"
          >
            <div
              class="text-2xl font-bold {summary.to_update > 0
                ? 'text-yellow-400'
                : 'text-gray-500'}"
            >
              {summary.to_update}
            </div>
            <div class="text-xs text-gray-400 mt-1">To Update</div>
          </div>
          <div
            class="bg-gray-800 rounded-lg p-4 text-center border border-gray-700"
          >
            <div
              class="text-2xl font-bold {summary.to_delete > 0
                ? 'text-red-400'
                : 'text-gray-500'}"
            >
              {summary.to_delete}
            </div>
            <div class="text-xs text-gray-400 mt-1">To Delete</div>
          </div>
          <div
            class="bg-gray-800 rounded-lg p-4 text-center border border-gray-700"
          >
            <div class="text-2xl font-bold text-gray-500">
              {summary.unchanged}
            </div>
            <div class="text-xs text-gray-400 mt-1">Unchanged</div>
          </div>
        </div>
      {/if}

      <!-- Resource Changes -->
      {#if changes.length > 0}
        <div>
          <h4 class="text-sm font-medium text-gray-300 mb-3">
            Affected Resources ({changes.length})
          </h4>
          <div class="max-h-96 overflow-y-auto">
            <DriftDiff {changes} />
          </div>
        </div>
      {:else if result.status === "in_sync"}
        <div
          class="text-center py-8 text-gray-400 bg-gray-800/50 rounded-lg border border-gray-700"
        >
          <svg
            class="w-16 h-16 mx-auto mb-4 text-green-500/50"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"
            />
          </svg>
          <p class="text-lg font-medium text-green-400">
            Infrastructure is in sync
          </p>
          <p class="text-sm mt-1">
            No drift detected between desired and actual state.
          </p>
        </div>
      {/if}

      <!-- Error from resolve attempt -->
      {#if error}
        <div
          class="bg-red-900/20 border border-red-700 rounded-lg p-4 text-red-300 text-sm"
        >
          {error}
        </div>
      {/if}
    </div>

    <!-- Footer Actions -->
    <div
      class="flex items-center justify-end gap-3 p-4 border-t border-gray-700 bg-gray-900/50"
    >
      <button
        onclick={onClose}
        class="px-4 py-2 text-sm font-medium text-gray-300 hover:text-white bg-gray-800 hover:bg-gray-700 rounded-lg transition-colors"
      >
        Close
      </button>
      {#if hasDrift}
        <button
          onclick={handleResolve}
          disabled={resolving}
          class="px-4 py-2 text-sm font-medium text-white bg-yellow-600 hover:bg-yellow-500 disabled:opacity-50 disabled:cursor-not-allowed rounded-lg transition-colors flex items-center gap-2"
        >
          {#if resolving}
            <svg class="w-4 h-4 animate-spin" fill="none" viewBox="0 0 24 24">
              <circle
                class="opacity-25"
                cx="12"
                cy="12"
                r="10"
                stroke="currentColor"
                stroke-width="4"
              />
              <path
                class="opacity-75"
                fill="currentColor"
                d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
              />
            </svg>
            Resolving...
          {:else}
            <svg
              class="w-4 h-4"
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
              />
            </svg>
            Apply Changes
          {/if}
        </button>
      {/if}
    </div>
  {/snippet}
</Modal>
