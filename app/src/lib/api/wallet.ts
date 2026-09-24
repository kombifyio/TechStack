import { fetchApi, post } from "./client";
import type { WalletItem } from "#lib/wallet/types.js";
export type { WalletItem } from "#lib/wallet/types.js";

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

interface WalletListResponse {
  items?: WalletItem[];
}

type WalletItemWrite = Omit<Partial<WalletItem>, "kit_deployment_id"> & {
  stack_id?: string;
};

function walletItemToWire(item: Partial<WalletItem>): WalletItemWrite {
  const { kit_deployment_id, ...walletItem } = item;
  return {
    ...walletItem,
    stack_id: kit_deployment_id,
    source_type:
      walletItem.source_type === "kit_deployment"
        ? "stack"
        : walletItem.source_type,
  };
}

function walletItemsFromResponse(
  response: WalletListResponse | WalletItem[],
): WalletItem[] {
  return Array.isArray(response) ? response : (response.items ?? []);
}

export async function getWalletItems(): Promise<WalletItem[]> {
  const res = await fetchApi<WalletListResponse | WalletItem[]>(
    "/api/v1/wallet",
    { method: "GET", credentials: "include" },
  );
  return walletItemsFromResponse(res.data);
}

export async function getWalletItemsByKitDeployment(
  kitDeploymentId: string,
): Promise<WalletItem[]> {
  const res = await fetchApi<WalletListResponse | WalletItem[]>(
    `/api/v1/wallet?stack_id=${encodeURIComponent(kitDeploymentId)}`,
    { method: "GET", credentials: "include" },
  );
  return walletItemsFromResponse(res.data);
}

export async function createWalletItem(
  data: Partial<WalletItem>,
): Promise<WalletItem> {
  const res = await fetchApi<WalletItem>("/api/v1/wallet", {
    method: "POST",
    credentials: "include",
    body: JSON.stringify(walletItemToWire(data)),
  });
  return res.data;
}

export async function updateWalletItem(
  id: string,
  data: Partial<WalletItem>,
): Promise<WalletItem> {
  const res = await fetchApi<WalletItem>(
    `/api/v1/wallet/${encodeURIComponent(id)}`,
    {
      method: "PATCH",
      credentials: "include",
      body: JSON.stringify(walletItemToWire(data)),
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
): Promise<WalletItem> {
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
): Promise<Pick<WalletItem, "id" | "secret" | "totp">> {
  const res = await fetchApi<Pick<WalletItem, "id" | "secret" | "totp">>(
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
