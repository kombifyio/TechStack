<script lang="ts">
  import Skeleton from "./Skeleton.svelte";

  interface Props {
    lines?: number;
    class?: string;
  }

  let { lines = 3, class: className = "" }: Props = $props();
</script>

<div class="space-y-3 {className}">
  {#each Array(lines) as _, i}
    <Skeleton width={i === lines - 1 ? "75%" : "100%"} height="1rem" />
  {/each}
</div>
