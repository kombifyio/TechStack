<script lang="ts">
  /**
   * The account pill and its menu, one component for every shell.
   *
   * It used to be a snippet inside the sidebar only, which is why embedded
   * mode (tab bar, no sidebar) had no way to reach Settings, Help or the
   * theme at all, and why the collapsed rail's avatar toggled a menu that was
   * never rendered. Placement is a prop: `up` above the pill in the expanded
   * rail, `down` under it in the tab bar, `right` beside the avatar when the
   * rail is collapsed — that last one portaled and fixed, because the rail
   * wrapper clips (same reason as the package's nav flyout).
   *
   * Logout is opt-in. In embedded mode kombify Cloud owns the federated
   * sign-out and relays it into the frame; a Techstack-only logout there
   * would end half a session.
   */
  import { fly } from "svelte/transition";
  import { tr } from "#lib/i18n.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { stackIdentity } from "#lib/stores/stackIdentity.js";
  import { StackIdentityBadge } from "#lib/components/open-core/index.js";
  import ThemeToggle from "#lib/components/ui/ThemeToggle.svelte";
  import { requestIntroductionStart } from "#lib/onboarding/introduction.js";
  import { bindTransientPanelDismiss, portal } from "@kombiverselabs/ui/shell";
  import {
    ChevronDown,
    ChevronUp,
    ExternalLink,
    Compass,
    HelpCircle,
    LogOut,
    Settings,
    Sun,
  } from "@lucide/svelte";

  interface Props {
    placement?: "up" | "down" | "right";
    /** Avatar-only trigger (collapsed rail, tight tab bars). */
    compact?: boolean;
    showLogout?: boolean;
    apiVersion?: string;
    onOpenLocalAdmin?: () => void;
    class?: string;
  }

  let {
    placement = "up",
    compact = false,
    showLogout = true,
    apiVersion = "",
    onOpenLocalAdmin,
    class: className = "",
  }: Props = $props();

  let open = $state(false);
  let root = $state<HTMLElement | null>(null);
  let trigger = $state<HTMLButtonElement | null>(null);
  let fixedPos = $state<{ left: number; top: number } | null>(null);
  let menuHeight = $state(0);

  const userEmail = $derived(authStore.userEmail || "User");
  const userInitial = $derived(userEmail.charAt(0).toUpperCase() || "U");
  const portaled = $derived(placement === "right");

  function place() {
    if (!portaled || !trigger) return;
    const rect = trigger.getBoundingClientRect();
    const vh = window.innerHeight;
    const top = Math.max(12, Math.min(rect.bottom - menuHeight, vh - menuHeight - 12));
    fixedPos = { left: Math.round(rect.right + 10), top: Math.round(top) };
  }

  $effect(() => {
    if (!open) return;
    void menuHeight;
    place();
    const onChange = () => place();
    window.addEventListener("scroll", onChange, true);
    window.addEventListener("resize", onChange);
    const unbind = bindTransientPanelDismiss({
      isOpen: () => open,
      containsTarget: (target) =>
        (root?.contains(target) ?? false) ||
        (document.querySelector("[data-user-menu]")?.contains(target) ?? false),
      onDismiss: () => (open = false),
    });
    return () => {
      window.removeEventListener("scroll", onChange, true);
      window.removeEventListener("resize", onChange);
      unbind();
    };
  });

  async function handleLogout() {
    open = false;
    await authStore.logout({ manualLogin: true });
  }
</script>

