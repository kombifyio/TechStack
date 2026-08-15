import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import type { ViteUserConfig } from "vitest/config";
import { loadConfigFromFile } from "vite";

describe("Vitest resource contract", () => {
  it("bounds worker concurrency without widening test timeouts", async () => {
    const loaded = await loadConfigFromFile(
      { command: "serve", mode: "test" },
      resolve(process.cwd(), "vitest.config.ts"),
      process.cwd(),
    );

    expect(loaded).not.toBeNull();
    const config = loaded?.config as ViteUserConfig | undefined;
    expect(config?.test?.maxWorkers).toBe(1);
    expect(config?.test).not.toHaveProperty("testTimeout");
  });
});
