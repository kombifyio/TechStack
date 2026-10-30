<script lang="ts">
  import { onMount } from "svelte";
  import { Button, SegmentedControl } from "@kombiverselabs/ui/primitives";
  import { SettingsSection, SettingsRow } from "@kombiverselabs/ui/settings";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    listEdgeDevices,
    readEdgeSettings,
    writeEdgeSettings,
    removeEdgeDevice,
    record,
    reportedOptions,
    supportsDesign,
    supportsSetting,
    firmwareVersionPattern,
    type BoxSettings,
    type EdgeDevice,
    type SettingsRead,
  } from "#lib/api/edgeDevices.js";

  let { device, onremoved }: { device: EdgeDevice; onremoved: () => void } =
    $props();
  // The parent keys the editor by device id, so each mount owns this snapshot.
  // svelte-ignore state_referenced_locally
  let live = $state<EdgeDevice>(device);
  let current = $state<SettingsRead | null>(null);
  let busy = $state(false);
  let message = $state("");
  let pollError = $state(false);
  let pending = $state<{ section: string; key: string; value: unknown } | null>(
    null,
  );
  let confirming = $state(false);
  let invalidPin = $state(false);
  let disposed = false;

  const desired = $derived(current?.document ?? live.settings);
  const rejected = $derived(live.sync?.rejected_revision === desired.revision);
  const waiting = $derived(
    (live.sync?.applied_revision ?? 0) < desired.revision,
  );
  const updatePending = $derived(
    (desired.firmware?.update_request ?? 0) >
      (live.firmware_update?.handled_request ?? 0),
  );
  const updating = $derived(
    ["checking", "installing", "rebooting"].includes(
      live.firmware_update?.state ?? "idle",
    ),
  );
  const supports = (path: string) => supportsSetting(live, path);
  const hasDesign = $derived(supportsDesign(live));
  const hasFirmware = $derived(
    [
      "firmware.channel",
      "firmware.target_version",
      "firmware.auto_update",
      "firmware.update_request",
    ].some(supports),
  );
  const options = $derived({
    phrase: reportedOptions(live, "wake", "phrases").filter((v) =>
      /^[a-z0-9_]{1,15}$/.test(v),
    ),
    voice: reportedOptions(live, "assist", "voices", [
      "auto",
      "female",
      "male",
    ]),
    style: reportedOptions(live, "assist", "sound_cue_styles", [
      "chime",
      "pop",
      "glass",
      "bell",
    ]),
    design: reportedOptions(live, "display", "designs", ["classic", "watch"]),
    locale: reportedOptions(live, "", "locales", [
      "en",
      "de",
      "es",
      "zh-Hans",
      "ar",
      "hi",
    ]),
  });

  function value(section: string, key: string): unknown {
    if (pending?.section === section && pending.key === key)
      return pending.value;
    if (!section) return desired.locale ?? live.reported.locale;
    const part = record(desired[section as keyof BoxSettings]);
    // An explicit null pin follows the channel; it must not revive an old
    // device-reported target while the new revision is still pending.
    return part && Object.hasOwn(part, key)
      ? part[key]
      : record(live.reported[section])?.[key];
  }
  function text(section: string, key: string): string {
    const v = value(section, key);
    return typeof v === "string" || typeof v === "number" ? String(v) : "";
  }
  async function reload() {
    busy = true;
    try {
      const result = await readEdgeSettings(device.device_id);
      if (disposed) return;
      if (result.state === "ready") {
        current = result.value;
        message = "";
      } else {
        current = null;
        message = "box.loadError";
      }
    } finally {
      busy = false;
    }
  }
  async function save(section: string, key: string, next: unknown) {
    if (!current || busy) return;
    if (section === "firmware" && key === "target_version") {
      invalidPin =
        next !== null &&
        (typeof next !== "string" || !firmwareVersionPattern.test(next));
      if (invalidPin) return;
    }
    pending = { section, key, value: next };
    const document = section
      ? {
          ...current.document,
          [section]: {
            ...record(current.document[section as keyof BoxSettings]),
            [key]: next,
          },
        }
      : { ...current.document, [key]: next };
    busy = true;
    message = "";
    try {
      const result = await writeEdgeSettings(
        device.device_id,
        document,
        current.etag,
      );
      if (disposed) return;
      if (result.state === "ready") {
        current = result.value;
        pending = null;
        message = "box.saved";
      } else if (result.status === 400 || result.status === 404) {
        pending = null;
        await reload();
        message = result.status === 400 ? "box.invalidSave" : "box.deviceGone";
      } else if (result.state === "conflict") {
        const fresh = await readEdgeSettings(device.device_id);
        current = fresh.state === "ready" ? fresh.value : null;
        if (section === "firmware" && key === "update_request" && current) {
          pending = null;
          message = "box.updateConflict";
        } else message = "box.conflict";
      } else message = "box.saveError";
    } finally {
      busy = false;
    }
  }
  async function refresh() {
    if (busy) return;
    const result = await listEdgeDevices();
    if (disposed || busy) return;
    const found =
      result.state === "ready"
        ? result.value.find((d) => d.device_id === device.device_id)
        : undefined;
    pollError = !found;
    if (found) {
      live = found;
      if (
        !pending &&
        current &&
        current.document.revision !== found.settings.revision
      )
        await reload();
    }
  }
  async function retry() {
    if (!pending || !current) return;
    if (
      pending.section === "firmware" &&
      pending.key === "update_request" &&
      (current.document.firmware?.update_request ?? 0) >= Number(pending.value)
    ) {
      // A lost response may have committed the request already. Never lower
      // the counter or turn an uncertain response into a second OTA command.
      pending = null;
      message = "box.updateConflict";
      return;
    }
    await save(pending.section, pending.key, pending.value);
  }
  async function updateNow() {
    if (!current || busy || pending || updatePending || updating) return;
    await save(
      "firmware",
      "update_request",
      Math.max(
        desired.firmware?.update_request ?? 0,
        live.firmware_update?.handled_request ?? 0,
      ) + 1,
    );
  }
  async function remove() {
    busy = true;
    const result = await removeEdgeDevice(device.device_id);
    if (!disposed) {
      if (result.state === "ready") onremoved();
      else message = "box.removeError";
      busy = false;
    }
  }
  onMount(() => {
    void reload();
    // Poll only while this sheet is open. No background dashboard watch loop.
    let polling = false;
    const timer = setInterval(() => {
      if (!polling) {
        polling = true;
        void refresh().finally(() => (polling = false));
      }
    }, 15000);
    return () => {
      disposed = true;
      clearInterval(timer);
    };
  });
