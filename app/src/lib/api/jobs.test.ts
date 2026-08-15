import { beforeEach, describe, expect, it, vi } from "vitest";
import { getJob, getJobs, getLatestStackProvisionJob, listJobs } from "./jobs";

function requestPath(input: unknown): string {
  const raw = input instanceof Request ? input.url : String(input);
  return new URL(raw, "https://techstack.test").pathname;
}

describe("jobs API client", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.stubGlobal("window", {
      location: {
        origin: "https://techstack.test",
      },
    });
  });

  it("loads a job through the authenticated v1 API instead of the PocketBase collection API", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            id: "job-123",
            type: "provision",
            state: "running",
            progress: 40,
            step: "unify_services",
            current_step: "Identifying services",
            message: "Resolving services",
            result: { stack_id: "stack-123" },
            created: "2026-05-18T10:00:00Z",
            updated: "2026-05-18T10:01:00Z",
          },
        }),
        {
          status: 200,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    const job = await getJob("job-123");

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(requestPath(fetchMock.mock.calls[0][0])).toBe(
      "/api/v1/jobs/job-123",
    );
    expect(job).toMatchObject({
      id: "job-123",
      state: "running",
      step: "unify_services",
      result: { stack_id: "stack-123" },
    });
  });

  it("keeps legacy current_step as status text, not as a frontend step id", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            id: "job-legacy",
            type: "provision",
            state: "running",
            progress: 10,
            current_step: "Waiting for managed VM lease enrollment...",
            message: "Waiting for managed VM lease enrollment...",
            created: "2026-05-18T10:00:00Z",
            updated: "2026-05-18T10:01:00Z",
          },
        }),
        {
          status: 200,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    const job = await getJob("job-legacy");

    expect(job.step).toBeUndefined();
    expect(job.current_step).toBe("Waiting for managed VM lease enrollment...");
    expect(job.message).toBe("Waiting for managed VM lease enrollment...");
  });

  it("preserves resumable enrollment wait metadata", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            id: "job-waiting-enrollment",
            type: "provision",
            state: "waiting",
            progress: 20,
            wait_reason: "waiting_enrollment",
            next_resume_at: "2026-07-19T08:15:00Z",
            resume_available_at: "2026-07-19T08:17:00Z",
            resume_available: false,
            message: "Provider server is not enrolled yet",
            created_at: "2026-07-19T08:00:00Z",
            updated_at: "2026-07-19T08:05:00Z",
          },
        }),
        {
          status: 200,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    const job = await getJob("job-waiting-enrollment");

    expect(job).toMatchObject({
      state: "waiting",
      wait_reason: "waiting_enrollment",
      next_resume_at: "2026-07-19T08:15:00Z",
      resume_available_at: "2026-07-19T08:17:00Z",
      resume_available: false,
    });
  });

  it("lists jobs through the BFF with filters and pagination", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            items: [
              {
                id: "job-1",
                type: "provision",
                state: "pending",
                progress: 0,
                stack_id: "stack-1",
                created: "2026-05-18T10:00:00Z",
                updated: "2026-05-18T10:00:00Z",
              },
            ],
            page: 2,
            per_page: 10,
            total_items: 11,
            total_pages: 2,
          },
        }),
        {
          status: 200,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    const result = await listJobs({
      page: 2,
      perPage: 10,
      stackId: "stack-1",
      type: "provision",
      search: "install",
    });
    const calledURL = new URL(String(fetchMock.mock.calls[0][0]));

    expect(calledURL.pathname).toBe("/api/v1/jobs");
    expect(calledURL.searchParams.get("page")).toBe("2");
    expect(calledURL.searchParams.get("per_page")).toBe("10");
    expect(calledURL.searchParams.get("stack_id")).toBe("stack-1");
    expect(calledURL.searchParams.get("type")).toBe("provision");
    expect(calledURL.searchParams.get("search")).toBe("install");
    expect(result.totalItems).toBe(11);
    expect(result.items[0]).toMatchObject({
      id: "job-1",
      stack_id: "stack-1",
      state: "pending",
    });
  });

  it("loads dashboard and latest provision jobs from the jobs list BFF", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              items: [
                {
                  id: "job-latest",
                  type: "provision",
                  state: "completed",
                  progress: 100,
                  stack_id: "stack-1",
                  created: "2026-05-18T10:00:00Z",
                  updated: "2026-05-18T10:10:00Z",
                },
              ],
              page: 1,
              per_page: 1,
              total_items: 1,
              total_pages: 1,
            },
          }),
          {
            status: 200,
            headers: { "content-type": "application/json" },
          },
        ),
      ),
    );

    const latest = await getLatestStackProvisionJob("stack-1");
    const list = await getJobs();

    expect(latest?.id).toBe("job-latest");
    expect(list[0].id).toBe("job-latest");
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(
      new URL(String(fetchMock.mock.calls[0][0])).searchParams.get("type"),
    ).toBe("provision");
    expect(
      new URL(String(fetchMock.mock.calls[1][0])).searchParams.get("limit"),
    ).toBe("50");
  });
});
