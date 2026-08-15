import { writable } from "svelte/store";

export type InAppDialogTone = "primary" | "warning" | "danger";

export interface InAppDialogRequest {
  kind: "confirm" | "prompt" | "notice";
  title: string;
  message: string;
  confirmText: string;
  cancelText?: string;
  tone: InAppDialogTone;
  inputLabel?: string;
  inputType?: "text" | "password";
  initialValue?: string;
}

export const inAppDialog = writable<InAppDialogRequest | null>(null);

let pending:
  | { kind: InAppDialogRequest["kind"]; resolve: (value: unknown) => void }
  | undefined;

function openDialog<T>(request: InAppDialogRequest, cancelled: T): Promise<T> {
  if (pending) pending.resolve(cancelled);
  inAppDialog.set(request);
  return new Promise<T>((resolve) => {
    pending = {
      kind: request.kind,
      resolve: resolve as (value: unknown) => void,
    };
  });
}

export function confirmInApp(options: {
  title: string;
  message: string;
  confirmText?: string;
  cancelText?: string;
  tone?: InAppDialogTone;
}): Promise<boolean> {
  return openDialog(
    {
      kind: "confirm",
      title: options.title,
      message: options.message,
      confirmText: options.confirmText ?? "Confirm",
      cancelText: options.cancelText ?? "Cancel",
      tone: options.tone ?? "primary",
    },
    false,
  );
}

export function promptInApp(options: {
  title: string;
  message: string;
  inputLabel: string;
  inputType?: "text" | "password";
  confirmText?: string;
  cancelText?: string;
  tone?: InAppDialogTone;
}): Promise<string | null> {
  return openDialog(
    {
      kind: "prompt",
      title: options.title,
      message: options.message,
      inputLabel: options.inputLabel,
      inputType: options.inputType ?? "text",
      confirmText: options.confirmText ?? "Continue",
      cancelText: options.cancelText ?? "Cancel",
      tone: options.tone ?? "primary",
    },
    null,
  );
}

export function noticeInApp(options: {
  title: string;
  message: string;
  confirmText?: string;
  tone?: InAppDialogTone;
}): Promise<void> {
  return openDialog(
    {
      kind: "notice",
      title: options.title,
      message: options.message,
      confirmText: options.confirmText ?? "OK",
      tone: options.tone ?? "primary",
    },
    undefined,
  );
}

export function settleInAppDialog(value?: string): void {
  const current = pending;
  pending = undefined;
  inAppDialog.set(null);
  if (!current) return;
  if (current.kind === "confirm") current.resolve(true);
  else if (current.kind === "prompt") current.resolve(value ?? null);
  else current.resolve(undefined);
}

export function cancelInAppDialog(): void {
  const current = pending;
  pending = undefined;
  inAppDialog.set(null);
  if (!current) return;
  current.resolve(
    current.kind === "confirm"
      ? false
      : current.kind === "prompt"
        ? null
        : undefined,
  );
}
