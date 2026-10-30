<!--
  Homelab settings through the Creation Wizard's five modules. Users and Login
  are active: both act through the Homelab identity runtime
  (/api/v1/stacks/{id}/identity), the same supported path the creation finish
  uses. Goals, Server and Access stay inactive with their reason: applications
  and servers have their own operations, and access has no supported change path
  for an existing Homelab, so nothing here claims to change it.
-->
<script lang="ts">
  import { ArrowUpRight } from "@lucide/svelte";
  import Stepper from "./Stepper.svelte";
  import IdentitySetup from "./owner/IdentitySetup.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { EASY_STEPS } from "#lib/wizard/index.js";
  import { addServerHref } from "#lib/server-links.js";
  import {
    CREATION_PROCESSES,
    createCreationSession,
    creationStepKey,
    creationStepNumber,
    type CreationStepKey,
  } from "#lib/creation/core/index.js";

  interface Props {
    deploymentId: string;
  }

  let { deploymentId }: Props = $props();

  const steps = EASY_STEPS;
  const process = CREATION_PROCESSES["homelab-settings"];
  const inactiveReasons: Record<number, string> = Object.fromEntries(
    Object.entries(process.inactive).map(([key, reason]) => [
      creationStepNumber(key as CreationStepKey),
      tr(reason),
    ]),
  );
  // Nothing here has to be complete before the other module opens: each acts on
  // the running homelab by itself.
  const session = createCreationSession({ process, isComplete: () => true });
  const stepKey = $derived($session.current);
  const step = $derived(creationStepNumber(stepKey));
</script>

<div class="w-full min-w-0" data-testid="homelab-settings-wizard">
  <Stepper
    {steps}
    disabledStepReasons={inactiveReasons}
    currentStep={step}
    showDeploy={false}
    onprev={session.prev}
    onnext={session.next}
    reachableSteps={session.activeSteps().map(creationStepNumber)}
    onselect={(id) => {
      const key = creationStepKey(id);
      if (key) session.open(key);
    }}
  />

  <ul class="mb-6 grid gap-2 text-sm sm:grid-cols-3" data-testid="homelab-settings-inactive">
    <li class="rounded-lg border border-border/70 bg-muted/20 p-3">
      <p class="font-medium text-foreground">{steps[0].label}</p>
      <p class="mt-1 text-muted-foreground">{inactiveReasons[1]}</p>
      <a class="mt-2 inline-flex items-center gap-1 text-primary" href="/services"
        >{tr("wizard.settings.openAddApplication")}<ArrowUpRight
          size={13}
          aria-hidden="true"
        /></a
      >
    </li>
    <li class="rounded-lg border border-border/70 bg-muted/20 p-3">
      <p class="font-medium text-foreground">{steps[1].label}</p>
      <p class="mt-1 text-muted-foreground">{inactiveReasons[2]}</p>
      <a
        class="mt-2 inline-flex items-center gap-1 text-primary"
        href={addServerHref(deploymentId)}
        data-testid="homelab-settings-add-server"
        >{tr("wizard.settings.openAddNode")}<ArrowUpRight
          size={13}
          aria-hidden="true"
        /></a
      >
    </li>
    <li class="rounded-lg border border-border/70 bg-muted/20 p-3">
      <p class="font-medium text-foreground">{steps[2].label}</p>
      <p class="mt-1 text-muted-foreground">{inactiveReasons[3]}</p>
    </li>
  </ul>

  {#key `${deploymentId}:${stepKey}`}
    {#if stepKey === "users"}
      <section data-testid="homelab-settings-users">
        <h2 class="text-lg font-semibold text-foreground">
          {tr("wizard.settings.usersTitle")}
        </h2>
        <p class="mb-4 mt-1 text-sm text-muted-foreground">
          {tr("wizard.settings.usersIntro")}
        </p>
        <IdentitySetup {deploymentId} section="household" />
      </section>
    {:else if stepKey === "login"}
      <section data-testid="homelab-settings-login">
        <h2 class="text-lg font-semibold text-foreground">
          {tr("wizard.settings.loginTitle")}
        </h2>
        <p class="mb-4 mt-1 text-sm text-muted-foreground">
          {tr("wizard.settings.loginIntro")}
        </p>
        <IdentitySetup {deploymentId} section="owner" />
      </section>
    {/if}
  {/key}
</div>
