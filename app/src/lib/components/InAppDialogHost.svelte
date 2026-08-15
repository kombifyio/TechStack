<script lang="ts">
  import Modal from "./Modal.svelte";
  import {
    cancelInAppDialog,
    inAppDialog,
    settleInAppDialog,
  } from "$lib/dialogs/in-app-dialog";

  let inputValue = $state("");

  $effect(() => {
    inputValue = $inAppDialog?.initialValue ?? "";
  });

  const confirmClasses = $derived(
    $inAppDialog?.tone === "danger"
      ? "bg-destructive text-destructive-foreground hover:bg-destructive/90"
      : $inAppDialog?.tone === "warning"
        ? "bg-amber-600 text-white hover:bg-amber-500"
        : "bg-primary text-primary-foreground hover:bg-primary/90",
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
          class="input w-full"
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
        <button
          type="button"
          class="btn btn-outline"
          onclick={cancelInAppDialog}
        >
          {$inAppDialog.cancelText ?? "Cancel"}
        </button>
      {/if}
      <button
        type="button"
        class="btn {confirmClasses}"
        onclick={() =>
          settleInAppDialog(
            $inAppDialog?.kind === "prompt" ? inputValue : undefined,
          )}
        disabled={$inAppDialog.kind === "prompt" && !inputValue.trim()}
      >
        {$inAppDialog.confirmText}
      </button>
    {/snippet}
  </Modal>
{/if}
