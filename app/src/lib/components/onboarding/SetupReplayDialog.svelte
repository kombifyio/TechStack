<script lang="ts">
  import { onMount } from "svelte";
  import { X } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    hasDeviceContextBridge,
    readDeviceContext,
    type DeviceContext,
  } from "#lib/client/device-context.js";
  import { windowsClientModeLabel } from "#lib/client/windows-onboarding.js";

  let {
    onContinue,
    onDismiss,
  }: { onContinue: () => void; onDismiss: () => void } = $props();
  let context = $state<DeviceContext | null>(null);
  let loading = $state(true);
  let native = $state(false);
  let origin = $state("");
  let dialog = $state<HTMLDivElement | null>(null);

  function onKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") onDismiss();
    if (event.key !== "Tab" || !dialog) return;
    const buttons = dialog.querySelectorAll<HTMLButtonElement>(
      "button:not(:disabled)",
    );
    const first = buttons[0];
    const last = buttons[buttons.length - 1];
    if (
      event.shiftKey &&
      (document.activeElement === first || document.activeElement === dialog)
    ) {
      event.preventDefault();
      last?.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first?.focus();
    }
  }

  onMount(() => {
    const controller = new AbortController();
    const previousFocus = document.activeElement;
    native = hasDeviceContextBridge();
    origin = window.location.origin;
    dialog?.focus();
    void readDeviceContext(controller.signal).then((snapshot) => {
      if (controller.signal.aborted) return;
      context = snapshot;
      loading = false;
    });
    return () => {
      controller.abort();
      if (previousFocus instanceof HTMLElement && previousFocus.isConnected)
        previousFocus.focus();
    };
  });
</script>

<div
  class="fixed inset-0 z-[100] grid place-items-center p-4"
  data-finish="kombify"
>
  <div data-kx="scrim" aria-hidden="true"></div>
  <div
    bind:this={dialog}
    data-kx="menu-plate"
    class="relative w-full max-w-lg rounded-2xl p-6"
    role="dialog"
    aria-modal="true"
    aria-labelledby="setup-replay-title"
    tabindex="-1"
    onkeydown={onKeydown}
  >
    <header class="flex items-start justify-between gap-4">
      <h2
        id="setup-replay-title"
        class="text-2xl font-semibold text-foreground"
      >
        {tr("setupReplay.title")}
      </h2>
      <button
        type="button"
        data-kx="control"
        class="grid h-8 w-8 place-items-center rounded-lg"
        aria-label={tr("onboarding.ui.introduction.close")}
        onclick={onDismiss}><X class="h-4 w-4" /></button
      >
    </header>
    <p class="mt-3 text-sm text-muted-foreground">{tr("setupReplay.body")}</p>
    {#if loading}
      <p class="mt-5 text-sm" role="status">{tr("common.loading")}</p>
    {:else if context}
      <dl class="mt-5 space-y-3 text-sm">
        <div>
          <dt class="text-muted-foreground">{tr("setupReplay.edition")}</dt>
          <dd>
            {context.installer_edition === "cloud"
              ? tr("setupReplay.cloudEdition")
              : tr("setupReplay.localEdition")}
          </dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{tr("setupReplay.connection")}</dt>
          <dd>{windowsClientModeLabel(context.connection_mode)}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{tr("setupReplay.address")}</dt>
          <dd class="break-all">{context.base_url}</dd>
        </div>
      </dl>
    {:else if native}
      <p class="mt-5 text-sm" role="status">{tr("setupReplay.unavailable")}</p>
    {:else}
      <dl class="mt-5 space-y-3 text-sm">
        <div>
          <dt class="text-muted-foreground">{tr("setupReplay.connection")}</dt>
          <dd>{tr("setupReplay.browser")}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{tr("setupReplay.address")}</dt>
          <dd class="break-all">{origin}</dd>
        </div>
      </dl>
    {/if}
    <footer class="mt-7 flex flex-wrap items-center justify-end gap-2">
      <button
        type="button"
        data-kx="control"
        class="mr-auto rounded-lg px-3 py-2 text-sm text-muted-foreground"
        onclick={onContinue}>{tr("setupReplay.skip")}</button
      >
      <button
        type="button"
        data-kx="control"
        data-variant="primary"
        class="rounded-lg px-4 py-2 text-sm"
        disabled={loading || (native && !context)}
        onclick={onContinue}>{tr("setupReplay.keep")}</button
      >
    </footer>
  </div>
</div>
