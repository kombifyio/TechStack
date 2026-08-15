import {
  normalizeServerOutcome,
  type ServerOutcome,
} from "$lib/support/server-outcome";

export interface ParsedApiError {
  status: number;
  code?: string;
  message: string;
  details?: unknown;
  retryable?: boolean;
  /**
   * Structured availability/outcome envelope when the backend supplied one
   * (FEATURE-ENTITLEMENT-UX-STANDARD). Present for denials/blocked/degraded
   * responses; `undefined` for plain errors. Additive — existing fields
   * are unchanged.
   */
  outcome?: ServerOutcome;
  fieldErrors: Record<string, { code: string; message: string }>;
  isValidationError: boolean;
  isAuthError: boolean;
  isForbidden: boolean;
  isNotFound: boolean;
  originalError: unknown;
}

function messageFromUnknown(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  if (
    typeof err === "object" &&
    err !== null &&
    "message" in err &&
    typeof (err as { message?: unknown }).message === "string"
  ) {
    return (err as { message: string }).message;
  }
  return "An unknown error occurred";
}

function statusFromUnknown(err: unknown): number {
  if (
    typeof err === "object" &&
    err !== null &&
    "status" in err &&
    typeof (err as { status?: unknown }).status === "number"
  ) {
    return (err as { status: number }).status;
  }
  return 0;
}

export function parseApiError(err: unknown): ParsedApiError {
  const status = statusFromUnknown(err);
  const code =
    typeof err === "object" &&
    err !== null &&
    "code" in err &&
    typeof (err as { code?: unknown }).code === "string"
      ? (err as { code: string }).code
      : undefined;
  const details =
    typeof err === "object" && err !== null && "details" in err
      ? (err as { details?: unknown }).details
      : undefined;
  const retryable =
    typeof err === "object" &&
    err !== null &&
    "retryable" in err &&
    typeof (err as { retryable?: unknown }).retryable === "boolean"
      ? (err as { retryable: boolean }).retryable
      : undefined;
  return {
    status,
    code,
    message: messageFromUnknown(err),
    details,
    retryable,
    outcome: normalizeServerOutcome(details) ?? undefined,
    fieldErrors: {},
    isValidationError: status === 400 || status === 422,
    isAuthError: status === 401,
    isForbidden: status === 403,
    isNotFound: status === 404,
    originalError: err,
  };
}

export function isCancelledRequestError(err: unknown): boolean {
  const maybeAny = err as { name?: unknown; message?: unknown } | null;
  const name = typeof maybeAny?.name === "string" ? maybeAny.name : "";
  const message =
    err instanceof Error
      ? err.message
      : typeof maybeAny?.message === "string"
        ? maybeAny.message
        : "";

  return (
    name === "AbortError" ||
    message.toLowerCase().includes("autocancel") ||
    message.toLowerCase().includes("aborted")
  );
}
