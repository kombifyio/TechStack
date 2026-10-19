import { redirect } from "@sveltejs/kit";
import type { PageLoad } from "./$types";

/**
 * Retired route (slug contract, KOMBIFY-DESIGN-SYSTEM-STANDARD §10.2.1 rule 7):
 * the Homelab settings are the `homelab` tab of the settings. The old address
 * keeps working and carries `kit_deployment_id` along.
 */
export const load: PageLoad = ({ url }) => {
  const params = new URLSearchParams(url.search);
  params.set("tab", "homelab");
  throw redirect(308, `/settings?${params.toString()}`);
};
