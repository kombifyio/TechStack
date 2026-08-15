export interface AnalyticsUser {
  authSubject?: string | null;
  organizationId?: string | null;
  role?: string | null;
  authMethod?: string | null;
  isStaff?: boolean | null;
}

export interface PostHogConfig {
  apiKey?: string | null;
  host?: string | null;
  environment?: string | null;
  edition?: string | null;
  appVersion?: string | null;
  service?: string;
  product?: string;
  surface?: string;
  fetchImpl?: typeof fetch;
  location?: LocationLike;
  now?: () => number;
  randomId?: () => string;
}

export interface CaptureOptions {
  user?: AnalyticsUser | null;
  properties?: Record<string, unknown>;
}

interface LocationLike {
  pathname?: string;
}

interface CapturePayload {
  api_key: string;
  event: string;
  distinct_id: string;
  properties: Record<string, unknown>;
}

export interface PostHogClient {
  enabled: boolean;
  capture(event: string, options?: CaptureOptions): Promise<boolean>;
}

export interface TechstackCloudUserLike {
  sub?: string | null;
  orgId?: string | null;
  organizationId?: string | null;
  tenantId?: string | null;
  roles?: string[] | null;
  is_admin?: boolean | null;
}

export interface TechstackCreationEventInput {
  wizardType?: string | null;
  provider?: string | null;
  stackMode?: string | null;
  serverProvisioningMode?: string | null;
  creationOperation?: string | null;
  runtimePhase?: string | null;
  verificationStatus?: string | null;
  serviceCount?: number | null;
  goalCount?: number | null;
  detectedAddonCount?: number | null;
  proofKeyCount?: number | null;
  pipelineValid?: boolean | null;
  resolvedStackkit?: string | null;
  stackId?: string | null;
  jobId?: string | null;
  serverId?: string | null;
  serviceId?: string | null;
  requestId?: string | null;
  reasonCode?: string | null;
  connectionState?: string | null;
  healthState?: string | null;
  accessMode?: string | null;
  serverCount?: number | null;
  errorStatus?: number | null;
  errorCode?: string | null;
  retryable?: boolean | null;
  conflict?: boolean | null;
  identityHandoffPresent?: boolean | null;
}

const DEFAULT_HOST = "https://e.kombify.io";
const DEFAULT_SERVICE = "kombify-techstack-frontend";
const DEFAULT_PRODUCT = "kombify-techstack";
const DEFAULT_SURFACE = "techstack";
const DEFAULT_EDITION = "saas-standalone";
const EVENT_NAME_PATTERN =
  /^(?:\$[A-Za-z0-9_.:-]+|[a-z0-9]+:[a-z0-9]+(?:_[a-z0-9]+)*)$/;
const SENSITIVE_KEY_PATTERN =
  /(email|name|username|password|secret|token|authorization|cookie|api[_-]?key|host|url|fqdn|ip$|addr|address)/i;
const RESERVED_IDENTITY_PROPERTY_KEYS = new Set([
  "$groups",
  "$process_person_profile",
  "distinct_id",
  "api_key",
]);
const EMAIL_VALUE_PATTERN = /\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/i;
const URL_VALUE_PATTERN = /\b(?:https?:\/\/|www\.)\S+/i;
const ANONYMOUS_IDENTITY_PATTERN =
  /^(?:(?:local[_:-])?anonymous|anon|guest|unknown)(?:[_:-].*)?$/i;
