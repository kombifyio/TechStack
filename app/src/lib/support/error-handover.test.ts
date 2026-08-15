import { describe, expect, it } from "vitest";
import {
  buildErrorAiHandoverPrompt,
  createErrorAiHandoverContext,
} from "./error-handover";

describe("error AI handover", () => {
  it("builds a tenant-scoped support prompt from safe error context", () => {
    const context = createErrorAiHandoverContext({
      surface: "stacks.create",
      title: "Create failed at: Persist TechStack",
      userMessage: "Step: Persist TechStack\nRequest ID: req_123",
      status: 500,
      code: "STACK_SAVE_FAILED",
      requestId: "req_123",
      phase: "persist_stack",
      resourceName: "homelab",
      appVersion: "0.4.7",
    });

    const prompt = buildErrorAiHandoverPrompt(context);

    expect(context.schema).toBe("kombify.error-handover.v1");
    expect(context.source).toBe("kombify-techstack");
    expect(prompt).toContain("tenant-scoped data");
    expect(prompt).toContain("Request ID: req_123");
    expect(prompt).toContain("API code: STACK_SAVE_FAILED");
    expect(prompt).toContain("Resource name: homelab");
  });

  it("strips control characters and truncates long fields", () => {
    const context = createErrorAiHandoverContext({
      surface: "x".repeat(300),
      title: "Bad\u0000Title",
      userMessage: "A".repeat(1_500),
    });

    expect(context.surface.length).toBeLessThanOrEqual(160);
    expect(context.title).toBe("Bad Title");
    expect(context.userMessage.length).toBeLessThanOrEqual(1_200);
    expect(context.userMessage.endsWith("...")).toBe(true);
  });
});
