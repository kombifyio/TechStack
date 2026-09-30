<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
  import { tr } from "#lib/i18n.svelte.js";
</script>

          {#if creation.serverProvisioningMode === "kombify-cloud" && creation.lease}
            <div
              class="bg-success/10 border border-success/30 rounded-xl p-5 mb-6"
              data-testid="managed-server-provisioning-card"
            >
              <div class="flex items-start justify-between gap-4 mb-3">
                <div>
                  <h3 class="text-lg font-medium text-foreground mb-1">
                    {creation.creationOperation === "add-server"
                      ? tr("ui.stacksCreatingCreationLease.managedNodeRequested")
                      : tr("ui.stacksCreatingCreationLease.managedRuntimeReady")}
                  </h3>
                  <p class="text-sm text-muted-foreground">
                    {#if creation.creationOperation === "add-server"}
                      {tr("ui.stacksCreatingCreationLease.theAdditionalSubscriptionVmRequest")}
                    {:else}
                      {tr("ui.stacksCreatingCreationLease.stackkitDeployedOnTheLeased")}
                    {/if}
                  </p>
                </div>
                <span
                  data-kx="status"
                  data-status="ok"
                  class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium shrink-0"
                >
                  {creation.creationOperation === "add-server" ? tr("ui.stacksCreatingCreationLease.requested") : tr("ui.stacksCreatingCreationLease.live")}
                </span>
              </div>
              <dl
                class="grid gap-3 sm:grid-cols-2 text-sm"
                data-testid="managed-lease-summary"
              >
                <div>
                  <dt
                    class="text-xs uppercase tracking-wide text-muted-foreground"
                  >
                    {tr("ui.stacksCreatingCreationLease.leaseId")}
                  </dt>
                  <dd class="mt-1 font-mono text-foreground break-all">
                    {creation.lease.id}
                  </dd>
                </div>
                {#if creation.lease.provider}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      {tr("ui.stacksCreatingCreationLease.provider")}
                    </dt>
                    <dd class="mt-1 text-foreground">{creation.lease.provider}</dd>
                  </div>
                {/if}
                {#if creation.lease.offering}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      {tr("ui.stacksCreatingCreationLease.offering")}
                    </dt>
                    <dd class="mt-1 text-foreground">{creation.lease.offering}</dd>
                  </div>
                {/if}
                {#if creation.lease.host || creation.lease.publicIp}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      {tr("ui.stacksCreatingCreationLease.host")}
                    </dt>
                    <dd class="mt-1 font-mono text-foreground break-all">
                      {creation.lease.host || creation.lease.publicIp}
                    </dd>
                  </div>
                {/if}
                {#if creation.lease.desiredState}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      {tr("ui.stacksCreatingCreationLease.desiredState")}
                    </dt>
                    <dd class="mt-1 text-foreground">{creation.lease.desiredState}</dd>
                  </div>
                {/if}
                {#if creation.lease.billingMode}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      {tr("ui.stacksCreatingCreationLease.billing")}
                    </dt>
                    <dd class="mt-1 text-foreground">{creation.lease.billingMode}</dd>
                  </div>
                {/if}
              </dl>
            </div>
          {:else if creation.serverProvisioningMode === "hypervisor"}
            <div
              class="bg-primary/10 border border-primary/30 rounded-xl p-5 mb-6"
              data-testid="hypervisor-provisioning-card"
            >
              <h3 class="text-lg font-medium text-foreground mb-2">
                {tr("wizard.server.hypervisor.title")}
              </h3>
              <p class="text-sm text-muted-foreground">
                {tr("wizard.server.hypervisor.progress")}
              </p>
            </div>
          {:else if creation.serverProvisioningMode === "connect-remote"}
            <div
              class="bg-primary/10 border border-primary/30 rounded-xl p-5 mb-6"
              data-testid="remote-server-provisioning-card"
            >
              <h3 class="text-lg font-medium text-foreground mb-2">
                {tr("ui.stacksCreatingCreationLease.remoteServerConnection")}
              </h3>
              <p class="text-sm text-muted-foreground">
                {#if creation.remoteServerHost && creation.remoteServerUser}
                  {tr("ui.creationLease.sshHostUser", { host: creation.remoteServerHost + (creation.remoteServerPort ? ":" + creation.remoteServerPort : ""), user: creation.remoteServerUser })}
                {:else if creation.remoteServerHost}
                  {tr("ui.creationLease.sshHost", { host: creation.remoteServerHost + (creation.remoteServerPort ? ":" + creation.remoteServerPort : "") })}
                {:else if creation.remoteServerUser}
                  {tr("ui.creationLease.sshUser", { user: creation.remoteServerUser })}
                {:else}
                  {tr("ui.stacksCreatingCreationLease.kombifyWillUseTheCaptured")}.
                {/if}
              </p>
            </div>
          {/if}

          {#if creation.creationOperation === "stack" && creation.serverProvisioningMode === "kombify-cloud" && creation.proofRows.length > 0}
            <div
              data-kx="plate"
              class="p-5 mb-6"
              data-testid="runtime-proof-card"
            >
              <div class="mb-4">
                <h3 class="text-lg font-medium text-foreground">
                  {tr("ui.stacksCreatingCreationLease.runtimeProof")}
                </h3>
                <p class="text-sm text-muted-foreground">
                  {tr("ui.stacksCreatingCreationLease.theseStatusesComeFromRuntime")}
                </p>
              </div>
              <div class="grid gap-3 sm:grid-cols-2">
                {#each creation.proofRows as row (row.key)}
                  <div
                    class="rounded-lg border border-border bg-background/40 p-3"
                  >
                    <p class="text-sm font-medium text-foreground">
                      {row.label}
                    </p>
                    <div class="mt-2 flex items-center gap-2">
                      <span
                        data-kx="status"
                        data-status={row.status === "verified" ||
                        row.status === "applied" ||
                        row.status === "accepted" ||
                        row.status === "passed"
                          ? "ok"
                          : "warn"}
                        class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium"
                      >
                        {row.status}
                      </span>
                      {#if row.action}
                        <span class="text-xs text-muted-foreground truncate">
                          {row.action}
                        </span>
                      {/if}
                    </div>
                  </div>
                {/each}
              </div>
            </div>
          {/if}

          {#if creation.creationOperation === "stack" && creation.serverProvisioningMode === "kombify-cloud"}
            {#if creation.hasStackKitIdentityHandoff && creation.stackKitHandoff}
              <div
                class="bg-success/10 border border-success/30 rounded-xl p-5 mb-6"
                data-testid="stackkit-identity-handoff-card"
              >
                <div class="flex items-start justify-between gap-4 mb-4">
                  <div>
                    <h3 class="text-lg font-medium text-foreground mb-2">
                      {tr("ui.stacksCreatingCreationLease.firstLoginAndRecovery")}
                    </h3>
                    <p class="text-sm text-muted-foreground">
                      {tr("ui.stacksCreatingCreationLease.stackkitReturnedTheOwnerLogin")}
                    </p>
                  </div>
                  <span
                    data-kx="status"
                    data-status="ok"
                    class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium shrink-0"
                    >{tr("ui.stacksCreatingCreationLease.ready")}</span
                  >
                </div>

                <div class="grid gap-3 md:grid-cols-3">
                  <div
                    data-kx="plate"
                    class="p-3"
                    data-testid="stackkit-owner-identity"
                  >
                    <p class="text-xs text-muted-foreground mb-1">{tr("ui.stacksCreatingCreationCompletion.owner")}</p>
                    <p class="text-sm text-foreground font-medium truncate">
                      {creation.stackKitHandoff.ownerDisplayName ||
                        creation.stackKitHandoff.ownerUsername}
                    </p>
                    {#if creation.stackKitHandoff.ownerUsername}
                      <p class="text-xs text-muted-foreground truncate">
                        {creation.stackKitHandoff.ownerUsername}
                      </p>
                    {/if}
                    {#if creation.stackKitHandoff.ownerEmail}
                      <p class="text-xs text-muted-foreground truncate">
                        {creation.stackKitHandoff.ownerEmail}
                      </p>
                    {/if}
                  </div>

                  <div data-kx="plate" class="p-3">
                    <p class="text-xs text-muted-foreground mb-2">
                      {tr("ui.stacksCreatingCreationLease.loginGateway")}
                    </p>
                    <a
                      href={creation.stackKitHandoff.loginGatewayUrl || "#"}
                      target="_blank"
                      rel="noopener noreferrer"
                      data-kx="control"
                      class="inline-flex w-full items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                      data-testid="stackkit-login-gateway-link"
                    >
                      {creation.stackKitHandoff.loginGatewayLabel || tr("ui.stacksCreatingCreationLease.openFirstLogin")}
                    </a>
                  </div>

                  <div data-kx="plate" class="p-3">
                    <p class="text-xs text-muted-foreground mb-2">{tr("ui.stacksCreatingCreationLease.wallet")}</p>
                    <div class="flex flex-wrap gap-2">
                      <a
                        href="/wallet#access"
                        data-kx="control"
                        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                        data-testid="wallet-access-handoff-link"
                      >
                        {tr("ui.stacksCreatingCreationLease.access")}
                      </a>
                      <a
                        href="/wallet#recovery"
                        data-kx="control"
                        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                        data-testid="wallet-recovery-handoff-link"
                      >
                        {tr("ui.stacksCreatingCreationCompletion.recovery")}
                      </a>
                    </div>
                    {#if creation.stackKitHandoff.recoveryRef}
                      <p class="mt-2 text-xs text-muted-foreground truncate">
                        {creation.stackKitHandoff.recoveryRef}
                      </p>
                    {/if}
                  </div>
                </div>
              </div>
            {:else}
              <div
                class="bg-warning/10 border border-warning/30 rounded-xl p-5 mb-6"
                data-testid="stackkit-handoff-missing-card"
              >
                <h3 class="text-lg font-semibold text-warning mb-2">
                  {tr("ui.stacksCreatingCreationLease.stackkitIdentityHandoffIsMissing")}
                </h3>
                <p class="text-sm text-warning/80">
                  {tr("ui.stacksCreatingCreationLease.theRolloutCompletedButThe")}
                  <span class="font-mono text-foreground">stackkit_outputs</span
                  >.
                </p>
              </div>
            {/if}
          {/if}

          {#if creation.oneLinerPreviewRequired && !creation.simulationPreviewUrl}
            <div
              class="bg-info/10 border border-info/30 rounded-xl p-5 mb-6"
              data-testid="oneliner-simulation-preview-card"
            >
              <h3 class="text-lg font-semibold text-info mb-2">
                {tr("ui.stacksCreatingCreationLease.simulateDemoPreview")}
              </h3>
              <p class="text-sm text-info/80">
                {tr("ui.stacksCreatingCreationLease.yourInstallCommandIsReady")}
              </p>
              {#if creation.simulationPreviewStatus}
                <p class="mt-3 text-xs text-info/80">
                  {tr("ui.stacksCreatingCreationLease.status")} <span class="font-mono"
                    >{creation.simulationPreviewStatus}</span
                  >
                </p>
              {/if}
            </div>
          {/if}
