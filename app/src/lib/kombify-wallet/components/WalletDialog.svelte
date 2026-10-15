<script lang="ts">
  /**
   * A Wallet dialog on the app's modal. Escape, the backdrop and the close
   * button all end in `onclose`, so callers keep their state in step.
   */
  import type { Snippet } from "svelte";
  import Modal from "#lib/components/Modal.svelte";

  interface Props {
    title: string;
    description?: string;
    onclose: () => void;
    maxWidth?: "sm" | "md" | "lg" | "xl" | "2xl";
    children: Snippet;
    // Accepted for call-site parity with the Cloud dialogs; the modal owns both.
    open?: boolean;
    closeLabel?: string;
    protectedSurface?: boolean;
  }

  let {
    title,
    description,
    onclose,
    maxWidth = "xl",
    children,
  }: Props = $props();
</script>

<Modal {title} {description} onClose={onclose} {maxWidth}>
  {@render children()}
</Modal>
