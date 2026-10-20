/**
 * Customer wording for reason codes. The code and its typed parameters are the
 * localization authority (KOMBIFY-DESIGN-SYSTEM-STANDARD §11.3): a code this
 * client has a message for is rendered from the catalog; any other code (for
 * example a StackKits `stackkits:*` reason) renders the server's English text.
 */
import { formatNumber, getLocale, tr } from "#lib/i18n.svelte.js";

interface CodedReason {
  code: string;
  params?: Record<string, string>;
}

/** Parameters the planner states in GB or counts; shown in the owner's number format. */
const NUMBER_PARAMS = new Set(["ram", "need", "largest", "free"]);

/** The hardware facts a node has not reported are listed by their axis name. */
function missingFacts(value: string): string {
  const labels = value
    .split(",")
    .map((axis) => axis.trim())
    .filter(Boolean)
    .map((axis) => {
      const key = `layout.axis.${axis}`;
      const label = tr(key);
      return label === key ? axis.replace(/[-_]/g, " ") : label;
    });
  try {
    return new Intl.ListFormat(getLocale(), {
      style: "long",
      type: "conjunction",
    }).format(labels);
  } catch {
    return labels.join(", ");
  }
}

function localizedParams(
  params: Record<string, string>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [name, value] of Object.entries(params)) {
    const number = Number(value);
    if (
      NUMBER_PARAMS.has(name) &&
      value.trim() !== "" &&
      Number.isFinite(number)
    ) {
      out[name] = formatNumber(number);
    } else if (name === "missing") {
      out[name] = missingFacts(value);
    } else {
      out[name] = value;
    }
  }
  return out;
}

/** One layout reason as a sentence for the owner. */
export function layoutReasonText(
  reason: CodedReason & { text?: string },
): string {
  const key = `layout.reason.${reason.code}`;
  const message = tr(key, localizedParams(reason.params ?? {}));
  return message === key ? reason.text || reason.code : message;
}

/** One wizard recommendation reason (decision trace or capacity) as a sentence. */
export function hintReasonText(
  reason: CodedReason & { message?: string },
): string {
  const key = `wizard.hints.reason.${reason.code}`;
  const message = tr(key, localizedParams(reason.params ?? {}));
  return message === key ? reason.message || "" : message;
}
