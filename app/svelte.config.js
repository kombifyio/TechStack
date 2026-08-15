import adapterStatic from "@sveltejs/adapter-static";
import { vitePreprocess } from "@sveltejs/vite-plugin-svelte";
import { mdsvex } from "mdsvex";

/** @type {import('@sveltejs/kit').Config} */
const config = {
  extensions: [".svelte", ".md"],

  preprocess: [
    vitePreprocess(),
    mdsvex({
      extensions: [".md"],
    }),
  ],

  kit: {
    // Single build for every deployment (ADR-033 OQ2 web convergence): a
    // static SPA in build-static, embedded into the Go binary via
    // internal/frontend/dist (-tags techstack_static_ui) and served by Go.
    // The legacy TECHSTACK_DESKTOP_STATIC toggle is accepted but ignored —
    // the static build is no longer desktop-only.
    adapter: adapterStatic({
      pages: "build-static",
      assets: "build-static",
      fallback: "index.html",
      strict: false,
    }),
  },
};

export default config;
