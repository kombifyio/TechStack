/** kombify-Brand commit the vendored /brand files were taken from. */
const ASSET_REVISION = "15f0702";

/**
 * Techstack assets from kombify-Brand/assets/logo/system/techstack.
 * The WebP web tier (128 px rosette, 96 px tall lockup) serves UI chrome; the
 * lockup SVG master stays in the srcset for renders the web tier cannot cover.
 */
export const TECHSTACK_ROSETTE = `/brand/techstack-rosette-128.webp?v=${ASSET_REVISION}`;
export const TECHSTACK_ROSETTE_DARK = `/brand/techstack-rosette-128-dark.webp?v=${ASSET_REVISION}`;
export const TECHSTACK_TOOL_LOCKUP = `/brand/techstack-tool-lockup-96.webp?v=${ASSET_REVISION}`;
export const TECHSTACK_TOOL_LOCKUP_DARK = `/brand/techstack-tool-lockup-96-dark.webp?v=${ASSET_REVISION}`;
export const TECHSTACK_TOOL_LOCKUP_SRCSET = `${TECHSTACK_TOOL_LOCKUP} 513w, /brand/techstack-tool-lockup.svg?v=${ASSET_REVISION} 1710w`;
export const TECHSTACK_TOOL_LOCKUP_DARK_SRCSET = `${TECHSTACK_TOOL_LOCKUP_DARK} 513w, /brand/techstack-tool-lockup-dark.svg?v=${ASSET_REVISION} 1710w`;

/** Vendored umbrella wordmark for global Kombify footers; it never includes a rosette or a remote request. */
export const KOMBIFY_MAIN_WORDMARK =
  "/brand/kombify-brand-wordmark.png?v=main-wordmark-v1";
