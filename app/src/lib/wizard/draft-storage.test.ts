import { describe, expect, it } from "vitest";
import {
  clearWizardDraft,
  loadWizardDraft,
  mergeOverDefaults,
  saveWizardDraft,
  withoutSecrets,
} from "./draft-storage.js";
import { createDefaultConfig } from "./types.js";

function memoryStorage() {
  const items = new Map<string, string>();
  return {
    items,
    getItem: (key: string) => items.get(key) ?? null,
    setItem: (key: string, value: string) => void items.set(key, value),
    removeItem: (key: string) => void items.delete(key),
  };
}

describe("wizard draft", () => {
  it("never writes a password or a value derived from a typed secret", () => {
    const config = createDefaultConfig();
    config.serverProvisioning.remote.host = "server.example.com";
    config.serverProvisioning.remote.sshPassword = "hunter2-ssh-password";
    config.owner.recoveryPassphraseHash =
      "$argon2id$v=19$derived-from-passphrase";
    config.owner.email = "owner@example.com";

    const storage = memoryStorage();
    saveWizardDraft("test", { step: 3, config }, storage);
    const stored = [...storage.items.values()].join("\n");

    expect(stored).not.toContain("hunter2-ssh-password");
    expect(stored).not.toContain("derived-from-passphrase");
    // Everything else the person entered survives a reload.
    expect(stored).toContain("server.example.com");
    expect(stored).toContain("owner@example.com");
    // The live config is untouched.
    expect(config.serverProvisioning.remote.sshPassword).toBe(
      "hunter2-ssh-password",
    );
    expect(withoutSecrets(config).serverProvisioning.remote.sshPassword).toBe(
      "",
    );
  });

  it("restores the step and answers, and ends on clear", () => {
    const config = createDefaultConfig();
    config.goals!.photos = true;
    const storage = memoryStorage();
    saveWizardDraft("test", { step: 4, config }, storage);

    const draft = loadWizardDraft("test", storage);
    expect(draft?.step).toBe(4);
    expect(draft?.config.goals?.photos).toBe(true);

    clearWizardDraft("test", storage);
    expect(loadWizardDraft("test", storage)).toBeNull();
  });

  it("ignores unreadable drafts and keeps defaults for new fields", () => {
    const storage = memoryStorage();
    storage.setItem("techstack.wizardDraft.v1.test", "{not json");
    expect(loadWizardDraft("test", storage)).toBeNull();

    const defaults = createDefaultConfig();
    const merged = mergeOverDefaults(defaults, {
      owner: { email: "a@b.c" },
      futureField: 1,
    });
    expect(merged.owner.email).toBe("a@b.c");
    expect(merged.owner.bootstrapMode).toBe(defaults.owner.bootstrapMode);
  });
});
