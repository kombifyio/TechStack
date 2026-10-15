import { describe, expect, it } from "vitest";
import { sanitizeViewContext, type ViewContextInput } from "./view-context";

// The view context goes to the host page, which hands it to an agent. What a
// page holds beyond ids and short codes must not be able to ride along.
describe("view context sanitizing", () => {
  it("keeps hostile page data out of what the host receives", () => {
    const hostile = {
      route: "/stacks/creating?job_id=job-1&token=kpt1.secret#registration",
      title: "Creation\nfailed",
      state: "creation failed\n",
      subject: {
        resourceType: "stackkit_deployment",
        resourceId: "stack-1",
        label: `base\u0000ment\u001b[31m ${"x".repeat(300)}`,
      },
      facts: {
        stackId: "stack-1",
        jobId: "job-1",
        reasonCode: "typed stackkit\napply timeout",
        leaseId: "203.0.113.7",
        serverId: "ops@example.com",
        failedStep: "ks_" + "a1".repeat(32),
        registrationToken: "tsra.opaque.abc",
        serverAddress: "198.51.100.9",
      },
    } as unknown as ViewContextInput;

    const sent = sanitizeViewContext(hostile);
    const wire = JSON.stringify(sent);

    // Nothing the page should keep to itself reaches the message.
    for (const leaked of [
      "kpt1",
      "secret",
      "registration",
      "203.0.113.7",
      "198.51.100.9",
      "ops@example.com",
      "ks_",
      "tsra",
      "apply timeout",
    ]) {
      expect(wire).not.toContain(leaked);
    }
    for (const text of [sent.route, sent.title, sent.subject?.label ?? ""]) {
      expect(text).not.toMatch(/[\u0000-\u001f]/);
    }
    expect(sent.route).not.toMatch(/[?#]/);

    // Display text stays inside its caps.
    expect(sent.route.length).toBeLessThanOrEqual(240);
    expect(sent.title.length).toBeLessThanOrEqual(120);
    expect(sent.subject?.label?.length ?? 0).toBeLessThanOrEqual(80);

    // The ids the host needs still arrive.
    expect(sent.facts).toMatchObject({ stackId: "stack-1", jobId: "job-1" });
    expect(sent.subject?.resourceId).toBe("stack-1");
  });
});
