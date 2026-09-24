<script lang="ts">
  import { onMount } from "svelte";
  import { X } from "@lucide/svelte";
  import { createTerminalSession } from "#lib/api/server-access.js";
  import { parseApiError } from "#lib/api/errors.js";
  import "@xterm/xterm/css/xterm.css";

  interface Props {
    serverId: string;
    serverName: string;
    onClose: () => void;
    onReauthRequired?: () => void;
  }

  let { serverId, serverName, onClose, onReauthRequired }: Props = $props();
  let container: HTMLDivElement;
  let dialog: HTMLDivElement;
  let connectionState = $state<"connecting" | "connected" | "closed" | "error">(
    "connecting",
  );
  let reason = $state("");

  onMount(() => {
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    let disposed = false;
    let socket: WebSocket | undefined;
    let observer: ResizeObserver | undefined;
    let terminal: import("@xterm/xterm").Terminal | undefined;
    let fit: import("@xterm/addon-fit").FitAddon | undefined;

    const start = async () => {
      try {
        const [{ Terminal }, { FitAddon }, session] = await Promise.all([
          import("@xterm/xterm"),
          import("@xterm/addon-fit"),
          createTerminalSession(serverId),
        ]);
        if (disposed) return;
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
        socket = new WebSocket(session.stream_url);
        socket.addEventListener("open", () => {
          if (!terminal || !fit) return;
          const dimensions = terminal;
          socket?.send(
            JSON.stringify({
              type: "resize",
              cols: dimensions.cols,
              rows: dimensions.rows,
            }),
          );
        });
        socket.addEventListener("message", (event) => {
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
              reason = message.reason || "Terminal connection failed";
            }
          } catch {
            connectionState = "error";
            reason = "Invalid terminal stream response";
          }
        });
        socket.addEventListener("close", () => {
          if (connectionState !== "error") connectionState = "closed";
        });
        terminal.onData((data) => {
          if (socket?.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ type: "input", data }));
          }
        });
        terminal.onResize(({ cols, rows }) => {
          if (socket?.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ type: "resize", cols, rows }));
          }
        });
        observer = new ResizeObserver(() => fit?.fit());
        observer.observe(container);
      } catch (cause) {
        const parsed = parseApiError(cause);
        connectionState = "error";
        reason = parsed.message;
        if (parsed.isForbidden) onReauthRequired?.();
      }
    };
    void start();
    queueMicrotask(() => dialog?.focus());

    return () => {
      disposed = true;
      observer?.disconnect();
      if (socket?.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "close" }));
      }
      socket?.close();
      terminal?.dispose();
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
        'button:not([disabled]), [tabindex]:not([tabindex="-1"]), textarea, input',
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
          Terminal · {serverName}
        </h2>
        <p class="mt-0.5 text-xs text-white/60" aria-live="polite">
          {connectionState}{reason ? ` · ${reason.replaceAll("_", " ")}` : ""}
        </p>
      </div>
      <button class="rounded-md p-2 text-white/70 hover:bg-white/10 hover:text-white" aria-label="Close terminal" onclick={onClose}>
        <X class="h-5 w-5" />
      </button>
    </header>
    <div class="relative min-h-0 flex-1 p-2 sm:p-3">
      {#if connectionState === "connecting"}
        <div class="absolute inset-0 z-10 grid place-items-center bg-[#07090b] text-sm text-white/60">Creating a new protected session…</div>
      {:else if connectionState === "error"}
        <div class="absolute inset-0 z-10 grid place-items-center bg-[#07090b] p-6 text-center text-sm text-red-300" role="alert">{reason}</div>
      {/if}
      <div bind:this={container} class="h-full w-full overflow-hidden" data-testid="server-terminal-xterm"></div>
    </div>
  </div>
</div>
