<script lang="ts">
  import {
    TECHSTACK_ROSETTE,
    TECHSTACK_ROSETTE_DARK,
    TECHSTACK_TOOL_LOCKUP,
    TECHSTACK_TOOL_LOCKUP_DARK,
    TECHSTACK_TOOL_LOCKUP_DARK_SRCSET,
    TECHSTACK_TOOL_LOCKUP_SRCSET,
  } from "#lib/brand-assets.js";

  // A lockup needs its rendered CSS width as `sizes` so the srcset keeps the
  // 96 px WebP up to 2x and only switches to the SVG master beyond it.
  type Props = { class?: string } & (
    | { collapsed: true; sizes?: never }
    | { collapsed?: false; sizes: string }
  );

  let { collapsed = false, sizes, class: className = "" }: Props = $props();

  const lightSrc = $derived(
    collapsed ? TECHSTACK_ROSETTE : TECHSTACK_TOOL_LOCKUP,
  );
  const darkSrc = $derived(
    collapsed ? TECHSTACK_ROSETTE_DARK : TECHSTACK_TOOL_LOCKUP_DARK,
  );
  const lightSrcset = $derived(
    collapsed ? undefined : TECHSTACK_TOOL_LOCKUP_SRCSET,
  );
  const darkSrcset = $derived(
    collapsed ? undefined : TECHSTACK_TOOL_LOCKUP_DARK_SRCSET,
  );
</script>

<img
  {sizes}
  srcset={darkSrcset}
  src={darkSrc}
  alt="kombify Techstack"
  class="{className} logo-dark"
/>
<img
  {sizes}
  srcset={lightSrcset}
  src={lightSrc}
  alt="kombify Techstack"
  class="{className} logo-light"
/>

<style>
  .logo-light {
    display: none;
  }

  :global(html[data-theme="light"]) .logo-light {
    display: block;
  }

  :global(html[data-theme="light"]) .logo-dark {
    display: none;
  }
</style>
