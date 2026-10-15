<script lang="ts">
  import { Check, Copy } from "@lucide/svelte";
  import { encode } from "uqr";
  import { tr } from "#lib/i18n.svelte.js";

  /*
   * How to use a private AI application from the user's other devices: the
   * phone scans the address, editors and Home Assistant talk to its
   * OpenAI-compatible endpoint. Shown in the application sheet of a chat app
   * (Open WebUI); `apiBase` is the router's endpoint when one is installed,
   * otherwise the chat app's own.
   */
  interface Props {
    url: string;
    apiBase: string;
    /** The key comes from the router (owner custody), not the chat app. */
    routerKey?: boolean;
  }

  let { url, apiBase, routerKey = false }: Props = $props();

  // The code is black on white whatever the theme, so every phone camera
  // reads it.
  const qr = $derived.by(() => {
    const { data, size } = encode(url, { border: 2 });
    let path = "";
    data.forEach((row, y) =>
      row.forEach((dark, x) => {
        if (dark) path += `M${x} ${y}h1v1h-1z`;
      }),
    );
    return { size, path };
  });
  let copied = $state("");

  function copy(value: string) {
    void navigator.clipboard?.writeText(value);
    copied = value;
    setTimeout(() => (copied = ""), 2000);
  }
</script>

{#snippet field(value: string)}
  <div class="mt-1.5 flex items-center gap-2">
    <code
      class="min-w-0 flex-1 truncate rounded-md bg-muted px-2 py-1 font-mono text-xs"
      >{value}</code
    >
    <button
      type="button"
      class="inline-flex size-7 shrink-0 items-center justify-center rounded-md border border-border text-muted-foreground hover:text-foreground"
      aria-label={tr("ui.connectDevices.copy")}
      onclick={() => copy(value)}
    >
      {#if copied === value}<Check class="size-3.5" />{:else}<Copy
          class="size-3.5"
        />{/if}
    </button>
  </div>
{/snippet}

<section class="space-y-4" data-testid="connect-devices-guide">
  <div>
    <h3 class="text-sm font-semibold">{tr("ui.connectDevices.title")}</h3>
    <p class="mt-0.5 text-xs text-muted-foreground">
      {tr("ui.connectDevices.intro")}
    </p>
  </div>

  <div class="flex gap-4">
    <svg
      class="size-28 shrink-0 rounded-lg border border-border bg-white"
      viewBox="0 0 {qr.size} {qr.size}"
      shape-rendering="crispEdges"
      role="img"
      aria-label={tr("ui.connectDevices.qrLabel")}
    >
      <path d={qr.path} fill="#000" />
    </svg>
    <div class="min-w-0 text-sm">
      <p class="font-medium">{tr("ui.connectDevices.phone")}</p>
      <p class="mt-0.5 text-xs text-muted-foreground">
        {tr("ui.connectDevices.phoneSteps")}
      </p>
      {@render field(url)}
    </div>
  </div>

  <div class="text-sm">
    <p class="font-medium">{tr("ui.connectDevices.editor")}</p>
    <p class="mt-0.5 text-xs text-muted-foreground">
      {tr("ui.connectDevices.editorSteps")}
    </p>
    {@render field(apiBase)}
  </div>

  <div class="text-sm">
    <p class="font-medium">{tr("ui.connectDevices.homeAssistant")}</p>
    <p class="mt-0.5 text-xs text-muted-foreground">
      {tr("ui.connectDevices.homeAssistantSteps")}
    </p>
  </div>

  <p class="text-xs text-muted-foreground">
    {tr(
      routerKey
        ? "ui.connectDevices.keyRouter"
        : "ui.connectDevices.keyChat",
    )}
  </p>
</section>
