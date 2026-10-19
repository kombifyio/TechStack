/**
 * What the Techstack frame tells its host about the page the user is looking
 * at (`kombify.view-context.v1`). Inside kombify Cloud the Companion belongs to
 * the host page and cannot see the frame, so this is the only way it learns
 * which page and which object a question such as "check the error here" is
 * about.
 *
 * The message carries opaque ids and short codes only. Page text, error
 * details, logs, command output, tokens, addresses and raw query strings never
 * enter it; every value passes through the sanitizer below, whatever the caller
 * handed in. Display names are labels only.
 */
import { sanitizeSensitiveText } from "#lib/security/sensitive-data.js";

export const VIEW_CONTEXT_SCHEMA = "kombify.view-context.v1";

export const VIEW_CONTEXT_FACT_KEYS = [
  "stackId",
  "jobId",
  "jobType",
  "leaseId",
  "serverId",
  "failedStep",
  "reasonCode",
  "homelabId",
  "serviceId",
  "outcome",
] as const;

export type ViewContextFactKey = (typeof VIEW_CONTEXT_FACT_KEYS)[number];

export interface ViewContextSubject {
  resourceType: string;
  resourceId: string;
  /** Short display name (stack or server name); user-controlled, label only. */
  label?: string;
}

/** What one page adds on top of the route the layout already describes. */
export interface ViewContextDetails {
  state?: string;
  subject?: ViewContextSubject;
  facts?: Partial<Record<ViewContextFactKey, string | null | undefined>>;
}

export interface ViewContextInput extends ViewContextDetails {
  route: string;
  title: string;
}

/** The sanitized payload of a `view-context` message, without envelope. */
export interface ViewContextBody {
  route: string;
  title: string;
  state?: string;
  subject?: ViewContextSubject;
  facts?: Record<string, string>;
}

const MAX_ROUTE = 240;
const MAX_TITLE = 120;
const MAX_CODE = 64;
const MAX_LABEL = 80;
const MAX_FACTS = 16;
const FALLBACK_TITLE = "kombify Techstack";

// C0 and C1 controls plus the Unicode line and paragraph separators.
const CONTROL_CHARS = new RegExp(
  `[\u0000-\u001f\u007f-\u009f${String.fromCharCode(0x2028, 0x2029)}]`,
  "g",
);
const SNAKE_CASE = /^[a-z][a-z0-9_]*$/;
const OPAQUE_VALUE = /^[A-Za-z0-9._:@/+-]{1,160}$/;
// Hex and colons with at least two colons: an IPv6 or MAC address.
const ADDRESS_LIKE = /^(?=(?:[^:]*:){2})[0-9a-f:.]+$/i;

function capChars(value: string, max: number): string {
  const chars = Array.from(value);
  return chars.length <= max ? value : chars.slice(0, max).join("");
}

function cleanLine(value: unknown, max: number): string {
  if (typeof value !== "string") return "";
  return capChars(
    value.replace(CONTROL_CHARS, " ").replace(/\s+/g, " ").trim(),
    max,
  );
}

function cleanRoute(value: unknown): string {
  const raw = typeof value === "string" ? value : "";
  // Pathname only: the query and the hash can carry anything.
  const path = (raw.split(/[?#]/, 1)[0] ?? "")
    .replace(CONTROL_CHARS, "")
    .replace(/\s/g, "");
  return capChars(path.startsWith("/") ? path : `/${path}`, MAX_ROUTE);
}

function cleanCode(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  return value.length <= MAX_CODE && SNAKE_CASE.test(value) ? value : undefined;
}

/**
 * An opaque id or short code. Anything that is not exactly that, or that the
 * secret scrubber would rewrite (addresses, e-mail addresses, known tokens),
 * is dropped rather than repaired.
 */
function cleanOpaque(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  if (!OPAQUE_VALUE.test(value) || ADDRESS_LIKE.test(value)) return undefined;
  return sanitizeSensitiveText(value) === value ? value : undefined;
}

function cleanSubject(
  subject: ViewContextSubject | undefined,
): ViewContextSubject | undefined {
  if (!subject) return undefined;
  const resourceType = cleanCode(subject.resourceType);
  const resourceId = cleanOpaque(subject.resourceId);
  if (!resourceType || !resourceId) return undefined;
  const label = cleanLine(
    sanitizeSensitiveText(
      typeof subject.label === "string"
        ? subject.label.replace(CONTROL_CHARS, " ")
        : "",
    ),
    MAX_LABEL,
  );
  return { resourceType, resourceId, ...(label ? { label } : {}) };
}

function cleanFacts(
  facts: ViewContextDetails["facts"],
): Record<string, string> | undefined {
  if (!facts) return undefined;
  const clean: Record<string, string> = {};
  // Only the known keys travel: an unknown key is not a fact the host reads.
  for (const key of VIEW_CONTEXT_FACT_KEYS) {
    if (Object.keys(clean).length >= MAX_FACTS) break;
    const value = cleanOpaque(facts[key]);
    if (value) clean[key] = value;
  }
  return Object.keys(clean).length > 0 ? clean : undefined;
}

/** Reduce whatever a page and the layout handed in to the wire payload. */
export function sanitizeViewContext(input: ViewContextInput): ViewContextBody {
  const state = cleanCode(input.state);
  const subject = cleanSubject(input.subject);
  const facts = cleanFacts(input.facts);
  return {
    route: cleanRoute(input.route),
    title: cleanLine(input.title, MAX_TITLE) || FALLBACK_TITLE,
    ...(state ? { state } : {}),
    ...(subject ? { subject } : {}),
    ...(facts ? { facts } : {}),
  };
}

const ROUTE_TITLES: ReadonlyArray<readonly [RegExp, string]> = [
  [/^\/(?:dashboard)?$/, "Homelab dashboard"],
  [/^\/stacks$/, "StackKit deployments"],
  [/^\/stacks\/new$/, "Creation wizard"],
  [/^\/stacks\/creating$/, "Stack creation progress"],
  [/^\/stacks\/[^/]+\/servers\/new$/, "Add server"],
  [/^\/stacks\/[^/]+\/servers\/[^/]+$/, "Server detail"],
  [/^\/stacks\/[^/]+$/, "StackKit deployment"],
  [/^\/services$/, "Services"],
  [/^\/services\/[^/]+$/, "Service detail"],
  [/^\/monitoring$/, "Monitoring"],
  [/^\/monitoring\/[^/]+$/, "Server monitoring"],
  [/^\/actors$/, "People and agents"],
  [/^\/servers$/, "Servers"],
  [/^\/nodes$/, "Servers"],
  [/^\/wallet$/, "Wallet"],
  [/^\/settings$/, "Settings"],
  [/^\/notifications$/, "Notifications"],
  [/^\/login$/, "Sign in"],
  [/^\/client\/onboarding$/, "Client onboarding"],
  [/^\/client\/local$/, "Local client"],
  [/^\/preview\/creation-wizard$/, "Creation wizard preview"],
];

/**
 * A stable English page name for a route. The host's agent reads it, so it
 * does not follow the interface language and does not change with the page's
 * content.
 */
export function titleForRoute(pathname: string): string {
  const path = pathname.length > 1 ? pathname.replace(/\/+$/, "") : pathname;
  for (const [pattern, title] of ROUTE_TITLES) {
    if (pattern.test(path)) return title;
  }
  const segment = path.split("/").find(Boolean) ?? "";
  const words = segment.replace(/[^A-Za-z0-9]+/g, " ").trim();
  return words ? `Techstack ${words}` : "Techstack";
}
