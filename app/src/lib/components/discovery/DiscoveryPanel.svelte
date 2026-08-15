<!--
  DiscoveryPanel Component
  
  Main component for network discovery functionality.
  Can be used in both Dashboard and Wizard contexts.
  
  Props:
  - mode: 'view' (default) or 'select' - selection mode for wizard
  - selectable: Enable device selection
  - multiSelect: Allow multiple device selection
  - autoScan: Automatically start scan on mount
  - showProbeButton: Show deep probe button on devices
  - onselect: Callback when selection changes
-->
<script lang="ts">
  import { onMount } from "svelte";
  import type { DiscoveredDevice, ScanProfile } from "$lib/discovery";
  import { getDiscoveryStore } from "$lib/discovery";
  import { authStore } from "$lib/stores/auth.svelte";
  import DeviceCard from "./DeviceCard.svelte";
  import ProbeDialog from "./ProbeDialog.svelte";
  import AnimatedList from "$lib/components/ui/AnimatedList.svelte";
  import { noticeInApp } from "$lib/dialogs/in-app-dialog";

  // Scan step type for progress tracking
  interface ScanStep {
    id: string;
    label: string;
    status: "pending" | "running" | "completed" | "failed";
    message?: string;
  }

  interface Props {
    mode?: "view" | "select";
    selectable?: boolean;
    multiSelect?: boolean;
    autoScan?: boolean;
    showProbeButton?: boolean;
    compact?: boolean;
    onselect?: (devices: DiscoveredDevice[]) => void;
  }

  let {
    mode = "view",
    selectable = false,
    multiSelect = true,
    autoScan = false,
    showProbeButton = true,
    compact = false,
    onselect,
  }: Props = $props();

  const store = getDiscoveryStore();

  // Probe dialog state
  let probeDialogOpen = $state(false);
  let probeDialogDevice = $state<DiscoveredDevice | null>(null);
  let probeDialogLoading = $state(false);
  let probeDialogError = $state<string | null>(null);

  // Scan profile selection
  let scanProfile = $state<ScanProfile>("active");

  // Manual subnet and experimental flags
  let manualSubnet = $state("");
  let detectTailscale = $state(false);
  let useMdns = $state(false);

  // Scan steps for progress visualization
  let scanSteps = $state<ScanStep[]>([]);

  // Computed
  const isSelectable = $derived(mode === "select" || selectable);

  // Initialize scan steps
  function initScanSteps(): ScanStep[] {
    return [
      { id: "init", label: "Initializing network scan...", status: "pending" },
      {
        id: "networks",
        label: "Detecting network interfaces",
        status: "pending",
      },
      {
        id: "arp",
        label: "Scanning ARP table for active hosts",
        status: "pending",
      },
      {
        id: "ports",
        label: "Checking common service ports",
        status: "pending",
      },
      { id: "identify", label: "Identifying device types", status: "pending" },
      { id: "complete", label: "Scan complete", status: "pending" },
    ];
  }

  // Update scan step based on progress
  function updateScanProgress(
    progress: number,
    scannedHosts: number,
    totalHosts: number,
  ) {
    const steps = [...scanSteps];

    // Step 1: Init (0-5%)
    if (progress >= 0) {
      steps[0].status = progress >= 5 ? "completed" : "running";
      steps[0].message = progress >= 5 ? undefined : "Starting scan engine...";
    }

    // Step 2: Networks (5-15%)
    if (progress >= 5) {
      steps[1].status = progress >= 15 ? "completed" : "running";
      steps[1].message =
        progress >= 15
          ? `Found ${store.networks.length} interface(s)`
          : "Enumerating interfaces...";
    }

    // Step 3: ARP scan (15-50%)
    if (progress >= 15) {
      steps[2].status = progress >= 50 ? "completed" : "running";
      steps[2].message =
        progress >= 50
          ? `Scanned ${totalHosts} hosts`
          : `Scanning hosts... (${scannedHosts}/${totalHosts})`;
    }

    // Step 4: Port scanning (50-80%)
    if (progress >= 50) {
      steps[3].status = progress >= 80 ? "completed" : "running";
      const deviceCount = store.devices.length;
      steps[3].message =
        progress >= 80
          ? `Found ${deviceCount} device(s) with open ports`
          : `Checking ports on ${deviceCount} device(s)...`;
    }

    // Step 5: Identification (80-95%)
    if (progress >= 80) {
      steps[4].status = progress >= 95 ? "completed" : "running";
      steps[4].message =
        progress >= 95
          ? "Device roles assigned"
          : "Analyzing device signatures...";
    }

    // Step 6: Complete (95-100%)
    if (progress >= 95) {
      steps[5].status = progress >= 100 ? "completed" : "running";
      steps[5].message =
        progress >= 100
          ? `Discovered ${store.devices.length} device(s)`
          : "Finalizing results...";
    }

    scanSteps = steps;
  }

  onMount(async () => {
    // Load networks on mount
    await store.loadNetworks();

    // Load cached devices
    await store.loadDevices();

    // Auto-scan if enabled and no devices cached
    if (autoScan && store.deviceCount === 0) {
      startSubnetScan();
    }
  });

  async function startSubnetScan() {
    const req = {
      profile: scanProfile,
      subnet: manualSubnet?.trim() || undefined,
      detect_tailscale: detectTailscale || undefined,
      mdns: useMdns || undefined,
    };
    // basic validation
    if (req.subnet && !req.subnet.includes("/")) {
      await noticeInApp({
        title: "Invalid network range",
        message: "Enter a CIDR such as 192.168.178.0/24.",
        tone: "warning",
      });
      return;
    }

    scanSteps = initScanSteps();
    scanSteps[0].status = "running";
    scanSteps[0].message = "Starting network scan...";

    const scanPromise = store.startScan(req);

    const progressInterval = setInterval(() => {
      if (store.isScanning && store.currentScan) {
        const { progress, scanned_hosts, total_hosts } = store.currentScan;
        updateScanProgress(progress, scanned_hosts, total_hosts);
      }
    }, 200);

    await scanPromise;
    clearInterval(progressInterval);

    if (store.scanError && !store.scancanceled) {
      const failedIndex = scanSteps.findIndex((s) => s.status === "running");
      if (failedIndex >= 0) {
        scanSteps[failedIndex].status = "failed";
        scanSteps[failedIndex].message = store.scanError;
      }
    } else if (!store.scancanceled) {
      updateScanProgress(
        100,
        store.currentScan?.total_hosts ?? 0,
        store.currentScan?.total_hosts ?? 0,
      );
    }

    if (onselect) {
      onselect(store.selectedDevices);
    }
  }

  function handleCancelScan() {
    store.cancelScan();
    // Mark current running step as canceled
    const runningIndex = scanSteps.findIndex((s) => s.status === "running");
    if (runningIndex >= 0) {
      scanSteps[runningIndex].status = "failed";
      scanSteps[runningIndex].message = "canceled by user";
    }
  }

  function handleDeviceSelect(device: DiscoveredDevice) {
    if (multiSelect) {
      store.toggleDevice(device.device_id);
    } else {
      store.selectSingle(device.device_id);
    }

    if (onselect) {
      onselect(store.selectedDevices);
    }
  }

  function handleProbeClick(device: DiscoveredDevice) {
    probeDialogDevice = device;
    probeDialogError = null;
    probeDialogOpen = true;
  }

  async function handleProbeSubmit(credentials: {
    ssh_user: string;
    ssh_password?: string;
    ssh_private_key?: string;
    ssh_port: number;
  }) {
    if (!probeDialogDevice) return;

    probeDialogLoading = true;
    probeDialogError = null;

    await store.probeDevice(probeDialogDevice.device_id, credentials);

    if (store.probeError) {
      probeDialogError = store.probeError;
      probeDialogLoading = false;
    } else {
      probeDialogLoading = false;
      probeDialogOpen = false;
      probeDialogDevice = null;
    }
  }

  function handleProbeClose() {
    probeDialogOpen = false;
    probeDialogDevice = null;
    probeDialogError = null;
  }

  function handleClearCache() {
    store.clearCache();
  }

  function enrichCurrentScan() {
    store.enrichCurrentScan();
  }
