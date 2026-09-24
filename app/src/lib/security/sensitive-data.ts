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
const KOMBIFY_TOKEN =
  /\b(?:kpt1\.[A-Za-z0-9_-]{1,340}\.[A-Za-z0-9_-]{43}|tsra\.(?:opaque|[A-Za-z0-9_-]{8,})\.[A-Za-z0-9_-]{43}|ks_[a-fA-F0-9]{64})\b/g;
const ASSIGNMENT =
  /\b([A-Za-z_]*(?:password|passwd|pwd|secret|api[_-]?key|token))\s*[=:]\s*[^\s,;]+/gi;
const MAX_TEXT = 2_000;
const MAX_DEPTH = 6;

export function sanitizeSensitiveText(value: unknown): string {
  return String(value ?? "")
    .replace(PRIVATE_KEY, "[private-key]")
    .replace(BEARER, "Bearer [redacted]")
    .replace(KNOWN_TOKEN, "[token]")
    .replace(KOMBIFY_TOKEN, "[token]")
    .replace(ASSIGNMENT, "$1=[redacted]")
    .replace(EMAIL, "[email]")
    .replace(IPV4, "[ip]")
    .replace(URL, "[url]")
    .slice(0, MAX_TEXT);
}

export function scrubSensitiveValue(value: unknown, depth = 0): unknown {
  if (value == null) return value;
  if (typeof value === "string") return sanitizeSensitiveText(value);
  if (typeof value !== "object") return value;
  if (depth > MAX_DEPTH) return "[truncated]";
  if (Array.isArray(value)) {
    return value
      .slice(0, 100)
      .map((item) => scrubSensitiveValue(item, depth + 1));
  }

  const result: Record<string, unknown> = {};
  for (const [key, child] of Object.entries(value)) {
    result[key] = SENSITIVE_KEY.test(key)
      ? "[redacted]"
      : scrubSensitiveValue(child, depth + 1);
  }
  return result;
}

export function isSensitiveKey(key: string): boolean {
  return SENSITIVE_KEY.test(key);
}
