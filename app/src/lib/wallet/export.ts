/**
 * Wallet import/export utilities
 * Supports encrypted JSON export and import
 */

import type { PBWalletItem, CredentialType } from "$lib/stores/wallet";
import { encrypt, decrypt, isCryptoAvailable } from "./crypto";

// Export format version for future compatibility
const EXPORT_VERSION = 1;

export interface WalletExport {
  version: number;
  exportedAt: string;
  itemCount: number;
  encrypted: boolean;
  data: string; // JSON stringified credentials or encrypted blob
}

export interface ExportableCredential {
  name: string;
  kind: CredentialType;
  username?: string;
  secret?: string;
  totp?: string;
  url?: string;
  notes?: string;
  expires_at?: string;
  auto_generated?: boolean;
}

function extractTotpFromNotes(notes?: string): string | undefined {
  const match = notes?.match(/TOTP:\s*([^\n]+)/i);
  const value = match?.[1]?.trim();
  return value ? value : undefined;
}

function stripTotpFromNotes(notes?: string): string | undefined {
  const stripped = notes?.replace(/TOTP:\s*[^\n]+\n?/gi, "").trim();
  return stripped ? stripped : undefined;
}

/**
 * Convert PBWalletItem to exportable format (removes PocketBase metadata)
 */
function toExportable(item: PBWalletItem): ExportableCredential {
  const legacyTotp = extractTotpFromNotes(item.notes);
  return {
    name: item.name,
    kind: item.kind,
    username: item.username,
    secret: item.secret,
    url: item.url,
    totp: item.totp || legacyTotp,
    notes: stripTotpFromNotes(item.notes),
    expires_at: item.expires_at,
    auto_generated: item.auto_generated,
  };
}

/**
 * Export wallet items to JSON (optionally encrypted)
 */
export async function exportWallet(
  items: PBWalletItem[],
  password?: string,
): Promise<WalletExport> {
  const exportData = items.map(toExportable);
  const jsonData = JSON.stringify(exportData, null, 2);

  if (password && password.length > 0) {
    if (!isCryptoAvailable()) {
      throw new Error(
        "Encryption not available. Please use a modern browser with HTTPS.",
      );
    }
    const encryptedData = await encrypt(jsonData, password);
    return {
      version: EXPORT_VERSION,
      exportedAt: new Date().toISOString(),
      itemCount: items.length,
      encrypted: true,
      data: encryptedData,
    };
  }

  return {
    version: EXPORT_VERSION,
    exportedAt: new Date().toISOString(),
    itemCount: items.length,
    encrypted: false,
    data: jsonData,
  };
}

/**
 * Import wallet items from JSON (handles encrypted and plain)
 */
export async function importWallet(
  exportFile: WalletExport,
  password?: string,
): Promise<ExportableCredential[]> {
  if (exportFile.version !== EXPORT_VERSION) {
    throw new Error(
      `Unsupported export version: ${exportFile.version}. Expected: ${EXPORT_VERSION}`,
    );
  }

  let jsonData: string;

  if (exportFile.encrypted) {
    if (!password) {
      throw new Error("Password required for encrypted export");
    }
    if (!isCryptoAvailable()) {
      throw new Error(
        "Decryption not available. Please use a modern browser with HTTPS.",
      );
    }
    jsonData = await decrypt(exportFile.data, password);
  } else {
    jsonData = exportFile.data;
  }

  try {
    const items = JSON.parse(jsonData) as ExportableCredential[];
    // Validate structure
    if (!Array.isArray(items)) {
      throw new Error("Invalid export format: expected array");
    }
    for (const item of items) {
      if (!item.name || !item.kind) {
        throw new Error("Invalid credential: missing name or kind");
      }
    }
    return items;
  } catch (err) {
    if (err instanceof SyntaxError) {
      throw new Error("Invalid export file: malformed JSON");
    }
    throw err;
  }
}

/**
 * Export wallet items to CSV format (KeePass/Generic compatible)
 */
export function exportToCSV(items: PBWalletItem[]): string {
  const headers = [
    "Group",
    "Title",
    "Username",
    "Password",
    "URL",
    "Notes",
    "TOTP",
  ];

  // Escape CSV value (handle commas, quotes, newlines)
  const escapeCSV = (value: string | undefined | null): string => {
    if (!value) return "";
    const str = String(value);
    if (str.includes('"') || str.includes(",") || str.includes("\n")) {
      return `"${str.replace(/"/g, '""')}"`;
    }
    return str;
  };

  const rows = items.map((item) => {
    const totp = item.totp || extractTotpFromNotes(item.notes) || "";
    const notesWithoutTotp = stripTotpFromNotes(item.notes) || "";

    // Format notes as "Key: Value" pairs
    let formattedNotes = notesWithoutTotp;
    if (
      item.stack_id ||
      item.service_id ||
      item.expires_at ||
      item.last_rotated
    ) {
      const customFields: string[] = [];
      if (notesWithoutTotp) customFields.push(notesWithoutTotp);
      if (item.stack_id) customFields.push(`Stack ID: ${item.stack_id}`);
      if (item.service_id) customFields.push(`Service ID: ${item.service_id}`);
      if (item.expires_at)
        customFields.push(
          `Expires: ${new Date(item.expires_at).toISOString()}`,
        );
      if (item.last_rotated)
        customFields.push(
          `Last Rotated: ${new Date(item.last_rotated).toISOString()}`,
        );
      formattedNotes = customFields.join("\n");
    }

    return [
      escapeCSV(item.kind), // Group (credential type)
      escapeCSV(item.name), // Title
      escapeCSV(item.username), // Username
      escapeCSV(item.secret), // Password/Secret
      escapeCSV(item.url), // URL
      escapeCSV(formattedNotes), // Notes
      escapeCSV(totp), // TOTP
    ].join(",");
  });

  return [headers.join(","), ...rows].join("\n");
}

