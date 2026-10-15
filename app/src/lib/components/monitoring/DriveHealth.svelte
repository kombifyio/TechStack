<script lang="ts">
  import { untrack } from "svelte";
  import { HardDrive, RefreshCw } from "@lucide/svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import { tr, formatDateTime, formatNumber } from "#lib/i18n.svelte.js";
  import {
    getDriveHealth,
    driveCondition,
    type DriveHealthReport,
    type DriveReading,
    type DriveAlert,
  } from "#lib/api/drive-health.js";

  let {
    serverId,
    refreshKey = 0,
    disconnected = false,
  }: {
    serverId: string;
    refreshKey?: number;
    disconnected?: boolean;
  } = $props();

  let report = $state<DriveHealthReport | null>(null);
  let loading = $state(true);
  let unavailable = $state(false);
  let localRefresh = $state(0);
  let now = $state(Date.now());
  let request = 0;
  let reportScope = "";

  $effect(() => {
    const scope = serverId;
    void refreshKey;
    void localRefresh;
    const current = ++request;
    untrack(() => {
      if (reportScope !== scope) report = null;
      reportScope = scope;
      loading = true;
      if (!scope) {
        unavailable = true;
        loading = false;
        return;
      }
      void getDriveHealth(scope).then(
        (value) => {
          if (current !== request) return;
          report = value;
          unavailable = false;
          loading = false;
          now = Date.now();
        },
        () => {
          if (current !== request) return;
          unavailable = true;
          loading = false;
        },
      );
    });
    return () => {
      request++;
    };
  });

  $effect(() => {
    const timer = setInterval(() => {
      now = Date.now();
    }, 60_000);
    return () => clearInterval(timer);
  });

  const servers = $derived(
    report?.servers.filter((item) => item.server_id === serverId) ?? [],
  );

  function stamp(value?: string): string {
    return value &&
      Number.isFinite(Date.parse(value)) &&
      !value.startsWith("0001-")
      ? formatDateTime(value)
      : tr("storage.unknown");
  }

  function status(value: string): string {
    const key = `storage.drive.state.${value}`;
    const message = tr(key);
    return message === key ? tr("storage.drive.state.unknown") : message;
  }

  function alerts(
    items: DriveAlert[],
    category: DriveAlert["category"],
  ): DriveAlert[] {
    return items.filter(
      (item) => item.category === category && !item.resolved_at,
    );
  }

  function alertMessage(item: DriveAlert, step = false): string {
    if (item.code === "temperature")
      return tr(`storage.drive.${step ? "step" : "alert"}.temperature`);
    if (
      item.code === "error_log_entries" ||
      item.code === "error_log_entries_increase"
    ) {
      return tr(`storage.drive.${step ? "step" : "alert"}.errorLog`);
    }
    const counters = [
      "media_errors",
      "error_log_entries",
      "reallocated_sectors",
      "pending_sectors",
      "uncorrectable_sectors",
    ];
    const increased = item.code.endsWith("_increase");
    const collection = ["collection", "observation", "stale"].includes(
      item.code,
    );
    const hardware =
      increased ||
      [
        "smart_failed",
        "self_test_failed",
        "attribute_failed",
        "nvme_warning",
        "spare",
      ].includes(item.code);
    const base = step
      ? collection
        ? "collection"
        : item.code === "disconnected"
          ? "disconnected"
          : item.code === "wear"
            ? "wear"
            : counters.includes(item.code)
              ? "counter"
              : hardware
                ? "hardware"
                : item.code
      : increased
        ? "counter_increase"
        : counters.includes(item.code)
          ? "counter"
          : item.code === "observation"
            ? "collection"
            : item.code;
    const key = `storage.drive.${step ? "step" : "alert"}.${base}`;
    const message = tr(key);
    return message === key
      ? tr(`storage.drive.${step ? "stepUnknown" : "alertUnknown"}`)
      : message;
  }

  function readings(value: DriveReading): { key: string; value: string }[] {
    const rows: { key: string; value: string }[] = [];
    const number = (key: string, field: number | null, suffix = "") => {
      rows.push({
        key,
        value:
          typeof field === "number" && Number.isFinite(field)
            ? formatNumber(field) + suffix
            : tr("storage.drive.notReported"),
      });
    };
    const counter = (key: string, field: string | null) => {
      rows.push({ key, value: field ?? tr("storage.drive.notReported") });
    };
    rows.push({
      key: "smart",
      value:
        value.smart_passed == null
          ? tr("storage.drive.notReported")
          : tr(`storage.drive.${value.smart_passed ? "passed" : "failed"}`),
    });
    rows.push({
      key: "selfTest",
      value:
        value.self_test_failed == null
          ? tr("storage.drive.notReported")
          : tr(
              `storage.drive.${value.self_test_failed ? "failed" : "selfTestNoFailure"}`,
            ),
    });
    number("temperature", value.temperature_celsius, " °C");
    number("temperatureWarning", value.temperature_warning_celsius, " °C");
    number("temperatureCritical", value.temperature_critical_celsius, " °C");
    if (value.protocol === "NVMe") {
      number("criticalWarning", value.critical_warning);
      number("wear", value.percentage_used, "%");
      number("spare", value.available_spare, "%");
      number("spareThreshold", value.spare_threshold, "%");
      counter("mediaErrors", value.media_errors);
      counter("errorLog", value.error_log_entries);
    }
    if (value.protocol === "ATA") {
      counter("reallocated", value.reallocated_sectors);
      counter("pending", value.pending_sectors);
      counter("uncorrectable", value.uncorrectable_sectors);
    }
    counter("powerOn", value.power_on_hours);
    return rows;
  }

  function nvmeWarnings(bits: number): string[] {
    if (!Number.isInteger(bits) || bits < 0 || bits > 255) return [];
    const meanings = [0, 1, 2, 3, 4, 5]
      .filter((bit) => (bits & (1 << bit)) !== 0)
      .map((bit) => tr(`storage.drive.nvme.bit.${bit}`));
    const unknown = bits & 0xc0;
    if (unknown)
      meanings.push(
        tr("storage.drive.nvme.unknownBits", { bits: formatNumber(unknown) }),
      );
    return meanings;
  }
