import { describe, expect, it, vi } from "vitest";
import {
  buildCapturePayload,
  buildTechstackCreationEventProperties,
  classifyRoute,
  createPostHogClient,
  isValidEventName,
  isValidPostHogProjectApiKey,
  normalizePostHogHost,
  sanitizeNavigationTarget,
  toTechstackAnalyticsUser,
} from "./posthog";

describe("Techstack PostHog analytics", () => {
  it("accepts Kombify taxonomy event names", () => {
    expect(isValidEventName("techstack:page_viewed")).toBe(true);
    expect(isValidEventName("techstack:stack_create_started")).toBe(true);
    expect(isValidEventName("techstack:pipeline_previewed")).toBe(true);
    expect(isValidEventName("techstack:stack_created")).toBe(true);
    expect(isValidEventName("techstack:stack_create_failed")).toBe(true);
    expect(isValidEventName("techstack:rollout_completed")).toBe(true);
    expect(isValidEventName("techstack:rollout_failed")).toBe(true);
    expect(isValidEventName("techstack:connected")).toBe(true);
    expect(isValidEventName("navigation:item_clicked")).toBe(true);
    expect(isValidEventName("auth:session_identified")).toBe(true);
    expect(isValidEventName("Techstack Page Viewed")).toBe(false);
    expect(isValidEventName("page_viewed")).toBe(false);
  });

  it("accepts only PostHog Project API Keys", () => {
    expect(isValidPostHogProjectApiKey("phc_test")).toBe(true);
    expect(isValidPostHogProjectApiKey("phx_personal")).toBe(false);
    expect(isValidPostHogProjectApiKey("posthog_admin_key")).toBe(false);
    expect(isValidPostHogProjectApiKey("")).toBe(false);
  });

  it("uses the Kombify EU proxy as the safe host fallback", () => {
    expect(normalizePostHogHost("https://eu.i.posthog.com/capture/")).toBe(
      "https://e.kombify.io",
    );
    expect(normalizePostHogHost("not a url")).toBe("https://e.kombify.io");
    expect(normalizePostHogHost()).toBe("https://e.kombify.io");
  });

  it("does not build anonymous payloads without an Auth0 subject", () => {
    const payload = buildCapturePayload({
      apiKey: "phc_test",
      event: "techstack:page_viewed",
      config: { appVersion: "0.4.13", location: { pathname: "/stacks" } },
      randomId: () => "fixed",
      now: () => 1000,
    });

    expect(payload).toBeNull();
  });

  it("does not build payloads with personal or admin keys", () => {
    for (const apiKey of ["phx_personal", "posthog_admin_key"]) {
      const payload = buildCapturePayload({
        apiKey,
        event: "techstack:page_viewed",
        config: { appVersion: "0.4.13", location: { pathname: "/stacks" } },
        user: { authSubject: "auth0|techstack-user" },
        randomId: () => "fixed",
        now: () => 1000,
      });

      expect(payload).toBeNull();
    }
  });

  it("maps cloud Auth0 users without email or display name", () => {
    const user = toTechstackAnalyticsUser({
      sub: "auth0|techstack-user",
      orgId: "org_123",
      roles: ["admin"],
      is_admin: true,
    });
    const payload = buildCapturePayload({
      apiKey: "phc_test",
      event: "auth:session_identified",
      config: {
        environment: "prod",
        appVersion: "0.4.13",
        location: { pathname: "/wallet?email=person@example.com#private" },
      },
      user,
      properties: {
        email: "person@example.com",
        display_name: "Person Example",
        host: "private.example.com",
        safe: "ok",
      },
      randomId: () => "fixed",
      now: () => 1000,
    });

    expect(payload?.distinct_id).toBe("auth0|techstack-user");
    expect(payload?.properties).toMatchObject({
      service: "kombify-techstack-frontend",
      product: "kombify-techstack",
      surface: "techstack",
      env: "production",
      edition: "saas-standalone",
      app_version: "0.4.13",
      path: "/wallet",
      correlation_id: "ph_1000_fixed",
      role: "admin",
      auth_method: "auth0",
      is_staff: true,
      safe: "ok",
      $groups: { organization: "org_123" },
    });
    expect(payload?.properties.$process_person_profile).toBeUndefined();
    expect(payload?.properties.email).toBeUndefined();
    expect(payload?.properties.display_name).toBeUndefined();
    expect(payload?.properties.host).toBeUndefined();
  });

  it("rejects unsafe Techstack identity values before payload creation", () => {
    expect(
      buildCapturePayload({
        apiKey: "phc_test",
        event: "auth:session_identified",
        config: {},
        user: {
          authSubject: "person@example.com",
          organizationId: "org_123",
        },
      }),
    ).toBeNull();

    const payload = buildCapturePayload({
      apiKey: "phc_test",
      event: "auth:session_identified",
      config: {},
      user: {
        authSubject: "auth0|techstack-user",
        organizationId: "https://example.com/org/private",
      },
      randomId: () => "fixed",
      now: () => 1000,
    });

    expect(payload?.distinct_id).toBe("auth0|techstack-user");
    expect(payload?.properties.$groups).toBeUndefined();
    expect(payload?.properties.organization_id).toBeUndefined();
    expect(payload?.properties.$process_person_profile).toBe(false);
    expect(JSON.stringify(payload)).not.toContain("example.com");

    expect(
      buildCapturePayload({
        apiKey: "phc_test",
        event: "auth:session_identified",
        config: {},
        user: {
          authSubject: "anonymous_techstack",
          organizationId: "org_123",
        },
      }),
    ).toBeNull();
  });

  it("does not allow caller properties to override PostHog identity controls", () => {
    const cyclic: Record<string, unknown> = { keep: "safe" };
    cyclic.self = cyclic;
    const grouped = buildCapturePayload({
      apiKey: "phc_test",
      event: "techstack:page_viewed",
      config: {},
      user: { authSubject: "auth0|user", organizationId: "org_123" },
      properties: {
        service: "attacker-service",
        path: "/evil?email=person@example.com",
        correlation_id: "attacker-correlation",
        $groups: { organization: "org_attacker" },
        $process_person_profile: false,
        Distinct_ID: "auth0|attacker",
        Api_Key: "phc_attacker",
        nested: {
          keep: "safe",
          email: "person@example.com",
        },
        cyclic,
      },
      randomId: () => "fixed",
      now: () => 1000,
    });
    const ungrouped = buildCapturePayload({
      apiKey: "phc_test",
      event: "techstack:page_viewed",
      config: {},
      user: { authSubject: "auth0|user" },
      properties: {
        $groups: { organization: "org_attacker" },
      },
      randomId: () => "fixed",
      now: () => 1000,
    });

    expect(grouped?.properties.$groups).toEqual({ organization: "org_123" });
    expect(grouped?.properties.$process_person_profile).toBeUndefined();
    expect(grouped?.properties.service).toBe("kombify-techstack-frontend");
    expect(grouped?.properties.path).toBe("/");
    expect(grouped?.properties.correlation_id).toBe("ph_1000_fixed");
    expect(grouped?.properties.Distinct_ID).toBeUndefined();
    expect(grouped?.properties.Api_Key).toBeUndefined();
    expect(grouped?.properties.nested).toEqual({ keep: "safe" });
    expect(grouped?.properties.cyclic).toEqual({ keep: "safe" });
    expect(ungrouped?.properties.$groups).toBeUndefined();
    expect(ungrouped?.properties.$process_person_profile).toBe(false);
  });

  it("drops unsafe route-like custom properties", () => {
    const payload = buildCapturePayload({
      apiKey: "phc_test",
      event: "navigation:item_clicked",
      config: { location: { pathname: "/stacks/new#ignored" } },
      user: { authSubject: "auth0|user", organizationId: "org_123" },
      properties: {
        href: "/wallet",
        source_path: "/stacks/new?token=secret",
        target_path: "/wallet#billing",
        route: "https://techstack.kombify.io/wallet",
      },
      randomId: () => "fixed",
      now: () => 1000,
    });

    expect(payload?.properties.path).toBe("/stacks/new");
    expect(payload?.properties.href).toBe("/wallet");
    expect(payload?.properties).not.toHaveProperty("source_path");
    expect(payload?.properties).not.toHaveProperty("target_path");
    expect(payload?.properties).not.toHaveProperty("route");
    expect(JSON.stringify(payload?.properties)).not.toContain("?");
    expect(JSON.stringify(payload?.properties)).not.toContain("#");
    expect(JSON.stringify(payload?.properties)).not.toContain("https://");
  });

  it("drops non-finite numbers and bounds custom property arrays", () => {
    const payload = buildCapturePayload({
      apiKey: "phc_test",
      event: "techstack:page_viewed",
      config: {},
      user: { authSubject: "auth0|techstack-user" },
      properties: {
        ok_count: 2,
        bad_count: Number.NaN,
        bad_cost: Number.POSITIVE_INFINITY,
        external_link: "https://example.com/private?token=secret",
        labels: Array.from({ length: 40 }, (_, index) => `label-${index}`),
        links: [
          "safe",
          "https://example.com/private",
          "www.example.com/private",
        ],
      },
      randomId: () => "fixed",
      now: () => 1000,
    });

    expect(payload?.properties.ok_count).toBe(2);
    expect(payload?.properties.bad_count).toBeUndefined();
    expect(payload?.properties.bad_cost).toBeUndefined();
    expect(payload?.properties.external_link).toBeUndefined();
    expect(payload?.properties.labels).toHaveLength(25);
    expect(payload?.properties.links).toEqual(["safe"]);
  });

  it("builds stack creation funnel properties without owner or host PII", () => {
    const properties = buildTechstackCreationEventProperties({
      wizardType: "easy",
      provider: "kombify-cloud",
      stackMode: "managed",
      serverProvisioningMode: "kombify-cloud",
      serviceCount: 4,
      goalCount: 3,
      detectedAddonCount: 2,
      pipelineValid: true,
      resolvedStackkit: "stackkit-default",
      stackId: "stack_123",
      jobId: "job_123",
      serverId: "server_123",
      serviceId: "service_123",
      requestId: "request_123",
      reasonCode: "server_offline",
      connectionState: "stale",
      healthState: "unknown",
      accessMode: "relay",
      serverCount: 1,
      errorStatus: 409,
      errorCode: "stack_conflict",
      retryable: false,
      conflict: true,
      creationOperation: "stack",
      runtimePhase: "verified",
      verificationStatus: "verified",
      proofKeyCount: 4,
      identityHandoffPresent: true,
    });

    expect(properties).toEqual({
      wizard_type: "easy",
      provider: "kombify-cloud",
      stack_mode: "managed",
      server_provisioning_mode: "kombify-cloud",
      service_count: 4,
      goal_count: 3,
      detected_addon_count: 2,
      pipeline_valid: true,
      resolved_stackkit: "stackkit-default",
      stack_id: "stack_123",
      job_id: "job_123",
      server_id: "server_123",
      service_id: "service_123",
      request_id: "request_123",
      reason_code: "server_offline",
      connection_state: "stale",
      health_state: "unknown",
      access_mode: "relay",
      server_count: 1,
      error_status: 409,
      error_code: "stack_conflict",
      retryable: false,
      conflict: true,
      creation_operation: "stack",
      runtime_phase: "verified",
      verification_status: "verified",
      proof_key_count: 4,
      identity_handoff_present: true,
    });
    expect(JSON.stringify(properties)).not.toContain("@");
    expect(JSON.stringify(properties)).not.toContain("host");
  });

  it("rejects email-like creation identifiers and non-finite counts", () => {
    const properties = buildTechstackCreationEventProperties({
      provider: "provider@example.com",
      stackId: "stack@example.com",
      jobId: "www.example.com/job/private",
      serviceCount: Number.POSITIVE_INFINITY,
      goalCount: -1,
      detectedAddonCount: 1.9,
      errorStatus: Number.NaN,
    });

    expect(properties).toEqual({
      detected_addon_count: 1,
    });
    expect(JSON.stringify(properties)).not.toContain("example.com");
  });

  it("disables capture for self-hosted OSS editions", async () => {
    const fetchImpl = vi.fn();
    const client = createPostHogClient({
      apiKey: "phc_test",
      edition: "selfhost-oss",
      fetchImpl,
    });

    expect(client.enabled).toBe(false);
    await expect(
      client.capture("techstack:page_viewed", {
        user: { authSubject: "auth0|user" },
      }),
    ).resolves.toBe(false);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("does not post browser events without an Auth0 subject", async () => {
    const fetchImpl = vi.fn();
    const client = createPostHogClient({
      apiKey: "phc_test",
      fetchImpl,
    });

    await expect(client.capture("techstack:page_viewed")).resolves.toBe(false);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("does not post browser events with personal or admin keys", async () => {
    for (const apiKey of ["phx_personal", "posthog_admin_key"]) {
      const fetchImpl = vi.fn();
      const client = createPostHogClient({
        apiKey,
        fetchImpl,
      });

      expect(client.enabled).toBe(false);
      await expect(
        client.capture("techstack:page_viewed", {
          user: { authSubject: "auth0|abc" },
        }),
      ).resolves.toBe(false);
      expect(fetchImpl).not.toHaveBeenCalled();
    }
  });

  it("posts capture payloads with project API key and required properties", async () => {
    const fetchImpl = vi.fn().mockResolvedValue({ ok: true });
    const client = createPostHogClient({
      apiKey: "phc_test",
      host: "https://e.kombify.io",
      environment: "development",
      appVersion: "0.4.13",
      fetchImpl,
      randomId: () => "id",
      now: () => 42,
    });

    await expect(
      client.capture("techstack:page_viewed", {
        user: { authSubject: "auth0|abc", organizationId: "org_123" },
        properties: { route_group: "stacks" },
      }),
    ).resolves.toBe(true);

    expect(fetchImpl).toHaveBeenCalledWith(
      "https://e.kombify.io/capture/",
      expect.objectContaining({ method: "POST" }),
    );
    const body = JSON.parse(fetchImpl.mock.calls[0][1].body);
    expect(body.api_key).toBe("phc_test");
    expect(body.distinct_id).toBe("auth0|abc");
    expect(body.properties.correlation_id).toBe("ph_42_id");
    expect(body.properties.route_group).toBe("stacks");
    expect(body.properties.$groups).toEqual({ organization: "org_123" });
  });

  it("classifies routes and strips query strings from navigation targets", () => {
    expect(classifyRoute("/stacks/new")).toBe("stacks");
    expect(classifyRoute("/monitoring")).toBe("monitoring");
    expect(classifyRoute("/unknown")).toBe("other");
    expect(
      sanitizeNavigationTarget(
        "https://techstack.kombify.io/wallet?email=person@example.com",
        "https://techstack.kombify.io",
      ),
    ).toBe("/wallet");
    expect(
      sanitizeNavigationTarget(
        "https://docs.kombify.io/techstack",
        "https://techstack.kombify.io",
      ),
    ).toBeNull();
  });
});
