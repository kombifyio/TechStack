<script lang="ts">
  import { onMount } from "svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import type { StackConfig } from "#lib/wizard/types.js";
  import type { DiscoveredDevice } from "#lib/discovery/types.js";
  import {
    listSubstrates,
    enableSubstrate,
    substrateProfiles,
    substrateInventory,
    discoveryCapabilities,
    discoveredSubstrateDevices,
    startSubstrateScan,
    substrateScanStatus,
    verifiedProxmoxDevices,
    type SubstrateConnection,
    type SubstrateInventory,
    type SubstrateProfile,
    type DiscoveryCapabilities,
  } from "#lib/api/substrates.js";
  import SubstrateEnrollment from "#lib/components/hub/SubstrateEnrollment.svelte";

  let {
    config = $bindable(),
    deploymentId,
  }: { config: StackConfig; deploymentId?: string } = $props();
  let connections = $state<SubstrateConnection[]>([]);
  let profile = $state<SubstrateProfile>();
  let applianceProfile = $state<SubstrateProfile>();
  let inventory = $state<SubstrateInventory>();
  let capabilities = $state<DiscoveryCapabilities>();
  let candidates = $state<DiscoveredDevice[]>([]);
  let loading = $state(true);
  let scanning = $state(false);
  let error = $state("");
  let scanError = $state("");
  let showEnrollment = $state(false);
  let generation = 0;
  let destroyed = false;
  let scanTimer: ReturnType<typeof setTimeout> | undefined;
  const guest = $derived(config.serverProvisioning.substrate);
  const wantsAppliance = $derived(
    config.goals?.["smart-home"] &&
      config.useCaseSettings?.["smart-home"]?.["operating-form"] === "haos" &&
      config.useCaseSettings?.["smart-home"]?.["instance-origin"] !==
        "existing",
  );
  const applianceStorage = $derived(
    inventory?.storage.filter(
      (item) =>
        item.active === 1 &&
        ["images", "import", "vztmpl"].every((content) =>
          item.content.split(",").includes(content),
        ),
    ) ?? [],
  );
  $effect(() => {
    if (
      wantsAppliance &&
      guest &&
      inventory &&
      applianceProfile &&
      !guest.appliance
    ) {
      guest.appliance = {
        profileId: "haos",
        storage: applianceStorage[0]?.storage ?? "",
        bridge: guest.bridge,
        cpu: applianceProfile.min_cpu,
        memoryMiB: applianceProfile.min_memory_mib,
        diskGiB: applianceProfile.min_disk_gib,
      };
    }
  });
  const storage = $derived(
    inventory?.storage.filter(
      (item) =>
        item.active === 1 &&
        item.content.split(",").includes("images") &&
        item.content.split(",").includes("import") &&
        item.content.split(",").includes("snippets"),
    ) ?? [],
  );
  const bridges = $derived(
    inventory?.networks.filter(
      (item) => item.active === 1 && item.type === "bridge",
    ) ?? [],
  );

  async function load() {
    loading = true;
    error = "";
    if (guest) {
      guest.storage = "";
      guest.bridge = "";
    }
    const results = await Promise.allSettled([
      listSubstrates(),
      substrateProfiles(),
      discoveryCapabilities(),
      discoveredSubstrateDevices(),
    ]);
    if (destroyed) return;
    const [hosts, profiles, discovery, devices] = results;
    applianceProfile =
      profiles.status === "fulfilled"
        ? profiles.value.profiles.find(
            (item) => item.id === "haos" && item.appliance,
          )
        : undefined;
    connections = hosts.status === "fulfilled" ? hosts.value.substrates : [];
    profile =
      profiles.status === "fulfilled"
        ? profiles.value.profiles.find(
            (item) =>
              item.id === "ubuntu-24.04" && !item.appliance && item.cloud_init,
          )
        : undefined;
    capabilities =
      discovery.status === "fulfilled" ? discovery.value : undefined;
    candidates =
      devices.status === "fulfilled"
        ? verifiedProxmoxDevices(devices.value.devices ?? [])
        : [];
    loading = false;
    if (hosts.status === "rejected" || !profile) {
      error = tr("wizard.server.hypervisor.unavailable");
      return;
    }
    if (guest?.serverId) await choose(guest.serverId);
  }

  async function choose(id: string) {
    const request = ++generation;
    inventory = undefined;
    error = "";
    if (!guest) return;
    guest.serverId = id;
    guest.storage = "";
    guest.bridge = "";
    guest.advisoryInventory = undefined;
    guest.appliance = undefined;
    if (!id) return;
    if (
      !profile ||
      !connections.some((item) => item.server_id === id && item.available)
    ) {
      error = tr("wizard.server.hypervisor.unavailable");
      return;
    }
    loading = true;
    try {
      const current = await substrateInventory(id);
      if (destroyed || request !== generation) return;
      inventory = current;
      guest.advisoryInventory = {
        cpu: current.cpu,
        memoryMiB: Math.floor(current.memory_available / 1048576),
        diskGiB: Math.floor(
          Math.max(
            0,
            ...current.storage
              .filter((item) => item.active === 1)
              .map((item) => item.avail),
          ) / 1073741824,
        ),
        hasBridge: current.networks.some(
          (item) => item.active === 1 && item.type === "bridge",
        ),
      };
      guest.storage =
        current.storage.find(
          (item) =>
            item.active === 1 &&
            item.content.split(",").includes("images") &&
            item.content.split(",").includes("import") &&
            item.content.split(",").includes("snippets"),
        )?.storage ?? "";
      guest.bridge =
        current.networks.find(
          (item) => item.active === 1 && item.type === "bridge",
        )?.iface ?? "";
    } catch {
      if (request === generation)
        error = tr("wizard.server.hypervisor.unavailable");
    } finally {
      if (request === generation) loading = false;
    }
  }

  async function scan() {
    if (
      !capabilities?.lan_executor_available ||
      !capabilities.proxmox_fingerprinting ||
      scanning
    )
      return;
    scanning = true;
    scanError = "";
    const started = Date.now();
    const poll = async (id: string) => {
      try {
        const result = await substrateScanStatus(id);
        if (destroyed) return;
        candidates = verifiedProxmoxDevices(result.devices ?? []);
        if (result.status === "completed") {
          scanning = false;
          return;
        }
        if (
          ["failed", "canceled"].includes(result.status) ||
          Date.now() - started > 60000
        )
          throw new Error();
        scanTimer = setTimeout(() => void poll(id), 1500);
      } catch {
        scanning = false;
        scanError = tr("wizard.server.hypervisor.scanFailed");
      }
    };
    try {
      const result = await startSubstrateScan();
      if (!destroyed) await poll(result.scan_id);
    } catch {
      scanning = false;
      scanError = tr("wizard.server.hypervisor.scanFailed");
    }
  }

  async function authorize(host: SubstrateConnection) {
    loading = true;
    error = "";
    try {
      await enableSubstrate(host.server_id, host.revision);
      await load();
      await choose(host.server_id);
    } catch {
      error = tr("wizard.server.hypervisor.authorizeFailed");
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void load();
    return () => {
      destroyed = true;
      generation++;
      clearTimeout(scanTimer);
    };
  });
