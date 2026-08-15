const SENSITIVE_KEY =
  /(authorization|cookie|password|passwd|secret|token|api[_-]?key|client[_-]?secret|private[_-]?key|credential)/i;
const EMAIL = /\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b/gi;
const IPV4 = /\b\d{1,3}(?:\.\d{1,3}){3}\b/g;
const URL = /\bhttps?:\/\/[^\s"'<>]+/gi;
const PRIVATE_KEY =
  /-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----/g;
const BEARER = /\bBearer\s+[A-Za-z0-9._~-]{16,}\b/gi;
const KNOWN_TOKEN =
  /\b(?:dp\.(?:pt|st)\.[A-Za-z0-9._-]{16,}|(?:gh[opusr]|github_pat)_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}|rnd_[A-Za-z0-9]{20,}|xox[abprs]-[A-Za-z0-9-]{10,})\b/g;
const ASSIGNMENT =
  /\b([A-Za-z_]*(?:password|passwd|pwd|secret|api[_-]?key|token))\s*[=:]\s*[^\s,;]+/gi;
const MAX_TEXT = 2_000;
const MAX_DEPTH = 6;

export function sanitizeSentryText(value: unknown): string {
  return String(value ?? "")
    .replace(PRIVATE_KEY, "[private-key]")
    .replace(BEARER, "Bearer [redacted]")
    .replace(KNOWN_TOKEN, "[token]")
    .replace(ASSIGNMENT, "$1=[redacted]")
    .replace(EMAIL, "[email]")
    .replace(IPV4, "[ip]")
    .replace(URL, "[url]")
    .slice(0, MAX_TEXT);
}

export function scrubSentryEvent<T extends Record<string, any>>(event: T): T {
  const mutable = event as Record<string, any>;
  if (mutable.request) {
    mutable.request.data = undefined;
    mutable.request.cookies = undefined;
    mutable.request.query_string = undefined;
    if (typeof mutable.request.url === "string") {
      mutable.request.url = mutable.request.url.split(/[?#]/, 1)[0];
    }
    if (mutable.request.headers) {
      for (const key of Object.keys(mutable.request.headers)) {
        if (
          SENSITIVE_KEY.test(key) ||
          key.toLowerCase().startsWith("x-user-") ||
          key.toLowerCase().startsWith("x-org-")
        ) {
          mutable.request.headers[key] = "[redacted]";
        }
      }
    }
  }

  for (const key of ["contexts", "extra", "tags"] as const) {
    if (mutable[key]) mutable[key] = scrubValue(mutable[key], 0);
  }
  if (Array.isArray(mutable.breadcrumbs)) {
    mutable.breadcrumbs = mutable.breadcrumbs.map((breadcrumb: any) => ({
      ...breadcrumb,
      message: sanitizeSentryText(breadcrumb?.message),
      data: scrubValue(breadcrumb?.data, 0),
    }));
  }
  if (typeof mutable.message === "string") {
    mutable.message = sanitizeSentryText(mutable.message);
  }
  if (mutable.exception?.values) {
    mutable.exception.values = mutable.exception.values.map(
      (exception: any) => ({
        ...exception,
        value: sanitizeSentryText(exception?.value),
      }),
    );
  }
  return event;
}

function scrubValue(value: unknown, depth: number): unknown {
  if (depth > MAX_DEPTH || value == null) return value;
  if (typeof value === "string") return sanitizeSentryText(value);
  if (typeof value !== "object") return value;
  if (Array.isArray(value)) {
    return value.slice(0, 100).map((item) => scrubValue(item, depth + 1));
  }

  const result: Record<string, unknown> = {};
  for (const [key, child] of Object.entries(value)) {
    result[key] = SENSITIVE_KEY.test(key)
      ? "[redacted]"
      : scrubValue(child, depth + 1);
  }
  return result;
}
