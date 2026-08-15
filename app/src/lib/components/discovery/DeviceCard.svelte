<!--
  DeviceCard Component
  
  Displays a single discovered device with its details.
  Supports selection mode for wizard integration.
-->
<script lang="ts">
  import type { DiscoveredDevice } from "$lib/discovery";
  import {
    formatConfidence,
    getConfidenceColor,
    getPrimaryRole,
    getRoleDisplayName,
    getProbeStatusDisplay,
    hasSSH,
    hasDocker,
  } from "$lib/discovery";

  interface Props {
    device: DiscoveredDevice;
    selected?: boolean;
    selectable?: boolean;
    showProbeButton?: boolean;
    onselect?: (device: DiscoveredDevice) => void;
    onprobe?: (device: DiscoveredDevice) => void;
  }

  let {
    device,
    selected = false,
    selectable = false,
    showProbeButton = false,
    onselect,
    onprobe,
  }: Props = $props();

  const probeStatus = $derived(getProbeStatusDisplay(device.probe_status));
  const primaryRole = $derived(getPrimaryRole(device));
  const roleDisplay = $derived(getRoleDisplayName(primaryRole));
  const confidenceColor = $derived(getConfidenceColor(device.confidence));

  function handleClick() {
    if (selectable && onselect) {
      onselect(device);
    }
  }

  function handleProbe(e: Event) {
    e.stopPropagation();
    if (onprobe) {
      onprobe(device);
    }
  }
</script>

<div
  class="device-card p-4 bg-gray-800/50 rounded-lg border transition-all {selected
    ? 'border-primary bg-primary/10'
    : 'border-gray-700 hover:border-gray-600'}"
>
  <div class="flex items-start justify-between gap-4">
    <!-- Left: Selection indicator + main info -->
    <div class="flex items-start gap-3">
      {#if selectable}
        <div class="mt-1">
          <input
            type="checkbox"
            checked={selected}
            class="rounded border-gray-600 text-primary focus:ring-primary"
            onchange={handleClick}
            aria-label="Select {device.hostname || device.ip}"
          />
        </div>
      {/if}

      <div class="flex-1">
        <!-- Hostname / IP -->
        <div class="flex items-center gap-2">
          <h4 class="font-medium text-white">
            {device.hostname || device.ip}
          </h4>
          {#if device.hostname}
            <span class="text-sm text-gray-500">{device.ip}</span>
          {/if}
        </div>

        <!-- MAC Address -->
        {#if device.mac}
          <p class="text-xs text-gray-500 font-mono mt-0.5">{device.mac}</p>
        {/if}

        <!-- Services -->
        <div class="flex flex-wrap gap-1.5 mt-2">
          {#if hasSSH(device)}
            <span
              class="px-2 py-0.5 text-xs bg-green-900/50 text-green-300 rounded"
            >
              SSH
            </span>
          {/if}
          {#if hasDocker(device)}
            <span
              class="px-2 py-0.5 text-xs bg-blue-900/50 text-blue-300 rounded"
            >
              Docker
            </span>
          {/if}
          {#each (device.services || []).slice(0, 4) as service}
            {#if service.name !== "ssh" && service.name !== "docker"}
              <span
                class="px-2 py-0.5 text-xs bg-gray-700 text-gray-300 rounded"
              >
                {service.name}:{service.port}
              </span>
            {/if}
          {/each}
          {#if (device.services?.length || 0) > 6}
            <span class="px-2 py-0.5 text-xs bg-gray-700 text-gray-400 rounded">
              +{(device.services?.length || 0) - 6} more
            </span>
          {/if}
        </div>

        <!-- System info (if deep probed) -->
        {#if device.system}
          <div class="flex flex-wrap gap-3 mt-2 text-xs text-gray-400">
            {#if device.system.distribution}
              <span
                >{device.system.distribution}
                {device.system.version || ""}</span
              >
            {/if}
            {#if device.system.cpu_cores}
              <span>{device.system.cpu_cores} cores</span>
            {/if}
            {#if device.system.memory_mb}
              <span>{Math.round(device.system.memory_mb / 1024)} GB RAM</span>
            {/if}
            {#if device.system.disk_gb}
              <span>{device.system.disk_gb} GB disk</span>
            {/if}
          </div>
        {/if}
      </div>
    </div>

    <!-- Right: Role + Confidence + Actions -->
    <div class="flex flex-col items-end gap-2">
      <!-- Role badge -->
      <span
        class="px-2 py-1 text-xs font-medium rounded {primaryRole === 'main'
          ? 'bg-primary/20 text-primary'
          : primaryRole === 'worker'
            ? 'bg-primary/10 text-primary'
            : primaryRole === 'storage'
              ? 'bg-orange-900/50 text-orange-300'
              : 'bg-gray-700 text-gray-400'}"
      >
        {roleDisplay}
      </span>

      <!-- Confidence -->
      <span class="text-xs {confidenceColor}">
        {formatConfidence(device.confidence)} confidence
      </span>

      <!-- Probe status -->
      <span class="text-xs {probeStatus.color}">
        {probeStatus.label}
      </span>

      <!-- Probe button -->
      {#if showProbeButton && hasSSH(device) && device.probe_status !== "done"}
        <button
          class="mt-1 px-2 py-1 text-xs bg-gray-700 hover:bg-gray-600 text-white rounded transition-colors"
          onclick={handleProbe}
        >
          Deep Probe
        </button>
      {/if}
    </div>
  </div>

  <!-- Probe error -->
  {#if device.probe_error}
    <div class="mt-2 p-2 text-xs bg-red-900/30 text-red-300 rounded">
      {device.probe_error}
    </div>
  {/if}
</div>
