<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
</script>

          <!-- Requirements Info -->
          {#if creation.requirements}
            <div
              class="bg-info/10 border border-info/30 rounded-xl p-4 mb-6"
              data-testid="requirements-card"
            >
              <div class="flex items-start gap-3">
                <svg
                  class="w-5 h-5 text-info shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                <div class="flex-1">
                  <!-- StackKit Badge -->
                  {#if creation.requirements.stackKit}
                    <div class="flex items-center gap-2 mb-2">
                      <span
                        class="text-xs px-2 py-0.5 bg-info/30 text-info rounded"
                      >
                        {creation.requirements.stackKit}
                      </span>
                      {#if creation.requirements.detectedAddons && creation.requirements.detectedAddons.length > 0}
                        {#each creation.requirements.detectedAddons as addon (addon)}
                          <span
                            class="text-xs px-2 py-0.5 bg-primary/30 text-primary rounded"
                          >
                            +{addon}
                          </span>
                        {/each}
                      {/if}
                    </div>
                  {/if}

                  <p class="text-foreground font-medium text-sm">
                    {creation.requirements.description}
                  </p>

                  <!-- Hardware Requirements -->
                  {#if creation.requirements.minRAM || creation.requirements.minCPU || creation.requirements.specialRequirements?.length}
                    <div class="mt-3 flex flex-wrap gap-2 text-xs">
                      {#if creation.requirements.minRAM}
                        <span
                          class="px-2 py-1 bg-muted text-foreground rounded font-medium"
                        >
                          RAM: Min. {Math.round(creation.requirements.minRAM / 1024)}GB
                        </span>
                      {/if}
                      {#if creation.requirements.minCPU}
                        <span
                          class="px-2 py-1 bg-muted text-foreground rounded font-medium"
                        >
                          CPU: Min. {creation.requirements.minCPU} cores
                        </span>
                      {/if}
                    </div>
                  {/if}

                  <!-- Special Requirements -->
                  {#if creation.requirements.specialRequirements && creation.requirements.specialRequirements.length > 0}
                    <ul class="mt-3 text-sm text-warning space-y-1">
                      {#each creation.requirements.specialRequirements as req (req)}
                        <li class="flex items-start gap-2">
                          <span aria-hidden="true">•</span>
                          <span>{req}</span>
                        </li>
                      {/each}
                    </ul>
                  {/if}

                  <!-- Legacy details fallback -->
                  {#if creation.requirements.details && creation.requirements.details.length > 0}
                    <ul class="mt-3 text-sm text-foreground space-y-1">
                      {#each creation.requirements.details as detail (detail)}
                        <li>• {detail}</li>
                      {/each}
                    </ul>
                  {/if}
                </div>
              </div>
            </div>
          {:else if creation.backendCompleted && creation.creationOperation === "stack"}
            <div
              class="bg-warning/10 border border-warning/40 rounded-xl p-4 mb-6"
              data-testid="requirements-missing-card"
            >
              <p class="text-sm font-medium text-foreground">
                Backend requirements are unavailable
              </p>
              <p class="mt-1 text-sm text-muted-foreground">
                The page is not filling this with frontend estimates. Re-run
                preparation if requirements are needed for review.
              </p>
            </div>
          {/if}

          <!-- Required Credentials Section -->
          {#if creation.requirements?.requiredCredentials && creation.requirements.requiredCredentials.length > 0}
            <div
              class="bg-warning/10 border border-warning/30 rounded-xl p-4 mb-6"
            >
              <div class="flex items-start gap-3">
                <svg
                  class="w-5 h-5 text-warning shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M15 7a2 2 0 012 2m4 0a6 6 0 01-7.743 5.743L11 17H9v2H7v2H4a1 1 0 01-1-1v-2.586a1 1 0 01.293-.707l5.964-5.964A6 6 0 1121 9z"
                  />
                </svg>
                <div class="flex-1">
                  <p class="text-warning font-semibold text-sm mb-2">
                    Required credentials
                  </p>
                  <ul class="space-y-2">
                    {#each creation.requirements.requiredCredentials as cred (cred.key)}
                      <li class="bg-muted/70 rounded p-3">
                        <div class="flex items-center justify-between">
                          <span class="text-foreground text-sm font-medium"
                            >{cred.label}</span
                          >
                          {#if cred.helpUrl}
                            <a
                              href={cred.helpUrl}
                              target="_blank"
                              rel="noopener noreferrer"
                              class="text-xs text-primary hover:text-primary/80 underline"
                            >
                              Guide →
                            </a>
                          {/if}
                        </div>
                        {#if cred.description}
                          <p class="text-sm text-foreground/85 mt-1">
                            {cred.description}
                          </p>
                        {/if}
                        {#if cred.required}
                          <span
                            class="inline-block mt-2 text-xs px-2 py-0.5 rounded bg-destructive/20 text-destructive font-medium"
                            >Required</span
                          >
                        {/if}
                      </li>
                    {/each}
                  </ul>
                </div>
              </div>
            </div>
          {/if}

          <!-- Pre-Checks Section -->
          {#if creation.requirements?.requiredPreChecks && creation.requirements.requiredPreChecks.length > 0}
            <div
              class="bg-primary/10 border border-primary/40 rounded-xl p-4 mb-6"
            >
              <div class="flex items-start gap-3">
                <svg
                  class="w-5 h-5 text-primary shrink-0 mt-0.5"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4"
                  />
                </svg>
                <div class="flex-1">
                  <p class="text-foreground font-semibold text-sm mb-1">
                    Requirements your servers must meet
                  </p>
                  <p class="text-xs text-muted-foreground mb-3">
                    The orchestrator blocks rollout when a `Required` item is
                    missing on the target server. Optional items only emit a log
                    warning.
                  </p>
                  <ul class="space-y-2">
                    {#each creation.requirements.requiredPreChecks as check (check.type)}
                      <li
                        class="flex items-start gap-2 text-sm text-foreground"
                      >
                        <span
                          class="shrink-0 inline-flex items-center px-2 py-0.5 rounded text-xs font-medium {check.blocking
                            ? 'bg-primary/30 text-primary'
                            : 'bg-muted text-muted-foreground'}"
                        >
                          {check.blocking ? "Required" : "Optional"}
                        </span>
                        <div class="min-w-0 flex-1">
                          <span class="font-medium">{check.type}</span>
                          {#if check.minVersion}
                            <span
                              class="ml-2 text-xs px-1.5 py-0.5 bg-muted rounded text-foreground"
                              >min: {check.minVersion}</span
                            >
                          {/if}
                          {#if check.description}
                            <p class="text-sm text-muted-foreground mt-0.5">
                              {check.description}
                            </p>
                          {/if}
                        </div>
                      </li>
                    {/each}
                  </ul>
                </div>
              </div>
            </div>
          {/if}
