<!--
  LoginMethodCards - optional login methods (password / MFA / passwordless)
  for custom owner bootstraps, plus the "activation follows rollout" note
  when no method is selected. Owner identity fields only render for local
  owners; cloud-linked owners derive identity server-side.
-->
<script lang="ts">
  import type { StackConfig } from "$lib/wizard";
  import type { OwnerStepState } from "$lib/wizard/owner-state.svelte";
  import { tr } from "$lib/i18n.svelte";

  interface Props {
    config: StackConfig;
    owner: OwnerStepState;
  }

  let { config, owner }: Props = $props();

  const showOwnerIdentityInputs = $derived(
    config.owner.source !== "cloud-linked",
  );
</script>

{#if !owner.hasLoginMethod}
  <div class="p-4 rounded-lg border border-info/30 bg-info/10">
    <div class="flex items-start gap-3">
      <svg
        class="w-5 h-5 flex-shrink-0 mt-0.5 text-info"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path
          stroke-linecap="round"
          stroke-linejoin="round"
          stroke-width="2"
          d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
        />
      </svg>
      <div>
        <p class="font-medium text-foreground">
          Pocket ID owner activation follows rollout
        </p>
        <p class="text-sm text-muted-foreground mt-1">
          You can proceed without a password in the wizard. TechStack will
          prepare the Owner bootstrap and the StackKit will expose a passkey
          setup handoff when provisioning completes.
        </p>
      </div>
    </div>
  </div>
{/if}

<div class="grid gap-4">
  <!-- Password Card -->
  <div
    class="card p-4 transition-all {config.auth.requirePassword
      ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
      : ''}"
  >
    <button
      type="button"
      class="w-full text-left"
      onclick={() => owner.togglePasswordAuth()}
      aria-pressed={config.auth.requirePassword}
      data-testid="easy-auth-password"
    >
      <div class="flex items-start gap-4">
        <div class="mt-0.5">
          {#if config.auth.requirePassword}
            <div
              class="w-5 h-5 rounded-full bg-primary flex items-center justify-center"
            >
              <svg
                class="w-3 h-3 text-primary-foreground"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="3"
                  d="M5 13l4 4L19 7"
                />
              </svg>
            </div>
          {:else}
            <div
              class="w-5 h-5 rounded-full border-2 border-muted-foreground/30"
            ></div>
          {/if}
        </div>
        <div class="flex-1">
          <p class="text-foreground font-semibold">
            {tr("wizard.login.password.title")}
          </p>
          <p class="text-muted-foreground text-sm">
            {tr("wizard.login.password.description")}
          </p>
        </div>
      </div>
    </button>

    {#if config.auth.requirePassword}
      <div class="mt-4 pt-4 border-t border-border space-y-3">
        <div class="grid gap-3 md:grid-cols-2">
          {#if showOwnerIdentityInputs}
            <div>
              <label
                for="admin-username"
                class="block text-sm text-muted-foreground mb-1"
                >{tr("wizard.login.username")}</label
              >
              <input
                id="admin-username"
                type="text"
                bind:value={config.owner.username}
                placeholder="owner"
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
              />
            </div>
            <div>
              <label
                for="admin-email"
                class="block text-sm text-muted-foreground mb-1"
                >{tr("wizard.login.email")}</label
              >
              <input
                id="admin-email"
                type="email"
                bind:value={config.owner.email}
                placeholder="owner@example.com"
                class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
              />
            </div>
          {/if}
          <div>
            <label
              for="admin-password"
              class="block text-sm text-muted-foreground mb-1"
              >{tr("wizard.login.password")}</label
            >
            <input
              id="admin-password"
              type="password"
              bind:value={config.admin.password}
              class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground focus:border-primary focus:ring-1 focus:ring-primary"
            />
          </div>
          <div>
            <label
              for="admin-password-confirm"
              class="block text-sm text-muted-foreground mb-1"
              >{tr("wizard.login.confirmPassword")}</label
            >
            <input
              id="admin-password-confirm"
              type="password"
              bind:value={owner.adminPasswordConfirm}
              class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground focus:border-primary focus:ring-1 focus:ring-primary {!owner.passwordsMatch &&
              owner.adminPasswordConfirm
                ? 'border-destructive'
                : ''}"
            />
            {#if !owner.passwordsMatch && owner.adminPasswordConfirm}
              <p class="text-destructive text-xs mt-1">
                {tr("wizard.login.passwordMismatch")}
              </p>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>

  <!-- MFA Card -->
  <div
    class="card p-4 transition-all {config.auth.requireMfa
      ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
      : ''}"
  >
    <button
      type="button"
      class="w-full text-left"
      onclick={() => owner.toggleMfaAuth()}
      aria-pressed={config.auth.requireMfa}
      data-testid="easy-auth-mfa"
    >
      <div class="flex items-start gap-4">
        <div class="mt-0.5">
          {#if config.auth.requireMfa}
            <div
              class="w-5 h-5 rounded-full bg-primary flex items-center justify-center"
            >
              <svg
                class="w-3 h-3 text-primary-foreground"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="3"
                  d="M5 13l4 4L19 7"
                />
              </svg>
            </div>
          {:else}
            <div
              class="w-5 h-5 rounded-full border-2 border-muted-foreground/30"
            ></div>
          {/if}
        </div>
        <div class="flex-1">
          <p class="text-foreground font-semibold">
            {tr("wizard.login.mfa.title")}
          </p>
          <p class="text-muted-foreground text-sm">
            {tr("wizard.login.mfa.description")}
          </p>
        </div>
      </div>
    </button>

    {#if config.auth.requireMfa}
      <div class="mt-4 pt-4 border-t border-border">
        <p class="text-sm text-muted-foreground mb-2">
          {tr("wizard.login.mfa.help")}
        </p>
        <div class="flex gap-4">
          <label
            class="flex items-center gap-2 text-foreground text-sm cursor-pointer"
          >
            <input
              type="radio"
              name="mfa-method"
              value="totp"
              checked={config.auth.mfaMethod === "totp"}
              onchange={() => (config.auth.mfaMethod = "totp")}
              class="text-primary"
            />
            Authenticator App (TOTP)
          </label>
          <label
            class="flex items-center gap-2 text-foreground text-sm cursor-pointer"
          >
            <input
              type="radio"
              name="mfa-method"
              value="email"
              checked={config.auth.mfaMethod === "email"}
              onchange={() => (config.auth.mfaMethod = "email")}
              class="text-primary"
            />
            Email Code
          </label>
        </div>
      </div>
    {/if}
  </div>

  <!-- Passwordless Card -->
  <div
    class="card p-4 transition-all {config.auth.allowPasswordless
      ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
      : ''}"
  >
    <button
      type="button"
      class="w-full text-left"
      onclick={() => owner.togglePasswordlessAuth()}
      aria-pressed={config.auth.allowPasswordless}
      data-testid="easy-auth-passwordless"
    >
      <div class="flex items-start gap-4">
        <div class="mt-0.5">
          {#if config.auth.allowPasswordless}
            <div
              class="w-5 h-5 rounded-full bg-primary flex items-center justify-center"
            >
              <svg
                class="w-3 h-3 text-primary-foreground"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="3"
                  d="M5 13l4 4L19 7"
                />
              </svg>
            </div>
          {:else}
            <div
              class="w-5 h-5 rounded-full border-2 border-muted-foreground/30"
            ></div>
          {/if}
        </div>
        <div class="flex-1">
          <p class="text-foreground font-semibold">
            {tr("wizard.login.passwordless.title")}
          </p>
          <p class="text-muted-foreground text-sm">
            {tr("wizard.login.passwordless.description")}
          </p>
        </div>
      </div>
    </button>

    {#if config.auth.allowPasswordless && showOwnerIdentityInputs}
      <div class="mt-4 pt-4 border-t border-border">
        <div>
          <label
            for="passwordless-email"
            class="block text-sm text-muted-foreground mb-1"
            >{tr("wizard.login.email")}</label
          >
          <input
            id="passwordless-email"
            type="email"
            bind:value={config.owner.email}
            placeholder="owner@example.com"
            class="w-full px-3 py-2 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:ring-1 focus:ring-primary"
          />
        </div>
      </div>
    {/if}
  </div>
</div>
