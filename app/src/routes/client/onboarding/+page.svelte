<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { goto } from "$app/navigation";
  import { Monitor, Server, ShieldCheck } from "@lucide/svelte";
  import { setLocalLANBridge } from "#lib/api/tunnel.js";
  import {
    cloudUiLoginUrl,
    normalizeServerUrl,
    rememberWindowsLocalClientContext,
    windowsLocalClientReturnUrl,
  } from "#lib/client/windows-onboarding.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import TechstackBrandLogo from "#lib/components/TechstackBrandLogo.svelte";

  let serverUrl = $state("");
  let showServerInput = $state(false);
  let localStarting = $state(false);
  let localError = $state("");
  const normalizedServerUrl = $derived(normalizeServerUrl(serverUrl));

  async function useLocalInstallation() {
    localStarting = true;
    localError = "";
    try {
      await setLocalLANBridge(true);
      try {
        rememberWindowsLocalClientContext(window.localStorage);
      } catch {
        // Storage can be unavailable in restricted browser contexts.
      }
      await authStore.init();
      if (authStore.isAuthenticated || authStore.v2SessionActive) {
        await goto("/dashboard");
        return;
      }
      await goto(windowsLocalClientReturnUrl);
    } catch (error) {
      localError =
        error instanceof Error
          ? error.message
          : tr("ui.clientOnboarding.thePrivateNetworkEnrollmentChannel");
    } finally {
      localStarting = false;
    }
  }
</script>

<svelte:head>
  <title>{tr("ui.clientOnboarding.windowsClientOnboardingKombifyTechstack")}</title>
</svelte:head>

<main class="min-h-screen bg-background text-foreground">
  <section
    class="mx-auto flex min-h-screen w-full max-w-6xl flex-col px-6 py-8"
  >
    <header class="flex items-center justify-between">
      <a
        href="/"
        class="flex h-10 w-64 items-center"
      >
        <TechstackBrandLogo
          sizes="193px"
          class="h-9 w-auto max-w-full object-contain object-left"
        />
      </a>
    </header>
    <div
      class="grid flex-1 items-center gap-8 py-10 lg:grid-cols-[1.02fr_0.98fr]"
    >
      <div class="max-w-xl">
        <div
          class="mb-7 inline-flex items-center gap-2 rounded-md border border-border bg-muted/40 px-3 py-2 text-sm font-semibold"
        >
          <Monitor class="h-4 w-4" /> {tr("ui.clientOnboarding.localWindowsInstallation")}
        </div>
        <h1
          class="text-5xl font-semibold leading-tight tracking-normal text-foreground"
        >
          {tr("ui.clientOnboarding.setUpKombifyTechstackOn")}
        </h1>
        <p class="mt-5 text-lg leading-8 text-muted-foreground">
          {tr("ui.clientOnboarding.thisClientIsTheLocal")}
        </p>
      </div>
      <div class="grid gap-4 md:grid-cols-2 lg:grid-cols-2">
        <article data-kx="plate" class="p-5">
          <div
            class="mb-4 flex h-10 w-10 items-center justify-center rounded-md bg-primary text-primary-foreground"
          >
            <Monitor class="h-5 w-5" />
          </div>
          <h2 class="text-xl font-semibold text-foreground">{tr("ui.clientOnboarding.installLocally")}</h2>
          <p class="mt-3 min-h-20 text-sm leading-6 text-muted-foreground">
            {tr("ui.clientOnboarding.noTechstackAccountRequiredTechstack")}
          </p>
          <button
            type="button"
            data-kx="control"
            data-variant="primary"
            class="mt-5 inline-flex h-10 w-full items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
            disabled={localStarting}
            onclick={useLocalInstallation}
          >
            {localStarting ? tr("ui.clientOnboarding.startingLocally") : tr("ui.clientOnboarding.useLocally")}
          </button>
          {#if localError}
            <p class="mt-3 text-sm text-destructive" role="alert">{localError}</p>
          {/if}
        </article>
        <article data-kx="plate" class="p-5">
          <div
            class="mb-4 flex h-10 w-10 items-center justify-center rounded-md bg-muted text-primary"
          >
            <ShieldCheck class="h-5 w-5" />
          </div>
          <h2 class="text-xl font-semibold text-foreground">kombify Cloud</h2>
          <p class="mt-3 min-h-20 text-sm leading-6 text-muted-foreground">
            {tr("ui.clientOnboarding.signInWithYourKombify")}
          </p>
          <a
            data-kx="control"
            class="mt-5 inline-flex h-10 w-full items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
            href={cloudUiLoginUrl}
          >
            {tr("ui.clientOnboarding.signInWithKombifyCloud")}
          </a>
          <button
            type="button"
            class="mt-3 text-left text-xs font-semibold text-muted-foreground underline-offset-4 hover:text-primary hover:underline"
            onclick={() => (showServerInput = !showServerInput)}
          >
            {tr("ui.clientOnboarding.useYourOwnSelfHosted")}
          </button>
        </article>
        {#if showServerInput}
          <article data-kx="plate" class="p-5 md:col-span-2">
            <div
              class="mb-4 flex items-center gap-3 text-sm font-semibold text-foreground"
            >
              <Server class="h-4 w-4 text-primary" /> {tr("ui.clientOnboarding.connectAnExistingSelfHosted")}
            </div>
            <div class="flex flex-col gap-3 sm:flex-row">
              <input
                class="h-10 min-w-0 flex-1 rounded-md border border-border bg-background px-3 text-sm outline-none focus:border-primary"
                bind:value={serverUrl}
                placeholder="https://techstack.home"
              />
              <a
                class:opacity-50={!normalizedServerUrl}
                class:pointer-events-none={!normalizedServerUrl}
                data-kx="control"
                data-variant="primary"
                class="inline-flex h-10 items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
                href={normalizedServerUrl || "#"}
              >
                {tr("ui.clientOnboarding.connectServer")}
              </a>
            </div>
          </article>
        {/if}
      </div>
    </div>
  </section>
</main>
