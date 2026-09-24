<script lang="ts">
  import { page } from "$app/state";
  import { ArrowLeft, CheckCircle2, KeyRound, Monitor } from "@lucide/svelte";
  import { onMount } from "svelte";
  import { createLocalOwnerAccount } from "#lib/auth/local-owner.js";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    deriveLocalOwnerName,
    localSetupReady,
    rememberWindowsLocalClientContext,
  } from "#lib/client/windows-onboarding.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import TechstackBrandLogo from "#lib/components/TechstackBrandLogo.svelte";

  let adminEmail = $state("");
  let adminPassword = $state("");
  let existingOwnerEmail = $state("");
  let existingOwnerPassword = $state("");
  let loading = $state(true);
  let submitting = $state(false);
  let saved = $state(false);
  let error = $state("");
  let signedInEmail = $state("");
  let setupAvailable = $state(true);

  const canSubmit = $derived(
    setupAvailable && localSetupReady(adminEmail, adminPassword),
  );
  const canSignInExistingOwner = $derived(
    !setupAvailable &&
      existingOwnerEmail.trim().length > 0 &&
      existingOwnerPassword.length > 0,
  );
  const ownerName = $derived(deriveLocalOwnerName(adminEmail));
  const windowsClient = $derived(
    page.url.searchParams.get("client") === "windows",
  );
  const backHref = $derived(
    windowsClient ? "/client/onboarding?client=windows" : "/login",
  );
  const operatorEntryPath = "/dashboard";

  function openOperatorUi() {
    window.location.assign(operatorEntryPath);
  }

  onMount(async () => {
    if (windowsClient) {
      try {
        rememberWindowsLocalClientContext(window.localStorage);
      } catch {
        // Storage can be unavailable in restricted browser contexts.
      }
    }

    await authStore.init();
    if (authStore.isAuthenticated) {
      signedInEmail = authStore.userEmail || "";
      saved = true;
      openOperatorUi();
      return;
    }
    setupAvailable = authStore.isFirstRun;
    loading = false;
  });

  async function saveLocalSetup() {
    if (!canSubmit || submitting) return;

    error = "";
    submitting = true;

    try {
      const email = await createLocalOwnerAccount({
        email: adminEmail,
        password: adminPassword,
        passwordConfirm: adminPassword,
        isFirstRun: authStore.isFirstRun,
      });
      const ok = await authStore.loginWithPassword(email, adminPassword);
      if (!ok) {
        error =
          authStore.error || "Local owner was created, but sign-in failed.";
        return;
      }
      signedInEmail = email;
      saved = true;
      openOperatorUi();
    } catch (err) {
      const parsed = parseApiError(err);
      error =
        parsed.fieldErrors.email?.message ||
        parsed.fieldErrors.password?.message ||
        parsed.message ||
        (err instanceof Error ? err.message : "Local setup failed.");
    } finally {
      submitting = false;
    }
  }

  async function signInExistingOwner() {
    if (!canSignInExistingOwner || submitting) return;

    error = "";
    submitting = true;

    try {
      const ok = await authStore.loginWithPassword(
        existingOwnerEmail,
        existingOwnerPassword,
      );
      if (!ok) {
        error = authStore.error || "Local sign-in failed.";
        return;
      }
      signedInEmail = existingOwnerEmail;
      saved = true;
      openOperatorUi();
    } finally {
      submitting = false;
    }
  }
</script>

<svelte:head>
  <title>Local TechStack setup | kombify Techstack</title>
</svelte:head>

