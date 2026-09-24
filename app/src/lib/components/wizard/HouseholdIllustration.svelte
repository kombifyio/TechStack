<script lang="ts">
  import type { HouseholdProfile } from "#lib/wizard/household-draft.js";
  import {
    House,
    KeyRound,
    UsersRound,
    Globe2,
    LockKeyhole,
  } from "@lucide/svelte";
  let {
    profile,
    publicAccess = false,
  }: { profile: HouseholdProfile; publicAccess?: boolean } = $props();
</script>

<div class="audience-map" aria-hidden="true">
  <span
    >{#if profile === "solo"}<KeyRound
        size={29}
        strokeWidth={1.3}
      />{:else}<UsersRound size={29} strokeWidth={1.3} />{/if}</span
  ><i></i><House size={66} strokeWidth={1.1} /><i class:public={publicAccess}
  ></i><span
    >{#if publicAccess}<Globe2 size={29} strokeWidth={1.3} />{:else}<LockKeyhole
        size={29}
        strokeWidth={1.3}
      />{/if}</span
  >
</div>

<style>
  .audience-map {
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 150px;
    color: var(--primary);
  }
  span {
    display: grid;
    place-items: center;
    width: 58px;
    height: 58px;
    border: 1px solid var(--border);
    border-radius: 50%;
  }
  i {
    width: 30px;
    border-top: 1px dashed var(--border);
  }
  i.public {
    border-color: var(--primary);
  }
</style>
