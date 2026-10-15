<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { untrack } from "svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    decideGuardIdentityUpgrade,
    denialReason,
    getGuardIdentityUpgrade,
    type GuardIdentityUpgrade,
  } from "#lib/api/guard.js";
  import GuardStepUpNotice from "./GuardStepUpNotice.svelte";

  interface Props {
    workerId: string;
    /** Only a server that still authenticates with its bearer credential needs this. */
    required: boolean;
    onChanged?: () => void | Promise<void>;
  }

  let { workerId, required, onChanged }: Props = $props();

  let upgrade = $state<GuardIdentityUpgrade | null>(null);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let stepUp = $state(false);
  let approved = $state(false);

  async function load() {
    try {
      upgrade = await getGuardIdentityUpgrade(workerId);
    } catch {
      upgrade = null;
    }
  }

  $effect(() => {
    if (workerId && required) {
      untrack(() => {
        void load();
      });
    }
  });

  async function decide(decision: Parameters<typeof decideGuardIdentityUpgrade>[1]) {
    busy = true;
    error = null;
    stepUp = false;
    try {
      upgrade = await decideGuardIdentityUpgrade(workerId, decision);
      approved = "approve" in decision;
      await onChanged?.();
    } catch (cause) {
      const parsed = parseApiError(cause);
      if (
        parsed.isForbidden &&
        denialReason(parsed.details) === "identity_upgrade_step_up_required"
      ) {
        stepUp = true;
      } else {
        error = parsed.message;
      }
    } finally {
      busy = false;
    }
  }
</script>

{#if required || approved}
  <section
    class="rounded-lg border border-border bg-muted/20 p-4"
    aria-labelledby="guard-upgrade-title"
    data-testid="guard-identity-upgrade"
  >
    <h3 id="guard-upgrade-title" class="text-sm font-semibold text-foreground">
      {tr("guard.upgrade.title")}
    </h3>
    {#if approved && upgrade?.identity_binding === "host_key"}
      <p class="mt-2 text-xs text-foreground" role="status">
        {tr("guard.upgrade.done")}
      </p>
    {:else if upgrade?.pending}
      <p class="mt-2 text-xs text-foreground" data-testid="guard-upgrade-pending">
        {tr("guard.upgrade.pending", {
          fingerprint: upgrade.pending.key_thumbprint,
        })}
      </p>
      <div class="mt-3 flex flex-wrap gap-2">
        <Button
          variant="primary"
          size="sm"
          testId="guard-upgrade-approve"
          disabled={busy}
          onclick={() =>
            void decide({ approve: upgrade?.pending?.key_thumbprint ?? "" })}
        >
          {tr("guard.upgrade.approve")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          testId="guard-upgrade-reject"
          disabled={busy}
          onclick={() => void decide({ reject: true })}
        >
          {tr("guard.upgrade.reject")}
        </Button>
      </div>
    {:else}
      <p class="mt-2 text-xs text-muted-foreground">{tr("guard.upgrade.none")}</p>
    {/if}

    {#if stepUp}
      <GuardStepUpNotice
        class="mt-3"
        body={tr("guard.upgrade.stepUp")}
        onConfirmed={() => (stepUp = false)}
      />
    {/if}
    {#if error}
      <p class="mt-3 text-xs text-destructive" role="alert">{error}</p>
    {/if}
  </section>
{/if}
