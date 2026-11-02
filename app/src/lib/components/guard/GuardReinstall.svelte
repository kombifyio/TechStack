<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { createGuardReinstallCommand } from "#lib/api/tunnel.js";

  let preparing = $state(false);
  let command = $state("");
  let expiresAt = $state("");
  let failed = $state(false);
  let copied = $state(false);

  async function prepare() {
    if (preparing) return;
    preparing = true;
    failed = false;
    copied = false;
    command = "";
    try {
      const minted = await createGuardReinstallCommand();
      command = minted.linux ?? "";
      expiresAt = minted.expires_at ?? "";
      failed = !command;
    } catch {
      failed = true;
    } finally {
      preparing = false;
    }
  }

  async function copyCommand() {
    if (!command) return;
    try {
      await navigator.clipboard.writeText(command);
      copied = true;
    } catch {
      copied = false;
    }
  }
</script>

<section
  class="rounded-lg border border-border bg-muted/20 p-4"
  aria-labelledby="guard-reinstall-title"
  data-testid="guard-reinstall"
>
  <h3 id="guard-reinstall-title" class="text-sm font-semibold text-foreground">
    {tr("guard.reinstall.title")}
  </h3>
  <p class="mt-2 text-xs text-muted-foreground">{tr("guard.reinstall.body")}</p>
  <!-- A command expires after 15 minutes, so a new one can always be made. -->
  <Button
    class="mt-3"
    variant={command ? "secondary" : "primary"}
    size="sm"
    testId="guard-reinstall-prepare"
    disabled={preparing}
    onclick={() => void prepare()}
  >
    {preparing ? tr("guard.reinstall.preparing") : tr("guard.reinstall.action")}
  </Button>
  {#if failed}
    <p class="mt-2 text-xs text-foreground" role="alert">{tr("guard.reinstall.failed")}</p>
  {/if}
  {#if command}
    <pre
      class="mt-3 overflow-x-auto whitespace-pre-wrap break-all rounded-md bg-background p-3 text-xs text-foreground"
      data-testid="guard-reinstall-command">{command}</pre>
    {#if expiresAt}
      <p class="mt-2 text-xs text-muted-foreground">
        {tr("guard.reinstall.expires", { at: new Date(expiresAt).toLocaleTimeString() })}
      </p>
    {/if}
    <Button
      class="mt-2"
      variant="secondary"
      size="sm"
      testId="guard-reinstall-copy"
      onclick={() => void copyCommand()}
    >
      {copied ? tr("guard.reinstall.copied") : tr("guard.reinstall.copy")}
    </Button>
  {/if}
</section>
