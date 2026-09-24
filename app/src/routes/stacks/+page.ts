import { redirect } from "@sveltejs/kit";
import type { PageLoad } from "./$types";

/** Legacy product route. The B2C home is the owner's singular Homelab dashboard. */
export const load: PageLoad = ({ url }) => {
  throw redirect(308, `/dashboard${url.search}`);
};
