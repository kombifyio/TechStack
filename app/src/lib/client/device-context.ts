/** Read-only shell snapshot. Connection and credential custody stay native. */
export interface DeviceContext {
  installer_edition: "local" | "cloud";
  connection_mode: "local" | "server" | "cloud";
  base_url: string;
  instance_id: string | null;
}

interface DocumentBinding {
  document_revision: number;
  document_token: string;
}

interface DeviceWindow extends Window {
  __kombifyTechstackDocument?: DocumentBinding;
  chrome?: {
    webview?: {
      postMessage(message: unknown): void;
      addEventListener(
        type: "message",
        listener: (event: MessageEvent) => void,
      ): void;
      removeEventListener(
        type: "message",
        listener: (event: MessageEvent) => void,
      ): void;
    };
  };
}

export function readDeviceContext(
  signal: AbortSignal,
): Promise<DeviceContext | null> {
  const host = window as DeviceWindow;
  const bridge = host.chrome?.webview;
  if (!bridge || signal.aborted) return Promise.resolve(null);
  const document = host.location.href;
  const requestId = crypto.randomUUID();
  return new Promise((resolve) => {
    let requestedBinding: DocumentBinding | undefined;
    const finish = (context: DeviceContext | null) => {
      clearTimeout(timeout);
      bridge.removeEventListener("message", receive);
      host.removeEventListener("kombify-techstack-device-context-ready", send);
      host.removeEventListener("pagehide", cancel);
      signal.removeEventListener("abort", cancel);
      resolve(context);
    };
    const cancel = () => finish(null);
    const receive = (event: MessageEvent) => {
      if (signal.aborted || host.location.href !== document) return cancel();
      const response = event.data;
      if (
        !response ||
        response.type !== "kombify.techstack.device-context.result.v1" ||
        response.request_id !== requestId ||
        response.document_revision !== requestedBinding?.document_revision
      )
        return;
      const context = response.context;
      if (
        !context ||
        !["local", "cloud"].includes(context.installer_edition) ||
        !["local", "server", "cloud"].includes(context.connection_mode) ||
        (context.installer_edition === "cloud") !==
          (context.connection_mode === "cloud") ||
        typeof context.base_url !== "string" ||
        (context.instance_id !== null &&
          typeof context.instance_id !== "string")
      )
        return;
      try {
        const base = new URL(context.base_url);
        if (
          base.origin !== host.location.origin ||
          base.username ||
          base.password ||
          base.pathname !== "/" ||
          base.search ||
          base.hash
        )
          return;
      } catch {
        return;
      }
      finish({
        installer_edition: context.installer_edition,
        connection_mode: context.connection_mode,
        base_url: context.base_url,
        instance_id: context.instance_id,
      });
    };
    const send = () => {
      const binding = host.__kombifyTechstackDocument;
      if (
        !binding ||
        !Number.isSafeInteger(binding.document_revision) ||
        typeof binding.document_token !== "string" ||
        requestedBinding
      )
        return;
      requestedBinding = {
        document_revision: binding.document_revision,
        document_token: binding.document_token,
      };
      try {
        bridge.postMessage({
          type: "kombify.techstack.device-context.read.v1",
          request_id: requestId,
          ...requestedBinding,
        });
      } catch {
        finish(null);
      }
    };
    const timeout = setTimeout(cancel, 2500);
    bridge.addEventListener("message", receive);
    host.addEventListener("kombify-techstack-device-context-ready", send);
    host.addEventListener("pagehide", cancel);
    signal.addEventListener("abort", cancel, { once: true });
    send();
  });
}

export function hasDeviceContextBridge(): boolean {
  return Boolean((window as DeviceWindow).chrome?.webview);
}
