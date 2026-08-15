// The app ships as a static SPA (adapter-static + Go embed, ADR-033 OQ2):
// there is no SSR runtime in any deployment, so rendering is client-side only.
export const ssr = false;
