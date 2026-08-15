<script lang="ts">
  import { goto } from "$app/navigation";
  import { Monitor, Server, ShieldCheck } from "@lucide/svelte";
  import { setLocalLANBridge } from "$lib/api/tunnel";
  import {
    cloudUiLoginUrl,
    normalizeServerUrl,
  } from "$lib/client/windows-onboarding";

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
      await goto("/stacks");
    } catch (error) {
      localError =
        error instanceof Error
          ? error.message
          : "The private-network enrollment channel could not be enabled.";
    } finally {
      localStarting = false;
    }
  }
</script>

<svelte:head>
  <title>Windows Client Onboarding | kombify TechStack</title>
</svelte:head>

<main class="min-h-screen bg-slate-50 text-slate-950">
  <section
    class="mx-auto flex min-h-screen w-full max-w-6xl flex-col px-6 py-8"
  >
    <header class="flex items-center justify-between">
      <a
        href="/"
        class="inline-flex items-center gap-3 text-sm font-semibold text-slate-700"
      >
        <img src="/kombify-navy.png" alt="" class="h-9 w-9 object-contain" />
        kombify TechStack
      </a>
    </header>
    <div
      class="grid flex-1 items-center gap-8 py-10 lg:grid-cols-[1.02fr_0.98fr]"
    >
      <div class="max-w-xl">
        <div
          class="mb-7 inline-flex items-center gap-2 rounded-md border border-blue-200 bg-blue-50 px-3 py-2 text-sm font-semibold text-blue-950"
        >
          <Monitor class="h-4 w-4" /> Local Windows installation
        </div>
        <h1
          class="text-5xl font-semibold leading-tight tracking-normal text-slate-950"
        >
          Set up kombify TechStack on this Windows device.
        </h1>
        <p class="mt-5 text-lg leading-8 text-slate-600">
          This client is the local desktop entry for orchestrating your own
          servers and StackKits. The default path installs TechStack locally on
          this device.
        </p>
      </div>
      <div class="grid gap-4 md:grid-cols-2 lg:grid-cols-2">
        <article
          class="rounded-lg border border-blue-200 bg-white p-5 shadow-sm"
        >
          <div
            class="mb-4 flex h-10 w-10 items-center justify-center rounded-md bg-blue-950 text-white"
          >
            <Monitor class="h-5 w-5" />
          </div>
          <h2 class="text-xl font-semibold text-slate-950">Install locally</h2>
          <p class="mt-3 min-h-20 text-sm leading-6 text-slate-600">
            No TechStack account required. TechStack runs on this device and
            opens a token-protected enrollment channel for your private network.
          </p>
          <button
            type="button"
            class="mt-5 inline-flex h-10 w-full items-center justify-center rounded-md bg-blue-950 px-4 text-sm font-semibold text-white hover:bg-blue-900"
            disabled={localStarting}
            onclick={useLocalInstallation}
          >
            {localStarting ? "Starting locally..." : "Use locally"}
          </button>
          {#if localError}
            <p class="mt-3 text-sm text-red-700" role="alert">{localError}</p>
          {/if}
        </article>
        <article
          class="rounded-lg border border-slate-200 bg-white p-5 shadow-sm"
        >
          <div
            class="mb-4 flex h-10 w-10 items-center justify-center rounded-md bg-slate-100 text-blue-950"
          >
            <ShieldCheck class="h-5 w-5" />
          </div>
          <h2 class="text-xl font-semibold text-slate-950">kombify Cloud</h2>
          <p class="mt-3 min-h-20 text-sm leading-6 text-slate-600">
            Sign in with your kombify Cloud account and connect this desktop
            client.
          </p>
          <a
            class="mt-5 inline-flex h-10 w-full items-center justify-center rounded-md border border-blue-950 px-4 text-sm font-semibold text-blue-950 hover:bg-blue-50"
            href={cloudUiLoginUrl}
          >
            Sign in with kombify Cloud
          </a>
          <button
            type="button"
            class="mt-3 text-left text-xs font-semibold text-slate-500 underline-offset-4 hover:text-blue-950 hover:underline"
            onclick={() => (showServerInput = !showServerInput)}
          >
            Use your own self-hosted server instead
          </button>
        </article>
        {#if showServerInput}
          <article
            class="rounded-lg border border-slate-200 bg-white p-5 shadow-sm md:col-span-2"
          >
            <div
              class="mb-4 flex items-center gap-3 text-sm font-semibold text-slate-800"
            >
              <Server class="h-4 w-4 text-blue-950" /> Connect an existing self-hosted
              server
            </div>
            <div class="flex flex-col gap-3 sm:flex-row">
              <input
                class="h-10 min-w-0 flex-1 rounded-md border border-slate-300 px-3 text-sm outline-none focus:border-blue-700"
                bind:value={serverUrl}
                placeholder="https://techstack.home.local"
              />
              <a
                class:opacity-50={!normalizedServerUrl}
                class:pointer-events-none={!normalizedServerUrl}
                class="inline-flex h-10 items-center justify-center rounded-md bg-slate-950 px-4 text-sm font-semibold text-white"
                href={normalizedServerUrl || "#"}
              >
                Connect server
              </a>
            </div>
          </article>
        {/if}
      </div>
    </div>
  </section>
</main>