const ROUTE_PROPERTY_KEYS = new Set([
  "gateway_route",
  "href",
  "path",
  "pathname",
  "referrer_path",
  "route",
  "source_path",
  "target_path",
]);
const UNSAFE_ROUTE_VALUE_PATTERN = /(?:https?:\/\/|www\.|[?#])/i;
const MAX_PROPERTY_ARRAY_LENGTH = 25;

export function createPostHogClient(config: PostHogConfig): PostHogClient {
  const apiKey = normalizeString(config.apiKey);
  const host = normalizePostHogHost(config.host);
  const disabled =
    !isValidPostHogProjectApiKey(apiKey) || isSelfHostedEdition(config.edition);
  const fetchImpl = config.fetchImpl ?? globalThis.fetch?.bind(globalThis);
  const now = config.now ?? Date.now;
  const randomId = config.randomId ?? defaultRandomId;

  return {
    enabled: !disabled,

    async capture(
      event: string,
      options: CaptureOptions = {},
    ): Promise<boolean> {
      if (disabled || !apiKey || !fetchImpl || !isValidEventName(event)) {
        return false;
      }

      const payload = buildCapturePayload({
        apiKey,
        event,
        config,
        now,
        randomId,
        user: options.user,
        properties: options.properties,
      });
      if (!payload) {
        return false;
      }

      try {
        const response = await fetchImpl(`${host}/capture/`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
          keepalive: true,
        });
        return response.ok;
      } catch {
        return false;
      }
    },
  };
}

export function buildCapturePayload(input: {
  apiKey: string;
  event: string;
  config: PostHogConfig;
  now?: () => number;
  randomId?: () => string;
  user?: AnalyticsUser | null;
  properties?: Record<string, unknown>;
}): CapturePayload | null {
  if (!isValidPostHogProjectApiKey(input.apiKey)) {
    return null;
  }

  const distinctId = normalizeAnalyticsIdentityValue(input.user?.authSubject);
  if (!distinctId) {
    return null;
  }
  const organizationId = normalizeAnalyticsIdentityValue(
    input.user?.organizationId,
  );

  const now = input.now ?? Date.now;
  const randomId = input.randomId ?? defaultRandomId;
  const callerProperties = sanitizeProperties(input.properties ?? {});
  const properties: Record<string, unknown> = {
    ...callerProperties,
    service: input.config.service ?? DEFAULT_SERVICE,
    product: input.config.product ?? DEFAULT_PRODUCT,
    surface: input.config.surface ?? DEFAULT_SURFACE,
    env: normalizeEnvironment(input.config.environment),
    edition: normalizeEdition(input.config.edition),
    app_version: normalizeString(input.config.appVersion) ?? "unknown",
    correlation_id: `ph_${now()}_${randomId()}`,
    path: sanitizePath(input.config.location?.pathname),
    role: input.user?.role ?? undefined,
    auth_method: input.user?.authMethod ?? undefined,
    is_staff: input.user?.isStaff === true,
  };

  if (organizationId) {
    properties.$groups = { organization: organizationId };
    properties.organization_id = organizationId;
  }

  if (!properties.$groups) {
    properties.$process_person_profile = false;
  }

  return {
    api_key: input.apiKey,
    event: input.event,
    distinct_id: distinctId,
    properties,
  };
}

export function isValidEventName(event: string): boolean {
  return EVENT_NAME_PATTERN.test(event);
}

export function isValidPostHogProjectApiKey(
  apiKey?: string | null,
): apiKey is string {
  return apiKey?.startsWith("phc_") === true;
}

export function normalizePostHogHost(host?: string | null): string {
  const raw = normalizeString(host) ?? DEFAULT_HOST;
  try {
    const parsed = new URL(raw);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return DEFAULT_HOST;
    }
    if (isPostHogManagedHost(parsed.hostname)) {
      return DEFAULT_HOST;
    }
    return parsed.origin;
  } catch {
    return DEFAULT_HOST;
  }
}

function isPostHogManagedHost(hostname: string): boolean {
  const normalized = hostname.toLowerCase();
  return normalized === "posthog.com" || normalized.endsWith(".posthog.com");
}

export function toTechstackAnalyticsUser(
  user: TechstackCloudUserLike | null | undefined,
): AnalyticsUser | null {
  if (!user?.sub) {
    return null;
  }

  return {
    authSubject: user.sub,
    organizationId:
      normalizeAnalyticsIdentityValue(user.orgId) ??
      normalizeAnalyticsIdentityValue(user.organizationId) ??
      normalizeAnalyticsIdentityValue(user.tenantId),
    role: user.roles?.find((role) => role.trim()) ?? undefined,
    authMethod: "auth0",
    isStaff: user.is_admin === true,
  };
}

