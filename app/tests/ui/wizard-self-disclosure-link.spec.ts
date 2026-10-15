import { test, expect } from "./fixtures";
import { mockLoggedInContext, requireAppBase } from "../helpers/test-utils";

type SelfDisclosureWrite = {
  source?: string;
  disclosure?: { goals?: string[]; notes?: string };
};

// Regression for the Cloud → Techstack hand-off (agent-native web plan,
// finding 15): anyone can craft a link, so opening one as a signed-in operator
// must not write the self-disclosure. Only the operator's confirmation stores
// the link's answers, tagged as coming from a link and without its free text.
test("a self-disclosure link writes nothing until the signed-in operator confirms it", async ({
  context,
  baseURL,
}) => {
  const origin = requireAppBase(baseURL);
  await mockLoggedInContext(context, { allowMockAuth: true });
  await context.route("**/api/v1/stacks**", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({ json: { data: [] } });
  });
  const writes: SelfDisclosureWrite[] = [];
  await context.route("**/api/v1/operator/self-disclosure", async (route) => {
    if (route.request().method() !== "PUT") return route.fallback();
    const body = route.request().postDataJSON() as SelfDisclosureWrite;
    writes.push(body);
    await route.fulfill({ json: { data: { ...body, profile: undefined } } });
  });

  const page = await context.newPage();
  await page.goto(`${origin}/stacks/new?goals=photos&intent=x`);

  const review = page.getByTestId("self-disclosure-review");
  await expect(review).toBeVisible();
  await page.waitForLoadState("networkidle");
  expect(writes).toEqual([]);

  await page.getByTestId("self-disclosure-use").click();

  await expect.poll(() => writes[0]?.source).toBe("link");
  expect(writes[0]?.disclosure?.goals).toEqual(["photos"]);
  expect(writes[0]?.disclosure?.notes).toBeUndefined();
});