</script>

<div class="discovery-panel space-y-4" data-testid="discovery-panel">
  <!-- Header with Scan Controls -->
  <div class="flex flex-wrap items-center justify-between gap-4">
    <div>
      <h3 class="text-lg font-semibold text-white">Network Discovery</h3>
      <p class="text-sm text-gray-400">
        {#if store.isScanning}
          Scanning network... {store.scanProgress}%
        {:else if store.deviceCount > 0}
          Found {store.deviceCount} device{store.deviceCount !== 1 ? "s" : ""}
          {#if store.hasSelection}
            · {store.selectedDeviceIds.size} selected
          {/if}
        {:else}
          Scan your local network to discover available servers
        {/if}
      </p>
    </div>

    <div class="flex items-center gap-2">
      <!-- Scan Profile Select -->
      {#if !compact}
        <select
          bind:value={scanProfile}
          class="px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm disabled:opacity-50 disabled:cursor-not-allowed"
          disabled={store.isScanning}
        >
          <option value="passive">Passive (fast)</option>
          <option value="active">Active (recommended)</option>
          <option value="deep">Deep (slow)</option>
        </select>
      {/if}

      <!-- Scan/Cancel Button -->
      {#if store.isScanning}
        <button
          class="px-4 py-2 bg-red-600 hover:bg-red-500 text-white rounded-lg transition-colors flex items-center gap-2"
          onclick={handleCancelScan}
        >
          <svg
            class="w-4 h-4"
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
          Cancel Scan
        </button>
      {:else}
        <button
          class="px-4 py-2 bg-primary hover:bg-primary/90 text-white rounded-lg transition-colors disabled:opacity-50 disabled:cursor-not-allowed flex items-center gap-2"
          onclick={startSubnetScan}
        >
          <svg
            class="w-4 h-4"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
            />
          </svg>
          Scan Network
        </button>
      {/if}

      <!-- Clear Cache Button -->
      {#if store.deviceCount > 0 && !compact && authStore.isAdmin}
        <button
          class="px-3 py-2 bg-gray-700 hover:bg-gray-600 text-white rounded-lg transition-colors text-sm"
          onclick={handleClearCache}
          disabled={store.isScanning}
        >
          Clear
        </button>
      {/if}
    </div>
  </div>

  <!-- Progress Bar (while scanning) -->
  {#if store.isScanning && store.currentScan}
    <div class="relative h-2 bg-gray-700 rounded-full overflow-hidden">
      <div
        class="absolute h-full bg-primary transition-all duration-300"
        style="width: {store.scanProgress}%"
      ></div>
    </div>
  {/if}

  <!-- Detailed Scan Status Panel -->
  {#if store.isScanning || store.scanStatus.phase === "failed" || store.scanStatus.phase === "complete"}
    <div class="bg-gray-800/50 rounded-lg border border-gray-700 p-4">
      <div class="flex items-center justify-between mb-3">
        <h4 class="text-sm font-medium text-gray-300">Scan Progress</h4>
        {#if store.isScanning}
          <span class="text-xs text-primary animate-pulse">
            {#if store.scanStatus.elapsedMs}
              {Math.round(store.scanStatus.elapsedMs / 1000)}s elapsed
            {:else}
              Scanning...
            {/if}
          </span>
        {/if}
      </div>

      <!-- Current Status -->
      <div class="flex items-start gap-3">
        <!-- Status Icon -->
        <div class="flex-shrink-0 mt-0.5">
          {#if store.scanStatus.phase === "failed"}
            <svg
              class="w-5 h-5 text-red-400"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
              />
            </svg>
          {:else if store.scanStatus.phase === "complete"}
            <svg
              class="w-5 h-5 text-green-400"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="2"
                d="M5 13l4 4L19 7"
              />
            </svg>
          {:else if store.scanStatus.phase === "canceled"}
            <svg
              class="w-5 h-5 text-yellow-400"
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
          {:else}
            <svg
              class="w-5 h-5 text-primary animate-spin"
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
          {/if}
        </div>

        <!-- Status Content -->
        <div class="flex-1 min-w-0">
          <p
            class="text-sm font-medium {store.scanStatus.phase === 'failed'
              ? 'text-red-300'
              : store.scanStatus.phase === 'complete'
                ? 'text-green-300'
                : store.scanStatus.phase === 'canceled'
                  ? 'text-yellow-300'
                  : 'text-primary'}"
          >
            {store.scanStatus.message}
          </p>
          {#if store.scanStatus.detail}
            <p class="text-xs text-gray-500 mt-1 font-mono break-all">
              {store.scanStatus.detail}
            </p>
          {/if}
        </div>
      </div>

      <!-- Phase indicator -->
      {#if store.isScanning}
        <div class="mt-3 pt-3 border-t border-gray-700">
          <div class="flex flex-wrap gap-2 text-xs">
            {#each ["connecting", "loading-networks", "starting-scan", "waiting-response", "polling"] as phase}
              <span
                class="px-2 py-1 rounded {store.scanStatus.phase === phase
                  ? 'bg-primary text-white'
                  : [
                        'connecting',
                        'loading-networks',
                        'starting-scan',
                        'waiting-response',
                        'polling',
                      ].indexOf(store.scanStatus.phase) >
                      [
                        'connecting',
                        'loading-networks',
                        'starting-scan',
                        'waiting-response',
                        'polling',
                      ].indexOf(phase)
                    ? 'bg-green-900/50 text-green-400'
                    : 'bg-gray-700 text-gray-500'}"
              >
                {phase.replace(/-/g, " ")}
              </span>
            {/each}
          </div>
        </div>
      {/if}

      <!-- ARP Enrichment / Suggestions -->
      {#if store.currentScan}
        <div
          class="mt-3 pt-3 border-t border-gray-700 flex items-center justify-between"
        >
          <div class="text-xs text-gray-400">
            If you're seeing very few devices, try enabling mDNS or running ARP
            enrichment to fill MAC addresses.
          </div>
          <div class="flex items-center gap-2">
            <button
              class="px-3 py-1 bg-gray-700 hover:bg-gray-600 text-white rounded text-sm"
              onclick={enrichCurrentScan}
              disabled={store.isScanning}
            >
              Run ARP enrichment
            </button>
          </div>
        </div>
      {/if}
    </div>
  {/if}

  <!-- Legacy Scan Steps Progress (AnimatedList) - keep for additional detail -->
  {#if scanSteps.length > 0 && scanSteps.some((s) => s.status !== "pending") && store.scanProgress > 10}
    <div class="bg-gray-800/50 rounded-lg border border-gray-700 p-4">
      <div class="flex items-center justify-between mb-3">
        <h4 class="text-sm font-medium text-gray-300">Scan Progress</h4>
        {#if store.isScanning}
          <span class="text-xs text-primary animate-pulse">Scanning...</span>
        {/if}
      </div>
      <AnimatedList
        items={scanSteps.filter((s) => s.status !== "pending")}
        delay={150}
      >
        {#snippet children({ item })}
          {@const step = item as ScanStep}
          <div class="flex items-start gap-3">
            <!-- Status Icon -->
            <div class="flex-shrink-0 mt-0.5">
              {#if step.status === "running"}
                <svg
                  class="w-5 h-5 text-primary animate-spin"
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
              {:else if step.status === "completed"}
                <svg
                  class="w-5 h-5 text-green-400"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    stroke-width="2"
                    d="M5 13l4 4L19 7"
                  />
                </svg>
              {:else if step.status === "failed"}
                <svg
                  class="w-5 h-5 text-red-400"
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
              {:else}
                <div
                  class="w-5 h-5 rounded-full border-2 border-gray-600"
                ></div>
              {/if}
            </div>

            <!-- Step Content -->
            <div class="flex-1 min-w-0">
              <p
                class="text-sm font-medium {step.status === 'running'
                  ? 'text-primary'
                  : step.status === 'completed'
                    ? 'text-green-300'
                    : step.status === 'failed'
                      ? 'text-red-300'
                      : 'text-gray-400'}"
              >
                {step.label}
              </p>
              {#if step.message}
                <p class="text-xs text-gray-500 mt-0.5">{step.message}</p>
              {/if}
            </div>
          </div>
        {/snippet}
      </AnimatedList>
    </div>
  {/if}

  <!-- Error Display with Details -->
  {#if store.scanError && store.scanStatus.phase === "failed"}
    <div class="p-4 bg-red-900/30 border border-red-700 rounded-lg">
      <div class="flex items-start gap-3">
        <svg
          class="w-5 h-5 text-red-400 flex-shrink-0 mt-0.5"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
          />
        </svg>
        <div class="flex-1">
          <p class="font-medium text-red-200">Scan Failed</p>
          <p class="text-sm text-red-300 mt-1">{store.scanError}</p>
          {#if store.scanStatus.detail}
            <p class="text-xs text-red-400/70 mt-2 font-mono">
              {store.scanStatus.detail}
            </p>
          {/if}
          <div class="mt-3 text-xs text-red-300/70">
            <p class="font-medium mb-1">Troubleshooting:</p>
            <ul class="list-disc list-inside space-y-1">
              {#if store.scanError.includes("connect") || store.scanError.includes("Network")}
                <li>
                  Ensure the backend is running: <code
                    class="bg-red-900/50 px-1 rounded">docker compose up</code
                  >
                </li>
                <li>Check if port 5260 is accessible</li>
              {:else if store.scanError.includes("timeout") || store.scanError.includes("Timeout")}
                <li>The subnet may be too large (e.g., /16 = 65,000 hosts)</li>
                <li>Try "Passive" mode for faster scans</li>
                <li>Specify a smaller subnet in settings</li>
              {:else}
                <li>Check the browser console for details</li>
                <li>Try refreshing the page</li>
              {/if}
            </ul>
          </div>
        </div>
      </div>
    </div>
  {/if}

  <!-- Networks Info (collapsed by default) -->
  {#if !compact}
    <details class="bg-gray-800/50 rounded-lg border border-gray-700">
      <summary
        class="px-4 py-3 text-sm text-gray-400 cursor-pointer hover:text-gray-300 flex items-center justify-between"
      >
        <span>Available Networks ({store.networks.length})</span>
        <div class="flex items-center gap-2">
          <button
            class="px-2 py-1 text-xs bg-gray-700 rounded text-white"
            onclick={store.loadNetworks}>Refresh</button
          >
          <button
            class="px-2 py-1 text-xs bg-gray-700 rounded text-white"
            onclick={() => {
              store.loadNetworks();
              store.loadDevices();
            }}>Refresh & Devices</button
          >
        </div>
      </summary>
      <div class="px-4 pb-3 grid gap-2">
        {#each store.networks as network}
          <div class="flex items-center gap-3 text-sm">
            <span class="text-white font-mono">{network.name}</span>
            <span class="text-gray-500">{network.subnets.join(", ")}</span>
          </div>
        {/each}

        <!-- Manual subnet input for scanning remote subnets -->
        <div class="pt-2">
          <label for="manual-subnet" class="text-xs text-gray-400"
            >Manual subnet (CIDR)</label
          >
          <div class="flex gap-2 mt-1">
            <input
              id="manual-subnet"
              type="text"
              bind:value={manualSubnet}
              placeholder="e.g., 192.168.178.0/24"
              class="px-3 py-2 bg-gray-900 border border-gray-700 rounded text-white text-sm w-full"
            />
            <button
              class="px-3 py-2 bg-primary text-white rounded"
              onclick={() => startSubnetScan()}>Scan</button
            >
          </div>
          <div class="flex items-center gap-3 mt-2 text-xs text-gray-400">
            <label class="inline-flex items-center gap-2"
              ><input type="checkbox" bind:checked={detectTailscale} /> Detect Tailscale</label
            >
            <label class="inline-flex items-center gap-2"
              ><input type="checkbox" bind:checked={useMdns} /> Use mDNS (experimental)</label
            >
          </div>
        </div>
      </div>
    </details>
  {/if}

  <!-- Device List -->
  {#if store.devices.length > 0}
    <div class="space-y-3">
      {#each store.devices as device (device.device_id)}
        <DeviceCard
          {device}
          selected={store.selectedDeviceIds.has(device.device_id)}
          selectable={isSelectable}
          {showProbeButton}
          onselect={handleDeviceSelect}
          onprobe={handleProbeClick}
        />
      {/each}
    </div>
  {:else if !store.isScanning}
    <!-- Empty State -->
    <div class="text-center py-8">
      <div class="text-gray-500 mb-2">
        <svg
          class="w-12 h-12 mx-auto"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="1.5"
            d="M9.75 17L9 20l-1 1h8l-1-1-.75-3M3 13h18M5 17h14a2 2 0 002-2V5a2 2 0 00-2-2H5a2 2 0 00-2 2v10a2 2 0 002 2z"
          />
        </svg>
      </div>
      <p class="text-gray-400">No devices discovered yet</p>
      <p class="text-sm text-gray-500 mt-1">
        Click "Scan Network" to discover devices in your local network
      </p>
    </div>
  {/if}

  <!-- Selection Actions (for wizard mode) -->
  {#if isSelectable && store.hasSelection && !compact}
    <div
      class="flex items-center justify-between p-4 bg-primary/10 border border-primary/40 rounded-lg"
    >
      <span class="text-primary">
        {store.selectedDeviceIds.size} device{store.selectedDeviceIds.size !== 1
          ? "s"
          : ""} selected
      </span>
      <div class="flex gap-2">
        <button
          class="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded transition-colors"
          onclick={() => store.clearSelection()}
        >
          Clear Selection
        </button>
        {#if multiSelect}
          <button
            class="px-3 py-1.5 text-sm bg-gray-700 hover:bg-gray-600 text-white rounded transition-colors"
            onclick={() => store.selectAll()}
          >
            Select All
          </button>
        {/if}
      </div>
    </div>
  {/if}
</div>

<!-- Probe Dialog -->
<ProbeDialog
  device={probeDialogDevice}
  open={probeDialogOpen}
  loading={probeDialogLoading}
  error={probeDialogError}
  onsubmit={handleProbeSubmit}
  onclose={handleProbeClose}
/>
