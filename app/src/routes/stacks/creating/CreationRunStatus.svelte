<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { tr, trParts } from "#lib/i18n.svelte.js";
  import { STEP_DETAILS } from "#lib/wizard/index.js";
</script>

        <!-- Progressive step details during creation -->
        <div class="space-y-4">
          {#if creation.waitingForManagedRuntime}
            <section
              class="rounded-2xl border border-warning/30 bg-warning/5 p-6"
              data-testid="waiting-enrollment-card"
              role="status"
              aria-live="polite"
            >
              <div
                class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
              >
                <div>
                  <p
                    class="text-xs font-semibold uppercase tracking-wide text-warning"
                  >
                    {tr("ui.stacksCreatingCreationRunStatus.provisioningInProgress")}
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    {tr("ui.stacksCreatingCreationProgress.nodeProvisioningIsStillIn")}
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    {#if creation.waitingForProviderProvision}
                      {tr("ui.stacksCreatingCreationRunStatus.theProviderOperationHasNot")}
                    {:else}
                      {tr("ui.stacksCreatingCreationRunStatus.theManagedRuntimeIsNot")}
                    {/if}
                  </p>
                  {#if creation.nextResumeLabel}
                    <p
                      class="mt-3 text-xs text-muted-foreground"
                      data-testid="waiting-enrollment-next-resume"
                    >
                      {tr("ui.stacksCreatingCreationRunStatus.nextScheduledCheck")}
                      <time datetime={creation.jobNextResumeAt}>{creation.nextResumeLabel}</time>
                    </p>
                  {/if}
                  <p class="mt-3 max-w-2xl text-xs text-muted-foreground">
                    {#if creation.jobResumeAvailableAt}
                      {tr("ui.creationRun.resumeStartingAt", { time: creation.resumeAvailableLabel })}
                    {:else}
                      {tr("ui.stacksCreatingCreationRunStatus.aManualResumeIsEnabled")}
                    {/if}
                    {tr("ui.stacksCreatingCreationRunStatus.thisDoesNotCreateAnother")}
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="waiting-enrollment-status"
                >
                  <span class="h-2 w-2 animate-pulse rounded-full bg-warning"
                  ></span>
                  {tr("ui.creationRun.notReachableYet")}
                </span>
              </div>

              {#if creation.currentStepMessage}
                <div
                  class="mt-5 rounded-lg border border-warning/20 bg-warning/10 px-3 py-2"
                >
                  <p class="text-xs font-medium text-warning">{tr("ui.stacksCreatingCreationRunStatus.currentStatus")}</p>
                  <p class="mt-1 text-sm text-foreground">
                    {creation.currentStepMessage}
                  </p>
                </div>
              {/if}

              <div
                class="mt-5 grid gap-2 sm:grid-cols-3"
                data-testid="waiting-enrollment-access"
              >
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  data-kx="control"
                  class="inline-flex cursor-not-allowed items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium opacity-50"
                  data-testid="waiting-dashboard-disabled"
                >
                  {tr("ui.settings.dashboard")}
                </button>
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  data-kx="control"
                  class="inline-flex cursor-not-allowed items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium opacity-50"
                  data-testid="waiting-services-disabled"
                >
                  {tr("ui.services.services")}
                </button>
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  data-kx="control"
                  class="inline-flex cursor-not-allowed items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium opacity-50"
                  data-testid="waiting-access-disabled"
                >
                  {tr("ui.stacksCreatingCreationRunStatus.nodeAccess")}
                </button>
              </div>
              <p class="mt-3 text-xs text-muted-foreground">
                {tr("ui.stacksCreatingCreationRunStatus.theseAccessPathsAreEnabled")}
              </p>
              {#if creation.waitingRecoveryAvailable}
                <div
                  class="mt-4 rounded-lg border border-info/30 bg-info/10 p-3"
                  data-testid="waiting-enrollment-recovery"
                >
                  <p class="text-xs text-muted-foreground">
                    {tr("ui.stacksCreatingCreationRunStatus.theScheduledCheckIsOverdue")}
                  </p>
                  <button
                    type="button"
                    data-kx="control"
                    data-variant="primary"
                    class="mt-3 inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
                    disabled={creation.retryingRollout}
                    onclick={creation.resumeOverdueManagedRuntime}
                    data-testid="waiting-enrollment-retry"
                  >
                    {creation.retryingRollout
                      ? tr("ui.stacksCreatingCreationRunStatus.validatingTheExistingVm")
                      : creation.waitingForProviderProvision
                        ? tr("ui.stacksCreatingCreationRunStatus.continueRolloutOnTheExisting")
                        : tr("ui.stacksCreatingCreationRunStatus.resumeEnrollmentOnTheExisting")}
                  </button>
                  {#if creation.retryError}
                    <p
                      class="mt-2 text-xs text-destructive"
                      data-testid="waiting-enrollment-retry-error"
                    >
                      {creation.retryError}
                    </p>
                  {/if}
                </div>
              {/if}
            </section>
          {:else if creation.serverProvisioningMode === "connect-remote" && creation.agentPairingWaiting}
            <section
              data-kx="plate"
              class="p-6"
              data-testid="remote-ssh-enrollment-card"
            >
              <div
                class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
              >
                <div>
                  <p
                    class="text-xs font-semibold uppercase tracking-wide text-primary"
                  >
                    {tr("ui.stacksCreatingCreationRunStatus.connectMyNode")}
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    {#if creation.remoteEnrollmentActive}
                      {tr("ui.stacksCreatingCreationRunStatus.connectingViaSsh")}
                    {:else if creation.remoteEnrollmentFailed}
                      {tr("ui.stacksCreatingCreationRunStatus.remoteSshConnectionFailed")}
                    {:else}
                      {tr("ui.stacksCreatingCreationRunStatus.waitingForGuardHeartbeat")}
                    {/if}
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    {#if creation.remoteEnrollmentActive}
                      {tr("ui.stacksCreatingCreationRunStatus.kombifyIsInstallingAndEnrolling")}
                    {:else if creation.remoteEnrollmentFailed}
                      {creation.pairingRecoveryError ||
                        tr("ui.stacksCreatingCreationRunStatus.checkSshHostCredentialsAnd")}
                    {:else}
                      {tr("ui.stacksCreatingCreationRunStatus.enrollmentOverSshFinishedThis")}
                    {/if}
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="remote-ssh-enrollment-status"
                >
                  <span class="h-2 w-2 rounded-full bg-warning"></span>
                  {#if creation.remoteEnrollmentActive}
                    {tr("ui.stacksCreatingCreationRunStatus.enrolling")}
                  {:else if creation.remoteEnrollmentFailed}
                    {tr("ui.stacksCreatingCreationRunStatus.failed")}
                  {:else}
                    {tr("ui.stacksCreatingCreationRunStatus.awaitingHeartbeat")}
                  {/if}
                </span>
              </div>

              {#if creation.remoteServerHost}
                <div
                  class="mt-5 rounded-lg border border-border bg-muted/40 p-3"
                >
                  <p class="text-xs text-muted-foreground">{tr("ui.stacksCreatingCreationRunStatus.remoteTarget")}</p>
                  <p class="mt-1 font-mono text-sm text-foreground">
                    {creation.remoteServerUser
                      ? `${creation.remoteServerUser}@`
                      : ""}{creation.remoteServerHost}{creation.remoteServerPort
                      ? `:${creation.remoteServerPort}`
                      : ""}
                  </p>
                </div>
              {/if}

              {#if creation.remoteEnrollmentActive}
                <div class="mt-5">
                  <div
                    class="h-2 overflow-hidden rounded-full bg-muted"
                    aria-hidden="true"
                  >
                    <div
                      class="h-full rounded-full bg-primary transition-all duration-500"
                      style={`width: ${Math.max(8, creation.remoteEnrollmentProgress)}%`}
                    ></div>
                  </div>
                  <p
                    class="mt-3 text-sm text-muted-foreground"
                    data-testid="remote-ssh-enrollment-message"
                  >
                    {creation.remoteEnrollmentMessage ||
                      tr("ui.stacksCreatingCreationRunStatus.connectingToYourNodeOver")}
                  </p>
                </div>
              {:else if creation.remoteEnrollmentFailed}
                <div class="mt-5">
                  <button
                    type="button"
                    data-kx="control"
                    data-variant="primary"
                    class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
                    disabled={creation.retryingRollout}
                    onclick={creation.retryRemoteSSHEnrollment}
                    data-testid="remote-ssh-enrollment-retry"
                  >
                    {creation.retryingRollout
                      ? tr("ui.stacksCreatingCreationRunStatus.retryingSshConnection")
                      : tr("ui.stacksCreatingCreationRunStatus.retryConnectionOnSavedServer")}
                  </button>
                  {#if creation.pairingRecoveryError}
                    <p
                      class="mt-2 text-xs text-destructive"
                      data-testid="remote-ssh-enrollment-retry-error"
                    >
                      {creation.pairingRecoveryError}
                    </p>
                  {/if}
                </div>
              {/if}
              {#if creation.connectionPollError}
                <p
                  class="mt-4 rounded-lg border border-warning/30 bg-warning/10 p-3 text-xs text-muted-foreground"
                  role="status"
                  data-testid="remote-ssh-connection-poll-error"
                >
                  {creation.connectionPollError}
                </p>
              {/if}
            </section>
          {:else if creation.agentPairingWaiting}
            <section
              data-kx="plate"
              class="p-6"
              data-testid="guard-pairing-card"
            >
              <div
                class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between"
              >
                <div>
                  <p
                    class="text-xs font-semibold uppercase tracking-wide text-primary"
                  >
                    {tr("ui.stacksCreatingCreationRunStatus.waitingForRealConnection")}
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    {tr("ui.stacksCreatingCreationRunStatus.pairingCommandReady")}
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    {tr("ui.stacksCreatingCreationRunStatus.runThisOneLinerOn")}
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="guard-pairing-status"
                >
                  <span class="h-2 w-2 rounded-full bg-warning"></span>
                  {tr("ui.creationRun.notConnectedYet")}
                </span>
              </div>

              {#if creation.remoteServerHost}
                <div
                  class="mt-5 rounded-lg border border-border bg-muted/40 p-3"
                >
                  <p class="text-xs text-muted-foreground">
                    {tr("ui.stacksCreatingCreationRunStatus.plannedRemoteTarget")}
                  </p>
                  <p class="mt-1 font-mono text-sm text-foreground">
                    {creation.remoteServerUser
                      ? `${creation.remoteServerUser}@`
                      : ""}{creation.remoteServerHost}{creation.remoteServerPort
                      ? `:${creation.remoteServerPort}`
                      : ""}
                  </p>
                  <p class="mt-1 text-xs text-muted-foreground">
                    {tr("ui.stacksCreatingCreationRunStatus.theSshDetailsArePlanning")}
                  </p>
                </div>
              {/if}

              {#if creation.registrationToken && creation.installCommand}
                <div class="mt-5">
                  <label
                    for="pairing-server-url"
                    class="block text-xs font-medium text-muted-foreground"
                  >
                    {tr("ui.stacksCreatingCreationRunStatus.techstackUrlReachableFromThe")}
                  </label>
                  <input
                    id="pairing-server-url"
                    type="text"
                    bind:value={creation.serverUrl}
                    class="mt-1 w-full rounded-lg border border-border bg-background px-3 py-2 text-sm text-foreground focus:border-primary focus:outline-none"
                  />
                </div>
                <pre
                  class="mt-4 max-w-full overflow-x-auto whitespace-pre-wrap break-all rounded-lg border border-border bg-background p-4 pr-4 font-mono text-sm leading-relaxed text-primary"
                  data-testid="server-registration-command">{creation.installCommand}</pre>
                <button
                  type="button"
                  onclick={() => creation.copyToClipboard(creation.installCommand, "pairing")}
                  disabled={creation.pairingTokenExpired}
                  data-kx="control"
                  data-variant="primary"
                  class="mt-3 inline-flex w-full items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50 sm:w-auto"
                  data-testid="copy-pairing-command"
                >
                  {#if creation.copiedCommand === "pairing"}
                    {tr("ui.stacksCreatingCreationRunStatus.pairingCommandCopied")}
                  {:else if creation.copiedCommand === "error:pairing"}
                    {tr("ui.stacksCreatingCreationRunStatus.copyFailedSelectTheCommand")}
                  {:else}
                    {tr("ui.stacksCreatingCreationRunStatus.copyPairingCommand")}
                  {/if}
                </button>
                {#if creation.pairingTokenExpiresAt}
                  <p class="mt-3 text-xs text-muted-foreground">
                    {trParts("ui.creationRun.pairingTokenExpires")[0]}<span class="font-mono">{creation.pairingTokenExpiresAt}</span>{trParts("ui.creationRun.pairingTokenExpires")[1]}
                  </p>
                {/if}
                {#if creation.pairingTokenExpired}
                  <div
                    class="mt-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3"
                    role="alert"
                  >
                    <p class="text-sm font-medium text-destructive">
                      {tr("ui.stacksCreatingCreationRunStatus.thisPairingTokenHasExpired")}
                    </p>
                    <p class="mt-1 text-xs text-muted-foreground">
                      {tr("ui.stacksCreatingCreationRunStatus.generateAFreshCommandTo")}
                    </p>
                    <button
                      type="button"
                      data-kx="control"
                      class="mt-3 inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                      onclick={creation.recoverPairingCommand}
                      disabled={creation.pairingRecoveryBusy}
                    >
                      {tr("ui.stacksCreatingCreationRunStatus.generateNewCommand")}
                    </button>
                  </div>
                {/if}
              {:else}
                <div
                  class="mt-5 rounded-lg border border-destructive/30 bg-destructive/10 p-4"
                  role="alert"
                >
                  <p class="text-sm font-medium text-destructive">
                    {tr("ui.stacksCreatingCreationRunStatus.pairingCommandUnavailable")}
                  </p>
                  <p class="mt-1 text-xs text-muted-foreground">
                    {tr("ui.stacksCreatingCreationRunStatus.generateAShortLivedCommand")}
                  </p>
                  <button
                    type="button"
                    data-kx="control"
                    class="mt-3 rounded-lg px-3 py-1.5 text-sm font-medium"
                    onclick={creation.recoverPairingCommand}
                    disabled={creation.pairingRecoveryBusy}
                  >
                    {creation.pairingRecoveryBusy
                      ? tr("ui.stacksCreatingCreationRunStatus.preparingCommand")
                      : tr("ui.stacksCreatingCreationRunStatus.generatePairingCommand")}
                  </button>
                </div>
              {/if}
              {#if creation.pairingRecoveryError}
                <p role="alert" class="mt-3 text-sm text-destructive">
                  {creation.pairingRecoveryError}
                </p>
              {/if}

              {#if creation.connectionPollError}
                <p
                  class="mt-4 rounded-lg border border-warning/30 bg-warning/10 p-3 text-xs text-muted-foreground"
                  role="status"
                >
                  {creation.connectionPollError}
                </p>
              {/if}
            </section>
          {/if}

          <!-- Current step detail card -->
          {#if creation.currentStepInfo && !creation.agentPairingWaiting && !creation.waitingForManagedRuntime}
            {#key creation.currentTask?.id}
              <div data-kx="plate" class="p-6">
                <div class="flex items-start gap-4">
                  <div
                    class="w-10 h-10 rounded-full bg-primary/20 flex items-center justify-center shrink-0"
                  >
                    <svg
                      class="w-5 h-5 text-primary animate-spin"
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <circle cx="12" cy="12" r="10" stroke-opacity="0.25" />
                      <path
                        d="M12 2a10 10 0 0 1 10 10"
                        stroke-linecap="round"
                      />
                    </svg>
                  </div>
                  <div class="flex-1 min-w-0">
                    <p class="text-xs text-primary font-medium mb-1">
                      {tr("ui.creationRun.stepOf", { step: creation.completedCount + 1, total: creation.tasks.length })}
                    </p>
                    <h3 class="text-lg font-semibold text-foreground mb-2">
                      {creation.currentStepInfo.title}
                    </h3>
                    <p class="text-sm text-muted-foreground leading-relaxed">
                      {creation.currentStepInfo.description}
                    </p>
                    {#if creation.currentStepMessage}
                      <div
                        class="mt-4 rounded-lg border border-primary/20 bg-primary/10 px-3 py-2"
                        aria-live="polite"
                      >
                        <p class="text-xs font-medium text-primary">
                          {tr("ui.stacksCreatingCreationRunStatus.currentStatus")}
                        </p>
                        <p class="mt-1 text-sm text-foreground">
                          {creation.currentStepMessage}
                        </p>
                      </div>
                    {/if}
                    <p
                      class="text-xs text-muted-foreground/70 mt-3 leading-relaxed"
                    >
                      {creation.currentStepInfo.detail}
                    </p>
                  </div>
                </div>
              </div>
            {/key}
          {/if}

          <!-- Completed steps summary -->
          {#if creation.completedCount > 0}
            <div data-kx="plate" class="p-5">
              <p class="text-xs text-muted-foreground font-medium mb-3">
                {tr("ui.stacksCreatingCreationRunStatus.completed")}
              </p>
              <div class="space-y-2">
                {#each creation.tasks.filter((t) => t.status === "completed") as done (done.id)}
                  {@const info = STEP_DETAILS[done.id]}
                  <div class="flex items-center gap-3">
                    <svg
                      class="w-4 h-4 text-success shrink-0"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        d="M5 13l4 4L19 7"
                      />
                    </svg>
                    <span class="text-sm text-muted-foreground">
                      {info?.title ?? done.label}
                    </span>
                  </div>
                {/each}
              </div>
            </div>
          {/if}

          <!-- What happens next -->
          <div data-kx="plate" class="p-5">
            <p class="text-xs text-muted-foreground font-medium mb-2">
              {tr("ui.stacksCreatingCreationRunStatus.afterCreation")}
            </p>
            <p class="text-sm text-muted-foreground leading-relaxed">
              {#if creation.serverProvisioningMode === "hypervisor"}
                {tr("wizard.server.hypervisor.progress")}
              {:else if creation.creationOperation === "add-server" && creation.serverProvisioningMode === "kombify-cloud"}
                {tr("ui.stacksCreatingCreationRunStatus.theAdditionalManagedNodeIs")}
              {:else if creation.creationOperation === "add-server" && creation.agentPairingRequired}
                {tr("ui.stacksCreatingCreationRunStatus.afterYouRunTheOne")}
              {:else if creation.creationOperation === "add-server"}
                {tr("ui.stacksCreatingCreationRunStatus.theAdditionalNodeIsRegistered")}
              {:else if creation.serverProvisioningMode === "kombify-cloud"}
                {tr("ui.stacksCreatingCreationRunStatus.kombifyWillProvisionTheSubscription")}
              {:else if creation.serverProvisioningMode === "connect-remote"}
                {tr("ui.stacksCreatingCreationRunStatus.kombifyWillUseTheRemote")}
              {:else}
                {tr("ui.stacksCreatingCreationRunStatus.youWillReceiveAWorker")}
              {/if}
            </p>
          </div>
        </div>
