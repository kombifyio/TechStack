<script lang="ts">
  import { tr, stateLabel } from "#lib/i18n.svelte.js";
  import { onMount } from "svelte";
  import { X } from "@lucide/svelte";
  import { createTerminalSession } from "#lib/api/server-access.js";
  import { denialMessage, parseApiError } from "#lib/api/errors.js";
  import { isEmbeddedWindow } from "#lib/auth/embedded-session.js";
  import {
    isStepUpRefusal,
    redirectToStepUp,
    SERVER_ACCESS_STEP_UP_REASON,
    serverStepUp,
  } from "#lib/auth/step-up.js";
  import StepUpPrompt from "./StepUpPrompt.svelte";
  import "@xterm/xterm/css/xterm.css";

  interface Props {
    serverId: string;
    serverName: string;
    onClose: () => void;
  }

  let { serverId, serverName, onClose }: Props = $props();
  let container: HTMLDivElement;
  let dialog: HTMLDivElement;
  let connectionState = $state<
    "connecting" | "connected" | "closed" | "error" | "step-up"
  >("connecting");
  let reason = $state("");

  // Session start and teardown live at component level so a step-up can run
  // the start again on the same modal.
  let disposed = false;
  let startRun = 0;
  let socket: WebSocket | undefined;
  let observer: ResizeObserver | undefined;
  let terminal: import("@xterm/xterm").Terminal | undefined;
  let fit: import("@xterm/addon-fit").FitAddon | undefined;

  function teardownSession() {
    observer?.disconnect();
    observer = undefined;
    if (socket?.readyState === WebSocket.OPEN) {
      socket.send(JSON.stringify({ type: "close" }));
    }
    socket?.close();
    socket = undefined;
    terminal?.dispose();
    terminal = undefined;
    fit = undefined;
  }

  async function startSession() {
    const run = ++startRun;
    teardownSession();
    // A start after a step-up keeps the prompt on screen until the new attempt
    // is answered, so a still-missing confirmation stays visible.
    if (connectionState !== "step-up") connectionState = "connecting";
    reason = "";
    try {
      const [{ Terminal }, { FitAddon }, session] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
        createTerminalSession(serverId),
      ]);
      if (disposed || run !== startRun) return;
      connectionState = "connecting";
      terminal = new Terminal({
        cursorBlink: true,
        convertEol: true,
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
        fontSize: 13,
        theme: { background: "#07090b", foreground: "#e5e7eb" },
        scrollback: 5000,
      });
      fit = new FitAddon();
      terminal.loadAddon(fit);
      terminal.open(container);
      fit.fit();
      const ownSocket = new WebSocket(session.stream_url);
      socket = ownSocket;
      ownSocket.addEventListener("open", () => {
        if (!terminal || !fit) return;
        const dimensions = terminal;
        ownSocket.send(
          JSON.stringify({
            type: "resize",
            cols: dimensions.cols,
            rows: dimensions.rows,
          }),
        );
      });
      ownSocket.addEventListener("message", (event) => {
        if (run !== startRun) return;
        try {
          const message = JSON.parse(String(event.data)) as {
            type: string;
            data?: string;
            reason?: string;
          };
          if (message.type === "output") terminal?.write(message.data || "");
          if (message.type === "status") {
            connectionState = message.data === "connected" ? "connected" : "closed";
            reason = message.reason || "";
            if (connectionState === "connected") terminal?.focus();
          }
          if (message.type === "error") {
            connectionState = "error";
            reason = message.reason || tr("ui.serverTerminalModal.terminalConnectionFailed");
          }
        } catch {
          connectionState = "error";
          reason = tr("ui.serverTerminalModal.invalidTerminalStreamResponse");
        }
      });
      ownSocket.addEventListener("close", () => {
        if (run !== startRun) return;
        if (connectionState !== "error") connectionState = "closed";
      });
      terminal.onData((data) => {
        if (ownSocket.readyState === WebSocket.OPEN) {
          ownSocket.send(JSON.stringify({ type: "input", data }));
        }
      });
      terminal.onResize(({ cols, rows }) => {
        if (ownSocket.readyState === WebSocket.OPEN) {
          ownSocket.send(JSON.stringify({ type: "resize", cols, rows }));
        }
      });
      observer = new ResizeObserver(() => fit?.fit());
      observer.observe(container);
    } catch (cause) {
      if (disposed || run !== startRun) return;
      const parsed = parseApiError(cause);
      const stepUp = isStepUpRefusal(parsed, SERVER_ACCESS_STEP_UP_REASON);
      if (stepUp && isEmbeddedWindow()) {
        // The frame must never navigate to the sign-in: wait for the owner to
        // confirm in a separate window, then start the session again.
        connectionState = "step-up";
        return;
      }
      connectionState = "error";
      reason = denialMessage(parsed);
      if (stepUp) redirectToStepUp(serverStepUp(serverId));
    }
  }

  onMount(() => {
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    void startSession();
    queueMicrotask(() => dialog?.focus());

    return () => {
      disposed = true;
      teardownSession();
      document.body.style.overflow = previousOverflow;
    };
  });

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
      return;
    }
    if (event.key !== "Tab" || !dialog) return;
    const focusable = Array.from(
      dialog.querySelectorAll<HTMLElement>(
        'button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"]), textarea, input',
      ),
    );
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div
  class="fixed inset-0 z-[70] flex items-stretch justify-center bg-black/80 p-0 backdrop-blur-sm sm:items-center sm:p-4"
  role="presentation"
  onclick={(event) => event.target === event.currentTarget && onClose()}
