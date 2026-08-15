import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const serverProvisioningSource = readFileSync(
  resolve(
    process.cwd(),
    "src/lib/components/wizard/ServerProvisioningStep.svelte",
  ),
  "utf8",
);

describe("managed provider copy contract", () => {
  it("describes adapter selection without hard-coding a deployment platform", () => {
    expect(serverProvisioningSource).toContain(
      "deployment follows the StackKit plan and its available deployment",
    );
    expect(serverProvisioningSource).not.toMatch(
      /Cloud Kit uses (?:Coolify|Komodo)/i,
    );
  });
});
