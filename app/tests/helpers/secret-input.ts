import { expect, type Locator } from "@playwright/test";

/**
 * Enters a secret (a test user's password) without it becoming a report step.
 * Playwright writes the arguments of `fill` and `type` into the HTML report
 * as step titles and into the call log of a failure, and CI uploads that
 * report as an artifact. The value is set through the input's native setter
 * with the events a typed value raises, so the report shows only "Evaluate".
 */
export async function fillSecret(
  locator: Locator,
  value: string,
): Promise<void> {
  await expect(locator).toBeEditable();
  await locator.evaluate((input, secret) => {
    const element = input as HTMLInputElement;
    element.focus();
    const setter = Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )?.set;
    if (setter) setter.call(element, secret);
    else element.value = secret;
    const raised = { bubbles: true, composed: true };
    element.dispatchEvent(new Event("input", raised));
    element.dispatchEvent(new Event("change", raised));
  }, value);
  if ((await locator.inputValue()) !== value) {
    throw new Error("The secret field did not retain its value");
  }
}
