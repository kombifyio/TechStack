export type CredentialType =
  "password" | "api_key" | "ssh_key" | "oauth_token" | "certificate" | "other";

export type WalletEntryArea = "tools" | "access" | "recovery";

export type WalletItemClass =
  | "launch"
  | "user_account"
  | "credential"
  | "recovery"
  | "machine_secret_ref"
  | "manual";

export type WalletAccessMode = "open" | "reveal" | "manage";

export type WalletItem = {
  id: string;
  collectionId?: string;
  collectionName?: string;
  created?: string;
  updated?: string;
  name: string;
  kind: CredentialType;
  username?: string;
  secret?: string;
  totp?: string;
  notes?: string;
  url?: string;
  kit_deployment_id?: string;
  service_id?: string;
  expires_at?: string;
  last_rotated?: string;
  auto_generated?: boolean;
  owner_id?: string;
  item_class?: WalletItemClass;
  source_type?: string;
  source_ref?: string;
  access_mode?: WalletAccessMode;
  revealable?: boolean;
  has_secret?: boolean;
  has_totp?: boolean;
};
