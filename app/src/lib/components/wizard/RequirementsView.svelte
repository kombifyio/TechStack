<!--
  RequirementsView Component
  
  Displays the calculated requirements from the Unifier analyze endpoint.
  Shows what the user needs before deployment can proceed:
  - StackKit selection
  - Worker/node requirements
  - Required credentials
  - Pre-checks that must pass
  
  Props:
  - requirements: RequirementsSpec from the API
  - loading: boolean for loading state
  - onconfirm: callback when user confirms requirements
-->
<script lang="ts">
  import type { RequirementsSpec } from "$lib/api/unifier";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    requirements: RequirementsSpec | null;
    loading?: boolean;
    error?: string | null;
    onconfirm?: () => void;
    onback?: () => void;
  }

  let {
    requirements,
    loading = false,
    error = null,
    onconfirm,
    onback,
  }: Props = $props();

  // Helper to format RAM in GB
  function formatRAM(mb: number): string {
    const gb = mb / 1024;
    return gb >= 1 ? `${gb.toFixed(1)} GB` : `${mb} MB`;
  }

  // Badge color based on blocking status
  function checkBadgeClass(blocking: boolean): string {
    return blocking
      ? "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200"
      : "bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200";
  }
</script>

<div class="requirements-view space-y-6">
  {#if loading}
    <!-- Loading State -->
    <div class="flex flex-col items-center justify-center py-12">
      <div
        class="animate-spin rounded-full h-12 w-12 border-b-2 border-primary-500"
      ></div>
      <p class="mt-4 text-secondary-500">Analyzing requirements...</p>
    </div>
  {:else if error}
    <!-- Error State -->
    <div
      class="bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-lg p-4"
    >
      <div class="flex items-start">
        <svg
          class="w-5 h-5 text-red-500 mt-0.5 mr-3 flex-shrink-0"
          fill="currentColor"
          viewBox="0 0 20 20"
        >
          <path
            fill-rule="evenodd"
            d="M10 18a8 8 0 100-16 8 8 0 000 16zM8.707 7.293a1 1 0 00-1.414 1.414L8.586 10l-1.293 1.293a1 1 0 101.414 1.414L10 11.414l1.293 1.293a1 1 0 001.414-1.414L11.414 10l1.293-1.293a1 1 0 00-1.414-1.414L10 8.586 8.707 7.293z"
            clip-rule="evenodd"
          />
        </svg>
        <div>
          <h3 class="font-medium text-red-800 dark:text-red-200">
            Analysis Failed
          </h3>
          <p class="mt-1 text-sm text-red-700 dark:text-red-300">{error}</p>
        </div>
      </div>
    </div>
  {:else if requirements}
    <!-- Requirements Display -->

    <!-- Summary Description -->
    <div
      class="bg-primary-50 dark:bg-primary-900/20 border border-primary-200 dark:border-primary-800 rounded-lg p-4"
    >
      <h3 class="font-semibold text-primary-800 dark:text-primary-200 mb-2">
        Summary
      </h3>
      <p class="text-secondary-700 dark:text-secondary-300">
        {requirements.description}
      </p>
    </div>

    <!-- StackKit Selection -->
    <div
      class="bg-white dark:bg-secondary-800 border border-secondary-200 dark:border-secondary-700 rounded-lg p-4"
    >
      <div class="flex items-center justify-between">
        <div>
          <h4 class="font-medium text-secondary-900 dark:text-secondary-100">
            StackKit
          </h4>
          <p class="text-sm text-secondary-500">{requirements.stackKit}</p>
        </div>
        <span
          class="inline-flex items-center px-3 py-1 rounded-full text-sm font-medium bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200"
        >
          Selected
        </span>
      </div>
    </div>

    <!-- Worker Requirements -->
    <div
      class="bg-white dark:bg-secondary-800 border border-secondary-200 dark:border-secondary-700 rounded-lg p-4"
    >
      <h4 class="font-medium text-secondary-900 dark:text-secondary-100 mb-3">
        Hardware Requirements
      </h4>

      <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
        <div
          class="text-center p-3 bg-secondary-50 dark:bg-secondary-700/50 rounded-lg"
        >
          <div
            class="text-2xl font-bold text-primary-600 dark:text-primary-400"
          >
            {requirements.requiredWorkers.minLocalServers}
          </div>
          <div class="text-sm text-secondary-500">Local Servers</div>
        </div>

        <div
          class="text-center p-3 bg-secondary-50 dark:bg-secondary-700/50 rounded-lg"
        >
          <div
            class="text-2xl font-bold text-primary-600 dark:text-primary-400"
          >
            {requirements.requiredWorkers.minCloudServers}
          </div>
          <div class="text-sm text-secondary-500">Cloud Servers</div>
        </div>

        <div
          class="text-center p-3 bg-secondary-50 dark:bg-secondary-700/50 rounded-lg"
        >
          <div
            class="text-2xl font-bold text-primary-600 dark:text-primary-400"
          >
            {formatRAM(requirements.requiredWorkers.minRAM)}
          </div>
          <div class="text-sm text-secondary-500">Min. RAM</div>
        </div>

        <div
          class="text-center p-3 bg-secondary-50 dark:bg-secondary-700/50 rounded-lg"
        >
          <div
            class="text-2xl font-bold text-primary-600 dark:text-primary-400"
          >
            {requirements.requiredWorkers.minCPU}
          </div>
          <div class="text-sm text-secondary-500">CPU Cores</div>
        </div>
      </div>

      {#if requirements.requiredWorkers.specialRequirements && requirements.requiredWorkers.specialRequirements.length > 0}
        <div class="mt-4 p-3 bg-yellow-50 dark:bg-yellow-900/20 rounded-lg">
          <h5
            class="text-sm font-medium text-yellow-800 dark:text-yellow-200 mb-1"
          >
            Special Requirements:
          </h5>
          <ul
            class="list-disc list-inside text-sm text-yellow-700 dark:text-yellow-300"
          >
            {#each requirements.requiredWorkers.specialRequirements as req}
              <li>{req}</li>
            {/each}
          </ul>
        </div>
      {/if}
    </div>

    <!-- Detected Add-ons -->
    {#if requirements.detectedAddons && requirements.detectedAddons.length > 0}
      <div
        class="bg-white dark:bg-secondary-800 border border-secondary-200 dark:border-secondary-700 rounded-lg p-4"
      >
        <h4 class="font-medium text-secondary-900 dark:text-secondary-100 mb-3">
          Detected Add-ons
        </h4>
        <div class="flex flex-wrap gap-2">
          {#each requirements.detectedAddons as addon}
            <span
              class="inline-flex items-center px-3 py-1 rounded-full text-sm font-medium bg-secondary-100 text-secondary-800 dark:bg-secondary-700 dark:text-secondary-200"
            >
              {addon}
            </span>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Required Credentials -->
    {#if requirements.requiredCredentials && requirements.requiredCredentials.length > 0}
      <div
        class="bg-white dark:bg-secondary-800 border border-secondary-200 dark:border-secondary-700 rounded-lg p-4"
      >
        <h4 class="font-medium text-secondary-900 dark:text-secondary-100 mb-3">
          Required Credentials
        </h4>
        <div class="space-y-3">
          {#each requirements.requiredCredentials as cred}
            <div
              class="flex items-start p-3 bg-secondary-50 dark:bg-secondary-700/50 rounded-lg"
            >
              <div class="flex-1">
                <div class="flex items-center gap-2">
                  <span
                    class="font-medium text-secondary-900 dark:text-secondary-100"
                    >{cred.label}</span
                  >
                  {#if cred.required}
                    <span
                      class="text-xs px-2 py-0.5 rounded bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300"
                      >Required</span
                    >
                  {:else}
                    <span
                      class="text-xs px-2 py-0.5 rounded bg-secondary-200 text-secondary-600 dark:bg-secondary-600 dark:text-secondary-300"
                      >Optional</span
                    >
                  {/if}
                </div>
                <p class="text-sm text-secondary-500 mt-1">
                  {cred.description}
                </p>
                {#if cred.helpUrl}
                  <a
                    href={cred.helpUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    class="text-sm text-primary-600 hover:text-primary-700 dark:text-primary-400 mt-1 inline-flex items-center"
                  >
                    Guide →
                  </a>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Pre-Checks -->
    {#if requirements.requiredPreChecks && requirements.requiredPreChecks.length > 0}
      <div
        class="bg-white dark:bg-secondary-800 border border-secondary-200 dark:border-secondary-700 rounded-lg p-4"
      >
        <h4 class="font-medium text-secondary-900 dark:text-secondary-100 mb-3">
          ✅ Pre-Flight Checks
        </h4>
        <div class="space-y-2">
          {#each requirements.requiredPreChecks as check}
            <div
              class="flex items-center justify-between p-2 bg-secondary-50 dark:bg-secondary-700/50 rounded"
            >
              <div class="flex items-center gap-2">
                <span
                  class={`text-xs px-2 py-0.5 rounded ${checkBadgeClass(check.blocking)}`}
                >
                  {check.blocking ? "Blocking" : "Warning"}
                </span>
                <span class="text-secondary-700 dark:text-secondary-300"
                  >{check.description}</span
                >
              </div>
              {#if check.minVersion}
                <span class="text-xs text-secondary-500"
                  >v{check.minVersion}+</span
                >
              {/if}
            </div>
          {/each}
        </div>
      </div>
    {/if}

    <!-- Applied Defaults (collapsible) -->
    {#if requirements.appliedDefaults && Object.keys(requirements.appliedDefaults).length > 0}
      <details
        class="bg-secondary-50 dark:bg-secondary-800/50 border border-secondary-200 dark:border-secondary-700 rounded-lg"
      >
        <summary
          class="p-4 cursor-pointer font-medium text-secondary-700 dark:text-secondary-300 hover:bg-secondary-100 dark:hover:bg-secondary-700/50 rounded-lg"
        >
          Applied Defaults ({Object.keys(requirements.appliedDefaults).length})
        </summary>
        <div class="px-4 pb-4">
          <pre
            class="text-xs bg-secondary-100 dark:bg-secondary-900 p-3 rounded overflow-x-auto">{JSON.stringify(
              requirements.appliedDefaults,
              null,
              2,
            )}</pre>
        </div>
      </details>
    {/if}

    <!-- Action Buttons -->
    <div
      class="flex justify-between pt-4 border-t border-secondary-200 dark:border-secondary-700"
    >
      {#if onback}
        <button
          type="button"
          onclick={onback}
          class="px-4 py-2 text-secondary-600 hover:text-secondary-800 dark:text-secondary-400 dark:hover:text-secondary-200 font-medium"
        >
          Back
        </button>
      {:else}
        <div></div>
      {/if}

      {#if onconfirm}
        <button
          type="button"
          onclick={onconfirm}
          class="px-6 py-2 bg-primary-600 hover:bg-primary-700 text-white font-medium rounded-lg transition-colors"
        >
          Confirm Requirements →
        </button>
      {/if}
    </div>
  {:else}
    <!-- No Requirements Yet -->
    <div class="text-center py-8 text-secondary-500">
      <p>No requirements available. Please fill in the configuration first.</p>
    </div>
  {/if}
</div>

<style>
  .requirements-view {
    max-width: 800px;
    margin: 0 auto;
  }
</style>