export function buildTechstackCreationEventProperties(
  input: TechstackCreationEventInput,
): Record<string, unknown> {
  return stripUndefinedProperties({
    wizard_type: normalizeAnalyticsEnum(input.wizardType),
    provider: normalizeAnalyticsEnum(input.provider),
    stack_mode: normalizeAnalyticsEnum(input.stackMode),
    server_provisioning_mode: normalizeAnalyticsEnum(
      input.serverProvisioningMode,
    ),
    creation_operation: normalizeAnalyticsEnum(input.creationOperation),
    runtime_phase: normalizeAnalyticsEnum(input.runtimePhase),
    verification_status: normalizeAnalyticsEnum(input.verificationStatus),
    service_count: normalizeNonNegativeInteger(input.serviceCount),
    goal_count: normalizeNonNegativeInteger(input.goalCount),
    detected_addon_count: normalizeNonNegativeInteger(input.detectedAddonCount),
    proof_key_count: normalizeNonNegativeInteger(input.proofKeyCount),
    pipeline_valid:
      typeof input.pipelineValid === "boolean"
        ? input.pipelineValid
        : undefined,
    resolved_stackkit: normalizeAnalyticsIdentifier(input.resolvedStackkit),
    stack_id: normalizeAnalyticsIdentifier(input.stackId),
    job_id: normalizeAnalyticsIdentifier(input.jobId),
    server_id: normalizeAnalyticsIdentifier(input.serverId),
    service_id: normalizeAnalyticsIdentifier(input.serviceId),
    request_id: normalizeAnalyticsIdentifier(input.requestId),
    reason_code: normalizeAnalyticsEnum(input.reasonCode),
    connection_state: normalizeAnalyticsEnum(input.connectionState),
    health_state: normalizeAnalyticsEnum(input.healthState),
    access_mode: normalizeAnalyticsEnum(input.accessMode),
    server_count: normalizeNonNegativeInteger(input.serverCount),
    error_status: normalizeNonNegativeInteger(input.errorStatus),
    error_code: normalizeAnalyticsEnum(input.errorCode),
    retryable:
      typeof input.retryable === "boolean" ? input.retryable : undefined,
    conflict: typeof input.conflict === "boolean" ? input.conflict : undefined,
    identity_handoff_present:
      typeof input.identityHandoffPresent === "boolean"
        ? input.identityHandoffPresent
        : undefined,
  });
}

export function classifyRoute(pathname: string): string {
  if (pathname === "/" || pathname.startsWith("/stacks")) return "stacks";
  if (pathname.startsWith("/monitoring")) return "monitoring";
  if (pathname.startsWith("/services")) return "services";
  if (pathname.startsWith("/wallet")) return "wallet";
  if (pathname.startsWith("/settings")) return "settings";
  if (pathname.startsWith("/login") || pathname.startsWith("/auth"))
    return "auth";
  return "other";
}

export function sanitizeNavigationTarget(
  href: string,
  origin: string,
): string | null {
  try {
    const url = new URL(href, origin);
    if (url.origin !== origin) {
      return null;
    }
    return url.pathname;
  } catch {
    return null;
  }
}

function sanitizeProperties(
  properties: Record<string, unknown>,
  seen = new WeakSet<object>(),
): Record<string, unknown> {
  if (seen.has(properties)) return {};
  seen.add(properties);

  const sanitized: Record<string, unknown> = {};

  for (const [key, value] of Object.entries(properties)) {
    if (isBlockedPropertyKey(key)) continue;
    if (isUnsafeRouteProperty(key, value)) continue;
    const sanitizedValue = sanitizeValue(value, 0, seen);
    if (sanitizedValue !== undefined) sanitized[key] = sanitizedValue;
  }

  seen.delete(properties);
  return sanitized;
}

function isUnsafeRouteProperty(key: string, value: unknown): boolean {
  if (typeof value !== "string") return false;
  const normalized = key.trim().toLowerCase();
  if (!ROUTE_PROPERTY_KEYS.has(normalized)) return false;
  return UNSAFE_ROUTE_VALUE_PATTERN.test(value);
}