</script>

{#snippet alertList(items: DriveAlert[], category: DriveAlert["category"])}
  {@const active = alerts(items, category)}
  {#if active.length}
    <div class="mt-4">
      <h4 class="text-sm font-medium">
        {tr(
          `storage.drive.${category === "hardware" ? "alerts" : "collectionErrors"}`,
        )}
      </h4>
      <ul class="mt-2 space-y-2">
        {#each active as alert (alert.id)}
          <li
            class="rounded-lg border p-3 text-sm {alert.severity === 'critical'
              ? 'border-destructive/40 bg-destructive/10'
              : 'border-warning/40 bg-warning/10'}"
            data-alert-category={category}
          >
            <p class="font-medium">
              {tr(
                `storage.drive.severity.${["critical", "warning", "info"].includes(alert.severity) ? alert.severity : "warning"}`,
              )} · {alertMessage(alert)}
            </p>
            {#if alert.drive_id}<p class="mt-1 break-all font-mono text-xs">
                {tr("storage.drive.identity", { identity: alert.drive_id })}
              </p>{/if}
            <dl class="mt-2 grid gap-1 text-xs">
              {#each Object.entries(alert.evidence ?? {}) as [key, value] (key)}<div
                  class="break-all"
                >
                  <dt class="inline font-mono">{key}:</dt>
                  <dd class="inline font-mono">{value}</dd>
                </div>{/each}
            </dl>
            <p class="mt-2">
              {tr("storage.drive.nextStep")}: {alertMessage(alert, true)}
            </p>
            <p class="mt-1 text-xs text-muted-foreground">
              {tr("storage.latest")}: {stamp(alert.last_seen_at)}
            </p>
          </li>
        {/each}
      </ul>
    </div>
  {/if}
{/snippet}

<section
  data-kx="plate"
  class="mb-6 p-5"
  data-testid="drive-health"
  aria-busy={loading}
>
  <div class="flex items-center justify-between gap-3">
    <div class="flex items-center gap-2">
      <HardDrive class="h-5 w-5 text-primary" />
      <h2 class="text-lg font-semibold">{tr("storage.drive.title")}</h2>
    </div>
    <Button
      variant="ghost"
      size="sm"
      disabled={loading}
      onclick={() => localRefresh++}
      testId="drive-health-refresh"
      ><RefreshCw class="h-4 w-4" />{tr(
        "ui.homelabDashboardPage.refresh",
      )}</Button
    >
  </div>
  <p class="mt-2 text-sm text-muted-foreground">{tr("storage.drive.intro")}</p>
  {#if loading}<p class="mt-4 text-sm" role="status">
      {tr("storage.drive.loading")}
    </p>{/if}
  {#if unavailable}<p class="mt-4 text-sm text-warning" role="alert">
      {tr(report ? "storage.drive.retained" : "storage.drive.loadFailed")}
    </p>{/if}
  {#if disconnected}<p class="mt-4 text-sm text-warning" role="status">
      {status("disconnected")}
    </p>{/if}
  {#if !loading && servers.length === 0 && !unavailable}<p class="mt-4 text-sm">
      {tr("storage.drive.noDrives")}
    </p>{/if}
  {#each servers as server (server.server_id)}
    {#if server.collection_status !== "available"}<p
        class="mt-4 text-sm text-warning"
      >
        {status(server.collection_status)}
      </p>{/if}
    <p class="mt-2 text-xs text-muted-foreground">
      {tr("storage.drive.lastAttempt", { when: stamp(server.last_attempt_at) })}
    </p>
    {@render alertList(server.alerts, "collection")}
    {#if server.drives.length === 0}<p class="mt-4 text-sm">
        {tr("storage.drive.noDrives")}
      </p>{/if}
    <div class="mt-4 space-y-4">
      {#each server.drives as drive (drive.drive_id)}
        {@const successful =
          drive.last_success?.drive_id === drive.drive_id
            ? drive.last_success
            : null}
        {@const reading =
          drive.last_attempt?.drive_id === drive.drive_id &&
          ["available", "incomplete"].includes(drive.last_attempt.status)
            ? drive.last_attempt
            : successful}
        {@const condition = driveCondition(
          drive,
          server,
          unavailable,
          disconnected,
          now,
        )}
        <article
          class="rounded-lg border border-border p-4"
          data-drive-id={drive.drive_id}
          data-condition={condition}
        >
          <div class="flex flex-wrap items-start justify-between gap-2">
            <h3 class="font-medium">
              {reading?.model || drive.device_path || drive.drive_id}
            </h3>
            <p
              class="text-sm {condition === 'healthy'
                ? 'text-success'
                : condition === 'critical'
                  ? 'text-destructive'
                  : 'text-warning'}"
            >
              {status(condition)}
            </p>
          </div>
          <p class="mt-2 break-all font-mono text-xs">
            {tr("storage.drive.identity", { identity: drive.drive_id })}
          </p>
          {#if reading}<p class="mt-1 break-all text-xs text-muted-foreground">
              {[
                drive.device_path,
                reading.protocol,
                reading.serial,
                reading.wwn,
              ]
                .filter(Boolean)
                .join(" · ")}
            </p>{/if}
          <p class="mt-2 text-xs text-muted-foreground">
            {tr("storage.drive.latest", {
              when: stamp(successful?.observed_at),
            })}
          </p>
          {#if drive.last_attempt_at}<p
              class="mt-1 text-xs text-muted-foreground"
            >
              {tr("storage.drive.lastAttempt", {
                when: stamp(drive.last_attempt_at),
              })}
            </p>{/if}
          {#if reading}
            <p class="mt-1 text-xs text-muted-foreground">
              {tr("storage.drive.readingAt", {
                when: stamp(reading.observed_at),
              })}
            </p>
            <p class="mt-1 text-xs text-muted-foreground">
              {tr("storage.source", { source: reading.source })}
            </p>
            <details class="mt-3" open={condition !== "healthy"}>
              <summary class="cursor-pointer text-sm font-medium"
                >{tr("storage.drive.evidence")}</summary
              >
              <dl class="mt-3 grid gap-3 sm:grid-cols-2">
                {#each readings(reading) as item (item.key)}<div>
                    <dt class="text-xs text-muted-foreground">
                      {tr(`storage.drive.${item.key}`)}
                    </dt>
                    <dd class="mt-1 break-all font-mono text-sm">
                      {item.value}
                    </dd>
                  </div>{/each}
              </dl>
              {#if reading.protocol === "NVMe" && typeof reading.critical_warning === "number" && reading.critical_warning > 0}
                <ul
                  class="mt-3 list-disc space-y-1 ps-5 text-xs"
                  data-testid="nvme-warning-meanings"
                >
                  {#each nvmeWarnings(reading.critical_warning) as warning (warning)}<li
                    >
                      {warning}
                    </li>{/each}
                </ul>
              {/if}
              {#if reading.protocol === "NVMe" && reading.percentage_used != null}<p
                  class="mt-3 text-xs text-muted-foreground"
                >
                  {tr("storage.drive.wearExplanation")}
                </p>{/if}
              {#if reading.protocol === "ATA"}
                <h4 class="mt-3 text-sm font-medium">
                  {tr("storage.drive.attributes")}
                </h4>
                <dl class="mt-2 space-y-2 text-xs">
                  {#if !reading.wear_attributes?.length}<p>
                      {tr("storage.drive.notReported")}
                    </p>{/if}
                  {#each reading.wear_attributes as attribute (attribute.id)}
                    <div>
                      <dt class="font-mono">
                        {attribute.id} · {attribute.name}
                      </dt>
                      <dd class="mt-1">
                        {tr("storage.drive.vendorAttribute", {
                          raw: attribute.raw ?? tr("storage.drive.notReported"),
                          value:
                            attribute.value == null
                              ? tr("storage.drive.notReported")
                              : formatNumber(attribute.value),
                        })}
                      </dd>
                    </div>
                  {/each}
                </dl>
              {/if}
              {#if reading.protocol === "ATA"}<p
                  class="mt-3 text-xs text-muted-foreground"
                >
                  {tr("storage.drive.ataExplanation")}
                </p>{/if}
              {#if reading.missing?.length}<p class="mt-3 text-xs">
                  {tr("storage.drive.state.incomplete")}:
                  <span class="font-mono">{reading.missing.join(", ")}</span>
                </p>{/if}
            </details>
          {:else}<p class="mt-3 text-sm text-muted-foreground">
              {tr("storage.drive.noReadings")}
            </p>{/if}
          {@render alertList(drive.alerts, "hardware")}
          {@render alertList(drive.alerts, "collection")}
        </article>
      {/each}
    </div>
  {/each}
</section>
