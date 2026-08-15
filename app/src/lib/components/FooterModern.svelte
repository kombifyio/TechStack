<script lang="ts">
  /**
   * FooterModern - THE CENTRAL, REUSABLE footer for all kombify tools
   *
   * This is the authoritative source for footer links and branding.
   * Use this component across all kombify applications.
   *
   * Variants:
   * - 'full': Logo + link grid + social icons (default)
   * - 'compact': Icon + copyright + essential links
   *
   * Configuration:
   * - logoStyle: 'dark' | 'light' | 'auto' (default: 'auto')
   * - showLogoSwitch: Toggle button for logo style (default: false)
   * - showProduct: Show product links section (default: true)
   * - showCompany: Show company links section (default: true)
   * - showLegal: Show legal links section (default: true)
   *
   * Company Info: Kombiverse Labs
   * Address: Oppelner Str. 3A, 33098 Paderborn, Germany
   * Contact: info@kombify.io
   */
  import { Heart, SunMoon } from "@lucide/svelte";
  import GithubIcon from "$lib/icons/GithubIcon.svelte";
  import XIcon from "$lib/icons/XIcon.svelte";

  type Variant = "compact" | "full";
  type LogoStyle = "dark" | "light" | "auto";

  interface FooterLink {
    label: string;
    href: string;
    external?: boolean;
  }

  interface Props {
    variant?: Variant;
    logoStyle?: LogoStyle;
    showLogoSwitch?: boolean;
    showProduct?: boolean;
    showCompany?: boolean;
    showLegal?: boolean;
    apiVersion?: string;
    instanceId?: string;
    portalUrl?: string;
    docsUrl?: string;
    class?: string;
  }

  let {
    variant = "compact",
    logoStyle = "auto",
    showLogoSwitch = false,
    showProduct = true,
    showCompany = true,
    showLegal = true,
    apiVersion = "",
    instanceId = "",
    portalUrl = "https://kombify.io",
    docsUrl = "https://docs.kombify.io",
    class: className = "",
  }: Props = $props();

  const currentYear = new Date().getFullYear();

  // Image fallback state
  let iconImageFailed = $state(false);
  let logoImageFailed = $state(false);
  let currentLogoStyle = $derived<LogoStyle>(logoStyle);

  function handleIconError() {
    iconImageFailed = true;
  }

  function handleLogoError() {
    logoImageFailed = true;
  }

  function toggleLogoStyle() {
    if (currentLogoStyle === "dark") {
      currentLogoStyle = "light";
    } else {
      currentLogoStyle = "dark";
    }
  }

  function getLogoSrc(): string {
    return "/kombify-techstack-kombi3d.png";
  }

  // ═══════════════════════════════════════════════════════════════════════════
  // AUTHORITATIVE LINK DATA - Update here for all kombify tools
  // ═══════════════════════════════════════════════════════════════════════════

  /**
   * Product Links - kombify tools and features
   */
  let productLinks: FooterLink[] = $derived([
    { label: "Features", href: `${portalUrl}/features`, external: true },
    { label: "Docs", href: docsUrl, external: true },
  ]);

  let companyLinks: FooterLink[] = $derived([
    { label: "About", href: `${portalUrl}/about`, external: true },
    { label: "Contact", href: "mailto:info@kombify.io", external: true },
  ]);

  let legalLinks: FooterLink[] = $derived([
    { label: "Impressum", href: `${portalUrl}/impressum`, external: true },
    { label: "Privacy", href: `${portalUrl}/privacy`, external: true },
    { label: "Terms", href: `${portalUrl}/terms`, external: true },
  ]);

  /**
   * Social Links - External profiles
   */
  const socialLinks = [
    { label: "GitHub", href: "https://github.com/kombiverse", icon: GithubIcon },
    { label: "Twitter", href: "https://twitter.com/kombify", icon: XIcon },
  ];

  // Helper to check if link is external
  function isExternal(link: FooterLink): boolean {
    return (
      link.external === true ||
      link.href.startsWith("http") ||
      link.href.startsWith("mailto:")
    );
  }
</script>

