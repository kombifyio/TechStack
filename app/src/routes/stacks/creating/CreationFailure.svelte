<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
</script>

        <div
          class="rounded-2xl border border-destructive/30 bg-destructive/5 p-6"
        >
          <div class="mb-6">
            <div class="flex items-center gap-3 mb-2">
              <div
                class="w-12 h-12 rounded-full bg-destructive/20 flex items-center justify-center"
              >
                <svg
                  class="w-6 h-6 text-destructive"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M6 18L18 6M6 6l12 12"
                  />
                </svg>
              </div>
              <div>
                <h2 class="text-xl font-semibold text-foreground">
                  {creation.handoffMissingFailure
                    ? "Rollout incomplete"
                    : "Creation failed"}
                </h2>
                <p class="text-muted-foreground text-sm">
                  {#if creation.failedTask?.errorMessage}
                    {creation.failedTask.errorMessage}
                  {:else}
                    An error occurred
                  {/if}
                </p>
              </div>
            </div>
          </div>

          {#if creation.postLeaseManagedCloudFailure && creation.lease}
            <div
              class="bg-primary/10 border border-primary/25 rounded-xl p-4 mb-6"
              data-testid="managed-lease-failure-summary"
            >
              <p class="text-sm font-medium text-foreground mb-2">
                Existing managed VM lease referenced
              </p>
              <p class="text-sm text-muted-foreground">
                Lease {creation.lease.id}{creation.lease.provider
                  ? ` · ${creation.lease.provider}`
                  : ""}{creation.lease.host || creation.lease.publicIp
                  ? ` · ${creation.lease.host || creation.lease.publicIp}`
                  : ""}
              </p>
              <p class="text-sm text-muted-foreground mt-2">
                {#if creation.stackKitArtifactOrRoutingFailure}
                  VPS provisioning completed. Only StackKit artifact, domain
                  routing, or later rollout work failed.
                {:else}
                  The rollout reached the existing managed VM.
                {/if}
                The exact retry endpoint validates this failed job and lease before
                continuing on the existing server.
              </p>
            </div>
          {/if}

          {#if creation.connectRemoteStackKitFailed}
            <div
              class="bg-primary/10 border border-primary/25 rounded-xl p-4 mb-6"
              data-testid="connect-remote-stackkit-failure-summary"
            >
              <p class="text-sm font-medium text-foreground mb-2">
                Node connection succeeded
              </p>
              <p class="text-sm text-muted-foreground">
                SSH enrollment completed and the Node is visible in your
                homelab. Only StackKit preparation or rollout failed. Retry
                continues on the connected Node without repeating SSH enrollment.
              </p>
            </div>
          {/if}

          {#if creation.wizardIdempotencyConflictFailed}
            <div
              class="bg-primary/10 border border-primary/25 rounded-xl p-4 mb-6"
              data-testid="wizard-idempotency-conflict-summary"
            >
              <p class="text-sm font-medium text-foreground mb-2">
                Earlier registration attempt already completed
              </p>
              <p class="text-sm text-muted-foreground">
                This browser reused an attempt key from a different submission.
                The earlier run may already have created a Node registration.
                Resume that attempt, or start fresh with a new key.
              </p>
              <button
                type="button"
                data-kx="control"
                class="mt-4 inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium"
                onclick={creation.resumePreviousWizardAttempt}
              >
                Resume previous attempt
              </button>
            </div>
          {/if}

          {#if creation.retryError}
            <div class="bg-destructive/10 rounded-xl p-3 mb-6">
              <p class="text-sm text-destructive">{creation.retryError}</p>
            </div>
          {/if}

          <!-- Detailed error info -->
          {#if creation.failedTask?.errorDetails}
            <div
              class="bg-destructive/10 border border-destructive/30 rounded-xl p-4 mb-6"
            >
              <div class="flex items-center justify-between gap-3">
                <p class="text-destructive text-sm font-medium">
                  Error details
                </p>
                <button
                  type="button"
                  data-kx="control"
                  class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                  data-testid="copy-error-details-button"
                  onclick={creation.copyErrorDetails}
                >
                  {creation.copiedErrorDetails ? "Copied" : "Copy error details"}
                </button>
              </div>
              <pre
                data-testid="error-details-text"
                class="mt-3 max-h-48 overflow-x-auto overflow-y-auto whitespace-pre-wrap rounded border border-destructive/20 bg-background p-3 text-xs text-foreground">{creation.failedTask.errorDetails}</pre>
            </div>
          {/if}

          <!-- Intelligent troubleshooting from failed task -->
          {#if creation.failedTask?.troubleshooting && creation.failedTask.troubleshooting.length > 0}
            <div
              class="rounded-xl border border-warning/30 bg-warning/10 p-4 mb-6"
              data-testid="creation-troubleshooting"
            >
              <p
                class="text-foreground font-medium text-sm mb-3 flex items-center gap-2"
              >
                <svg
                  class="w-5 h-5 text-warning"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"
                  />
                </svg>
                Troubleshooting
              </p>
              <ol
                class="text-sm text-foreground space-y-2 list-decimal list-inside"
              >
                {#each creation.failedTask.troubleshooting as step (step)}
                  <li class="leading-relaxed text-foreground/90">{step}</li>
                {/each}
              </ol>
            </div>
          {:else}
            <!-- Fallback suggestions if no specific troubleshooting available -->
            <div class="bg-muted/50 rounded-xl p-4 mb-6">
              <p class="text-sm text-muted-foreground mb-3">
                Possible solutions:
              </p>
              <ul class="text-sm text-foreground/80 space-y-2">
                <li class="flex items-start gap-2">
                  <span class="text-primary">•</span>
                  <span>Check your network connection</span>
                </li>
                <li class="flex items-start gap-2">
                  <span class="text-primary">•</span>
                  <span>Make sure the backend server is running</span>
                </li>
                <li class="flex items-start gap-2">
                  <span class="text-primary">•</span>
                  <span>Review your configuration settings</span>
                </li>
              </ul>
            </div>
          {/if}

          <div class="flex gap-3">
            {#if creation.failurePrimaryActionAvailable}
              <button
                onclick={creation.handleFailurePrimaryAction}
                disabled={creation.failurePrimaryActionDisabled}
                data-kx="control"
                data-variant="primary"
                class="inline-flex flex-1 items-center justify-center gap-2 whitespace-nowrap rounded-xl px-6 py-3 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
              >
                {creation.failurePrimaryActionLabel}
              </button>
            {/if}
            <button
              onclick={creation.goToStack}
              data-kx="control"
              class="inline-flex flex-1 items-center justify-center gap-2 whitespace-nowrap rounded-xl px-6 py-3 text-sm font-medium"
            >
              Operations
            </button>
          </div>
        </div>
