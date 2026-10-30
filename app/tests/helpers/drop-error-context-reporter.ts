import { rmSync } from "node:fs";
import type { Reporter, TestCase, TestResult } from "@playwright/test/reporter";

/**
 * Removes the `error-context.md` of a failed test before any other reporter
 * sees it. That file holds an ARIA snapshot of the page, which prints input
 * values, so a password still in its field ends up in the uploaded report.
 * `PLAYWRIGHT_NO_COPY_PROMPT` stops only the page snapshot taken at the end of
 * a test; a failed locator assertion attaches its own snapshot without any
 * switch. List this reporter first: the HTML reporter copies attachments.
 */
export default class DropErrorContextReporter implements Reporter {
  onTestEnd(_test: TestCase, result: TestResult): void {
    for (let i = result.attachments.length - 1; i >= 0; i--) {
      const attachment = result.attachments[i];
      if (attachment.name !== "error-context") continue;
      result.attachments.splice(i, 1);
      if (attachment.path) rmSync(attachment.path, { force: true });
    }
  }
}
