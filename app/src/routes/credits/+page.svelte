<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";

  /*
   * What the application recognition builds on, with each licence, and what
   * the owner can do about a logo. Plain text on purpose: no logo appears
   * here, a logo shows only next to software the owner runs.
   */
  const sources = [
    {
      id: "dashboardIcons",
      name: "homarr-labs/dashboard-icons",
      href: "https://github.com/homarr-labs/dashboard-icons",
      license: "Apache-2.0",
    },
    {
      id: "simpleIcons",
      name: "Simple Icons",
      href: "https://github.com/simple-icons/simple-icons",
      license: "CC0-1.0",
    },
    {
      id: "coolify",
      name: "Coolify service templates",
      href: "https://github.com/coollabsio/coolify",
      license: "Apache-2.0",
    },
    {
      id: "dokploy",
      name: "Dokploy templates",
      href: "https://github.com/Dokploy/templates",
      license: "MIT",
    },
    {
      id: "nuclei",
      name: "ProjectDiscovery nuclei-templates",
      href: "https://github.com/projectdiscovery/nuclei-templates",
      license: "MIT",
    },
  ] as const;
</script>

<svelte:head>
  <title>{tr("ui.credits.title")} – kombify Techstack</title>
</svelte:head>

<div class="mx-auto max-w-3xl p-4 md:p-6" data-testid="credits-page">
  <a
    href="/"
    class="text-sm text-primary hover:underline"
    data-testid="credits-back"
  >
    {tr("ui.credits.back")}
  </a>

  <h1 class="mt-3 text-2xl font-semibold text-foreground">
    {tr("ui.credits.title")}
  </h1>
  <p class="mt-2 text-sm text-muted-foreground">{tr("ui.credits.intro")}</p>

  <section class="mt-6" aria-labelledby="credits-sources-title">
    <h2
      id="credits-sources-title"
      class="text-base font-semibold text-foreground"
    >
      {tr("ui.credits.sources")}
    </h2>
    <ul class="mt-3 space-y-3" data-testid="credits-sources">
      {#each sources as source (source.id)}
        <li class="text-sm">
          <a
            href={source.href}
            target="_blank"
            rel="noopener noreferrer"
            class="font-medium text-primary hover:underline"
            >{source.name}</a
          >
          <span class="text-muted-foreground">
            · {tr("ui.credits.licence", { license: source.license })}</span
          >
          <p class="mt-0.5 text-muted-foreground">
            {tr(`ui.credits.use.${source.id}`)}
          </p>
        </li>
      {/each}
    </ul>
  </section>

  <section class="mt-6" aria-labelledby="credits-trademarks-title">
    <h2
      id="credits-trademarks-title"
      class="text-base font-semibold text-foreground"
    >
      {tr("ui.credits.trademarks.title")}
    </h2>
    <p class="mt-2 text-sm text-muted-foreground">
      {tr("ui.appLogo.notice")}
    </p>
    <p class="mt-2 text-sm text-muted-foreground">
      {tr("ui.credits.trademarks.body")}
    </p>
  </section>

  <section class="mt-6" aria-labelledby="credits-control-title">
    <h2
      id="credits-control-title"
      class="text-base font-semibold text-foreground"
    >
      {tr("ui.credits.control.title")}
    </h2>
    <p class="mt-2 text-sm text-muted-foreground">
      {tr("ui.credits.control.body")}
    </p>
  </section>

  <section class="mt-6" aria-labelledby="credits-removal-title">
    <h2
      id="credits-removal-title"
      class="text-base font-semibold text-foreground"
    >
      {tr("ui.credits.removal.title")}
    </h2>
    <p class="mt-2 text-sm text-muted-foreground">
      {tr("ui.credits.removal.body")}
      <a
        href="https://kombify.io/impressum"
        target="_blank"
        rel="noopener noreferrer"
        class="text-primary hover:underline"
        data-testid="credits-imprint-link">{tr("ui.credits.removal.link")}</a
      >
    </p>
  </section>
</div>
