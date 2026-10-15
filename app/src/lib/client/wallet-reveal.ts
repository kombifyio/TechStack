import { readDeviceContext } from "./device-context.js";
import type { WalletItem } from "#lib/wallet/types.js";

interface WalletWindow extends Window {
  __kombifyTechstackDocument?: {
    document_revision: number;
    document_token: string;
  };
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

type NativeReveal =
  | { available: false }
  | {
      available: true;
      result: Pick<WalletItem, "id" | "secret" | "totp"> | null;
    };

// The bridge carries only this explicit disclosure, never credentials or a
// reusable signer. The native host independently verifies the local context.
export async function revealNativeLocalWallet(
  id: string,
  reason: string,
): Promise<NativeReveal> {
  const host = window as WalletWindow;
  const cancellation = new AbortController();
  const pagehide = () => cancellation.abort();
  host.addEventListener("pagehide", pagehide, { once: true });
  try {
    const context = await readDeviceContext(cancellation.signal);
    if (
      context?.installer_edition !== "local" ||
      context.connection_mode !== "local"
    )
      return { available: false };
    const bridge = host.chrome?.webview;
    const binding = host.__kombifyTechstackDocument;
    if (!bridge || !binding || cancellation.signal.aborted)
      return { available: true, result: null };
    const document = host.location.href;
    const requestId = crypto.randomUUID();
    return await new Promise<NativeReveal>((resolve, reject) => {
      let complete = false;
      const finish = (
        result: Pick<WalletItem, "id" | "secret" | "totp"> | null,
        error?: string,
      ) => {
        if (complete) return;
        complete = true;
        clearTimeout(timeout);
        bridge.removeEventListener("message", receive);
        cancellation.signal.removeEventListener("abort", cancel);
        if (error) reject(new Error(error));
        else resolve({ available: true, result });
      };
      const cancel = () => {
        try {
          bridge.postMessage({
            type: "kombify.techstack.wallet.cancel.v1",
            request_id: requestId,
            ...binding,
            item_id: id,
            reason,
          });
        } catch {
          /* The document may already be gone. */
        }
        finish(null);
      };
      const receive = (event: MessageEvent) => {
        if (
          host.location.href !== document ||
          cancellation.signal.aborted ||
          host.__kombifyTechstackDocument?.document_token !==
            binding.document_token
        )
          return cancel();
        const response = event.data;
        if (
          !response ||
          response.type !== "kombify.techstack.wallet.reveal.result.v1" ||
          response.request_id !== requestId ||
          response.document_revision !== binding.document_revision ||
          response.document_token !== binding.document_token
        )
          return;
        if (response.status === "cancelled") return finish(null);
        if (response.status === "failed" && typeof response.error === "string")
          return finish(null, response.error);
        const result = response.result;
        if (
          response.status !== "revealed" ||
          !result ||
          result.id !== id ||
          (result.secret != null && typeof result.secret !== "string") ||
          (result.totp != null && typeof result.totp !== "string")
        )
          return;
        finish({ id, secret: result.secret || "", totp: result.totp || "" });
      };
      const timeout = setTimeout(cancel, 155_000);
      bridge.addEventListener("message", receive);
      cancellation.signal.addEventListener("abort", cancel, { once: true });
      try {
        bridge.postMessage({
          type: "kombify.techstack.wallet.reveal.v1",
          request_id: requestId,
          ...binding,
          item_id: id,
          reason,
        });
      } catch {
        finish(null);
      }
    });
  } finally {
    host.removeEventListener("pagehide", pagehide);
  }
}