</script>

{#snippet toggle(section: string, key: string, label: string)}
  {#if supports(`${section}.${key}`)}
    <SettingsRow {label}>
      <SegmentedControl
        class="flex-wrap"
        items={[
          { value: "on", label: tr("box.on") },
          { value: "off", label: tr("box.off") },
        ]}
        value={value(section, key) === true
          ? "on"
          : value(section, key) === false
            ? "off"
            : ""}
        {label}
        onchange={(next) => {
          if (!pending) void save(section, key, next === "on");
        }}
      />
    </SettingsRow>
  {/if}
{/snippet}

{#snippet picker(
  section: string,
  key: string,
  label: string,
  offered: string[],
  translated = true,
)}
  {#if supports(section ? `${section}.${key}` : key)}
    <SettingsRow
      {label}
      description={offered.length ? undefined : tr("box.noOptions")}
    >
      {#if offered.length > 0 && offered.length <= 4 && key !== "locale"}
        <SegmentedControl
          class="flex-wrap"
          items={offered.map((option) => ({
            value: option,
            label: translated ? tr(`box.option.${option}`) : option,
          }))}
          value={text(section, key)}
          {label}
          onchange={(next) => {
            if (!busy && current && !pending) void save(section, key, next);
          }}
        />
      {:else}
        <select
          aria-label={label}
          class="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
          value={text(section, key)}
          disabled={busy || !current || !!pending || !offered.length}
          onchange={(event) =>
            void save(section, key, event.currentTarget.value)}
        >
          <option value="" disabled>{tr("box.choose")}</option>
          {#each offered as option (option)}
            <option value={option}
              >{translated ? tr(`box.option.${option}`) : option}</option
            >
          {/each}
        </select>
      {/if}
    </SettingsRow>
  {/if}
{/snippet}

<div data-testid="box-settings" class="space-y-6">
  <div role="status" aria-live="polite" class="space-y-2 text-sm">
    <p>
      {tr(rejected ? "box.rejected" : waiting ? "box.pending" : "box.applied", {
        revision: desired.revision,
      })}
    </p>
    {#if rejected && live.sync?.reject_reason}<p class="text-warning">
        {live.sync.reject_reason}
      </p>{/if}
    {#if live.sync?.local_pending}<p>{tr("box.localPending")}</p>{/if}
    {#if pollError}<p class="text-warning">{tr("box.loadError")}</p>{/if}
    {#if message}<p>{tr(message)}</p>{/if}
    {#if pending}
      <Button variant="ghost" disabled={busy} onclick={() => void reload()}
        >{tr("box.refresh")}</Button
      >
      <Button disabled={busy || !current} onclick={() => void retry()}
        >{tr("box.retry")}</Button
      >
      <Button
        variant="ghost"
        disabled={busy}
        onclick={() => {
          pending = null;
          void reload();
        }}>{tr("box.discard")}</Button
      >
    {:else}
      <Button
        variant="ghost"
        disabled={busy}
        onclick={() => {
          void reload();
          void refresh();
        }}>{tr("box.refresh")}</Button
      >
    {/if}
  </div>
  <fieldset disabled={busy || !current || !!pending} class="min-w-0">
    <SettingsSection
      id="box-voice"
      title={tr("box.voice")}
      lead={tr("box.pollHint")}
    >
      {@render picker("assist", "voice", tr("box.voice"), options.voice)}
      {@render toggle("assist", "sound_cues", tr("box.cues"))}
      {@render picker(
        "assist",
        "sound_cue_style",
        tr("box.style"),
        options.style,
      )}
      {@render toggle("wake", "enabled", tr("box.wake"))}
      {@render picker(
        "wake",
        "phrase",
        tr("box.phrase"),
        options.phrase,
        false,
      )}
      {#if supports("assist.max_followups")}
        <SettingsRow label={tr("box.followups")}>
          <SegmentedControl
            items={[0, 1, 2, 3].map((n) => ({
              value: String(n),
              label: String(n),
            }))}
            value={text("assist", "max_followups")}
            label={tr("box.followups")}
            onchange={(next) =>
              void save("assist", "max_followups", Number(next))}
          />
        </SettingsRow>
      {/if}
      {#if supports("audio.volume")}
        <SettingsRow label={tr("box.volume")}>
          <input
            type="number"
            min="0"
            max="100"
            step="1"
            aria-label={tr("box.volume")}
            class="w-24 rounded-md border border-input bg-background px-3 py-2"
            value={text("audio", "volume")}
            disabled={busy || !current || !!pending}
            onchange={(event) => {
              if (
                event.currentTarget.value !== "" &&
                event.currentTarget.reportValidity()
              )
                void save("audio", "volume", event.currentTarget.valueAsNumber);
            }}
          />
        </SettingsRow>
      {/if}
      {@render picker("", "locale", tr("box.locale"), options.locale)}
      {@render picker("assist", "route", tr("box.route"), ["fast", "agent"])}
    </SettingsSection>
  </fieldset>
  {#if hasDesign}
    <fieldset disabled={busy || !current || !!pending} class="min-w-0">
      <SettingsSection id="box-display" title={tr("box.display")}>
        {@render picker("display", "design", tr("box.design"), options.design)}
      </SettingsSection>
    </fieldset>
  {/if}
  {#if hasFirmware}
    <fieldset disabled={busy || !current || !!pending} class="min-w-0">
      <SettingsSection
        id="box-firmware"
        title={tr("box.firmware")}
        lead={tr("box.installed", { version: live.firmware_version })}
      >
        {@render picker("firmware", "channel", tr("box.channel"), ["stable"])}
        {#if supports("firmware.target_version")}
          <SettingsRow
            label={tr("box.target")}
            description={tr("box.targetHint")}
          >
            <input
              type="text"
              maxlength="37"
              aria-invalid={invalidPin}
              aria-describedby={invalidPin ? "box-pin-error" : undefined}
              aria-label={tr("box.target")}
              class="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
              value={text("firmware", "target_version")}
              disabled={busy || !current || !!pending}
              onchange={(event) =>
                void save(
                  "firmware",
                  "target_version",
                  event.currentTarget.value.trim() || null,
                )}
            />
            {#if invalidPin}<p
                id="box-pin-error"
                role="alert"
                class="text-sm text-warning"
              >
                {tr("box.invalidPin")}
              </p>{/if}
          </SettingsRow>
        {/if}
        {@render toggle("firmware", "auto_update", tr("box.autoUpdate"))}
        {#if supports("firmware.update_request")}
          <SettingsRow label={tr("box.update")}>
            <div class="space-y-2">
              <Button
                disabled={busy ||
                  !current ||
                  !!pending ||
                  updatePending ||
                  updating}
                onclick={() => void updateNow()}>{tr("box.update")}</Button
              >
              {#if updatePending}<p class="text-sm" role="status">
                  {tr("box.updateQueued")}
                  {tr("box.lastCheckIn", {
                    time: new Date(live.last_seen_at).toLocaleString(),
                  })}
                </p>{/if}
              <p role="status" class="text-sm">
                {tr(`box.state.${live.firmware_update?.state ?? "idle"}`)}
                {live.firmware_update?.version ?? ""}
              </p>
              {#if live.firmware_update?.progress_percent !== undefined && updating}
                <progress
                  aria-label={tr("box.progress")}
                  max="100"
                  value={live.firmware_update.progress_percent}
                ></progress>
                <span class="text-sm"
                  >{live.firmware_update.progress_percent}%</span
                >
              {/if}
              {#if live.firmware_update?.detail}<p
                  class="text-sm text-muted-foreground"
                >
                  {live.firmware_update.detail}
                </p>{/if}
            </div>
          </SettingsRow>
        {/if}
      </SettingsSection>
    </fieldset>
  {/if}
  <SettingsSection
    id="box-remove"
    title={tr("box.remove")}
    tone="danger"
    lead={tr("box.removeHint")}
  >
    {#if confirming}
      <p class="mb-2 text-sm">{tr("box.confirmRemove")}</p>
      <Button disabled={busy} onclick={() => void remove()}
        >{tr("box.confirmRemove")}</Button
      >
      <Button
        variant="ghost"
        disabled={busy}
        onclick={() => (confirming = false)}>{tr("box.cancel")}</Button
      >
    {:else}
      <Button disabled={busy} onclick={() => (confirming = true)}
        >{tr("box.remove")}</Button
      >
    {/if}
  </SettingsSection>
</div>
