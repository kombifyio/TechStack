<script lang="ts">
  import { AccountMenu, type AccountMenuItem } from "@kombiverselabs/ui/shell";
  import {
    Compass,
    ExternalLink,
    HelpCircle,
    Settings,
    Sun,
  } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { stackIdentity } from "#lib/stores/stackIdentity.js";
  import { StackIdentityBadge } from "#lib/components/open-core/index.js";
  import ThemeToggle from "#lib/components/ui/ThemeToggle.svelte";
  import { requestIntroductionStart } from "#lib/onboarding/introduction.js";

  let {
    placement = "up",
    compact = false,
    showLogout = true,
    apiVersion = "",
    onOpenLocalAdmin,
    class: className = "",
  }: {
    placement?: "up" | "down" | "right";
    compact?: boolean;
    showLogout?: boolean;
    apiVersion?: string;
    onOpenLocalAdmin?: () => void;
    class?: string;
  } = $props();

  const userEmail = $derived(authStore.userEmail || tr("ui.wallet.user"));
  const items: AccountMenuItem[] = $derived([
    { label: tr("nav.settings"), href: "/settings", icon: Settings },
    {
      label: tr("nav.help"),
      href: "https://docs.kombify.io/techstack",
      external: true,
      icon: HelpCircle,
    },
    ...(onOpenLocalAdmin
      ? [
          {
            label: tr("nav.localAdmin"),
            icon: ExternalLink,
            onSelect: onOpenLocalAdmin,
          },
        ]
      : []),
    {
      label: tr("introduction.restart"),
      icon: Compass,
      onSelect: requestIntroductionStart,
    },
  ]);

  async function handleLogout() {
    // Embedded Cloud owns sign-out; this callback is omitted in that mode.
    await authStore.logout({ manualLogin: true });
  }
</script>

{#snippet extra()}
  <div
    data-kx="menu-item"
    class="flex items-center gap-3 rounded-lg px-3 py-2 text-sm"
  >
    <a href="/settings#appearance" class="flex flex-1 items-center gap-3">
      <Sun class="h-4 w-4" />{tr("nav.theme")}
    </a>
    <ThemeToggle />
  </div>
  {#if $stackIdentity}
    <div class="px-3 py-2">
      <StackIdentityBadge identity={$stackIdentity} />
    </div>
  {/if}
  {#if apiVersion}
    <div
      class="px-3 py-2 text-xs text-muted-foreground"
      data-testid="product-version-identity"
    >
      {tr("ui.userMenu.version", { version: apiVersion })}
    </div>
  {/if}
{/snippet}

<div data-onboarding-anchor="user-menu" class={className}>
  <AccountMenu
    user={{ email: userEmail, labels: [tr("nav.administrator")] }}
    {items}
    {placement}
    variant={compact ? "compact" : "card"}
    openOnHover={placement !== "down"}
    menuLabel={userEmail}
    onSignOut={showLogout ? handleLogout : undefined}
    signOutLabel={tr("nav.logout")}
    {extra}
  />
</div>
