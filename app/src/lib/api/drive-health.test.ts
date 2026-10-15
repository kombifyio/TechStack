import { describe, expect, it, vi } from "vitest";
vi.mock("./client", () => ({ fetchApi: vi.fn() }));
import {
  driveCondition,
  type DriveReading,
  type DriveServerState,
} from "./drive-health";

describe("current drive condition", () => {
  it("does not establish healthy hardware from lost, incomplete or replaced observations", () => {
    const now = Date.now();
    const reading = {
      drive_id: "serial:first",
      status: "available",
      observed_at: new Date(now).toISOString(),
    } as DriveReading;
    const drive = {
      drive_id: reading.drive_id,
      device_path: "/dev/sda",
      observation_status: "available",
      health: "healthy",
      last_success: reading,
      last_attempt: reading,
      alerts: [],
    };
    const server = {
      collection_status: "available",
      inventory_complete: true,
    } as DriveServerState;
    expect(driveCondition(drive, server, false, false, now)).toBe("healthy");
    expect(driveCondition(drive, server, true, false, now)).toBe("unknown");
    expect(driveCondition(drive, server, false, true, now)).toBe(
      "disconnected",
    );
    expect(
      driveCondition(
        { ...drive, last_success: null },
        server,
        false,
        false,
        now,
      ),
    ).toBe("missing");
    expect(
      driveCondition(
        { ...drive, drive_id: "serial:replacement" },
        server,
        false,
        false,
        now,
      ),
    ).toBe("missing");
    expect(
      driveCondition(
        drive,
        { ...server, inventory_complete: false },
        false,
        false,
        now,
      ),
    ).toBe("incomplete");
    expect(driveCondition(drive, server, false, false, now + 31 * 60_000)).toBe(
      "stale",
    );
    expect(driveCondition(drive, server, false, false, now - 6 * 60_000)).toBe(
      "unknown",
    );
    expect(
      driveCondition(
        { ...drive, observation_status: "inaccessible" },
        server,
        false,
        false,
        now,
      ),
    ).toBe("inaccessible");
  });
});
