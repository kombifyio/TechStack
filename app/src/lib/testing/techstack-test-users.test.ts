import { describe, expect, it } from "vitest";
import {
  buildTechStackTestUserReadiness,
  getTechStackTestUser,
  requireTechStackTestUser,
} from "./techstack-test-users";

describe("TechStack E2E test users", () => {
  it("requires real admin credentials for happy-path tests", () => {
    expect(() => requireTechStackTestUser("admin", {})).toThrow(
      /Missing required TechStack E2E test-user secrets/,
    );
  });

  it("prefers the shared kombify role before tool-specific compatibility overrides", () => {
    const env = {
      TECHSTACK_E2E_ADMIN_EMAIL: "techstack-admin@kombify.io",
      TECHSTACK_E2E_ADMIN_PASSWORD: "techstack-secret",
      TEST_PRO_PLUS_USER_EMAIL: "shared-techstack@kombify.io",
      TEST_PRO_PLUS_USER_PASSWORD: "shared-secret",
    };

    expect(requireTechStackTestUser("admin", env)).toEqual({
      email: "shared-techstack@kombify.io",
      password: "shared-secret",
    });
  });

  it("falls back to the shared kombify TechStack test user", () => {
    expect(
      requireTechStackTestUser("admin", {
        TEST_PRO_PLUS_USER_EMAIL: "shared-techstack@kombify.io",
        TEST_PRO_PLUS_USER_PASSWORD: "shared-secret",
      }),
    ).toEqual({
      email: "shared-techstack@kombify.io",
      password: "shared-secret",
    });
  });

  it("does not provide local fake credentials for missing happy-path users", () => {
    expect(getTechStackTestUser("admin", {})).toEqual({
      email: "",
      password: "",
    });

    expect(buildTechStackTestUserReadiness({}).admin.missingSecrets).toEqual([
      "TEST_PRO_PLUS_USER_EMAIL or TEST_ADMIN_EMAIL or TECHSTACK_E2E_ADMIN_EMAIL or TECHSTACK_ADMIN_EMAIL",
      "TEST_PRO_PLUS_USER_PASSWORD or TEST_ADMIN_PASSWORD or TECHSTACK_E2E_ADMIN_PASSWORD or TECHSTACK_ADMIN_PASSWORD",
    ]);
  });

  it("uses the local bootstrap admin for loopback preview targets", () => {
    const env = {
      PLAYWRIGHT_BASE_URL: "http://127.0.0.1:5261",
    };

    expect(getTechStackTestUser("admin", env)).toEqual({
      email: "admin@techstack.local",
      password: "dev-admin-password-change-me",
    });
    expect(buildTechStackTestUserReadiness(env).admin.missingSecrets).toEqual(
      [],
    );
  });

  it("prefers explicit local bootstrap overrides when loopback preview is active", () => {
    const env = {
      PLAYWRIGHT_BASE_URL: "http://localhost:5261",
      TECHSTACK_ADMIN_EMAIL: "local-admin@example.com",
      TECHSTACK_ADMIN_PASSWORD: "local-admin-password",
    };

    expect(getTechStackTestUser("admin", env)).toEqual({
      email: "local-admin@example.com",
      password: "local-admin-password",
    });
  });
});
