import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { buildGrubConfig, buildUserData } from "./ubuntu-netboot.mjs";

const options = {
  runId: "bm-0123456789abcdef",
  controllerUrl: "http://192.0.2.20:8765",
};

describe("Ubuntu bare-metal seed", () => {
  it("selects and recursively normalizes the largest physical disk", () => {
    const userData = buildUserData({
      ...options,
      sshPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
    });

    assert.match(userData, /size: largest/);
    assert.match(userData, /wipe: superblock-recursive/);
    assert.match(userData, /lvremove --force --yes/);
    assert.match(userData, /path: \/boot\/efi/);
    assert.match(userData, /path: \/\n/);
    assert.doesNotMatch(
      userData,
      /\n      match:\n        size: largest\n  user-data:/,
    );
  });

  it("boots with unattended NoCloud data and the local ISO", () => {
    const grub = buildGrubConfig(options);

    assert.match(grub, /\bautoinstall\b/);
    assert.match(grub, /cloud-config-url=\/dev\/null/);
    assert.match(
      grub,
      /url=http:\/\/192\.0\.2\.20:8765\/images\/ubuntu-24\.04\.4-live-server-amd64\.iso/,
    );
    assert.match(
      grub,
      /ds=nocloud-net;s=http:\/\/192\.0\.2\.20:8765\/seed\/bm-0123456789abcdef\//,
    );
  });
});