/**
 * Export wallet items to Bitwarden JSON format
 */
export function exportToBitwardenJSON(items: PBWalletItem[]): string {
  interface BitwardenItem {
    type: number; // 1 = login, 2 = secure note, 3 = card, 4 = identity
    name: string;
    notes: string | null;
    favorite: boolean;
    login?: {
      username: string | null;
      password: string | null;
      totp: string | null;
      uris: Array<{ match: null; uri: string }> | null;
    };
    secureNote?: {
      type: number;
    };
  }

  const bitwardenItems: BitwardenItem[] = items.map((item) => {
    const totp = item.totp || extractTotpFromNotes(item.notes) || null;

    // Check if this is a password/login type
    if (
      item.kind === "password" ||
      item.kind === "api_key" ||
      item.kind === "oauth_token"
    ) {
      return {
        type: 1, // Login type
        name: item.name,
        notes: stripTotpFromNotes(item.notes) || null,
        favorite: false,
        login: {
          username: item.username || null,
          password: item.secret || null,
          totp: totp,
          uris: item.url ? [{ match: null, uri: item.url }] : null,
        },
      };
    } else {
      // Export as secure note for SSH keys, certificates, etc.
      let noteContent = `Type: ${item.kind}\n`;
      if (item.username) noteContent += `Username: ${item.username}\n`;
      if (item.secret) noteContent += `Secret: ${item.secret}\n`;
      if (totp) noteContent += `TOTP: ${totp}\n`;
      if (item.url) noteContent += `URL: ${item.url}\n`;
      if (stripTotpFromNotes(item.notes))
        noteContent += `\n${stripTotpFromNotes(item.notes)}`;

      return {
        type: 2, // Secure note type
        name: item.name,
        notes: noteContent,
        favorite: false,
        secureNote: {
          type: 0, // Generic note
        },
      };
    }
  });

  const bitwardenExport = {
    encrypted: false,
    folders: [],
    items: bitwardenItems,
  };

  return JSON.stringify(bitwardenExport, null, 2);
}

/**
 * Download export file to user's device
 */
export function downloadExport(
  exportData: WalletExport,
  filename?: string,
): void {
  const json = JSON.stringify(exportData, null, 2);
  const blob = new Blob([json], { type: "application/json" });
  const url = URL.createObjectURL(blob);

  const date = new Date().toISOString().split("T")[0];
  const defaultFilename = `techstack-wallet-${date}${exportData.encrypted ? "-encrypted" : ""}.json`;

  const a = document.createElement("a");
  a.href = url;
  a.download = filename || defaultFilename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

/**
 * Download CSV export
 */
export function downloadCSV(csvContent: string, filename?: string): void {
  const blob = new Blob([csvContent], { type: "text/csv;charset=utf-8;" });
  const url = URL.createObjectURL(blob);

  const date = new Date().toISOString().split("T")[0];
  const defaultFilename = `techstack-wallet-${date}.csv`;

  const a = document.createElement("a");
  a.href = url;
  a.download = filename || defaultFilename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

/**
 * Download Bitwarden JSON export
 */
export function downloadBitwardenJSON(
  jsonContent: string,
  filename?: string,
): void {
  const blob = new Blob([jsonContent], { type: "application/json" });
  const url = URL.createObjectURL(blob);

  const date = new Date().toISOString().split("T")[0];
  const defaultFilename = `techstack-wallet-bitwarden-${date}.json`;

  const a = document.createElement("a");
  a.href = url;
  a.download = filename || defaultFilename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

/**
 * Read export file from File input
 */
export async function readExportFile(file: File): Promise<WalletExport> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      try {
        const result = reader.result as string;
        const parsed = JSON.parse(result) as WalletExport;

        // Validate structure
        if (
          typeof parsed.version !== "number" ||
          typeof parsed.encrypted !== "boolean" ||
          typeof parsed.data !== "string"
        ) {
          throw new Error("Invalid export file structure");
        }

        resolve(parsed);
      } catch (err) {
        reject(
          err instanceof Error ? err : new Error("Failed to parse export file"),
        );
      }
    };
    reader.onerror = () => reject(new Error("Failed to read file"));
    reader.readAsText(file);
  });
}
