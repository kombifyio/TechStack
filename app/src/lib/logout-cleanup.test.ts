// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("$app/environment", () => ({
  browser: true,
}));

vi.mock("$lib/auth/pocketbase-compat", () => ({
  clearPocketBaseCompatStoredSession: vi.fn(),
}));

vi.mock("$lib/stores/stackIdentity", () => ({
  clearStackIdentity: vi.fn(),
}));

import { clearPocketBaseCompatStoredSession } from "$lib/auth/pocketbase-compat";
import { clearStackIdentity } from "$lib/stores/stackIdentity";
import {
  clearCreatingSessionState,
  clearCreatingSessionStateForStack,
  clearTechstackSecuritySessionState,
} from "./logout-cleanup";

describe("clearTechstackSecuritySessionState", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
  });

  it("removes auth-derived and creation workflow state while keeping UI preferences", () => {
    window.localStorage.setItem("techstack-locale", "de");
    window.localStorage.setItem("techstack-theme", "dark");
    window.localStorage.setItem("techstack-mode", "cloud");
    window.sessionStorage.setItem("techstack.stack-identity", "{}");
    window.sessionStorage.setItem("creatingJobId", "job_123");
    window.sessionStorage.setItem("creatingStackId", "stack_123");
    window.sessionStorage.setItem("creating:job_123:config", "{}");
    window.sessionStorage.setItem("unrelated-session-pref", "keep");

    clearTechstackSecuritySessionState();

    expect(clearPocketBaseCompatStoredSession).toHaveBeenCalled();
    expect(clearStackIdentity).toHaveBeenCalled();
    expect(window.sessionStorage.getItem("creatingJobId")).toBeNull();
    expect(window.sessionStorage.getItem("creatingStackId")).toBeNull();
    expect(window.sessionStorage.getItem("creating:job_123:config")).toBeNull();
    expect(window.sessionStorage.getItem("unrelated-session-pref")).toBe(
      "keep",
    );
    expect(window.localStorage.getItem("techstack-locale")).toBe("de");
    expect(window.localStorage.getItem("techstack-theme")).toBe("dark");
    expect(window.localStorage.getItem("techstack-mode")).toBe("cloud");
  });

  it("removes only creation workflow state for stack reset", () => {
    window.sessionStorage.setItem("creatingJobId", "job_123");
    window.sessionStorage.setItem("creatingStackName", "Old Stack");
    window.sessionStorage.setItem("creating:job_123:stackId", "stack_123");
    window.sessionStorage.setItem("unrelated-session-pref", "keep");

    clearCreatingSessionState();

    expect(window.sessionStorage.getItem("creatingJobId")).toBeNull();
    expect(window.sessionStorage.getItem("creatingStackName")).toBeNull();
    expect(
      window.sessionStorage.getItem("creating:job_123:stackId"),
    ).toBeNull();
    expect(window.sessionStorage.getItem("unrelated-session-pref")).toBe(
      "keep",
    );
    expect(clearPocketBaseCompatStoredSession).not.toHaveBeenCalled();
    expect(clearStackIdentity).not.toHaveBeenCalled();
  });

  it("removes stale creation workflow state for one stack only", () => {
    window.sessionStorage.setItem("creatingJobId", "job_123");
    window.sessionStorage.setItem("creatingStackId", "stack_123");
    window.sessionStorage.setItem("creatingStackName", "Old Stack");
    window.sessionStorage.setItem("creating:job_123:stackId", "stack_123");
    window.sessionStorage.setItem("creating:job_123:stackName", "Old Stack");
    window.sessionStorage.setItem("creating:job_456:stackId", "stack_456");
    window.sessionStorage.setItem("creating:job_456:stackName", "New Stack");
    window.sessionStorage.setItem(
      "creating:add-server:stack_123:idempotency",
      '{"key":"old-key"}',
    );
    window.sessionStorage.setItem(
      "creating:add-server:stack_456:idempotency",
      '{"key":"new-key"}',
    );
    window.sessionStorage.setItem("unrelated-session-pref", "keep");

    clearCreatingSessionStateForStack("stack_123");

    expect(window.sessionStorage.getItem("creatingJobId")).toBeNull();
    expect(window.sessionStorage.getItem("creatingStackId")).toBeNull();
    expect(window.sessionStorage.getItem("creatingStackName")).toBeNull();
    expect(
      window.sessionStorage.getItem("creating:job_123:stackId"),
    ).toBeNull();
    expect(
      window.sessionStorage.getItem("creating:job_123:stackName"),
    ).toBeNull();
    expect(window.sessionStorage.getItem("creating:job_456:stackId")).toBe(
      "stack_456",
    );
    expect(
      window.sessionStorage.getItem(
        "creating:add-server:stack_123:idempotency",
      ),
    ).toBeNull();
    expect(
      window.sessionStorage.getItem(
        "creating:add-server:stack_456:idempotency",
      ),
    ).toBe('{"key":"new-key"}');
    expect(window.sessionStorage.getItem("unrelated-session-pref")).toBe(
      "keep",
    );
  });
});
