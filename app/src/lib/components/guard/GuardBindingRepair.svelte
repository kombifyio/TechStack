<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { fetchApi } from "#lib/api/client.js";
  import { getGuardBinaryPins } from "#lib/api/workers.js";
  import { generateInstallCommand } from "#lib/wizard/requirements.js";

  interface Props {
    serverId: string;
    compact?: boolean;
  }

  let { serverId, compact = false }: Props = $props();

  let preparing = $state(false);
  let command = $state("");
  let unavailable = $state(false);
  let failed = $state(false);
  let copied = $state(false);

  async function prepare() {
    if (preparing || !serverId) return;
    preparing = true;
    failed = false;
    unavailable = false;
    copied = false;
    command = "";
    try {
      const [minted, pins] = await Promise.all([
        fetchApi<{ token?: string }>(
          `/api/v1/servers/${encodeURIComponent(serverId)}/binding-repair`,
          { method: "POST" },
        ),
        getGuardBinaryPins(),
      ]);
      const next = generateInstallCommand(
        window.location.origin,
        minted.data?.token ?? "",
        "",
        pins,
      );
      if (!next) {
        unavailable = true;
        return;
      }
      command = next;
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
  class="rounded-lg border border-border bg-muted/20 {compact ? 'p-3' : 'p-4'}"
  aria-labelledby="guard-repair-title"
  data-testid="binding-repair"
>
  <h3 id="guard-repair-title" class="text-sm font-semibold text-foreground">
    {tr("guard.repair.title")}
  </h3>
  <p class="mt-2 text-xs text-muted-foreground">{tr("guard.repair.body")}</p>
  {#if !command}
    <Button
      class="mt-3"
      variant="primary"
      size="sm"
      testId="binding-repair-prepare"
      disabled={preparing}
      onclick={() => void prepare()}
    >
      {preparing ? tr("guard.repair.preparing") : tr("guard.repair.action")}
    </Button>
  {/if}
  {#if unavailable}
    <p class="mt-2 text-xs text-foreground" role="alert">{tr("guard.repair.unavailable")}</p>
  {/if}
  {#if failed}
    <p class="mt-2 text-xs text-foreground" role="alert">{tr("guard.repair.failed")}</p>
  {/if}
  {#if command}
    <p class="mt-3 text-xs text-muted-foreground">{tr("guard.repair.command")}</p>
    <pre
      class="mt-2 overflow-x-auto whitespace-pre-wrap break-all rounded-md bg-background p-3 text-xs text-foreground"
      data-testid="binding-repair-command">{command}</pre>
    <Button
      class="mt-2"
      variant="secondary"
      size="sm"
      testId="binding-repair-copy"
      onclick={() => void copyCommand()}
    >
      {copied ? tr("guard.repair.copied") : tr("guard.repair.copy")}
    </Button>
  {/if}
</section>
