<script lang="ts">
  import Modal from "./Modal.svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    cancelInAppDialog,
    inAppDialog,
    settleInAppDialog,
  } from "#lib/dialogs/in-app-dialog.js";

  let inputValue = $derived($inAppDialog?.initialValue ?? "");

  const confirmVariant = $derived(
    $inAppDialog?.tone === "danger" ? "destructive" : "primary",
  );
</script>

{#if $inAppDialog}
  <Modal
    title={$inAppDialog.title}
    description={$inAppDialog.message}
    onClose={cancelInAppDialog}
    maxWidth="sm"
  >
    {#if $inAppDialog.kind === "prompt"}
      <label class="block space-y-2 text-sm">
        <span class="font-medium text-foreground">
          {$inAppDialog.inputLabel}
        </span>
        <input
          class="flex h-10 w-full rounded-lg border border-input bg-transparent px-3 py-2 text-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
          type={$inAppDialog.inputType ?? "text"}
          bind:value={inputValue}
          autocomplete={$inAppDialog.inputType === "password"
            ? "current-password"
            : "off"}
        />
      </label>
    {/if}

    {#snippet footer()}
      {#if $inAppDialog.kind !== "notice"}
        <Button variant="secondary" onclick={cancelInAppDialog}>
          {$inAppDialog.cancelText ?? "Cancel"}
        </Button>
      {/if}
      <Button
        variant={confirmVariant}
        onclick={() =>
          settleInAppDialog(
            $inAppDialog?.kind === "prompt" ? inputValue : undefined,
          )}
        disabled={$inAppDialog.kind === "prompt" && !inputValue.trim()}
      >
        {$inAppDialog.confirmText}
      </Button>
    {/snippet}
  </Modal>
{/if}
