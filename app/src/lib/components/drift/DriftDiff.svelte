<script lang="ts">
  import type { ResourceChange } from "$lib/api/drift";

  interface Props {
    changes: ResourceChange[];
    expanded?: boolean;
  }

  let { changes, expanded = false }: Props = $props();

  let expandedItems = $state<Set<string>>(new Set());

  function toggleItem(address: string) {
    if (expandedItems.has(address)) {
      expandedItems.delete(address);
    } else {
      expandedItems.add(address);
    }
    expandedItems = new Set(expandedItems);
  }

  const changeTypeColors = {
    create: "text-green-400 bg-green-900/30",
    update: "text-yellow-400 bg-yellow-900/30",
    delete: "text-red-400 bg-red-900/30",
    "no-op": "text-gray-400 bg-gray-900/30",
  };

  const changeTypeIcons = {
    create: "+",
    update: "~",
    delete: "-",
    "no-op": "=",
  };

  function formatValue(value: unknown): string {
    if (value === null || value === undefined) return "null";
    if (typeof value === "object") {
      return JSON.stringify(value, null, 2);
    }
    return String(value);
  }

  function getChangedKeys(
    before?: Record<string, unknown>,
    after?: Record<string, unknown>
  ): string[] {
    const allKeys = new Set([
      ...Object.keys(before || {}),
      ...Object.keys(after || {}),
    ]);
    return Array.from(allKeys).filter((key) => {
      const beforeVal = before?.[key];
      const afterVal = after?.[key];
      return JSON.stringify(beforeVal) !== JSON.stringify(afterVal);
    });
  }
</script>

<div class="space-y-2">
  {#each changes as change (change.address)}
    {@const isExpanded = expandedItems.has(change.address)}
    {@const changedKeys = getChangedKeys(change.before, change.after)}

    <div class="border border-gray-700 rounded-lg overflow-hidden">
      <!-- Header -->
      <button
        class="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-gray-800/50 transition-colors"
        onclick={() => toggleItem(change.address)}
      >
        <!-- Change type icon -->
        <span
          class="w-6 h-6 flex items-center justify-center rounded font-mono text-sm {changeTypeColors[
            change.change_type
          ]}"
        >
          {changeTypeIcons[change.change_type]}
        </span>

        <!-- Resource info -->
        <div class="flex-1 min-w-0">
          <div class="font-mono text-sm text-white truncate">
            {change.address}
          </div>
          <div class="text-xs text-gray-400">
            {change.resource_type}
            {#if changedKeys.length > 0}
              · {changedKeys.length} field{changedKeys.length !== 1 ? "s" : ""} changed
            {/if}
          </div>
        </div>

        <!-- Expand arrow -->
        <svg
          class="w-5 h-5 text-gray-400 transition-transform {isExpanded
            ? 'rotate-90'
            : ''}"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M9 5l7 7-7 7"
          />
        </svg>
      </button>

      <!-- Expanded details -->
      {#if isExpanded}
        <div class="border-t border-gray-700 bg-gray-900/50">
          {#if change.change_type === "create"}
            <!-- Show planned values for creation -->
            <div class="p-4">
              <div class="text-xs text-gray-400 mb-2">
                Planned values (will be created):
              </div>
              <pre
                class="text-xs text-green-400 bg-gray-900 p-3 rounded overflow-x-auto">{formatValue(
                  change.after
                )}</pre>
            </div>
          {:else if change.change_type === "delete"}
            <!-- Show current values for deletion -->
            <div class="p-4">
              <div class="text-xs text-gray-400 mb-2">
                Current values (will be deleted):
              </div>
              <pre
                class="text-xs text-red-400 bg-gray-900 p-3 rounded overflow-x-auto">{formatValue(
                  change.before
                )}</pre>
            </div>
          {:else if change.change_type === "update"}
            <!-- Show diff for updates -->
            <div class="p-4 space-y-3">
              {#each changedKeys as key (key)}
                <div class="border border-gray-700 rounded overflow-hidden">
                  <div
                    class="px-3 py-2 bg-gray-800 text-xs font-mono text-gray-300"
                  >
                    {key}
                  </div>
                  <div class="grid grid-cols-2 divide-x divide-gray-700">
                    <!-- Before -->
                    <div class="p-3">
                      <div class="text-xs text-gray-500 mb-1">Before</div>
                      <pre
                        class="text-xs text-red-400 whitespace-pre-wrap break-all">{formatValue(
                          change.before?.[key]
                        )}</pre>
                    </div>
                    <!-- After -->
                    <div class="p-3">
                      <div class="text-xs text-gray-500 mb-1">After</div>
                      <pre
                        class="text-xs text-green-400 whitespace-pre-wrap break-all">{formatValue(
                          change.after?.[key]
                        )}</pre>
                    </div>
                  </div>
                </div>
              {/each}
            </div>
          {:else}
            <!-- No-op -->
            <div class="p-4 text-sm text-gray-400">
              No changes detected for this resource.
            </div>
          {/if}
        </div>
      {/if}
    </div>
  {/each}

  {#if changes.length === 0}
    <div class="text-center py-8 text-gray-400">
      <svg
        class="w-12 h-12 mx-auto mb-3 opacity-50"
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
      <p>No resource changes detected</p>
    </div>
  {/if}
</div>
