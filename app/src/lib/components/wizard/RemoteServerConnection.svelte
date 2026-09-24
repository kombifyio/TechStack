<script lang="ts">
  import {
    KeyRound,
    LockKeyhole,
    SlidersHorizontal,
    ChevronDown,
    PlugZap,
  } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { testWizardRemoteSSH } from "#lib/api/wizardRemoteSSH.js";
  import type { StackConfig } from "#lib/wizard/index.js";
  let {
    config = $bindable(),
    reviewMode = false,
  }: { config: StackConfig; reviewMode?: boolean } = $props();
  let remoteTestState = $state<"idle" | "testing" | "success" | "error">(
    "idle",
  );
  let remoteTestMessage = $state("");
  let advancedOpen = $state(false);
  let connectionRevision = 0;
  const remote = $derived(config.serverProvisioning.remote);
  const remoteUsesPasswordAuth = $derived(remote.authMethod === "password");
  $effect(() => {
    void [
      remote.host,
      remote.sshPort,
      remote.sshUser,
      remote.authMethod,
      remote.sshPassword,
      remote.sshKeyLabel,
      remote.useSudo,
    ];
    connectionRevision++;
    remoteTestState = "idle";
    remoteTestMessage = "";
  });
  async function testRemoteConnection() {
    if (reviewMode) return;
    const host = remote.host.trim();
    const user = remote.sshUser.trim() || "root";
    if (
      !host ||
      (remoteUsesPasswordAuth
        ? !remote.sshPassword.trim()
        : !remote.sshKeyLabel.trim())
    ) {
      remoteTestState = "error";
      remoteTestMessage = !host
        ? tr("wizard.server.remote.host")
        : tr(
            remoteUsesPasswordAuth
              ? "wizard.server.remote.password"
              : "wizard.server.remote.keyLabel",
          );
      return;
    }
    const revision = connectionRevision;
    remoteTestState = "testing";
    remoteTestMessage = "";
    try {
      const result = await testWizardRemoteSSH({
        host,
        port: remote.sshPort || 22,
        user,
        auth_method: remote.authMethod,
        password: remoteUsesPasswordAuth ? remote.sshPassword : undefined,
        ssh_key_label: remoteUsesPasswordAuth
          ? undefined
          : remote.sshKeyLabel.trim(),
      });
      if (revision !== connectionRevision) return;
      remoteTestState = result.success ? "success" : "error";
      remoteTestMessage = result.success
        ? result.message || tr("wizard.server.remote.test.success")
        : result.error || tr("wizard.server.remote.test.failed");
    } catch (error) {
      if (revision !== connectionRevision) return;
      remoteTestState = "error";
      remoteTestMessage =
        error instanceof Error
          ? error.message
          : tr("wizard.server.remote.test.failed");
    }
  }
</script>

