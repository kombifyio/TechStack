<script lang="ts">
  import Button from "#lib/components/ui/Button.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import type { OperatorSelfDisclosure } from "#lib/api/operatorProfile.js";
  import {
    SELF_DISCLOSURE_NOTES_MAX_CHARS,
    selfDisclosureReviewRows,
  } from "#lib/wizard/self-disclosure-handoff.js";

  interface Props {
    /** Answers a link handed over; shown, never stored by this component. */
    disclosure: OperatorSelfDisclosure;
    /** Store the shown answers plus what the operator typed as a note. */
    onuse: (notes: string) => void;
    /** Drop the handed-over answers without storing anything. */
    ondiscard: () => void;
  }

  const { disclosure, onuse, ondiscard }: Props = $props();

  const rows = $derived(selfDisclosureReviewRows(disclosure));
  let notes = $state("");
</script>

<section
  class="mb-6 rounded-lg border border-primary/30 bg-primary/5 p-4 text-sm text-foreground sm:p-5"
  aria-labelledby="self-disclosure-review-title"
  data-testid="self-disclosure-review"
>
  <h2 id="self-disclosure-review-title" class="text-base font-semibold">
    {tr("wizard.selfDisclosure.review.title")}
  </h2>
  <p class="mt-1 text-muted-foreground">
    {tr("wizard.selfDisclosure.review.body")}
  </p>

  <dl class="mt-4 grid gap-3 sm:grid-cols-2">
    {#each rows as row (row.question)}
      <div>
        <dt class="text-xs font-medium text-muted-foreground">
          {tr(row.questionKey)}
        </dt>
        <dd class="mt-1 flex flex-wrap gap-1.5">
          {#each row.answerKeys as answerKey (answerKey)}
            <span
              class="rounded-full border border-border bg-background px-2.5 py-0.5"
              >{tr(answerKey)}</span
            >
          {/each}
        </dd>
      </div>
    {/each}
  </dl>

  <label class="mt-4 block" for="self-disclosure-notes">
    <span class="font-medium">{tr("wizard.selfDisclosure.review.notes")}</span>
    <span class="mt-0.5 block text-muted-foreground">
      {tr("wizard.selfDisclosure.review.notesHint")}
    </span>
  </label>
  <textarea
    id="self-disclosure-notes"
    bind:value={notes}
    maxlength={SELF_DISCLOSURE_NOTES_MAX_CHARS}
    rows="3"
    class="mt-2 w-full rounded-lg border border-border bg-input px-3 py-2 text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
  ></textarea>

  <div class="mt-4 flex flex-wrap gap-2">
    <Button
      variant="primary"
      testId="self-disclosure-use"
      onclick={() => onuse(notes)}
    >
      {tr("wizard.selfDisclosure.review.use")}
    </Button>
    <Button
      variant="ghost"
      testId="self-disclosure-discard"
      onclick={ondiscard}
    >
      {tr("wizard.selfDisclosure.review.discard")}
    </Button>
  </div>
</section>
