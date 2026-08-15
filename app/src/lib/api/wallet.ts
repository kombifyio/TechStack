import { fetchApi, post } from "./client";
import type { PBWalletItem } from "$lib/stores/wallet";
export type { PBWalletItem } from "$lib/stores/wallet";

export type SystemAccountRole = "superuser" | "admin" | "developer";

export type WalletRevealOptions = {
  reason?: string;
  currentPassword?: string;
  reauthTimestamp?: string;
  reauthSignature?: string;
};

export type WalletReauthProof = {
  id: string;
  reauth_timestamp: string;
  reauth_signature: string;
  expires_at: string;
  reauth_method_hint?: string;
};

export interface WalletListResponse {
  items?: PBWalletItem[];
}

export async function getWalletItems(): Promise<PBWalletItem[]> {
  const res = await fetchApi<WalletListResponse | PBWalletItem[]>(
    "/api/v1/wallet",
    { method: "GET", credentials: "include" },
  );
  return Array.isArray(res.data) ? res.data : (res.data.items ?? []);
}

export async function getWalletItemsByStack(
  stackId: string,
): Promise<PBWalletItem[]> {
  const res = await fetchApi<WalletListResponse | PBWalletItem[]>(
    `/api/v1/wallet?stack_id=${encodeURIComponent(stackId)}`,
    { method: "GET", credentials: "include" },
  );
  return Array.isArray(res.data) ? res.data : (res.data.items ?? []);
}

export async function createWalletItem(
  data: Partial<PBWalletItem>,
): Promise<PBWalletItem> {
  const res = await fetchApi<PBWalletItem>("/api/v1/wallet", {
    method: "POST",
    credentials: "include",
    body: JSON.stringify(data),
  });
  return res.data;
}

export async function updateWalletItem(
  id: string,
  data: Partial<PBWalletItem>,
): Promise<PBWalletItem> {
  const res = await fetchApi<PBWalletItem>(
    `/api/v1/wallet/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      credentials: "include",
      body: JSON.stringify(data),
    },
  );
  return res.data;
}

export async function deleteWalletItem(id: string): Promise<boolean> {
  await fetchApi<{ ok?: boolean }>(`/api/v1/wallet/${encodeURIComponent(id)}`, {
    method: "DELETE",
    credentials: "include",
  });
  return true;
}

export async function rotateWalletItem(
  id: string,
  newSecret: string,
): Promise<PBWalletItem> {
  return updateWalletItem(id, {
    secret: newSecret,
    last_rotated: new Date().toISOString(),
  });
}

export async function requestWalletRevealProof(
  id: string,
  reason = "wallet reveal",
): Promise<WalletReauthProof> {
  const res = await fetchApi<WalletReauthProof>(
    `/api/v1/wallet/${encodeURIComponent(id)}/reauth-proof`,
    {
      method: "POST",
      credentials: "include",
      body: JSON.stringify({ reason }),
    },
  );
  if (!res.data.reauth_timestamp || !res.data.reauth_signature) {
    throw new Error("Wallet reveal authorization proof is incomplete");
  }
  return res.data;
}

export async function revealWalletItem(
  id: string,
  options: WalletRevealOptions = {},
): Promise<Pick<PBWalletItem, "id" | "secret" | "totp">> {
  const res = await fetchApi<Pick<PBWalletItem, "id" | "secret" | "totp">>(
    `/api/v1/wallet/${encodeURIComponent(id)}/reveal`,
    {
      method: "POST",
      credentials: "include",
      body: JSON.stringify({
        reason: options.reason || "wallet reveal",
        current_password: options.currentPassword,
        reauth_timestamp: options.reauthTimestamp,
        reauth_signature: options.reauthSignature,
      }),
    },
  );
  return res.data || { id };
}

export async function resetSystemAccountPassword(
  role: SystemAccountRole,
): Promise<{
  data: {
    role: SystemAccountRole;
    email: string;
    secretStored: boolean;
    walletServiceId: string;
  };
}> {
  return post(`/api/v1/system-accounts/${role}/reset`, { confirm: role });
}