</script>

<section
  class="space-y-4 py-4"
  aria-label={tr("wizard.server.hypervisor.title")}
>
  <div class="flex items-start justify-between gap-4">
    <p class="text-sm text-muted-foreground">
      {tr("wizard.server.hypervisor.standard")}
    </p>
    <button
      type="button"
      class="text-sm text-primary underline"
      onclick={() => void load()}
      disabled={loading}>{tr("wizard.server.hypervisor.refresh")}</button
    >
  </div>
  {#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
  {#if guest}
    <label class="block text-sm" for="hypervisor-server"
      >{tr("wizard.server.hypervisor.host")}</label
    >
    <select
      id="hypervisor-server"
      class="w-full rounded-lg border border-border bg-input p-2"
      value={guest.serverId}
      onchange={(event) => void choose(event.currentTarget.value)}
      disabled={loading || !profile}
    >
      <option value=""
        >{loading
          ? tr("wizard.server.hypervisor.loading")
          : tr("wizard.server.hypervisor.select")}</option
      >
      {#each connections as host (host.server_id)}<option
          value={host.server_id}
          disabled={!host.available}
          >{host.name || host.node}{!host.available
            ? ` — ${tr("wizard.server.hypervisor.offline")}`
            : ""}</option
        >{/each}
    </select>
    {#each connections.filter((host) => host.connected && !host.enabled) as host (host.server_id)}
      <button
        type="button"
        class="rounded-lg border border-border px-3 py-2 text-sm"
        disabled={loading}
        onclick={() => void authorize(host)}
      >
        {tr("wizard.server.hypervisor.authorize")} · {host.name || host.node}
      </button>
    {/each}
    {#if inventory && profile}
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="text-sm"
          >{tr("wizard.server.hypervisor.storage")}
          <select
            class="mt-1 w-full rounded-lg border border-border bg-input p-2"
            bind:value={guest.storage}
            ><option value="">{tr("wizard.server.hypervisor.select")}</option
            >{#each storage as item}<option value={item.storage}
                >{item.storage} · {Math.floor(item.avail / 1073741824)} GiB</option
              >{/each}</select
          >
        </label>
        <label class="text-sm"
          >{tr("wizard.server.hypervisor.bridge")}
          <select
            class="mt-1 w-full rounded-lg border border-border bg-input p-2"
            bind:value={guest.bridge}
            ><option value="">{tr("wizard.server.hypervisor.select")}</option
            >{#each bridges as bridge}<option value={bridge.iface}
                >{bridge.iface}</option
              >{/each}</select
          >
        </label>
      </div>
      <div class="grid gap-4 sm:grid-cols-3">
        <label class="text-sm"
          >CPU<input
            class="mt-1 w-full rounded-lg border border-border bg-input p-2"
            type="number"
            min={profile.min_cpu}
            max={inventory.cpu}
            step="1"
            bind:value={guest.cpu}
          /></label
        >
        <label class="text-sm"
          >RAM (MiB)<input
            class="mt-1 w-full rounded-lg border border-border bg-input p-2"
            type="number"
            min={profile.min_memory_mib}
            max={Math.floor(inventory.memory_available / 1048576)}
            step="1024"
            bind:value={guest.memoryMiB}
          /></label
        >
        <label class="text-sm"
          >{tr("wizard.server.hypervisor.disk")} (GiB)<input
            class="mt-1 w-full rounded-lg border border-border bg-input p-2"
            type="number"
            min={profile.min_disk_gib}
            step="1"
            bind:value={guest.diskGiB}
          /></label
        >
      </div>
      {#if !storage.length || !bridges.length}<p
          role="status"
          class="text-sm text-warning"
        >
          {tr("wizard.server.hypervisor.resourcesMissing")}
        </p>{/if}
      {#if wantsAppliance && guest.appliance && applianceProfile}
        <fieldset class="border-t border-border pt-4 space-y-3">
          <legend class="font-medium">Home Assistant OS</legend>
          <p class="text-sm text-muted-foreground">
            {tr("wizard.smartHome.haos")}
          </p>
          <div class="grid gap-3 sm:grid-cols-2">
            <label class="text-sm"
              >{tr("wizard.server.hypervisor.storage")}<select
                class="mt-1 w-full rounded-lg border border-border bg-input p-2"
                bind:value={guest.appliance.storage}
                ><option value=""
                  >{tr("wizard.server.hypervisor.select")}</option
                >{#each applianceStorage as item}<option value={item.storage}
                    >{item.storage}</option
                  >{/each}</select
              ></label
            >
            <label class="text-sm"
              >{tr("wizard.server.hypervisor.bridge")}<select
                class="mt-1 w-full rounded-lg border border-border bg-input p-2"
                bind:value={guest.appliance.bridge}
                ><option value=""
                  >{tr("wizard.server.hypervisor.select")}</option
                >{#each bridges as item}<option value={item.iface}
                    >{item.iface}</option
                  >{/each}</select
              ></label
            >
            <label class="text-sm"
              >CPU<input
                class="mt-1 w-full rounded-lg border border-border bg-input p-2"
                type="number"
                min={applianceProfile.min_cpu}
                max={inventory.cpu}
                bind:value={guest.appliance.cpu}
              /></label
            >
            <label class="text-sm"
              >RAM (MiB)<input
                class="mt-1 w-full rounded-lg border border-border bg-input p-2"
                type="number"
                min={applianceProfile.min_memory_mib}
                bind:value={guest.appliance.memoryMiB}
              /></label
            >
            <label class="text-sm"
              >{tr("wizard.server.hypervisor.disk")} (GiB)<input
                class="mt-1 w-full rounded-lg border border-border bg-input p-2"
                type="number"
                min={applianceProfile.min_disk_gib}
                bind:value={guest.appliance.diskGiB}
              /></label
            >
          </div>
        </fieldset>
      {/if}
    {/if}
  {/if}
  {#if config.goals?.["smart-home"]}<p class="text-sm text-muted-foreground">
      {tr("wizard.server.hypervisor.appliance")}
    </p>{/if}
  <div class="space-y-3 border-t border-border pt-4">
    {#if capabilities?.lan_executor_available && capabilities.proxmox_fingerprinting}
      <button
        type="button"
        class="rounded-lg border border-border px-3 py-2 text-sm"
        onclick={() => void scan()}
        disabled={scanning}
        >{scanning
          ? tr("wizard.server.hypervisor.scanning")
          : tr("wizard.server.hypervisor.scan")}</button
      >
    {:else}<p class="text-sm text-muted-foreground">
        {tr("wizard.server.hypervisor.noLan")}
      </p>{/if}
    {#if scanError}<p role="alert" class="text-sm text-destructive">
        {scanError}
      </p>{/if}
    {#if candidates.length}<ul class="space-y-1 text-sm">
        {#each candidates as device}<li>
            {device.hostname || device.ip} · {device.ip} — {tr(
              "wizard.server.hypervisor.detected",
            )}
          </li>{/each}
      </ul>{/if}
    <p class="text-sm text-muted-foreground">
      {tr("wizard.server.hypervisor.discoveryHint")}
    </p>
    <button
      type="button"
      class="text-sm text-primary underline"
      onclick={() => (showEnrollment = !showEnrollment)}
      >{tr("wizard.server.hypervisor.connect")}</button
    >
    {#if showEnrollment}<SubstrateEnrollment {deploymentId} flat />{/if}
  </div>
</section>