function sanitizeValue(
  value: unknown,
  depth: number,
  seen: WeakSet<object>,
): unknown {
  if (
    value === undefined ||
    value === null ||
    typeof value === "function" ||
    typeof value === "symbol"
  ) {
    return undefined;
  }
  if (typeof value === "string") {
    if (URL_VALUE_PATTERN.test(value)) return undefined;
    if (EMAIL_VALUE_PATTERN.test(value)) return undefined;
    return value.slice(0, 500);
  }
  if (typeof value === "number") {
    return Number.isFinite(value) ? value : undefined;
  }
  if (typeof value === "boolean") {
    return value;
  }
  if (depth >= 3) return undefined;
  if (Array.isArray(value)) {
    const items = value
      .slice(0, MAX_PROPERTY_ARRAY_LENGTH)
      .map((item) => sanitizeValue(item, depth + 1, seen))
      .filter((item) => item !== undefined);
    return items.length > 0 ? items : undefined;
  }
  if (isPlainObject(value)) {
    if (seen.has(value)) return undefined;
    seen.add(value);
    const nested: Record<string, unknown> = {};
    for (const [key, child] of Object.entries(value)) {
      if (isBlockedPropertyKey(key)) continue;
      const sanitized = sanitizeValue(child, depth + 1, seen);
      if (sanitized !== undefined) nested[key] = sanitized;
    }
    seen.delete(value);
    return Object.keys(nested).length > 0 ? nested : undefined;
  }
  return undefined;
}

function isBlockedPropertyKey(key: string): boolean {
  return (
    RESERVED_IDENTITY_PROPERTY_KEYS.has(key.toLowerCase()) ||
    SENSITIVE_KEY_PATTERN.test(key)
  );
}

function normalizeEnvironment(environment?: string | null): string {
  const value = normalizeString(environment)?.toLowerCase();
  if (!value) {
    return "development";
  }
  if (value === "prod") {
    return "production";
  }
  if (value === "dev") {
    return "development";
  }
  return value;
}

function normalizeEdition(edition?: string | null): string {
  return normalizeString(edition) ?? DEFAULT_EDITION;
}

function isSelfHostedEdition(edition?: string | null): boolean {
  const value = normalizeString(edition)?.toLowerCase();
  return value === "selfhost-oss" || value === "self-hosted" || value === "oss";
}

function sanitizePath(path?: string): string {
  if (!path || !path.startsWith("/")) {
    return "/";
  }
  return path.split(/[?#]/)[0] ?? "/";
}

function normalizeString(value?: string | null): string | undefined {
  const trimmed = value?.trim();
  return trimmed ? trimmed : undefined;
}

function normalizeAnalyticsIdentifier(
  value?: string | null,
): string | undefined {
  const trimmed = normalizeString(value);
  if (
    !trimmed ||
    EMAIL_VALUE_PATTERN.test(trimmed) ||
    URL_VALUE_PATTERN.test(trimmed)
  ) {
    return undefined;
  }
  if (!/^[A-Za-z0-9_.:-]+$/.test(trimmed)) return undefined;
  return trimmed.slice(0, 96);
}

function normalizeAnalyticsIdentityValue(
  value?: string | null,
): string | undefined {
  const trimmed = normalizeString(value);
  if (!trimmed) return undefined;
  if (EMAIL_VALUE_PATTERN.test(trimmed) || URL_VALUE_PATTERN.test(trimmed)) {
    return undefined;
  }
  if (/\s/.test(trimmed)) return undefined;
  if (ANONYMOUS_IDENTITY_PATTERN.test(trimmed)) return undefined;
  return trimmed.slice(0, 160);
}

function normalizeAnalyticsEnum(value?: string | null): string | undefined {
  const trimmed = normalizeString(value);
  if (
    !trimmed ||
    EMAIL_VALUE_PATTERN.test(trimmed) ||
    URL_VALUE_PATTERN.test(trimmed)
  ) {
    return undefined;
  }
  const normalized = trimmed
    .toLowerCase()
    .replace(/[^a-z0-9_.:-]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .slice(0, 64);
  return normalized || undefined;
}

function normalizeNonNegativeInteger(
  value?: number | null,
): number | undefined {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) {
    return undefined;
  }
  return Math.floor(value);
}

function stripUndefinedProperties(
  properties: Record<string, unknown>,
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(properties).filter(([, value]) => value !== undefined),
  );
}

function defaultRandomId(): string {
  const random =
    typeof crypto !== "undefined" && "randomUUID" in crypto
      ? crypto.randomUUID()
      : Math.random().toString(36).slice(2);
  return random.replace(/[^a-zA-Z0-9_-]/g, "");
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
