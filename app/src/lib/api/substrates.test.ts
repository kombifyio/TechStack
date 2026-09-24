import { expect, it } from "vitest";
import { verifiedProxmoxDevices } from "./substrates";
import type { DiscoveredDevice } from "#lib/discovery/types.js";

it("exposes discovery candidates only after a verified product fingerprint", () => {
  const candidates = [
    { device_id: "port-only", services: [{ name: "https", port: 8006 }] },
    {
      device_id: "unverified",
      services: [{ name: "proxmox", port: 8006, fingerprint_verified: false }],
    },
    {
      device_id: "verified",
      services: [{ name: "proxmox", port: 8006, fingerprint_verified: true }],
    },
  ] as DiscoveredDevice[];
  expect(
    verifiedProxmoxDevices(candidates).map((item) => item.device_id),
  ).toEqual(["verified"]);
});
