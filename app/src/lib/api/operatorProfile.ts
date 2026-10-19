/**
 * Operator self-disclosure API (KOMBIFY-DESIGN-SYSTEM-STANDARD.md §9.4).
 *
 * The client sends raw voluntary answers only. The capability profile in the
 * response is derived by the Techstack Unifier; the browser never computes or
 * sends a score.
 */
import { fetchApi } from "./client";

export type SelfDisclosureExperience =
  "hands-off" | "guided" | "curious" | "techie" | "expert";

export interface OperatorSelfDisclosure {
  experience?: SelfDisclosureExperience;
  goals?: string[];
  placement?: string;
  hardware?: string[];
  household?: string;
  motivations?: string[];
  notes?: string;
}

/**
 * How the answers reached the store: `link` answers arrived as URL state and
 * were confirmed by the operator, `techstack` answers were typed in Techstack.
 */
export type SelfDisclosureWriteSource = "link" | "techstack";

export interface OperatorSelfDisclosureRecord {
  disclosure: OperatorSelfDisclosure;
  profile?: {
    score: number;
    band: string;
    confidence?: number;
    source?: string;
  };
  /** `cloud` marks rows stored from a link before confirmation was required. */
  source?: SelfDisclosureWriteSource | "cloud";
  updated_at?: string;
}

export async function getOperatorSelfDisclosure(): Promise<OperatorSelfDisclosureRecord> {
  const res = await fetchApi<OperatorSelfDisclosureRecord>(
    "/api/v1/operator/self-disclosure",
  );
  return res.data;
}

export async function putOperatorSelfDisclosure(
  disclosure: OperatorSelfDisclosure,
  source: SelfDisclosureWriteSource,
): Promise<OperatorSelfDisclosureRecord> {
  const res = await fetchApi<OperatorSelfDisclosureRecord>(
    "/api/v1/operator/self-disclosure",
    {
      method: "PUT",
      body: JSON.stringify({ source, disclosure }),
    },
  );
  return res.data;
}
