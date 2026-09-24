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
                      ? "Managed Node requested"
                      : "Managed runtime ready"}
                  </h3>
                  <p class="text-sm text-muted-foreground">
                    {#if creation.creationOperation === "add-server"}
                      The additional subscription VM request is recorded. It
                      will appear in Operations as enrollment reports back.
                    {:else}
                      StackKit deployed on the leased subscription VM. Values
                      below are returned by the runtime job - no demo data.
                    {/if}
                  </p>
                </div>
                <span
                  data-kx="status"
                  data-status="ok"
                  class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium shrink-0"
                >
                  {creation.creationOperation === "add-server" ? "Requested" : "Live"}
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
                    Lease ID
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
                      Provider
                    </dt>
                    <dd class="mt-1 text-foreground">{creation.lease.provider}</dd>
                  </div>
                {/if}
                {#if creation.lease.offering}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Offering
                    </dt>
                    <dd class="mt-1 text-foreground">{creation.lease.offering}</dd>
                  </div>
                {/if}
                {#if creation.lease.host || creation.lease.publicIp}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Host
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
                      Desired state
                    </dt>
                    <dd class="mt-1 text-foreground">{creation.lease.desiredState}</dd>
                  </div>
                {/if}
                {#if creation.lease.billingMode}
                  <div>
                    <dt
                      class="text-xs uppercase tracking-wide text-muted-foreground"
                    >
                      Billing
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
                Remote server connection
              </h3>
              <p class="text-sm text-muted-foreground">
                kombify will use the captured SSH connection details for the
                existing server{#if creation.remoteServerHost}
                  <span>
                    at {creation.remoteServerHost}{#if creation.remoteServerPort}:{creation.remoteServerPort}{/if}
                  </span>{/if}{#if creation.remoteServerUser}
                  <span> as {creation.remoteServerUser}</span>{/if}.
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
                  Runtime proof
                </h3>
                <p class="text-sm text-muted-foreground">
                  These statuses come from Runtime Action responses and the
                  final e2e proof.
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
                      First login and recovery
                    </h3>
                    <p class="text-sm text-muted-foreground">
                      StackKit returned the owner login, login gateway, and
                      recovery references for this verified Cloud Kit rollout.
                    </p>
                  </div>
                  <span
                    data-kx="status"
                    data-status="ok"
                    class="inline-flex items-center rounded-full px-2.5 py-0.5 text-xs font-medium shrink-0"
                    >Ready</span
                  >
                </div>

                <div class="grid gap-3 md:grid-cols-3">
                  <div
                    data-kx="plate"
                    class="p-3"
                    data-testid="stackkit-owner-identity"
                  >
                    <p class="text-xs text-muted-foreground mb-1">Owner</p>
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
                      Login gateway
                    </p>
                    <a
                      href={creation.stackKitHandoff.loginGatewayUrl || "#"}
                      target="_blank"
                      rel="noopener noreferrer"
                      data-kx="control"
                      class="inline-flex w-full items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                      data-testid="stackkit-login-gateway-link"
                    >
                      {creation.stackKitHandoff.loginGatewayLabel || "Open first login"}
                    </a>
                  </div>

                  <div data-kx="plate" class="p-3">
                    <p class="text-xs text-muted-foreground mb-2">Wallet</p>
                    <div class="flex flex-wrap gap-2">
                      <a
                        href="/wallet#access"
                        data-kx="control"
                        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                        data-testid="wallet-access-handoff-link"
                      >
                        Access
                      </a>
                      <a
                        href="/wallet#recovery"
                        data-kx="control"
                        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-3 py-1.5 text-sm font-medium"
                        data-testid="wallet-recovery-handoff-link"
                      >
                        Recovery
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
                  StackKit identity handoff is missing
                </h3>
                <p class="text-sm text-warning/80">
                  The rollout completed, but the StackKit did not return owner
                  login, login gateway, and recovery outputs. Treat this as a
                  release blocker until the runtime action response includes
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
                Simulate demo preview
              </h3>
              <p class="text-sm text-info/80">
                Your install command is ready. kombify-simulate is used as a
                temporary demo preview when available and is limited to one
                hour.
              </p>
              {#if creation.simulationPreviewStatus}
                <p class="mt-3 text-xs text-info/80">
                  Status: <span class="font-mono"
                    >{creation.simulationPreviewStatus}</span
                  >
                </p>
              {/if}
            </div>
          {/if}
