<script lang="ts">
  /**
   * The Companion, mounted once for the whole app.
   *
   * There is no Techstack panel to build. AI Platform serves the only panel
   * renderer and hosts mount it through `mountCompanion()`
   * (AI-FLOATING-PANEL-V2-PLAN, decision 1) — so this component is a mount
   * point and a settings bridge, not a chat UI. It is deliberately small: every
   * line of local chrome here is a line that drifts from the other hosts.
   *
   * `launcher: "builtin"` is the whole point. The SDK's own launcher renders
   * the real Kubi (the Workbench-ported mascot, `createKubi`) with its idle /
   * thinking / listening states and the account's avatar and position
   * preferences. A host-owned launcher — what the Workbench still has — means a
   * third mascot to keep in step. Techstack takes the shared one.
   *
   * Transport is `token`, the same as the Workbench: the panel asks for the
   * user's own Auth0 access token for the Gateway audience. A same-origin
   * relay signed with Techstack's service secret was the first attempt and it
   * cannot work — the Gateway edge accepts service auth in place of a user JWT
   * on four paths only, none of them the Companion's, because `/v1/ai/*`
   * entitles against the real user. Probing production returned 401 on every
   * Companion route.
   */
  import { onMount } from "svelte";
  import {
    mountCompanion,
    type CompanionController,
    type CompanionLayout,
  } from "@kombiverselabs/embed";
  import { TRANSIENT_PANEL_DISMISS_EVENT } from "@kombiverselabs/ui/shell";
  import {
    companionMountOptions,
    resolveCompanionAppearance,
    resolveCompanionFinish,
  } from "#lib/companion/options.js";
  import { companionStackIdentityFromAccount } from "#lib/companion/stack-identity.js";
  import { getGatewayToken } from "#lib/auth/gateway-auth.js";
  import {
    stackIdentity,
    type StackIdentity,
  } from "#lib/stores/stackIdentity.js";
  import { theme } from "#lib/stores/theme.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { get } from "svelte/store";

  let {
    onOpenChange,
    onLayoutChange,
  }: {
    onOpenChange?: (open: boolean) => void;
    onLayoutChange?: (layout: Exclude<CompanionLayout, "inline">) => void;
  } = $props();

  let mount = $state<HTMLDivElement | null>(null);

  /* Read the appearance the theme store already resolved onto the document
   * rather than resolving it a second time: two resolutions can disagree, and
   * then the app and the panel show different themes on one screen. */
  const currentAppearance = () =>
    resolveCompanionAppearance(document.documentElement.dataset.theme);
  const currentFinish = () =>
    resolveCompanionFinish(document.documentElement.dataset.finish);

  onMount(() => {
    if (!mount) return;

    const controller: CompanionController = mountCompanion(mount, {
      ...companionMountOptions({
        getToken: getGatewayToken,
        appearance: currentAppearance(),
        finish: currentFinish(),
        locale: document.documentElement.lang || "en",
        messages: {
          frameTitle: tr("companion.frameTitle"),
          openLauncher: tr("companion.open"),
          closeLauncher: tr("companion.close"),
          startVoiceAgent: tr("companion.startVoiceAgent"),
          launcherLabel: tr("companion.launcherLabel"),
        },
        stackIdentity: companionStackIdentityFromAccount(get(stackIdentity)),
      }),
      onOpenChange: (open) => onOpenChange?.(open),
      onLayoutChange: (layout) => onLayoutChange?.(layout),
    });

    const syncStackIdentity = (identity: StackIdentity | null) => {
      const launcherIdentity = companionStackIdentityFromAccount(identity);
      if (
        "setStackIdentity" in controller &&
        typeof controller.setStackIdentity === "function"
      ) {
        controller.setStackIdentity(launcherIdentity);
      }
    };

    /* Through the controller, never by remounting: an axis change must repaint
     * the panel without replacing the iframe, because replacing it would
     * discard the conversation in progress. */
    const unsubscribeTheme = theme.subscribe(() => {
      controller.setTheme(currentAppearance());
    });
    const unsubscribeFinish = theme.finish.subscribe(() => {
      controller.setDesign({ finish: currentFinish() });
    });
    const unsubscribeIdentity = stackIdentity.subscribe(syncStackIdentity);

    /* The app's own "close transient panels" signal — the same one the nav
     * flyouts and the notification bell answer. A floating Companion is a
     * transient panel like any other. */
    const closeOnDismiss = () => controller.close();
    window.addEventListener(TRANSIENT_PANEL_DISMISS_EVENT, closeOnDismiss);

    return () => {
      unsubscribeTheme();
      unsubscribeFinish();
      unsubscribeIdentity();
      window.removeEventListener(TRANSIENT_PANEL_DISMISS_EVENT, closeOnDismiss);
      controller.destroy();
    };
  });
</script>

<!--
  A zero-size fixed anchor. The SDK positions its own launcher and frame; this
  host only gives it a stable place in the document and stays out of the way.
  Nothing here reaches into the panel's internals — CMO and the Workbench both
  restyle the SDK's own nodes with `!important`, which is what makes their
  companion chrome unshareable.
-->
<div bind:this={mount} class="companion-mount" data-testid="companion-mount"></div>

<style>
  .companion-mount {
    position: fixed;
    inset: auto 0 0 auto;
    width: 0;
    height: 0;
    overflow: visible;
    z-index: 60;
  }

  /*
   * Give the docked panel real room instead of letting it cover the page.
   *
   * The anchor above is deliberately zero-size and the SDK's sidepanel root is
   * `position: fixed`, so nothing in this app's layout knew the panel existed
   * and it simply lay over the content. The SDK states how much horizontal
   * room it is taking (`--kombify-companion-dock-inline-size`: 0px floating,
   * the rail's width collapsed, the frame width open including expanded), so
   * one declaration is correct in every state and no host-side conditions are
   * needed. Kept here rather than in +layout.svelte because it belongs to the
   * Companion integration, and because the shell renders `<main>` in more than
   * one branch. The fallback keeps this inert on SDK versions that predate the
   * property.
   */
  :global(main) {
    padding-inline-end: var(--kombify-companion-dock-inline-size, 0px);
    transition: padding-inline-end 180ms ease;
  }

  @media (prefers-reduced-motion: reduce) {
    :global(main) {
      transition: none;
    }
  }
</style>
