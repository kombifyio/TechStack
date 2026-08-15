import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const serverDetail = readFileSync(
  resolve(
    process.cwd(),
    "src/routes/stacks/[id]/servers/[serverId]/+page.svelte",
  ),
  "utf8",
);
const stackDashboard = readFileSync(
  resolve(process.cwd(), "src/routes/stacks/+page.svelte"),
  "utf8",
);

describe("server detail navigation contract", () => {
  it("places navigation before overview-only identity and metric cards", () => {
    const tabs = serverDetail.indexOf('data-testid="server-details-tabs"');
    const overview = serverDetail.indexOf(
      '{#if activeTab === "overview"}',
      tabs,
    );
    const identityCard = serverDetail.indexOf("<header", overview);
    const metrics = serverDetail.indexOf(
      'class="mb-6 grid gap-3 sm:grid-cols-2 xl:grid-cols-4"',
      identityCard,
    );

    expect(tabs).toBeGreaterThan(-1);
    expect(overview).toBeGreaterThan(tabs);
    expect(identityCard).toBeGreaterThan(overview);
    expect(metrics).toBeGreaterThan(identityCard);
  });

  it("keeps decommission behind the Settings danger zone confirmation", () => {
    expect(serverDetail).toContain(
      '["overview", "services", "checks", "logs", "settings"]',
    );
    expect(serverDetail).toContain("data-testid={`server-tab-${tab}`}");
    expect(serverDetail).toContain('data-testid="server-settings-panel"');
    expect(serverDetail).toContain('data-testid="server-danger-zone"');
    expect(serverDetail).toContain(
      'data-testid="server-decommission-confirmation"',
    );
    expect(serverDetail).toContain(
      'data-testid="server-decommission-confirm-button"',
    );
    expect(serverDetail).not.toContain("forceDecommissionMonthlyRuntime");
    expect(serverDetail).not.toContain("Force decommission");
  });

  it("does not expose decommission as a dashboard server-card action", () => {
    expect(stackDashboard).not.toContain(
      'data-testid="decommission-server-button"',
    );
    expect(stackDashboard).not.toContain(
      'data-testid="force-decommission-server-button"',
    );
    expect(stackDashboard).not.toContain("forceDecommissionMonthlyRuntime");
  });

  it("keeps lifecycle actions contextual to a server instead of a global picker", () => {
    expect(stackDashboard).not.toContain("StackKitLifecycleActions");
    expect(stackDashboard).not.toContain(
      'data-testid="stackkit-lifecycle-button"',
    );
    expect(serverDetail).toContain('data-testid="server-lifecycle-actions"');
    expect(serverDetail).toContain(
      "data-testid={`server-lifecycle-${action.operation}`}",
    );
    expect(serverDetail).toContain(
      'data-testid="server-lifecycle-confirmation"',
    );
    expect(serverDetail).not.toContain("stackkit-lifecycle-agent");
  });
});
