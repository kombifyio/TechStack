<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
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
                    Provisioning in progress
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    Node provisioning is still in progress
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    {#if creation.waitingForProviderProvision}
                      The provider operation has not yet handed the existing
                      Managed Runtime over to the StackKit rollout. Techstack
                      scheduled the next exact-lease check.
                    {:else}
                      The Managed Runtime is not fully reachable for rollout
                      yet. The pending signal may be its address, credentials,
                      or enrollment. Techstack scheduled the next check.
                    {/if}
                  </p>
                  {#if creation.nextResumeLabel}
                    <p
                      class="mt-3 text-xs text-muted-foreground"
                      data-testid="waiting-enrollment-next-resume"
                    >
                      Next scheduled check:
                      <time datetime={creation.jobNextResumeAt}>{creation.nextResumeLabel}</time>
                    </p>
                  {/if}
                  <p class="mt-3 max-w-2xl text-xs text-muted-foreground">
                    {#if creation.jobResumeAvailableAt}
                      Starting at {creation.resumeAvailableLabel},
                      the server can authorize a safe resume on the same VM.
                    {:else}
                      A manual resume is enabled only after server-side
                      validation.
                    {/if}
                    This does not create another VM.
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="waiting-enrollment-status"
                >
                  <span class="h-2 w-2 animate-pulse rounded-full bg-warning"
                  ></span>
                  Not reachable yet
                </span>
              </div>

              {#if creation.currentStepMessage}
                <div
                  class="mt-5 rounded-lg border border-warning/20 bg-warning/10 px-3 py-2"
                >
                  <p class="text-xs font-medium text-warning">Current status</p>
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
                  Dashboard
                </button>
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  data-kx="control"
                  class="inline-flex cursor-not-allowed items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium opacity-50"
                  data-testid="waiting-services-disabled"
                >
                  Services
                </button>
                <button
                  type="button"
                  disabled
                  aria-disabled="true"
                  data-kx="control"
                  class="inline-flex cursor-not-allowed items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium opacity-50"
                  data-testid="waiting-access-disabled"
                >
                  Node access
                </button>
              </div>
              <p class="mt-3 text-xs text-muted-foreground">
                These access paths are enabled only after a real enrollment
                signal.
              </p>
              {#if creation.waitingRecoveryAvailable}
                <div
                  class="mt-4 rounded-lg border border-info/30 bg-info/10 p-3"
                  data-testid="waiting-enrollment-recovery"
                >
                  <p class="text-xs text-muted-foreground">
                    The scheduled check is overdue. Techstack validates the
                    stack, source job, and exact lease together, then continues
                    only the rollout on the existing VM.
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
                      ? "Validating the existing VM..."
                      : creation.waitingForProviderProvision
                        ? "Continue rollout on the existing VM"
                        : "Resume enrollment on the existing VM"}
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
                    Connect my Node
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    {#if creation.remoteEnrollmentActive}
                      Connecting via SSH…
                    {:else if creation.remoteEnrollmentFailed}
                      Remote SSH connection failed
                    {:else}
                      Waiting for Guard heartbeat…
                    {/if}
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    {#if creation.remoteEnrollmentActive}
                      kombify is installing and enrolling the Guard on your server
                      over SSH. You do not need to run a command manually.
                    {:else if creation.remoteEnrollmentFailed}
                      {creation.pairingRecoveryError ||
                        "Check SSH host, credentials, and that the server can reach Techstack."}
                    {:else}
                      Enrollment over SSH finished. This page will show “Node
                      connected” once the Guard reports a fresh heartbeat.
                    {/if}
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="remote-ssh-enrollment-status"
                >
                  <span class="h-2 w-2 rounded-full bg-warning"></span>
                  {#if creation.remoteEnrollmentActive}
                    Enrolling
                  {:else if creation.remoteEnrollmentFailed}
                    Failed
                  {:else}
                    Awaiting heartbeat
                  {/if}
                </span>
              </div>

              {#if creation.remoteServerHost}
                <div
                  class="mt-5 rounded-lg border border-border bg-muted/40 p-3"
                >
                  <p class="text-xs text-muted-foreground">Remote target</p>
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
                      "Connecting to your Node over SSH…"}
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
                      ? "Retrying SSH connection…"
                      : "Retry connection on saved server"}
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
                    Waiting for real connection
                  </p>
                  <h2 class="mt-1 text-xl font-semibold text-foreground">
                    Pairing command ready
                  </h2>
                  <p class="mt-2 max-w-2xl text-sm text-muted-foreground">
                    Run this one-liner on the additional Node. The completed
                    registration job only created the pairing token; Techstack
                    will show “Node connected” only after the outbound Guard
                    reports a fresh heartbeat and the Node projection is
                    healthy.
                  </p>
                </div>
                <span
                  class="inline-flex shrink-0 items-center gap-2 rounded-full border border-warning/30 bg-warning/10 px-3 py-1 text-xs font-medium text-warning"
                  data-testid="guard-pairing-status"
                >
                  <span class="h-2 w-2 rounded-full bg-warning"></span>
                  Not connected yet
                </span>
              </div>

              {#if creation.remoteServerHost}
                <div
                  class="mt-5 rounded-lg border border-border bg-muted/40 p-3"
                >
                  <p class="text-xs text-muted-foreground">
                    Planned remote target
                  </p>
                  <p class="mt-1 font-mono text-sm text-foreground">
                    {creation.remoteServerUser
                      ? `${creation.remoteServerUser}@`
                      : ""}{creation.remoteServerHost}{creation.remoteServerPort
                      ? `:${creation.remoteServerPort}`
                      : ""}
                  </p>
                  <p class="mt-1 text-xs text-muted-foreground">
                    The SSH details are planning metadata. The current Guard
                    connection is outbound HTTPS and starts with the command
                    below.
                  </p>
                </div>
              {/if}

              {#if creation.registrationToken && creation.installCommand}
                <div class="mt-5">
                  <label
                    for="pairing-server-url"
                    class="block text-xs font-medium text-muted-foreground"
                  >
                    Techstack URL reachable from the server
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
                    Pairing command copied
                  {:else if creation.copiedCommand === "error:pairing"}
                    Copy failed — select the command above
                  {:else}
                    Copy pairing command
                  {/if}
                </button>
                {#if creation.pairingTokenExpiresAt}
                  <p class="mt-3 text-xs text-muted-foreground">
                    Pairing token expires at
                    <span class="font-mono">{creation.pairingTokenExpiresAt}</span>.
                  </p>
                {/if}
                {#if creation.pairingTokenExpired}
                  <div
                    class="mt-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3"
                    role="alert"
                  >
                    <p class="text-sm font-medium text-destructive">
                      This pairing token has expired.
                    </p>
                    <p class="mt-1 text-xs text-muted-foreground">
                      Generate a fresh command to continue connecting this Node.
                    </p>
                    <button
                      type="button"
                      data-kx="control"
                      class="mt-3 inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                      onclick={creation.recoverPairingCommand}
                      disabled={creation.pairingRecoveryBusy}
                    >
                      Generate new command
                    </button>
                  </div>
                {/if}
              {:else}
                <div
                  class="mt-5 rounded-lg border border-destructive/30 bg-destructive/10 p-4"
                  role="alert"
                >
                  <p class="text-sm font-medium text-destructive">
                    Pairing command unavailable
                  </p>
                  <p class="mt-1 text-xs text-muted-foreground">
                    Generate a short-lived command for this Node. Connection
                    credentials are not retained in job reports.
                  </p>
                  <button
                    type="button"
                    data-kx="control"
                    class="mt-3 rounded-lg px-3 py-1.5 text-sm font-medium"
                    onclick={creation.recoverPairingCommand}
                    disabled={creation.pairingRecoveryBusy}
                  >
                    {creation.pairingRecoveryBusy
                      ? "Preparing command…"
                      : "Generate pairing command"}
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
                      Step {creation.completedCount + 1} of {creation.tasks.length}
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
                          Current status
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
                Completed
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
              After creation
            </p>
            <p class="text-sm text-muted-foreground leading-relaxed">
              {#if creation.serverProvisioningMode === "hypervisor"}
                {tr("wizard.server.hypervisor.progress")}
              {:else if creation.creationOperation === "add-server" && creation.serverProvisioningMode === "kombify-cloud"}
                The additional managed Node is added to this Homelab and will
                appear in the Node dashboard once enrollment reports back.
              {:else if creation.creationOperation === "add-server" && creation.agentPairingRequired}
                After you run the one-liner, the outbound Guard enrolls with
                this Homelab. The dashboard only marks the Node connected after
                a fresh heartbeat is present in the Node projection.
              {:else if creation.creationOperation === "add-server"}
                The additional Node is registered with this Homelab and can then
                receive services from this StackKit deployment.
              {:else if creation.serverProvisioningMode === "kombify-cloud"}
                kombify will provision the subscription server and then continue
                with the StackKit rollout.
              {:else if creation.serverProvisioningMode === "connect-remote"}
                kombify will use the remote SSH configuration captured in the
                wizard.
              {:else}
                You will receive a worker installation command to connect your
                Nodes to this StackKit deployment.
              {/if}
            </p>
          </div>
        </div>
