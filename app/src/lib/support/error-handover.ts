export const ERROR_AI_HANDOVER_SCHEMA = "kombify.error-handover.v1";
export const AI_HANDOVER_SCHEMA = "kombify.ai-handover.v1";
export const SUPPORT_AGENT_ID = "kombify-support";

export type ErrorAiSource = "kombify-techstack";
export type ErrorAiPanelMode = "assistant" | "support" | "ril";

export interface ErrorAiHandoverContext {
  schema: typeof ERROR_AI_HANDOVER_SCHEMA;
  source: ErrorAiSource;
  surface: string;
  title: string;
  userMessage: string;
  occurredAt: string;
  status?: number;
  code?: string;
  errorCode?: string;
  action?: string;
  phase?: string;
  phaseLabel?: string;
  requestId?: string;
  resourceId?: string;
  resourceName?: string;
  route?: string;
  appVersion?: string;
  appCommit?: string;
  retryable?: boolean;
  reasonCode?: string;
  providerId?: string;
  capability?: string;
  missingFeatures?: string[];
  requiredFeatures?: string[];
}

interface CreateErrorAiHandoverContextInput extends Partial<
  Omit<ErrorAiHandoverContext, "schema" | "source">
> {
  surface: string;
  title: string;
  userMessage: string;
}

const MAX_TEXT_LENGTH = 1_200;
const MAX_FIELD_LENGTH = 160;

function cleanText(value: unknown, maxLength = MAX_TEXT_LENGTH): string {
  if (typeof value !== "string") return "";
  const cleaned = Array.from(value)
    .map((char) => {
      const code = char.charCodeAt(0);
      return code <= 8 ||
        code === 11 ||
        code === 12 ||
        (code >= 14 && code <= 31) ||
        code === 127
        ? " "
        : char;
    })
    .join("")
    .replace(/[ \t]+/g, " ")
    .trim();
  if (cleaned.length <= maxLength) return cleaned;
  return `${cleaned.slice(0, maxLength - 3)}...`;
}

function cleanOptionalText(value: unknown, maxLength = MAX_FIELD_LENGTH) {
  const cleaned = cleanText(value, maxLength);
  return cleaned || undefined;
}

function cleanStatus(value: unknown): number | undefined {
  if (typeof value !== "number" || !Number.isFinite(value)) return undefined;
  return Math.trunc(value);
}

function cleanStringArray(value: unknown): string[] | undefined {
  if (!Array.isArray(value)) return undefined;
  const cleaned = value
    .map((item) => cleanOptionalText(item))
    .filter((item): item is string => Boolean(item));
  return cleaned.length > 0 ? cleaned : undefined;
}

export function createErrorAiHandoverContext(
  input: CreateErrorAiHandoverContextInput,
): ErrorAiHandoverContext {
  return normalizeErrorAiHandoverContext({
    ...input,
    schema: ERROR_AI_HANDOVER_SCHEMA,
    source: "kombify-techstack",
    occurredAt: input.occurredAt || new Date().toISOString(),
  });
}

export function normalizeErrorAiHandoverContext(
  input: ErrorAiHandoverContext,
): ErrorAiHandoverContext {
  return {
    schema: ERROR_AI_HANDOVER_SCHEMA,
    source: "kombify-techstack",
    surface: cleanText(input.surface, MAX_FIELD_LENGTH) || "unknown",
    title: cleanText(input.title, MAX_FIELD_LENGTH) || "Platform error",
    userMessage:
      cleanText(input.userMessage) || "The platform surfaced an error.",
    occurredAt:
      cleanOptionalText(input.occurredAt, MAX_FIELD_LENGTH) ||
      new Date().toISOString(),
    status: cleanStatus(input.status),
    code: cleanOptionalText(input.code),
    errorCode: cleanOptionalText(input.errorCode),
    action: cleanOptionalText(input.action),
    phase: cleanOptionalText(input.phase),
    phaseLabel: cleanOptionalText(input.phaseLabel),
    requestId: cleanOptionalText(input.requestId),
    resourceId: cleanOptionalText(input.resourceId),
    resourceName: cleanOptionalText(input.resourceName),
    route: cleanOptionalText(input.route, 240),
    appVersion: cleanOptionalText(input.appVersion),
    appCommit: cleanOptionalText(input.appCommit),
    retryable:
      typeof input.retryable === "boolean" ? input.retryable : undefined,
    reasonCode: cleanOptionalText(input.reasonCode),
    providerId: cleanOptionalText(input.providerId),
    capability: cleanOptionalText(input.capability),
    missingFeatures: cleanStringArray(input.missingFeatures),
    requiredFeatures: cleanStringArray(input.requiredFeatures),
  };
}

export function buildErrorAiHandoverPrompt(
  input: ErrorAiHandoverContext,
): string {
  const context = normalizeErrorAiHandoverContext(input);
  const lines = [
    "Please help diagnose this kombify TechStack error.",
    "Use only tenant-scoped data for my current authenticated kombify account. If backend or Sentry context is needed, look it up through the tenant-filtered error-context backend by request ID; do not use or reveal errors from other users.",
    "",
    `Surface: ${context.surface}`,
    `Visible error: ${context.title}`,
    `Message: ${context.userMessage}`,
  ];

  const facts: Array<[string, string | number | boolean | undefined]> = [
    ["HTTP status", context.status],
    ["API code", context.code],
    ["Error code", context.errorCode],
    ["Action", context.action],
    ["Phase", context.phase],
    ["Phase label", context.phaseLabel],
    ["Request ID", context.requestId],
    ["Resource ID", context.resourceId],
    ["Resource name", context.resourceName],
    ["Route", context.route],
    ["App version", context.appVersion],
    ["App commit", context.appCommit],
    ["Retryable", context.retryable],
    ["Reason code", context.reasonCode],
    ["Provider", context.providerId],
    ["Capability", context.capability],
    ["Occurred at", context.occurredAt],
  ];

  for (const [label, value] of facts) {
    if (value === undefined || value === "") continue;
    lines.push(`${label}: ${value}`);
  }
  if (context.missingFeatures?.length) {
    lines.push(`Missing features: ${context.missingFeatures.join(", ")}`);
  }
  if (context.requiredFeatures?.length) {
    lines.push(`Required features: ${context.requiredFeatures.join(", ")}`);
  }

  return lines.join("\n");
}