{#if variant === "compact"}
  <!-- Compact Footer -->
  <footer
    class="w-full border-t border-border bg-background/80 backdrop-blur-sm {className}"
  >
    <div class="max-w-6xl mx-auto px-4 sm:px-6 py-4">
      <div class="flex flex-col sm:flex-row items-center justify-between gap-4">
        <!-- Logo Icon + Copyright -->
        <div class="flex items-center gap-3">
          <a href="/" class="w-8 h-8 rounded-lg overflow-hidden shrink-0">
            {#if !iconImageFailed}
              <img
                src="/kombify-techstack-k.png"
                alt="kombify TechStack"
                class="w-full h-full object-contain"
                onerror={handleIconError}
              />
            {:else}
              <div
                class="w-full h-full rounded-lg bg-linear-to-br from-primary to-primary/70 flex items-center justify-center text-primary-foreground font-bold text-sm"
              >
                K
              </div>
            {/if}
          </a>
          <span class="text-sm text-muted-foreground">
            © {currentYear} Kombiverse Labs
          </span>
          {#if apiVersion}
            <span
              data-testid="product-version-identity"
              class="text-xs text-muted-foreground/60">{apiVersion}</span
            >
          {/if}
          {#if instanceId}
            <span
              class="text-xs text-muted-foreground/60 font-mono"
              title={`Instance ${instanceId}`}
              >instance:{instanceId.slice(0, 8)}</span
            >
          {/if}
        </div>

        <!-- Links + Socials -->
        <div class="flex items-center gap-6">
          <nav class="flex items-center gap-4 text-sm">
            {#each legalLinks as link}
              <a
                href={link.href}
                target={isExternal(link) ? "_blank" : undefined}
                rel={isExternal(link) ? "noopener noreferrer" : undefined}
                class="text-muted-foreground hover:text-foreground transition-colors"
              >
                {link.label}
              </a>
            {/each}
          </nav>
          <div class="h-4 w-px bg-border hidden sm:block"></div>
          <div class="flex items-center gap-3">
            {#each socialLinks as social}
              {@const Icon = social.icon}
              <a
                href={social.href}
                target="_blank"
                rel="noopener noreferrer"
                class="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted transition-all"
                aria-label={social.label}
              >
                <Icon class="w-4 h-4" />
              </a>
            {/each}
          </div>
        </div>
      </div>
    </div>
  </footer>
{:else}
  <!-- Full Footer -->
  <footer
    class="w-full border-t border-border bg-linear-to-b from-background to-muted/20 {className}"
  >
    <div class="max-w-6xl mx-auto px-4 sm:px-6">
      <!-- Main Footer Content -->
      <div class="py-10 grid grid-cols-2 md:grid-cols-5 gap-8">
        <!-- Logo + Description Column (spans 2 cols) -->
        <div class="col-span-2">
          <div class="flex items-center gap-3 mb-4">
            <a href="/" class="inline-flex items-center gap-3 group">
              <div
                class="w-auto h-12 rounded-xl overflow-hidden shrink-0 transition-transform group-hover:scale-105"
              >
                {#if !logoImageFailed}
                  <img
                    src={getLogoSrc()}
                    alt="kombify"
                    class="h-full w-auto object-contain"
                    onerror={handleLogoError}
                  />
                {:else}
                  <div
                    class="h-full w-12 rounded-xl bg-linear-to-br from-primary to-primary/70 flex items-center justify-center text-primary-foreground font-bold text-lg"
                  >
                    K
                  </div>
                {/if}
              </div>
            </a>
            {#if showLogoSwitch}
              <button
                onclick={toggleLogoStyle}
                class="p-1.5 rounded-lg text-muted-foreground hover:text-foreground hover:bg-muted transition-all"
                title="Toggle logo style ({currentLogoStyle})"
              >
                <SunMoon class="w-4 h-4" />
              </button>
            {/if}
          </div>
          <p
            class="text-sm text-muted-foreground max-w-xs mb-4 leading-relaxed"
          >
            The hybrid infrastructure control plane. Deploy, manage, and monitor
            your homelab with confidence.
          </p>
          <!-- Social Links -->
          <div class="flex items-center gap-2">
            {#each socialLinks as social}
              {@const Icon = social.icon}
              <a
                href={social.href}
                target="_blank"
                rel="noopener noreferrer"
                class="p-2 rounded-lg text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-all"
                aria-label={social.label}
              >
                <Icon class="w-5 h-5" />
              </a>
            {/each}
          </div>
        </div>

        <!-- Product Links -->
        {#if showProduct}
          <div>
            <h3 class="text-sm font-semibold text-foreground mb-4">Product</h3>
            <ul class="space-y-3">
              {#each productLinks as link}
                <li>
                  <a
                    href={link.href}
                    target={isExternal(link) ? "_blank" : undefined}
                    rel={isExternal(link) ? "noopener noreferrer" : undefined}
                    class="text-sm text-muted-foreground hover:text-foreground transition-colors inline-flex items-center gap-1"
                  >
                    {link.label}
                  </a>
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <!-- Company Links -->
        {#if showCompany}
          <div>
            <h3 class="text-sm font-semibold text-foreground mb-4">Company</h3>
            <ul class="space-y-3">
              {#each companyLinks as link}
                <li>
                  <a
                    href={link.href}
                    target={isExternal(link) ? "_blank" : undefined}
                    rel={isExternal(link) ? "noopener noreferrer" : undefined}
                    class="text-sm text-muted-foreground hover:text-foreground transition-colors inline-flex items-center gap-2"
                  >
                    {link.label}
                  </a>
                </li>
              {/each}
            </ul>
          </div>
        {/if}

        <!-- Legal Links -->
        {#if showLegal}
          <div>
            <h3 class="text-sm font-semibold text-foreground mb-4">Legal</h3>
            <ul class="space-y-3">
              {#each legalLinks as link}
                <li>
                  <a
                    href={link.href}
                    target={isExternal(link) ? "_blank" : undefined}
                    rel={isExternal(link) ? "noopener noreferrer" : undefined}
                    class="text-sm text-muted-foreground hover:text-foreground transition-colors"
                  >
                    {link.label}
                  </a>
                </li>
              {/each}
            </ul>
          </div>
        {/if}
      </div>

      <!-- Bottom Bar -->
      <div
        class="py-4 border-t border-border flex flex-col sm:flex-row items-center justify-between gap-4"
      >
        <p class="text-sm text-muted-foreground">
          © {currentYear} Kombiverse Labs. Made for humans, powered by intelligence.
          {#if apiVersion}
            <span
              data-testid="product-version-identity"
              class="text-muted-foreground/60">• {apiVersion}</span
            >
          {/if}
          {#if instanceId}
            <span
              class="text-muted-foreground/60 font-mono"
              title={`Instance ${instanceId}`}
              >• instance:{instanceId.slice(0, 8)}</span
            >
          {/if}
        </p>
        <p class="text-sm text-muted-foreground flex items-center gap-1.5">
          Made with <Heart class="w-3.5 h-3.5 text-red-500 fill-red-500" /> for the
          homelab community
        </p>
      </div>
    </div>
  </footer>
{/if}
