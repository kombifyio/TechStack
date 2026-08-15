import { describe, it, expect } from "vitest";
import type { PBWalletItem } from "$lib/stores/wallet";
import { exportToBitwardenJSON, exportToCSV, exportWallet } from "./export";

function walletItem(partial: Partial<PBWalletItem>): PBWalletItem {
  return {
    id: "id-1",
    collectionId: "wallet",
    collectionName: "wallet",
    created: new Date().toISOString(),
    updated: new Date().toISOString(),
    name: "",
    kind: "password",
    ...partial,
  } as PBWalletItem;
}

describe("wallet export", () => {
  it("exports CSV with headers and escapes values", () => {
    const csv = exportToCSV([
      walletItem({
        name: 'My, "Service"',
        kind: "password",
        username: "user@example.com",
        secret: "p@ss,word\nline2",
        url: "https://example.com",
        notes: "hello\nTOTP: legacy-secret\nmore",
      }),
    ]);

    expect(
      csv.startsWith("Group,Title,Username,Password,URL,Notes,TOTP\n"),
    ).toBe(true);

    // notes should have TOTP line stripped; TOTP column should be populated
    expect(csv).toContain('password,"My, ""Service"""');
    expect(csv).toContain('"p@ss,word\nline2"');
    expect(csv).toContain(",legacy-secret");
    expect(csv).not.toContain("TOTP:");
  });

  it("prefers explicit item.totp over notes", () => {
    const csv = exportToCSV([
      walletItem({
        name: "Example",
        kind: "password",
        totp: "explicit-secret",
        notes: "TOTP: legacy-secret",
      }),
    ]);

    const row = csv.split("\n")[1];
    expect(row.endsWith(",explicit-secret")).toBe(true);
  });

  it("exports Bitwarden login items with totp", () => {
    const json = exportToBitwardenJSON([
      walletItem({
        name: "Login",
        kind: "password",
        username: "u",
        secret: "s",
        totp: "otpauth://totp/Example?secret=ABC",
        url: "https://example.com",
      }),
    ]);

    const parsed = JSON.parse(json) as any;
    expect(parsed.encrypted).toBe(false);
    expect(parsed.items).toHaveLength(1);
    expect(parsed.items[0].type).toBe(1);
    expect(parsed.items[0].login.totp).toContain("otpauth://");
  });

  it("includes totp in native JSON export (unencrypted)", async () => {
    const out = await exportWallet([
      walletItem({
        name: "Native",
        kind: "password",
        notes: "TOTP: legacy-secret",
      }),
    ]);

    expect(out.encrypted).toBe(false);

    const items = JSON.parse(out.data) as any[];
    expect(items).toHaveLength(1);
    expect(items[0].totp).toBe("legacy-secret");
    expect(items[0].notes || "").not.toContain("TOTP:");
  });
});
