import type { PBWalletItem, WalletEntryArea } from "$lib/stores/wallet";

type WalletEntryPayloadOptions = {
  sourceType?: string;
  sourceRef?: string;
};

function walletEntryHasSensitiveValue(data: Partial<PBWalletItem>): boolean {
  return Boolean(data.secret?.trim() || data.totp?.trim());
}

function inferWalletSourceMetadata(
  data: Partial<PBWalletItem>,
  options?: WalletEntryPayloadOptions,
): { sourceType: string; sourceRef?: string } {
  if (data.source_type?.trim()) {
    return {
      sourceType: data.source_type,
      sourceRef: data.source_ref?.trim() || undefined,
    };
  }

  const serviceID = data.service_id?.trim();
  const stackID = data.stack_id?.trim();

  if (options?.sourceType?.trim()) {
    return {
      sourceType: options.sourceType,
      sourceRef:
        data.source_ref?.trim() || options.sourceRef?.trim() || undefined,
    };
  }

  if (serviceID?.startsWith("system:")) {
    return { sourceType: "system_account", sourceRef: serviceID };
  }

  if (serviceID) {
    return { sourceType: "service", sourceRef: serviceID };
  }

  if (stackID) {
    return {
      sourceType: "stack",
      sourceRef:
        data.source_ref?.trim() || options?.sourceRef?.trim() || stackID,
    };
  }

  return {
    sourceType: "manual",
    sourceRef:
      data.source_ref?.trim() || options?.sourceRef?.trim() || undefined,
  };
}

export function buildWalletEntryPayload(
  area: WalletEntryArea,
  data: Partial<PBWalletItem>,
  options?: WalletEntryPayloadOptions,
): Partial<PBWalletItem> {
  const hasSensitiveValue = walletEntryHasSensitiveValue(data);
  const source = inferWalletSourceMetadata(data, options);

  switch (area) {
    case "tools":
      return {
        ...data,
        item_class: "launch",
        access_mode: "open",
        revealable: hasSensitiveValue,
        source_type: source.sourceType,
        source_ref: source.sourceRef,
      };
    case "access":
      return {
        ...data,
        item_class: data.username?.trim() ? "user_account" : "manual",
        access_mode: "manage",
        revealable: hasSensitiveValue,
        source_type: source.sourceType,
        source_ref: source.sourceRef,
      };
    default:
      return {
        ...data,
        item_class: "recovery",
        access_mode: "reveal",
        revealable: hasSensitiveValue,
        source_type: source.sourceType,
        source_ref: source.sourceRef,
      };
  }
}
