import {
  isSensitiveKey,
  sanitizeSensitiveText,
  scrubSensitiveValue,
} from "../security/sensitive-data";

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
          isSensitiveKey(key) ||
          key.toLowerCase().startsWith("x-user-") ||
          key.toLowerCase().startsWith("x-org-")
        ) {
          mutable.request.headers[key] = "[redacted]";
        }
      }
    }
  }

  for (const key of ["contexts", "extra", "tags"] as const) {
    if (mutable[key]) mutable[key] = scrubSensitiveValue(mutable[key]);
  }
  if (Array.isArray(mutable.breadcrumbs)) {
    mutable.breadcrumbs = mutable.breadcrumbs.map((breadcrumb: any) => ({
      ...breadcrumb,
      message: sanitizeSensitiveText(breadcrumb?.message),
      data: scrubSensitiveValue(breadcrumb?.data),
    }));
  }
  if (typeof mutable.message === "string") {
    mutable.message = sanitizeSensitiveText(mutable.message);
  }
  if (mutable.exception?.values) {
    mutable.exception.values = mutable.exception.values.map(
      (exception: any) => ({
        ...exception,
        value: sanitizeSensitiveText(exception?.value),
      }),
    );
  }
  return event;
}
