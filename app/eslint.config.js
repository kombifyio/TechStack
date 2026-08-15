import js from "@eslint/js";
import ts from "typescript-eslint";
import svelte from "eslint-plugin-svelte";
import globals from "globals";

export default ts.config(
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs["flat/recommended"],
  {
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node,
        JsonWebKey: "readonly",
      },
    },
  },
  {
    files: ["**/*.svelte"],
    languageOptions: {
      parserOptions: {
        parser: ts.parser,
      },
    },
  },
  {
    // Svelte 5 runes files (.svelte.ts) need special handling
    files: ["**/*.svelte.ts"],
    languageOptions: {
      parserOptions: {
        parser: ts.parser,
      },
    },
  },
  {
    ignores: [
      "build/",
      "build-static/",
      ".e2e/",
      ".svelte-kit/",
      "dist/",
      "**/dist/**",
      "node_modules/",
      "test-results*/",
      "playwright-report*/",
      "**/*.svelte.ts",
    ],
  },
  {
    rules: {
      "@typescript-eslint/no-unused-vars": [
        "warn",
        { argsIgnorePattern: "^_", varsIgnorePattern: "^_" },
      ],
      "@typescript-eslint/no-explicit-any": "warn",
      "@typescript-eslint/no-unused-expressions": "warn",
      // Allow empty patterns (used in destructuring)
      "no-empty-pattern": "warn",
      // Svelte specific - relaxed for existing codebase
      "svelte/no-at-html-tags": "warn",
      "svelte/require-each-key": "warn", // TODO: Add keys to each blocks
      "svelte/valid-compile": "warn", // Some svelte-ignore comments need updating
      "svelte/no-unused-svelte-ignore": "warn", // Some ignores are outdated
      "svelte/no-navigation-without-resolve": "off", // SvelteKit 2.x migration - disable for now
      "svelte/prefer-svelte-reactivity": "warn", // Svelte 5 runes migration - warn only
      "svelte/no-useless-children-snippet": "warn", // Svelte 5 feature - warn only
    },
  },
);
