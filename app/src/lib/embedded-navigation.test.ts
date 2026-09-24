import { describe, expect, it } from "vitest";
import {
  hostNavigationRequested,
  withHostNavigation,
} from "./embedded-navigation";

describe("embedded navigation ownership", () => {
  it("requires the explicit true host-navigation value", () => {
    expect(
      hostNavigationRequested(
        new URLSearchParams("embedded=true&host_navigation=true"),
      ),
    ).toBe(true);
    expect(hostNavigationRequested("/dashboard?host_navigation=1")).toBe(false);
    expect(hostNavigationRequested("/dashboard?host_navigation=false")).toBe(
      false,
    );
  });

  it("carries ownership across safe internal routes and keeps fragments", () => {
    expect(withHostNavigation("/monitoring?view=alerts#history")).toBe(
      "/monitoring?view=alerts&host_navigation=true#history",
    );
  });

  it("does not rewrite external or unsafe navigation targets", () => {
    expect(withHostNavigation("https://example.test/dashboard")).toBe(
      "https://example.test/dashboard",
    );
    expect(withHostNavigation("//example.test/dashboard")).toBe(
      "//example.test/dashboard",
    );
  });
});
