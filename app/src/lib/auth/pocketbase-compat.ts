export const POCKETBASE_AUTH_STORAGE_KEY = "pocketbase_auth";

export type AuthModel = {
  id?: string;
  email?: string;
  name?: string;
  [key: string]: unknown;
};

export type PocketBaseCompatAuthStore = {
  token?: string;
  model?: unknown | null;
  record?: unknown | null;
  isValid?: boolean;
  clear(): void;
  save(token: string, model?: unknown | null): void;
  onChange(
    callback: (token: string, model: unknown | null) => void,
  ): () => void;
};

export function isPocketBaseAuthCompatEnabled(): boolean {
  return import.meta.env.VITE_ENABLE_POCKETBASE_AUTH_COMPAT === "true";
}

export function getPocketBaseCompatAuthToken(
  authStore: Pick<PocketBaseCompatAuthStore, "token">,
): string {
  if (authStore.token) return authStore.token;
  return getPocketBaseCompatStoredAuthToken();
}

export function getPocketBaseCompatStoredAuthToken(): string {
  if (typeof window === "undefined") return "";

  try {
    const raw = window.localStorage.getItem(POCKETBASE_AUTH_STORAGE_KEY);
    if (!raw) return "";
    const parsed = JSON.parse(raw) as { token?: string };
    return parsed?.token || "";
  } catch {
    return "";
  }
}

export function getPocketBaseCompatStoredUser(): AuthModel | null {
  if (typeof window === "undefined") return null;

  try {
    const raw = window.localStorage.getItem(POCKETBASE_AUTH_STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as {
      model?: AuthModel | null;
      record?: AuthModel | null;
    };
    return parsed?.model ?? parsed?.record ?? null;
  } catch {
    return null;
  }
}

export function getPocketBaseCompatStoredAuthHeaders(): Record<string, string> {
  const token = getPocketBaseCompatStoredAuthToken();
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export function savePocketBaseCompatStoredSession(
  token: string,
  model?: AuthModel | null,
): void {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(
    POCKETBASE_AUTH_STORAGE_KEY,
    JSON.stringify({ token, model: model ?? null }),
  );
}

export function clearPocketBaseCompatStoredSession(): void {
  if (typeof window === "undefined") return;
  window.localStorage.removeItem(POCKETBASE_AUTH_STORAGE_KEY);
}

export function getPocketBaseCompatAuthHeaders(
  authStore: Pick<PocketBaseCompatAuthStore, "token">,
): Record<string, string> {
  const token = getPocketBaseCompatAuthToken(authStore);
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export function getPocketBaseCompatUser(
  authStore: Pick<PocketBaseCompatAuthStore, "model">,
): AuthModel | null {
  return (authStore.model as AuthModel | null | undefined) ?? null;
}

export function isPocketBaseCompatSessionActive(
  authStore: Pick<PocketBaseCompatAuthStore, "isValid">,
): boolean {
  return Boolean(authStore.isValid);
}

export function savePocketBaseCompatSession(
  authStore: Pick<PocketBaseCompatAuthStore, "save">,
  token: string,
  model?: AuthModel | null,
): void {
  authStore.save(token, model ?? null);
}

export function clearPocketBaseCompatSession(
  authStore: Pick<PocketBaseCompatAuthStore, "clear">,
): void {
  authStore.clear();
}

export function subscribePocketBaseCompatAuth(
  authStore: Pick<PocketBaseCompatAuthStore, "onChange">,
  callback: (user: AuthModel | null) => void,
): () => void {
  return authStore.onChange((_, model) =>
    callback((model as AuthModel | null | undefined) ?? null),
  );
}
