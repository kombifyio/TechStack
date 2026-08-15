export const cloudDeviceLoginUrl = "https://kombify.io/device";
export const cloudUiLoginUrl =
  "https://techstack.kombify.io/login?manual=1&client=windows";
export const windowsLocalClientReturnUrl = "/client/local?client=windows";
export const windowsClientContextStorageKey = "techstack.windowsClientContext";
export const windowsLocalClientContextValue = "local";

export const cloudLoginUrl = cloudDeviceLoginUrl;

export function normalizeServerUrl(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return "";
  if (/^https?:\/\//i.test(trimmed)) return trimmed.replace(/\/+$/, "");
  return `https://${trimmed.replace(/\/+$/, "")}`;
}

export function windowsClientModeLabel(mode: string | null): string {
  if (mode === "cloud") return "kombify Cloud";
  if (mode === "server") return "Server connection";
  return "Local Windows installation";
}

export function deriveLocalOwnerName(email: string): string {
  const normalized = email.trim();
  return normalized.split("@")[0]?.trim() || normalized;
}

export function localSetupReady(email: string, password: string): boolean {
  return email.trim().length > 0 && password.length >= 8;
}

export function rememberWindowsLocalClientContext(storage: Storage): void {
  storage.setItem(
    windowsClientContextStorageKey,
    windowsLocalClientContextValue,
  );
}

export function hasWindowsLocalClientContext(storage: Storage): boolean {
  return (
    storage.getItem(windowsClientContextStorageKey) ===
    windowsLocalClientContextValue
  );
}

export function loginRedirectForWindowsLocalClient(
  storage: Storage,
  fallback = "/login",
): string {
  return hasWindowsLocalClientContext(storage)
    ? windowsLocalClientReturnUrl
    : fallback;
}
