<!--
  Homelab settings through the Creation Wizard's five modules. Users and Login
  are active: both act through the Homelab identity runtime
  (/api/v1/stacks/{id}/identity), the same supported path the creation finish
  uses. Goals, Server and Access stay inactive with their reason: applications
  and Nodes have their own operations, and access has no supported change path
  for an existing Homelab, so nothing here claims to change it.
-->
<script lang="ts">
  import { ArrowUpRight } from "@lucide/svelte";
  import Stepper from "./Stepper.svelte";
  import IdentitySetup from "./owner/IdentitySetup.svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { EASY_STEPS } from "#lib/wizard/index.js";
  import { operationStepPlan } from "#lib/wizard/operation-steps.js";

  interface Props {
    deploymentId: string;
  }

  let { deploymentId }: Props = $props();

  const steps = EASY_STEPS;
  const inactiveReasons: Record<number, string> = {
    1: tr("wizard.settings.goalsElsewhere"),
    2: tr("wizard.settings.serverElsewhere"),
    3: tr("wizard.settings.accessUnsupported"),
  };
  const plan = operationStepPlan(steps, inactiveReasons);
  let step = $state(plan.first);
  const stepKey = $derived(steps.find((item) => item.id === step)?.key);
</script>

<div class="w-full min-w-0" data-testid="homelab-settings-wizard">
  <Stepper
    {steps}
    disabledStepReasons={inactiveReasons}
    currentStep={step}
    showDeploy={false}
    onprev={() => (step = plan.previous(step) ?? step)}
    onnext={() => (step = plan.next(step) ?? step)}
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
      <a class="mt-2 inline-flex items-center gap-1 text-primary" href="/stacks/new"
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
