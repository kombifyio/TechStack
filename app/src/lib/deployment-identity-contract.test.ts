import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

function source(path: string): string {
  return readFileSync(resolve(process.cwd(), path), "utf8");
}

describe("visible product deployment identity contract", () => {
  it("prefers API version plus revision and uses compile identity only on API failure", () => {
    const layout = source("src/routes/+layout.svelte");
    expect(layout).toMatch(
      /productIdentity\s*=\s*productIdentityLabel\(info\.version,\s*info\.revision\)/,
    );
    expect(layout).toMatch(
      /catch \(err\)[\s\S]*productIdentity\s*=\s*appDeployLabel/,
    );
    expect(layout).toMatch(/productIdentity\s*=\s*\$state\(""\)/);
    expect(layout).not.toMatch(/apiVersion\s*=\s*info\.version/);
  });

  it("renders one stable identity locator without adding a synthetic version prefix", () => {
    const inline = source("src/lib/components/navigation/InlineTabNav.svelte");
    const footer = source("src/lib/components/FooterModern.svelte");
    for (const component of [inline, footer]) {
      expect(component).toContain('data-testid="product-version-identity"');
      expect(component).not.toContain("v{apiVersion}");
    }
  });

  it("builds version and revision from immutable source inputs", () => {
    const vite = source("vite.config.ts");
    const vitest = source("vitest.config.ts");
    const docker = source("../Dockerfile");
    const appDocker = source("Dockerfile");
    const backendIdentity = source("../cmd/techstack/version.go");

    expect(vite).toContain('new URL("../VERSION", import.meta.url)');
    expect(vitest).toContain('new URL("../VERSION", import.meta.url)');
    expect(vite).not.toMatch(
      /process\.env\.(TECHSTACK_VERSION|PUBLIC_APP_VERSION|VITE_APP_VERSION)/,
    );
    expect(vitest).not.toMatch(
      /process\.env\.(GIT_COMMIT|RENDER_GIT_COMMIT|VITE_APP_VERSION)/,
    );
    expect(docker).toContain("COPY VERSION /build/VERSION");
    expect(docker).toContain('ARG TECHSTACK_PRODUCT_VERSION=""');
    expect(docker).toContain(
      'product_version="${TECHSTACK_PRODUCT_VERSION:-$(cat VERSION)}"',
    );
    expect(docker).not.toMatch(/^(ARG|ENV) TECHSTACK_VERSION\b/m);
    expect(appDocker).toContain("COPY VERSION /build/VERSION");
    expect(backendIdentity).not.toContain("os.Getenv");
    expect(backendIdentity).toContain("compiledBuildRevision(buildRevision)");
  });
});