{#snippet items()}
  <a
    href="/settings"
    data-kx="menu-item"
    class="flex items-center gap-3 rounded-lg px-3 py-2 text-sm"
    onclick={() => (open = false)}
    ><Settings class="h-4 w-4" />{tr("nav.settings")}</a
  >
  <a
    href="https://docs.kombify.io/techstack"
    target="_blank"
    rel="noopener"
    data-kx="menu-item"
    class="flex items-center gap-3 rounded-lg px-3 py-2 text-sm"
    onclick={() => (open = false)}
    ><HelpCircle class="h-4 w-4" />{tr("nav.help")}</a
  >
  <!-- The label is a link, not decoration: light/dark is one axis of the
       appearance settings, and the row used to look clickable while only the
       toggle answered. The toggle stays a sibling rather than a nested
       button. -->
  <div data-kx="menu-item" class="flex items-center gap-3 rounded-lg px-3 py-2 text-sm">
    <a
      href="/settings#appearance"
      class="flex flex-1 items-center gap-3"
      onclick={() => (open = false)}
    >
      <Sun class="h-4 w-4" />
      <span>Theme</span>
    </a>
    <ThemeToggle />
  </div>
  {#if onOpenLocalAdmin}
    <button
      type="button"
      data-kx="menu-item"
      onclick={() => {
        open = false;
        onOpenLocalAdmin?.();
      }}
      class="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left text-sm"
    >
      <ExternalLink class="h-4 w-4" />
      Open Local Admin
    </button>
  {/if}
  <button
    type="button"
    data-kx="menu-item"
    onclick={() => {
      open = false;
      requestIntroductionStart();
    }}
    class="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left text-sm"
  >
    <Compass class="h-4 w-4" />
    {tr("introduction.restart")}
  </button>
  {#if $stackIdentity}
    <div class="px-3 pt-2 pb-1">
      <StackIdentityBadge identity={$stackIdentity} />
    </div>
  {/if}
  {#if apiVersion}
    <div
      class="px-3 pt-2 pb-1 text-xs text-muted-foreground"
      data-testid="product-version-identity"
    >
      Version {apiVersion}
    </div>
  {/if}
  {#if showLogout}
    <button
      type="button"
      data-kx="menu-item"
      onclick={handleLogout}
      class="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left text-sm text-destructive"
    >
      <LogOut class="h-4 w-4" />
      Logout
    </button>
  {/if}
{/snippet}

<div class="user-menu relative {compact ? 'flex justify-center' : ''} {className}" bind:this={root}>
  {#if compact}
    <button
      bind:this={trigger}
      type="button"
      data-kx="control"
      data-onboarding-anchor="user-menu"
      class="flex h-10 w-10 items-center justify-center rounded-xl"
      aria-haspopup="menu"
      aria-expanded={open}
      aria-label={userEmail}
      title={userEmail}
      onclick={() => (open = !open)}
    >
      <span
        class="flex h-8 w-8 items-center justify-center rounded-full bg-primary text-sm font-semibold text-primary-foreground"
      >
        {userInitial}
      </span>
    </button>
  {:else}
    <button
      bind:this={trigger}
      type="button"
      data-kx="control"
      data-onboarding-anchor="user-menu"
      aria-haspopup="menu"
      aria-expanded={open}
      onclick={() => (open = !open)}
      class="flex w-full items-center gap-3 rounded-xl p-2 text-left"
    >
      <span
        class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-primary text-sm font-semibold text-primary-foreground"
      >
        {userInitial}
      </span>
      <span class="min-w-0 flex-1">
        <span class="block truncate text-sm font-medium text-foreground">{userEmail}</span>
        <span class="block text-xs text-muted-foreground">Administrator</span>
      </span>
      {#if placement === "down"}
        <ChevronDown class="h-4 w-4 text-muted-foreground transition-transform {open ? 'rotate-180' : ''}" />
      {:else}
        <ChevronUp class="h-4 w-4 text-muted-foreground transition-transform {open ? '' : 'rotate-180'}" />
      {/if}
    </button>
  {/if}

  {#if open && !portaled}
    <div
      data-kx="menu-plate"
      data-user-menu
      role="menu"
      class="absolute z-50 min-w-56 p-1.5 {placement === 'up'
        ? 'right-0 bottom-full left-0 mb-2'
        : 'right-0 top-full mt-2'}"
      transition:fly={{ y: placement === "up" ? 10 : -10, duration: 150 }}
    >
      {@render items()}
    </div>
  {/if}
</div>

{#if open && portaled}
  <div
    data-kx="menu-plate"
    data-user-menu
    role="menu"
    class="fixed z-[120] w-60 p-1.5"
    style:left={fixedPos ? `${fixedPos.left}px` : "-9999px"}
    style:top={fixedPos ? `${fixedPos.top}px` : "-9999px"}
    use:portal
    bind:clientHeight={menuHeight}
    transition:fly={{ x: -8, duration: 150 }}
  >
    {@render items()}
  </div>
{/if}
