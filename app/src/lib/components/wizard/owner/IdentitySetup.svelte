<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import {
    Fingerprint,
    Check,
    ArrowUpRight,
    RefreshCw,
    UsersRound,
    Copy,
  } from "@lucide/svelte";
  import {
    getHomelabIdentity,
    activateHomelabOwner,
    inviteHomelabMember,
    trustedIdentityActivationUrl,
    type HomelabIdentityStatus,
    type HouseholdPlannedPerson,
  } from "#lib/api/homelabIdentity.js";
  import { ownerUsernameFromEmail } from "#lib/wizard/session-owner.js";
  import { tr } from "#lib/i18n.svelte.js";
  let { deploymentId }: { deploymentId: string } = $props();
  let status = $state<HomelabIdentityStatus | null>(null);
  let loading = $state(true);
  let busy = $state(false);
  let error = $state("");
  let activationUrl = $state<string | null>(null);
  let invitations = $state<Record<string, string>>({});
  let prepared = $state<Record<string, boolean>>({});
  let copied = $state("");
  let timer: ReturnType<typeof setInterval> | undefined;
  let polls = 0;
  let destroyed = false;

  async function refresh() {
    if (busy || destroyed) return;
    loading = true;
    try {
      const next = await getHomelabIdentity(deploymentId);
      if (destroyed) return;
      status = next;
      error = "";
      if (next.owner.status === "active" || next.owner.status === "expired") {
        activationUrl = null;
        if (timer) clearInterval(timer);
      }
    } catch {
      if (!destroyed) error = tr("wizard.identity.statusError");
    } finally {
      if (!destroyed) loading = false;
    }
  }
  function observeActivation() {
    if (timer) clearInterval(timer);
    polls = 0;
    timer = setInterval(() => {
      if (++polls > 12 || destroyed) {
        clearInterval(timer);
        return;
      }
      if (document.visibilityState === "visible") void refresh();
    }, 10_000);
  }
  async function activate() {
    busy = true;
    error = "";
    activationUrl = null;
    // Open synchronously from the user gesture, so enrollment can continue
    // without replacing the wizard or depending on an unblocked async popup.
    const popup = window.open("about:blank", "_blank");
    if (popup) popup.opener = null;
    try {
      const result = await activateHomelabOwner(deploymentId);
      if (destroyed) {
        popup?.close();
        return;
      }
      if (status) {
        const { activation_url: _transient, ...owner } = result;
        status = { ...status, owner };
      }
      if (result.status === "active") {
        popup?.close();
        return;
      }
      const target = trustedIdentityActivationUrl(result);
      if (!target) {
        popup?.close();
        error = tr("wizard.identity.activationError");
        return;
      }
      activationUrl = target;
      if (popup) popup.location.replace(target);
      observeActivation();
    } catch {
      popup?.close();
      error = tr("wizard.identity.activationError");
    } finally {
      busy = false;
    }
  }
  async function invite(person: HouseholdPlannedPerson) {
    if (!person.email?.trim()) return;
    busy = true;
    error = "";
    try {
      const result = await inviteHomelabMember(deploymentId, {
        ...person,
        username: ownerUsernameFromEmail(person.email),
      });
      const target = trustedIdentityActivationUrl({
        ...result,
        origin: status?.owner.origin,
      });
      prepared = { ...prepared, [person.client_ref]: true };
      if (target) invitations = { ...invitations, [person.client_ref]: target };
      else if (result.status !== "active" && result.status !== "pending")
        error = tr("wizard.identity.invitationError");
    } catch {
      error = tr("wizard.identity.invitationError");
    } finally {
      busy = false;
    }
  }
  async function copyInvitation(person: HouseholdPlannedPerson) {
    try {
      await navigator.clipboard.writeText(invitations[person.client_ref]);
      copied = person.client_ref;
    } catch {
      error = tr("wizard.identity.copyError");
    }
  }
  onMount(() => {
    void refresh();
  });
  onDestroy(() => {
    destroyed = true;
    if (timer) clearInterval(timer);
    activationUrl = null;
    invitations = {};
  });
</script>

<section
  class="identity-setup"
  aria-labelledby="identity-setup-title"
  data-testid="homelab-identity-setup"