<main class="min-h-screen bg-background text-foreground">
  <section
    class="mx-auto flex min-h-screen w-full max-w-6xl flex-col px-6 py-8"
  >
    <header class="flex items-center justify-between">
      <a
        href={backHref}
        class="inline-flex items-center gap-2 text-sm font-semibold text-muted-foreground"
      >
        <ArrowLeft class="h-4 w-4" /> Back
      </a>
      <TechstackBrandLogo
        sizes="214px"
        class="h-10 w-auto max-w-[min(58vw,360px)] object-contain object-right"
      />
    </header>
    <div class="grid flex-1 items-center gap-10 py-10 lg:grid-cols-[1fr_0.9fr]">
      <div class="max-w-xl">
        <div
          class="mb-7 inline-flex items-center gap-2 rounded-md border border-border bg-muted/40 px-3 py-2 text-sm font-semibold"
        >
          <Monitor class="h-4 w-4" /> {windowsClient
            ? "Local Windows installation"
            : "Self-hosted local owner"}
        </div>
        <h1
          class="text-5xl font-semibold leading-tight tracking-normal text-foreground"
        >
          Local TechStack setup.
        </h1>
        <p class="mt-5 text-lg leading-8 text-muted-foreground">
          {windowsClient
            ? "Create the first local admin for this device. After setup, this Windows client opens the operator UI directly."
            : "Create or sign in as the local owner for this self-hosted Techstack. After setup, the operator UI opens directly."}
        </p>
      </div>
      <form
        data-kx="plate"
        class="p-6"
        onsubmit={(event) => {
          event.preventDefault();
          if (setupAvailable) {
            void saveLocalSetup();
            return;
          }
          void signInExistingOwner();
        }}
      >
        {#if loading}
          <div
            class="rounded-md border border-border bg-muted/40 p-4 text-sm text-muted-foreground"
          >
            Checking local auth state...
          </div>
        {:else if saved}
          <div
            class="rounded-xl border border-success/30 bg-success/5 p-4 text-sm text-success"
          >
            <div class="flex items-center gap-2 font-semibold">
              <CheckCircle2 class="h-4 w-4" /> Local owner signed in.
            </div>
            {#if signedInEmail}
              <p class="mt-2 text-xs text-success">{signedInEmail}</p>
            {/if}
          </div>
        {:else}
          {#if error}
            <div
              role="alert"
              class="mb-5 rounded-xl border border-destructive/30 bg-destructive/5 p-4 text-sm text-destructive"
            >
              {error}
            </div>
          {/if}
          {#if !setupAvailable}
            <div
              class="mb-5 flex items-center gap-2 text-lg font-semibold text-foreground"
            >
              <KeyRound class="h-5 w-5 text-primary" /> Local owner sign-in
            </div>
            <p class="mb-5 text-sm leading-6 text-muted-foreground">
              This device is already configured. Sign in with the local owner
              account to open Techstack.
            </p>
            <label
              class="block text-sm font-semibold text-foreground"
              for="existing-owner-email">Email</label
            >
            <input
              id="existing-owner-email"
              data-testid="windows-local-existing-email"
              class="mt-2 h-10 w-full rounded-md border border-border bg-background px-3 text-sm outline-none focus:border-primary"
              bind:value={existingOwnerEmail}
              type="email"
              autocomplete="email"
              placeholder="you@example.com"
              required
              disabled={submitting}
            />
            <label
              class="mt-5 block text-sm font-semibold text-foreground"
              for="existing-owner-password">Password</label
            >
            <input
              id="existing-owner-password"
              data-testid="windows-local-existing-password"
              class="mt-2 h-10 w-full rounded-md border border-border bg-background px-3 text-sm outline-none focus:border-primary"
              bind:value={existingOwnerPassword}
              type="password"
              autocomplete="current-password"
              minlength="8"
              required
              disabled={submitting}
            />
            <button
              type="submit"
              data-testid="windows-local-existing-submit"
              disabled={!canSignInExistingOwner || submitting}
              class="mt-6 inline-flex h-10 w-full items-center justify-center rounded-md bg-primary px-4 text-sm font-semibold text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {submitting ? "Signing in..." : "Sign in locally"}
            </button>
          {:else}
            <div
              class="mb-5 flex items-center gap-2 text-lg font-semibold text-foreground"
            >
              <KeyRound class="h-5 w-5 text-primary" /> First local admin
            </div>
            <label
              class="block text-sm font-semibold text-foreground"
              for="admin-email">Email</label
            >
            <input
              id="admin-email"
              data-testid="windows-local-admin-email"
              class="mt-2 h-10 w-full rounded-md border border-border bg-background px-3 text-sm outline-none focus:border-primary"
              bind:value={adminEmail}
              type="email"
              autocomplete="email"
              placeholder="you@example.com"
              required
              disabled={submitting}
            />
            {#if ownerName}
              <p class="mt-2 text-xs text-muted-foreground">Owner name: {ownerName}</p>
            {/if}
            <label
              class="mt-5 block text-sm font-semibold text-foreground"
              for="admin-password">Password</label
            >
            <input
              id="admin-password"
              data-testid="windows-local-admin-password"
              class="mt-2 h-10 w-full rounded-md border border-border bg-background px-3 text-sm outline-none focus:border-primary"
              bind:value={adminPassword}
              type="password"
              autocomplete="new-password"
              minlength="8"
              required
              disabled={submitting}
            />
            <button
              type="submit"
              data-testid="windows-local-setup-submit"
              disabled={!canSubmit || submitting}
              class="mt-6 inline-flex h-10 w-full items-center justify-center rounded-md bg-primary px-4 text-sm font-semibold text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {submitting ? "Creating local owner..." : "Continue local setup"}
            </button>
          {/if}
        {/if}
      </form>
    </div>
  </section>
</main>
