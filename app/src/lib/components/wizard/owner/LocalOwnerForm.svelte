<script lang="ts">
  import type { StackConfig } from "#lib/wizard/index.js";
  import { tr } from "#lib/i18n.svelte.js";
  import { useWizardField } from "#lib/wizard/validation-context.js";
  let { config }: { config: StackConfig } = $props();
  const emailField = useWizardField("owner-email");
</script>

<div class="owner-fields">
  <label for="owner-email"
    ><span>{tr("wizard.login.profile.email")}</span>
    <input
      id="owner-email"
      type="email"
      bind:value={config.owner.email}
      autocomplete="email"
      required
      aria-invalid={emailField.invalid ? "true" : undefined}
      aria-describedby={emailField.describedBy("owner-email-guidance")}
      placeholder="you@example.com"
    />
  </label>
  {#if emailField.invalid}
    <p id={emailField.errorId} class="field-error" data-testid="owner-email-error">
      {emailField.message}
    </p>
  {/if}
  <label for="owner-display-name"
    ><span>{tr("wizard.login.profile.name")}</span>
    <input
      id="owner-display-name"
      type="text"
      bind:value={config.owner.displayName}
      autocomplete="name"
    />
  </label>
  <p id="owner-email-guidance">{tr("wizard.login.profile.emailHint")}</p>
</div>

<style>
  .owner-fields {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 14px 22px;
    max-width: 850px;
  }
  label {
    display: grid;
    gap: 8px;
    font-size: 13px;
    font-weight: 550;
  }
  input {
    width: 100%;
    min-width: 0;
    padding: 12px 14px;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    background: var(--input);
    color: var(--foreground);
    font-size: 14px;
  }
  input::placeholder {
    color: var(--muted-foreground);
  }
  input:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 2px;
  }
  .field-error {
    color: var(--destructive);
  }
  p {
    grid-column: 1 / -1;
    font-size: 12px;
    color: var(--muted-foreground);
    line-height: 1.7;
  }
  @media (max-width: 650px) {
    .owner-fields {
      grid-template-columns: 1fr;
    }
  }
</style>