>
  <div class="identity-heading">
    <Fingerprint size={31} aria-hidden="true" />
    <div>
      <p class="eyebrow">{tr("wizard.identity.eyebrow")}</p>
      <h3 id="identity-setup-title">
        {tr(
          status?.owner.status === "active"
            ? "wizard.identity.ready"
            : "wizard.identity.title",
        )}
      </h3>
    </div>
  </div>
  <div aria-live="polite">
    {#if loading && !status}<p>{tr("wizard.identity.loading")}</p>
    {:else if status?.owner.status === "active"}<p class="ready">
        <Check size={17} aria-hidden="true" />{tr("wizard.identity.active")}
      </p>
    {:else}<p>
        {tr(
          status?.owner.status === "expired"
            ? "wizard.identity.expired"
            : status?.owner.status === "unavailable" ||
                status?.owner.status === "failed"
              ? "wizard.identity.unavailable"
              : "wizard.identity.pending",
        )}
      </p>{/if}
    {#if error}<p class="error" role="alert">{error}</p>{/if}
  </div>
  <div class="actions">
    {#if status && ["pending", "expired"].includes(status.owner.status)}
      <button type="button" class="primary" disabled={busy} onclick={activate}
        ><Fingerprint size={16} aria-hidden="true" />{tr(
          status.owner.status === "expired"
            ? "wizard.identity.reissue"
            : "wizard.identity.activate",
        )}<ArrowUpRight size={14} aria-hidden="true" /></button
      >
    {/if}
    {#if activationUrl}<a
        href={activationUrl}
        target="_blank"
        rel="noopener noreferrer"
        referrerpolicy="no-referrer"
        >{tr("wizard.identity.openActivation")}<ArrowUpRight
          size={14}
          aria-hidden="true"
        /></a
      >{/if}
    <button
      type="button"
      class="quiet"
      disabled={busy || loading}
      onclick={refresh}
      ><RefreshCw size={14} aria-hidden="true" />{tr(
        "wizard.identity.check",
      )}</button
    >
  </div>
  {#if status?.owner.origin}<p class="origin">
      {tr("wizard.identity.destination")} <span>{status.owner.origin}</span>
    </p>{/if}
  {#if status?.household.planned_people?.length}
    <section class="household">
      <h4>
        <UsersRound size={17} aria-hidden="true" />{tr(
          "wizard.identity.people",
        )}
      </h4>
      <p>
        {tr(
          status.owner.status === "active"
            ? "wizard.identity.peopleReady"
            : "wizard.identity.peopleWait",
        )}
      </p>
      {#each status.household.planned_people as person (person.client_ref)}
        <div class="person">
          <div>
            <strong>{person.name || person.email}</strong><span
              >{person.email || tr("wizard.identity.emailNeeded")}</span
            >
          </div>
          {#if invitations[person.client_ref]}<button
              type="button"
              class="quiet"
              onclick={() => copyInvitation(person)}
              ><Copy size={14} aria-hidden="true" />{tr(
                copied === person.client_ref
                  ? "wizard.identity.copied"
                  : "wizard.identity.copy",
              )}</button
            >
          {:else if prepared[person.client_ref] || status.household.members?.some((member) => member.email?.toLowerCase() === person.email?.toLowerCase())}<span
              >{tr("wizard.identity.memberReady")}</span
            >
          {:else}<button
              type="button"
              class="quiet"
              disabled={status.owner.status !== "active" ||
                busy ||
                !person.email}
              onclick={() => invite(person)}
              >{tr("wizard.identity.invite")}</button
            >{/if}
        </div>
      {/each}
    </section>
  {/if}
</section>

<style>
  .identity-setup {
    border-block: 1px solid var(--border);
    padding-block: 25px;
    margin-bottom: 25px;
  }
  .identity-heading {
    display: flex;
    align-items: center;
    gap: 14px;
    margin-bottom: 15px;
  }
  .identity-heading > :global(svg) {
    color: var(--primary);
  }
  .eyebrow {
    font-size: 10px;
    letter-spacing: 0.1em;
    text-transform: uppercase;
    color: var(--primary);
  }
  h3 {
    font-size: 22px;
    font-weight: 600;
    letter-spacing: -0.025em;
  }
  p,
  .person span {
    font-size: 13px;
    line-height: 1.7;
    color: var(--muted-foreground);
  }
  .actions,
  button,
  a,
  h4,
  .ready {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .actions {
    flex-wrap: wrap;
    gap: 12px;
    margin-top: 18px;
  }
  button,
  a {
    font-size: 12px;
    font-weight: 550;
    padding: 9px 12px;
    border-radius: var(--radius);
  }
  button:focus-visible,
  a:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
  }
  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .primary {
    background: var(--primary);
    color: var(--primary-foreground);
  }
  .quiet,
  a {
    color: var(--primary);
  }
  .origin {
    font-size: 11px;
    margin-top: 13px;
    overflow-wrap: anywhere;
  }
  .origin span {
    color: var(--foreground);
  }
  .error {
    color: var(--destructive);
    margin-top: 10px;
  }
  .ready {
    color: var(--success);
  }
  .household {
    border-top: 1px solid var(--border);
    padding-top: 22px;
    margin-top: 25px;
  }
  h4 {
    font-size: 15px;
    font-weight: 600;
    margin-bottom: 8px;
  }
  .person {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 10px;
    padding-block: 13px;
    border-bottom: 1px solid var(--border);
  }
  .person > div {
    display: grid;
    gap: 2px;
  }
  .person strong {
    font-size: 13px;
  }
</style>
