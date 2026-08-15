<script lang="ts">
  import Skeleton from "./Skeleton.svelte";

  interface Props {
    class?: string;
    showAvatar?: boolean;
    showMeta?: boolean;
  }

  let {
    class: className = "",
    showAvatar = true,
    showMeta = true,
  }: Props = $props();
</script>

<div class="card p-4 {className}">
  <div class="flex items-start gap-4">
    {#if showAvatar}
      <Skeleton width="48px" height="48px" rounded="full" />
    {/if}

    <div class="flex-1 space-y-3">
      <Skeleton width="60%" height="1.25rem" />
      <Skeleton width="100%" height="0.875rem" />
      <Skeleton width="80%" height="0.875rem" />

      {#if showMeta}
        <div class="flex gap-4 pt-2">
          <Skeleton width="80px" height="0.75rem" />
          <Skeleton width="60px" height="0.75rem" />
        </div>
      {/if}
    </div>
  </div>
</div>
