<!--
  ProbeDialog Component
  
  Modal dialog for entering SSH credentials to deep probe a device.
-->
<script lang="ts">
  import type { DiscoveredDevice } from "$lib/discovery";

  interface Props {
    device: DiscoveredDevice | null;
    open?: boolean;
    loading?: boolean;
    error?: string | null;
    onsubmit?: (credentials: {
      ssh_user: string;
      ssh_password?: string;
      ssh_private_key?: string;
      ssh_port: number;
    }) => void;
    onclose?: () => void;
  }

  let {
    device,
    open = false,
    loading = false,
    error = null,
    onsubmit,
    onclose,
  }: Props = $props();

  let sshUser = $state("root");
  let sshPassword = $state("");
  let sshPrivateKey = $state("");
  let sshPort = $state(22);
  let authMethod = $state<"password" | "key">("password");

  function handleSubmit(e: Event) {
    e.preventDefault();
    if (!onsubmit) return;

    onsubmit({
      ssh_user: sshUser,
      ssh_password: authMethod === "password" ? sshPassword : undefined,
      ssh_private_key: authMethod === "key" ? sshPrivateKey : undefined,
      ssh_port: sshPort,
    });
  }

  function handleClose() {
    if (!loading && onclose) {
      onclose();
    }
  }

  function handleBackdropClick(e: MouseEvent) {
    if (e.target === e.currentTarget) {
      handleClose();
    }
  }

  function handleDialogKeydown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      handleClose();
    }
  }
</script>

{#if open && device}
  <!-- Backdrop -->
  <div
    class="fixed inset-0 bg-black/70 flex items-center justify-center z-50"
    onclick={handleBackdropClick}
    onkeydown={handleDialogKeydown}
    tabindex="-1"
    role="dialog"
    aria-modal="true"
    aria-labelledby="probe-dialog-title"
  >
    <!-- Dialog -->
    <div
      class="bg-gray-800 rounded-lg border border-gray-700 w-full max-w-md mx-4 shadow-xl"
    >
      <!-- Header -->
      <div
        class="flex items-center justify-between p-4 border-b border-gray-700"
      >
        <h3 id="probe-dialog-title" class="text-lg font-semibold text-white">
          Deep Probe Device
        </h3>
        <button
          class="text-gray-400 hover:text-white transition-colors"
          onclick={handleClose}
          disabled={loading}
          aria-label="Close dialog"
        >
          <svg
            class="w-5 h-5"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M6 18L18 6M6 6l12 12"
            />
          </svg>
        </button>
      </div>

      <!-- Content -->
      <form onsubmit={handleSubmit} class="p-4 space-y-4">
        <!-- Device info -->
        <div class="p-3 bg-gray-900/50 rounded-lg">
          <p class="text-sm text-gray-400">Target Device</p>
          <p class="text-white font-medium">{device.hostname || device.ip}</p>
          {#if device.hostname}
            <p class="text-sm text-gray-500">{device.ip}</p>
          {/if}
        </div>

        <!-- SSH User -->
        <div>
          <label for="ssh-user" class="block text-sm text-gray-400 mb-1"
            >SSH User</label
          >
          <input
            id="ssh-user"
            type="text"
            bind:value={sshUser}
            placeholder="root"
            class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:ring-1 focus:ring-primary"
            disabled={loading}
            required
          />
        </div>

        <!-- SSH Port -->
        <div>
          <label for="ssh-port" class="block text-sm text-gray-400 mb-1"
            >SSH Port</label
          >
          <input
            id="ssh-port"
            type="number"
            bind:value={sshPort}
            min="1"
            max="65535"
            class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:ring-1 focus:ring-primary"
            disabled={loading}
          />
        </div>

        <!-- Auth Method Toggle -->
        <div>
          <span class="block text-sm text-gray-400 mb-2"
            >Authentication Method</span
          >
          <div class="flex gap-2">
            <button
              type="button"
              class="flex-1 px-3 py-2 rounded-lg text-sm transition-colors {authMethod ===
              'password'
                ? 'bg-primary text-white'
                : 'bg-gray-700 text-gray-300 hover:bg-gray-600'}"
              onclick={() => (authMethod = "password")}
              disabled={loading}
            >
              Password
            </button>
            <button
              type="button"
              class="flex-1 px-3 py-2 rounded-lg text-sm transition-colors {authMethod ===
              'key'
                ? 'bg-primary text-white'
                : 'bg-gray-700 text-gray-300 hover:bg-gray-600'}"
              onclick={() => (authMethod = "key")}
              disabled={loading}
            >
              SSH Key
            </button>
          </div>
        </div>

        <!-- Password Input -->
        {#if authMethod === "password"}
          <div>
            <label for="ssh-password" class="block text-sm text-gray-400 mb-1"
              >Password</label
            >
            <input
              id="ssh-password"
              type="password"
              bind:value={sshPassword}
              placeholder="Enter SSH password"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:ring-1 focus:ring-primary"
              disabled={loading}
            />
          </div>
        {:else}
          <div>
            <label for="ssh-key" class="block text-sm text-gray-400 mb-1"
              >Private Key (PEM)</label
            >
            <textarea
              id="ssh-key"
              bind:value={sshPrivateKey}
              placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;...&#10;-----END OPENSSH PRIVATE KEY-----"
              rows="4"
              class="w-full px-3 py-2 bg-gray-900 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:border-primary focus:ring-1 focus:ring-primary font-mono text-xs"
              disabled={loading}
            ></textarea>
          </div>
        {/if}

        <!-- Error -->
        {#if error}
          <div
            class="p-3 bg-red-900/30 border border-red-700 rounded-lg text-red-200 text-sm"
          >
            {error}
          </div>
        {/if}

        <!-- Actions -->
        <div class="flex gap-3 pt-2">
          <button
            type="button"
            class="flex-1 px-4 py-2 bg-gray-700 hover:bg-gray-600 text-white rounded-lg transition-colors"
            onclick={handleClose}
            disabled={loading}
          >
            Cancel
          </button>
          <button
            type="submit"
            class="flex-1 px-4 py-2 bg-primary hover:bg-primary/90 text-white rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
            disabled={loading}
          >
            {#if loading}
              <span class="flex items-center justify-center gap-2">
                <svg
                  class="w-4 h-4 animate-spin"
                  fill="none"
                  viewBox="0 0 24 24"
                >
                  <circle
                    class="opacity-25"
                    cx="12"
                    cy="12"
                    r="10"
                    stroke="currentColor"
                    stroke-width="4"
                  ></circle>
                  <path
                    class="opacity-75"
                    fill="currentColor"
                    d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
                  ></path>
                </svg>
                Probing...
              </span>
            {:else}
              Start Probe
            {/if}
          </button>
        </div>
      </form>
    </div>
  </div>
{/if}
