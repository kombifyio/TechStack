import { redirect } from "@sveltejs/kit";
import type { PageLoad } from "./$types";

/**
 * Retired route. The list of servers lives at `/servers`; the old address
 * keeps working for bookmarks and links (the query is kept; browsers carry
 * the fragment across the redirect themselves).
 */
export const load: PageLoad = ({ url }) => {
  throw redirect(308, `/servers${url.search}`);
};