<div class="remote-connect" data-testid="remote-server-config">
  <header>
    <h4>{tr("wizard.server.remote.connectionDetails")}</h4>
    <p>{tr("wizard.server.remote.connectionHint")}</p>
  </header>

  <div class="connection-fields">
    <label for="remote-server-host"
      >{tr("wizard.server.remote.host")}
      <input
        id="remote-server-host"
        type="text"
        bind:value={remote.host}
        placeholder="server.example.com"
        autocomplete="off"
        spellcheck={false}
        data-testid="remote-server-host"
      />
    </label>

    <fieldset>
      <legend>{tr("wizard.server.remote.auth")}</legend>
      <div class="auth-options">
        <label
          ><input
            type="radio"
            name="remote-server-auth"
            value="ssh-key"
            bind:group={remote.authMethod}
          /><KeyRound size={16} aria-hidden="true" /><span
            >{tr("wizard.server.remote.auth.sshKey")}</span
          ></label
        >
        <label
          ><input
            type="radio"
            name="remote-server-auth"
            value="password"
            bind:group={remote.authMethod}
          /><LockKeyhole size={16} aria-hidden="true" /><span
            >{tr("wizard.server.remote.auth.password")}</span
          ></label
        >
      </div>
    </fieldset>

    {#if remoteUsesPasswordAuth}
      <label for="remote-server-password"
        >{tr("wizard.server.remote.password")}<input
          id="remote-server-password"
          type="password"
          bind:value={remote.sshPassword}
          autocomplete="new-password"
          data-testid="remote-server-password"
        /></label
      >
    {:else}
      <label for="remote-server-key"
        >{tr("wizard.server.remote.keyLabel")}<input
          id="remote-server-key"
          type="text"
          bind:value={remote.sshKeyLabel}
          placeholder="root@server"
          autocomplete="off"
          data-testid="remote-server-key-label"
        /><small>{tr("wizard.server.remote.keyHint")}</small></label
      >
    {/if}
  </div>

  <div class="technical-settings">
    <button
      type="button"
      class="technical-toggle"
      aria-expanded={advancedOpen}
      aria-controls="remote-ssh-details"
      onclick={() => (advancedOpen = !advancedOpen)}
    >
      <SlidersHorizontal size={15} aria-hidden="true" />
      <span>{tr("wizard.server.remote.advanced")}</span>
      <ChevronDown
        size={15}
        aria-hidden="true"
        class={advancedOpen ? "open" : undefined}
      />
    </button>
    {#if advancedOpen}
      <div class="ssh-details" id="remote-ssh-details">
        <label for="remote-server-user"
          >{tr("wizard.server.remote.user")}<input
            id="remote-server-user"
            type="text"
            bind:value={remote.sshUser}
            placeholder="root"
            autocomplete="off"
            data-testid="remote-server-user"
          /></label
        >
        <label for="remote-server-port"
          >{tr("wizard.server.remote.port")}<input
            id="remote-server-port"
            type="number"
            min="1"
            max="65535"
            bind:value={remote.sshPort}
            data-testid="remote-server-port"
          /></label
        >
        <label class="sudo"
          ><input type="checkbox" bind:checked={remote.useSudo} />{tr(
            "wizard.server.remote.sudo",
          )}</label
        >
      </div>
    {/if}
  </div>
  <div class="connection-result">
    <button
      type="button"
      data-kx="control"
      data-variant="primary"
      onclick={testRemoteConnection}
      disabled={reviewMode || remoteTestState === "testing"}
      data-testid="remote-server-test-connection"
      ><PlugZap size={16} aria-hidden="true" />{tr(
        remoteTestState === "testing"
          ? "wizard.server.remote.testing"
          : "wizard.server.remote.test",
      )}</button
    >
    {#if remoteTestMessage}<p
        role="status"
        class:error={remoteTestState === "error"}
        data-testid="remote-server-test-result"
      >
        {remoteTestMessage}
      </p>{/if}
  </div>
</div>

<style>
  .remote-connect {
    display: grid;
    gap: 28px;
    max-width: 880px;
    padding-block: 4px 8px;
  }
  h4 {
    font-size: 16px;
    font-weight: 550;
  }
  p {
    font-size: 13px;
    color: var(--muted-foreground);
    line-height: 1.6;
    margin-top: 5px;
  }
  header {
    max-width: 62ch;
  }
  label,
  legend {
    font-size: 12px;
    font-weight: 500;
  }
  label {
    display: grid;
    gap: 8px;
  }
  input:not([type="radio"], [type="checkbox"]) {
    width: 100%;
    min-width: 0;
    color: var(--foreground);
    background: var(--input);
    border: 1px solid var(--border);
    padding: 11px 13px;
    border-radius: var(--radius-control, 10px);
    font-size: 14px;
    font-weight: 400;
  }
  input::placeholder {
    color: var(--muted-foreground);
  }
  small {
    color: var(--muted-foreground);
    font-size: 11px;
    font-weight: 400;
  }
  legend {
    margin-bottom: 8px;
  }
  .connection-fields {
    display: grid;
    grid-template-columns: minmax(0, 1.2fr) minmax(240px, 0.8fr);
    column-gap: 28px;
    row-gap: 22px;
    padding-block: 24px;
    border-block: 1px solid var(--border);
  }
  .connection-fields > :last-child {
    grid-column: 1 / -1;
    max-width: calc(60% - 12px);
  }
  .auth-options {
    display: inline-flex;
    gap: 22px;
    min-height: 40px;
    border-bottom: 1px solid var(--border);
  }
  .auth-options label {
    position: relative;
    display: flex;
    align-items: center;
    gap: 7px;
    padding: 0 2px 10px;
    color: var(--muted-foreground);
    cursor: pointer;
  }
  .auth-options label:has(:checked) {
    color: var(--foreground);
  }
  .auth-options label:has(:checked)::after {
    content: "";
    position: absolute;
    inset-inline: 0;
    bottom: -1px;
    height: 2px;
    background: var(--primary);
  }
  .auth-options label:has(input:focus-visible) {
    outline: 2px solid var(--ring);
    outline-offset: 4px;
  }
  input[type="radio"] {
    position: absolute;
    opacity: 0;
    pointer-events: none;
  }
  input[type="checkbox"] {
    accent-color: var(--primary);
    width: 16px;
    height: 16px;
  }
  .technical-settings {
    display: grid;
    gap: 20px;
  }
  .technical-toggle {
    display: flex;
    align-items: center;
    width: fit-content;
    gap: 8px;
    padding: 0 0 5px;
    color: var(--muted-foreground);
    border-bottom: 1px solid transparent;
    cursor: pointer;
    font-size: 12px;
  }
  .technical-toggle:hover {
    color: var(--foreground);
    border-bottom-color: var(--border);
  }
  .technical-toggle :global(svg.open) {
    transform: rotate(180deg);
  }
  .ssh-details {
    display: grid;
    grid-template-columns: minmax(0, 1fr) minmax(130px, 0.35fr);
    gap: 22px;
    padding-inline-start: 23px;
    border-inline-start: 2px solid
      color-mix(in oklch, var(--primary) 36%, var(--border));
  }
  .sudo {
    grid-column: 1/-1;
    display: flex;
    align-items: center;
    gap: 10px;
    font-weight: 400;
  }
  .connection-result {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 16px;
  }
  .connection-result button {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 10px 15px;
    font-size: 13px;
  }
  .error {
    color: var(--destructive);
  }
  input:focus-visible,
  button:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
  }
  @container (max-width: 660px) {
    .connection-fields {
      grid-template-columns: 1fr;
    }
    .connection-fields > :last-child {
      grid-column: auto;
      max-width: none;
    }
    .ssh-details {
      grid-template-columns: 1fr;
    }
  }
</style>