>
  <div
    bind:this={dialog}
    class="flex h-[100dvh] w-full flex-col overflow-hidden border-border bg-[#07090b] shadow-2xl outline-none sm:h-[78dvh] sm:max-h-[900px] sm:max-w-6xl sm:rounded-xl sm:border"
    role="dialog"
    aria-modal="true"
    aria-labelledby="server-terminal-title"
    tabindex="-1"
    onkeydown={handleKeydown}
    style="padding-top: env(safe-area-inset-top); padding-bottom: env(safe-area-inset-bottom)"
  >
    <header class="flex items-center justify-between gap-4 border-b border-white/10 px-4 py-3">
      <div class="min-w-0">
        <h2 id="server-terminal-title" class="truncate font-semibold text-white">
          {tr("ui.terminal.title", { name: serverName })}
        </h2>
        <p class="mt-0.5 text-xs text-white/60" aria-live="polite">
          {connectionState === "step-up" ? "" : connectionState}{reason ? ` · ${stateLabel(reason)}` : ""}
        </p>
      </div>
      <button class="rounded-md p-2 text-white/70 hover:bg-white/10 hover:text-white" aria-label={tr("ui.terminal.close")} onclick={onClose}>
        <X class="h-5 w-5" />
      </button>
    </header>
    <div class="relative min-h-0 flex-1 p-2 sm:p-3">
      {#if connectionState === "connecting"}
        <div class="absolute inset-0 z-10 grid place-items-center bg-[#07090b] text-sm text-white/60">{tr("ui.terminal.creating")}</div>
      {:else if connectionState === "step-up"}
        <div class="absolute inset-0 z-10 grid place-items-center bg-[#07090b] p-6 text-white">
          <StepUpPrompt
            request={serverStepUp(serverId)}
            body={tr("ui.serverTerminalModal.stepUpBody")}
            onRetry={startSession}
          />
        </div>
      {:else if connectionState === "error"}
        <div class="absolute inset-0 z-10 grid place-items-center bg-[#07090b] p-6 text-center text-sm text-red-300" role="alert">{reason}</div>
      {/if}
      <div bind:this={container} class="h-full w-full overflow-hidden" data-testid="server-terminal-xterm"></div>
    </div>
  </div>
</div>
