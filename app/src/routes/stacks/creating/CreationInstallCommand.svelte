<script lang="ts">
  import { creation } from "./creation-controller.svelte.js";
</script>

          <!-- Worker Installation Command -->
          {#if creation.showInstallCommand && creation.registrationToken && creation.installCommand && !creation.agentPairingRequired}
            <div class="bg-muted/70 rounded-xl p-6 mb-6">
              <h3 class="text-lg font-medium text-foreground mb-2">
                Install worker
              </h3>
              {#if creation.oneLinerPreviewRequired}
                <p class="text-sm text-muted-foreground mb-4">
                  Run this command on the server or device that should become
                  the real target for this Homelab. Simulate may also provide a
                  one-hour demo preview of the planned StackKit.
                </p>
                {#if creation.simulationPreviewUrl}
                  <a
                    href={creation.simulationPreviewUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    class="inline-flex items-center text-sm text-primary hover:underline mb-4"
                    data-testid="simulation-preview-link"
                  >
                    Open one-hour Simulate demo preview
                  </a>
                {/if}
                {#if creation.simulationPreviewExpiresAt}
                  <p class="text-xs text-muted-foreground mb-4">
                    Preview expires at {creation.simulationPreviewExpiresAt}
                  </p>
                {:else if creation.simulationPreviewUrl}
                  <p class="text-xs text-muted-foreground mb-4">
                    Preview lifetime is limited to one hour.
                  </p>
                {/if}
              {:else}
                <p class="text-sm text-muted-foreground mb-4">
                  Run this command on all servers you want to integrate into
                  your homelab:
                </p>
              {/if}

              <!-- Server URL Override -->
              <div class="mb-4">
                <label
                  for="server-url"
                  class="block text-xs text-muted-foreground mb-1"
                >
                  kombify-Techstack server URL (reachable by workers):
                </label>
                <input
                  id="server-url"
                  type="text"
                  bind:value={creation.serverUrl}
                  class="w-full bg-card border border-border rounded px-3 py-2 text-sm text-foreground focus:outline-none focus:border-primary transition-colors"
                  placeholder="http://192.168.x.x:5261"
                />
              </div>

              <!-- Main install command -->
              <div class="relative mb-4">
                <pre
                  class="px-4 py-3 bg-card rounded-lg text-primary text-sm font-mono overflow-x-auto pr-12">{creation.installCommand}</pre>
                <button
                  onclick={() => creation.copyToClipboard(creation.installCommand, "main")}
                  data-kx="control"
                  class="absolute right-2 top-1/2 -translate-y-1/2 rounded px-2 py-1"
                  title="Copy"
                  aria-label="Copy installation command to clipboard"
                >
                  {#if creation.copiedCommand === "main"}
                    <svg
                      class="w-4 h-4 text-success"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        stroke-width="2"
                        d="M5 13l4 4L19 7"
                      />
                    </svg>
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
                        d="M8 5H6a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2v-1M8 5a2 2 0 002 2h2a2 2 0 002-2M8 5a2 2 0 012-2h2a2 2 0 012 2m0 0h2a2 2 0 012 2v3m2 4H10m0 0l3-3m-3 3l3 3"
                      />
                    </svg>
                  {/if}
                </button>
              </div>

              <!-- Alternative commands -->
              <button
                onclick={() => creation.toggleAllCommands()}
                class="text-sm text-muted-foreground hover:text-foreground/80 flex items-center gap-1"
              >
                <svg
                  class="w-4 h-4 transition-transform {creation.showAllCommands
                    ? 'rotate-180'
                    : ''}"
                  fill="none"
                  viewBox="0 0 24 24"
                  stroke="currentColor"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M19 9l-7 7-7-7"
                  />
                </svg>
                Alternative installation methods
              </button>

              {#if creation.showAllCommands}
                <div class="mt-4 space-y-3">
                  {#each creation.allInstallCommands as cmd (cmd.platform)}
                    <div data-kx="plate" class="p-3">
                      <p class="text-xs text-muted-foreground mb-2">
                        {cmd.description}
                      </p>
                      <div class="relative">
                        <pre
                          class="text-xs font-mono text-foreground/80 overflow-x-auto pr-10">{cmd.command}</pre>
                        <button
                          onclick={() =>
                            creation.copyToClipboard(cmd.command, cmd.platform)}
                          class="absolute right-1 top-1/2 -translate-y-1/2 p-1 hover:bg-secondary rounded"
                          title="Copy"
                        >
                          {#if creation.copiedCommand === cmd.platform}
                            <svg
                              class="w-3 h-3 text-success"
                              fill="none"
                              viewBox="0 0 24 24"
                              stroke="currentColor"
                            >
                              <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                stroke-width="2"
                                d="M5 13l4 4L19 7"
                              />
                            </svg>
                          {:else}
                            <svg
                              class="w-3 h-3 text-muted-foreground"
                              fill="none"
                              viewBox="0 0 24 24"
                              stroke="currentColor"
                            >
                              <path
                                stroke-linecap="round"
                                stroke-linejoin="round"
                                stroke-width="2"
                                d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z"
                              />
                            </svg>
                          {/if}
                        </button>
                      </div>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          {/if}
