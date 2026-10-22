<script lang="ts">
  /**
   * The active data protection profile in the account menu (workspace
   * DATA-ARCHITECTURE.md §5.4.6). It is a status line, not a setting: the
   * profile is edited in kombify Cloud.
   */
  import { onMount } from "svelte";
  import { ShieldCheck, ShieldAlert } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    dataProfileLabelKeys,
    dataProfileState,
    loadDataProfile,
  } from "./data-profile.js";

  onMount(() => {
    void loadDataProfile();
  });

  const label = $derived(
    $dataProfileState.status === "resolved"
      ? dataProfileLabelKeys($dataProfileState.profile)
          .map((key) => tr(key))
          .join(" · ")
      : "",
  );
</script>

{#if $dataProfileState.status === "resolved"}
  <div
    class="flex items-center gap-2 px-3 py-2 text-xs text-muted-foreground"
    data-testid="data-profile-status"
    data-profile-res={$dataProfileState.profile.res}
    data-profile-ret={$dataProfileState.profile.ret}
    data-profile-loc={$dataProfileState.profile.loc}
  >
    <ShieldCheck class="h-4 w-4 shrink-0" aria-hidden="true" />
    <span>{tr("dataProfile.label")}: <span class="text-foreground">{label}</span></span>
  </div>
{:else if $dataProfileState.status === "unresolved"}
  <div
    class="flex items-start gap-2 px-3 py-2 text-xs"
    role="status"
    data-testid="data-profile-status"
    data-profile-unresolved="true"
  >
    <ShieldAlert class="mt-0.5 h-4 w-4 shrink-0 text-warning" aria-hidden="true" />
    <span>
      <span class="font-medium">{tr("dataProfile.label")}: {tr("dataProfile.unresolved")}</span>
      <span class="block text-muted-foreground">{tr("dataProfile.unresolvedHint")}</span>
    </span>
  </div>
{/if}
