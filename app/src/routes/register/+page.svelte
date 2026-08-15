<script lang="ts">
  import { goto } from "$app/navigation";
  import { onMount } from "svelte";
  import { parseApiError } from "$lib/api/errors";
  import { createLocalOwnerAccount } from "$lib/auth/local-owner";
  import { authStore } from "$lib/stores/auth.svelte";
  import { resolveLoginExperience } from "$lib/auth/login-experience";
  import { Mail, Lock, Eye, EyeOff, UserPlus, RefreshCw } from "@lucide/svelte";
  import ErrorCallout from "$lib/components/ui/ErrorCallout.svelte";

  let email = $state("");
  let password = $state("");
  let passwordConfirm = $state("");
  let error = $state("");
  let loading = $state(false);
  let ready = $state(false);
  let showPassword = $state(false);
  let showPasswordConfirm = $state(false);

  onMount(async () => {
    const searchParams = new URLSearchParams(window.location.search);
    const embedded =
      searchParams.get("embedded") === "true" || window.parent !== window;
    await authStore.init({ embedded });

    if (authStore.isAuthenticated) {
      await goto("/stacks");
      return;
    }

    if (
      resolveLoginExperience({
        deploymentMode: authStore.deploymentMode,
        embedded,
      }) === "saas-auth0"
    ) {
      await goto(searchParams.toString() ? `/login?${searchParams}` : "/login");
      return;
    }

    ready = true;
  });

  async function handleRegister() {
    error = "";

    if (password !== passwordConfirm) {
      error = "Passwords do not match.";
      return;
    }

    if (password.length < 8) {
      error = "Password must be at least 8 characters.";
      return;
    }

    loading = true;

    try {
      const normalizedEmail = await createLocalOwnerAccount({
        email,
        password,
        passwordConfirm,
        isFirstRun: authStore.isFirstRun,
      });

      const ok = await authStore.loginWithPassword(normalizedEmail, password);
      if (!ok) {
        error = authStore.error || "Account created, but sign-in failed.";
        return;
      }

      await goto("/stacks");
    } catch (err) {
      const parsed = parseApiError(err);
      error =
        parsed.fieldErrors.email?.message ||
        parsed.fieldErrors.password?.message ||
        parsed.message ||
        "Registration failed. Please try again.";
    } finally {
      loading = false;
    }
  }
</script>

<svelte:head>
  <title>Sign Up | kombify-TechStack</title>
  <meta name="description" content="Create your kombify-TechStack account" />
</svelte:head>

{#if ready}
  <div class="min-h-screen flex items-center justify-center px-4 bg-background">
    <div class="w-full max-w-md">
      <!-- Logo -->
      <div class="text-center mb-8">
        <img
          src="/kombify-techstack-kombi3d.png"
          alt="kombify-TechStack Logo"
          class="h-24 mx-auto"
        />
      </div>

      <!-- Register Card -->
      <div class="card overflow-hidden">
        <!-- Glassmorphism Header -->
        <div
          class="relative h-28 bg-linear-to-br from-primary/20 via-primary/10 to-transparent overflow-hidden"
        >
          <div
            class="absolute inset-0 bg-[radial-gradient(circle_at_30%_50%,rgba(var(--primary-rgb),0.15),transparent_50%)]"
          ></div>
          <div
            class="absolute inset-0 bg-[radial-gradient(circle_at_70%_80%,rgba(var(--primary-rgb),0.1),transparent_50%)]"
          ></div>
          <div class="absolute bottom-4 left-6 right-6">
            <h2 class="text-xl font-bold text-foreground">Create Account</h2>
            <p class="text-muted-foreground text-sm">
              Join kombify-TechStack to manage your infrastructure
            </p>
          </div>
        </div>

        <div class="p-6">
          {#if error}
            <ErrorCallout message={error} class="mb-4" />
          {/if}

          <form
            onsubmit={(e) => {
              e.preventDefault();
              void handleRegister();
            }}
            class="space-y-4"
          >
            <!-- Email Input -->
            <div class="space-y-2">
              <label for="email" class="text-sm font-medium">Email</label>
              <div class="relative">
                <Mail
                  class="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground"
                />
                <input
                  id="email"
                  type="email"
                  bind:value={email}
                  class="w-full pl-10 pr-4 py-2.5 rounded-lg border border-border bg-background
                    focus:outline-none focus:ring-2 focus:ring-primary/50 focus:border-primary
                    placeholder:text-muted-foreground/60 transition-all disabled:opacity-50"
                  placeholder="you@example.com"
                  required
                  disabled={loading}
                />
              </div>
            </div>

            <!-- Password Input -->
            <div class="space-y-2">
              <label for="password" class="text-sm font-medium">Password</label>
              <div class="relative">
                <Lock
                  class="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground"
                />
                <input
                  id="password"
                  type={showPassword ? "text" : "password"}
                  bind:value={password}
                  class="w-full pl-10 pr-12 py-2.5 rounded-lg border border-border bg-background
                    focus:outline-none focus:ring-2 focus:ring-primary/50 focus:border-primary
                    placeholder:text-muted-foreground/60 transition-all disabled:opacity-50"
                  placeholder="••••••••"
                  required
                  disabled={loading}
                />
                <button
                  type="button"
                  onclick={() => (showPassword = !showPassword)}
                  class="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                >
                  {#if showPassword}
                    <EyeOff class="h-4 w-4" />
                  {:else}
                    <Eye class="h-4 w-4" />
                  {/if}
                </button>
              </div>
            </div>

            <!-- Confirm Password Input -->
            <div class="space-y-2">
              <label for="passwordConfirm" class="text-sm font-medium"
                >Confirm Password</label
              >
              <div class="relative">
                <Lock
                  class="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground"
                />
                <input
                  id="passwordConfirm"
                  type={showPasswordConfirm ? "text" : "password"}
                  bind:value={passwordConfirm}
                  class="w-full pl-10 pr-12 py-2.5 rounded-lg border border-border bg-background
                    focus:outline-none focus:ring-2 focus:ring-primary/50 focus:border-primary
                    placeholder:text-muted-foreground/60 transition-all disabled:opacity-50"
                  placeholder="••••••••"
                  required
                  disabled={loading}
                />
                <button
                  type="button"
                  onclick={() => (showPasswordConfirm = !showPasswordConfirm)}
                  class="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground transition-colors"
                >
                  {#if showPasswordConfirm}
                    <EyeOff class="h-4 w-4" />
                  {:else}
                    <Eye class="h-4 w-4" />
                  {/if}
                </button>
              </div>
            </div>

            <button
              type="submit"
              class="w-full py-2.5 px-4 rounded-lg bg-primary text-primary-foreground font-medium
                hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed
                transition-all flex items-center justify-center gap-2"
              disabled={loading}
            >
              {#if loading}
                <RefreshCw class="h-4 w-4 animate-spin" />
                Creating account...
              {:else}
                <UserPlus class="h-4 w-4" />
                Create Account
              {/if}
            </button>
          </form>

          <!-- Footer Links -->
          <div class="mt-6 text-center text-sm text-muted-foreground">
            <p>
              Already have an account?
              <a href="/login" class="text-primary hover:underline ml-1"
                >Sign in</a
              >
            </p>
          </div>
        </div>
      </div>

      <!-- Footer -->
      <p class="text-center text-muted-foreground/60 text-xs mt-8">
        &copy; 2024-2026 kombify. All rights reserved.
      </p>
    </div>
  </div>
{:else}
  <div class="min-h-screen flex items-center justify-center px-4 bg-background">
    <p class="text-sm text-muted-foreground">Checking authentication mode…</p>
  </div>
{/if}
