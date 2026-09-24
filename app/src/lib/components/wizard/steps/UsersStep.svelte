<script lang="ts">
  import {
    ChevronDown,
    Clock3,
    Plus,
    Trash2,
    UsersRound,
    Globe2,
    LockKeyhole,
  } from "@lucide/svelte";
  import HouseholdIllustration from "../HouseholdIllustration.svelte";
  import type {
    AudienceConfigKey,
    BundleQuestionDefinition,
    StackConfig,
  } from "#lib/wizard/index.js";
  import {
    createHouseholdPersonDraft,
    type HouseholdPersonDraft,
    type HouseholdProfile,
  } from "#lib/wizard/household-draft.js";
  import { tr } from "#lib/i18n.svelte.js";

  interface Props {
    config: StackConfig;
    audienceQuestions?: BundleQuestionDefinition<AudienceConfigKey>[];
    householdProfile?: HouseholdProfile;
    peopleDrafts?: HouseholdPersonDraft[];
    onAudienceChange?: (audience: AudienceConfigKey, value: boolean) => void;
    onHouseholdProfileChange?: (profile: HouseholdProfile) => void;
    onPeopleDraftsChange?: (people: HouseholdPersonDraft[]) => void;
  }

  let {
    config,
    householdProfile,
    peopleDrafts = [],
    onAudienceChange,
    onHouseholdProfileChange,
    onPeopleDraftsChange,
  }: Props = $props();
  let accessOpen = $state(false);
  let peopleOpen = $state(false);
  const profiles: HouseholdProfile[] = ["solo", "shared"];
  const selectedProfile = $derived(
    householdProfile ?? (config.audience.onlyMe ? "solo" : "shared"),
  );

  function selectProfile(profile: HouseholdProfile) {
    onHouseholdProfileChange?.(profile);
    onAudienceChange?.("onlyMe", profile === "solo");
    onAudienceChange?.("familyFriends", profile !== "solo");
  }
  function addPerson() {
    onPeopleDraftsChange?.([...peopleDrafts, createHouseholdPersonDraft()]);
  }
  function updatePerson(id: string, field: "name" | "email", value: string) {
    onPeopleDraftsChange?.(
      peopleDrafts.map((person) =>
        person.id === id ? { ...person, [field]: value } : person,
      ),
    );
  }
  function removePerson(id: string) {
    onPeopleDraftsChange?.(peopleDrafts.filter((person) => person.id !== id));
  }
</script>

<div class="step" data-testid="easy-step-4">
  <header class="heading">
    <p class="eyebrow">{tr("wizard.users.eyebrow")}</p>
    <h2>{tr("wizard.users.heading")}</h2>
    <p class="intro">{tr("wizard.users.intro")}</p>
  </header>

  <div class="household-layout">
    <fieldset class="profiles">
      <legend class="sr-only">{tr("wizard.users.profile.label")}</legend>
      {#each profiles as profile (profile)}
        <label
          class:selected={selectedProfile === profile}
          data-testid={`easy-users-${profile}`}
        >
          <input
            class="sr-only"
            type="radio"
            name="household-profile"
            value={profile}
            checked={selectedProfile === profile}
            onchange={() => selectProfile(profile)}
          />
          <span class="profile-dot" aria-hidden="true"><span></span></span>
          <span
            ><strong>{tr(`wizard.users.profile.${profile}`)}</strong><small
              >{tr(`wizard.users.profile.${profile}Hint`)}</small
            ></span
          >
        </label>
      {/each}
    </fieldset>
    <div class="household-visual">
      <HouseholdIllustration
        profile={selectedProfile}
        publicAccess={config.network.publicAccess}
      />
    </div>
  </div>

  <section class="publication" aria-labelledby="public-audience-heading">
    <div class="publication-heading">
      <Globe2 size={23} aria-hidden="true" />
      <div>
        <h3 id="public-audience-heading">{tr("wizard.users.public.title")}</h3>
        <p>{tr("wizard.users.public.body")}</p>
      </div>
    </div>
    <label class="publication-toggle"
      ><input
        type="checkbox"
        checked={config.network.publicAccess}
        onchange={(event) =>
          onAudienceChange?.("public", event.currentTarget.checked)}
      /><span>{tr("wizard.users.public.choice")}</span></label
    >
    {#if config.network.publicAccess}
      <div class="publication-consequence" role="status">
        <strong>{tr("wizard.users.public.consequence")}</strong>
        <p>{tr("wizard.users.public.pending")}</p>
      </div>
    {:else}<p class="privacy-note">
        <LockKeyhole size={15} aria-hidden="true" />{tr(
          "wizard.users.public.private",
        )}
      </p>{/if}
  </section>

  {#if selectedProfile !== "solo"}
    <section class="shared-access">
      <button
        type="button"
        aria-expanded={peopleOpen}
        aria-controls="people-drafts"
        onclick={() => (peopleOpen = !peopleOpen)}
        ><UsersRound size={16} aria-hidden="true" /><span
          >{tr("wizard.users.people.prepare")}</span
        ><ChevronDown
          size={16}
          class={peopleOpen ? "rotated" : ""}
          aria-hidden="true"
        /></button
      >
      <p>{tr("wizard.users.people.laterBody")}</p>
    </section>
    {#if peopleOpen}
      <section class="people" id="people-drafts" aria-labelledby="people-title">
        <div class="section-heading">
          <div>
            <h3 id="people-title">{tr("wizard.users.people.title")}</h3>
            <p>{tr("wizard.users.people.body")}</p>
          </div>
          <button type="button" class="add-person" onclick={addPerson}
            ><Plus size={15} aria-hidden="true" />{tr(
              "wizard.users.people.add",
            )}</button
          >
        </div>

        <div class="person-list">
          <div class="person-row owner-row">
            <span class="person-avatar"
              ><UsersRound size={16} aria-hidden="true" /></span
            >
            <span class="person-identity"
              ><strong
                >{config.owner.displayName ||
                  tr("wizard.users.people.owner")}</strong
              ><small
                >{config.owner.email ||
                  tr("wizard.users.people.ownerFixed")}</small
              ></span
            >
            <span class="role">{tr("wizard.users.people.ownerFixed")}</span>
          </div>
          {#each peopleDrafts as person, index (person.id)}
            <div class="person-row draft-row">
              <span class="person-avatar" aria-hidden="true">{index + 1}</span>
              <div class="draft-fields">
                <label
                  ><span class="sr-only">{tr("wizard.users.people.name")}</span
                  ><input
                    type="text"
                    value={person.name}
                    placeholder={tr("wizard.users.people.namePlaceholder")}
                    oninput={(event) =>
                      updatePerson(
                        person.id,
                        "name",
                        event.currentTarget.value,
                      )}
                  /></label
                >
                <label
                  ><span class="sr-only">{tr("wizard.users.people.email")}</span
                  ><input
                    type="email"
                    value={person.email}
                    placeholder={tr("wizard.users.people.emailPlaceholder")}
                    oninput={(event) =>
                      updatePerson(
                        person.id,
                        "email",
                        event.currentTarget.value,
                      )}
                  /></label
                >
              </div>
              <span class="role">{tr("wizard.users.people.member")}</span>
              <button
                type="button"
                class="remove-person"
                aria-label={tr("wizard.users.people.remove")}
                title={tr("wizard.users.people.remove")}
                onclick={() => removePerson(person.id)}
                ><Trash2 size={15} aria-hidden="true" /></button
              >
            </div>
          {/each}
        </div>

        <div class="invite-later">
          <Clock3 size={16} aria-hidden="true" /><span
            ><strong>{tr("wizard.users.people.later")}</strong>{tr(
              "wizard.users.people.laterBody",
            )}</span
          >
        </div>
      </section>
    {/if}

    <section class="shared-access">
      <button
        type="button"
        aria-expanded={accessOpen}
        aria-controls="shared-access-details"
        onclick={() => (accessOpen = !accessOpen)}
      >
        <UsersRound size={16} aria-hidden="true" /><span
          >{tr("wizard.users.access.title")}</span
        ><ChevronDown
          class={accessOpen ? "rotated" : ""}
          size={16}
          aria-hidden="true"
        />
      </button>
      {#if accessOpen}<p id="shared-access-details">
          {tr("wizard.users.access.body")}
        </p>{/if}
    </section>
  {/if}
</div>

<style>
  .publication {
    border-block: 1px solid var(--border);
    padding-block: 24px;
  }
  .publication-heading {
    display: flex;
    gap: 14px;
    align-items: start;
  }
  .publication-heading :global(svg) {
    flex: none;
    color: var(--primary);
  }
  .publication h3 {
    font-size: 18px;
    font-weight: 600;
  }
  .publication p,
  .shared-access > p {
    color: var(--muted-foreground);
    font-size: 13px;
    line-height: 1.7;
    margin-top: 7px;
    max-width: 68ch;
  }
  .publication-toggle {
    display: flex;
    align-items: center;
    gap: 12px;
    margin-top: 20px;
    font-size: 14px;
    cursor: pointer;
  }
  .publication input {
    accent-color: var(--primary);
    width: 18px;
    height: 18px;
  }
  .publication-consequence {
    border-left: 2px solid var(--warning);
    padding-left: 15px;
    margin-top: 20px;
  }
  .publication-consequence strong {
    font-size: 13px;
  }
  .privacy-note {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .step {
    display: grid;
    gap: 32px;
  }
  .heading {
    padding-block: 14px 2px;
  }
  .eyebrow {
    margin-bottom: 9px;
    color: var(--primary);
    font-size: 10px;
    font-weight: 650;
    letter-spacing: 0.12em;
    text-transform: uppercase;
  }
  h2 {
    font-size: clamp(27px, 3vw, 36px);
    font-weight: 600;
    letter-spacing: -0.035em;
    line-height: 1.15;
    text-wrap: balance;
  }
  .intro {
    max-width: 65ch;
    margin-top: 14px;
    color: var(--muted-foreground);
    font-size: 14px;
    line-height: 1.75;
  }
  .household-layout {
    display: grid;
    gap: 26px;
    align-items: center;
  }
  .profiles {
    display: grid;
    gap: 0;
    padding: 0;
    overflow: hidden;
    border: 1px solid var(--border);
    border-radius: 14px;
    background: var(--card);
  }
  .profiles label {
    position: relative;
    display: grid;
    grid-template-columns: 18px minmax(0, 1fr);
    gap: 12px;
    align-items: center;
    min-height: 70px;
    padding: 13px 18px;
    border-bottom: 1px solid var(--border);
    cursor: pointer;
    transition: background 150ms ease;
  }
  .profiles label:last-child {
    border-bottom: 0;
  }
  .profiles label:hover {
    background: color-mix(in oklch, var(--primary) 3%, var(--card));
  }
  .profiles label:focus-within {
    z-index: 1;
    outline: 2px solid var(--ring);
    outline-offset: -3px;
  }
  .profiles label.selected {
    background: color-mix(in oklch, var(--primary) 6%, var(--card));
  }
  .profile-dot {
    width: 17px;
    height: 17px;
    padding: 4px;
    border: 1.5px solid var(--border);
    border-radius: 999px;
  }
  .selected .profile-dot {
    border-color: var(--primary);
  }
  .selected .profile-dot span {
    display: block;
    width: 100%;
    height: 100%;
    border-radius: inherit;
    background: var(--primary);
  }
  .profiles strong,
  .profiles small {
    display: block;
  }
  .profiles strong {
    font-size: 13px;
    font-weight: 620;
  }
  .profiles small {
    margin-top: 3px;
    color: var(--muted-foreground);
    font-size: 11px;
    line-height: 1.45;
  }
  .household-visual {
    max-width: 360px;
    justify-self: center;
    opacity: 0.9;
  }
  .people {
    padding-block: 6px 2px;
  }
  .section-heading {
    display: flex;
    align-items: start;
    justify-content: space-between;
    gap: 24px;
  }
  .section-heading h3 {
    font-size: 16px;
    font-weight: 620;
  }
  .section-heading p {
    max-width: 58ch;
    margin-top: 6px;
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.6;
  }
  .add-person {
    display: inline-flex;
    flex: none;
    align-items: center;
    gap: 7px;
    min-height: 36px;
    padding: 7px 12px;
    border: 1px solid var(--border);
    border-radius: 10px;
    color: var(--foreground);
    background: var(--card);
    font-size: 12px;
    font-weight: 600;
    cursor: pointer;
  }
  .add-person:hover {
    border-color: color-mix(in oklch, var(--primary) 45%, var(--border));
  }
  .add-person:focus-visible,
  .remove-person:focus-visible,
  .shared-access button:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 3px;
  }
  .person-list {
    margin-top: 20px;
    border-block: 1px solid var(--border);
  }
  .person-row {
    display: grid;
    grid-template-columns: 34px minmax(0, 1fr) auto;
    gap: 12px;
    align-items: center;
    min-height: 64px;
    padding: 10px 4px;
    border-bottom: 1px solid var(--border);
  }
  .person-row:last-child {
    border-bottom: 0;
  }
  .person-avatar {
    display: grid;
    width: 32px;
    height: 32px;
    place-items: center;
    border: 1px solid color-mix(in oklch, var(--primary) 20%, var(--border));
    border-radius: 999px;
    color: var(--primary);
    background: color-mix(in oklch, var(--primary) 5%, var(--card));
    font-size: 11px;
    font-weight: 650;
  }
  .person-identity strong,
  .person-identity small {
    display: block;
  }
  .person-identity strong {
    font-size: 13px;
    font-weight: 600;
  }
  .person-identity small {
    margin-top: 2px;
    color: var(--muted-foreground);
    font-size: 11px;
  }
  .role {
    color: var(--muted-foreground);
    font-size: 11px;
    white-space: nowrap;
  }
  .draft-row {
    grid-template-columns: 34px minmax(0, 1fr) auto 32px;
  }
  .draft-fields {
    display: grid;
    grid-template-columns: minmax(120px, 0.8fr) minmax(170px, 1.2fr);
    gap: 8px;
  }
  .draft-fields input {
    width: 100%;
    min-height: 36px;
    padding: 7px 10px;
    border: 1px solid var(--border);
    border-radius: 8px;
    color: var(--foreground);
    background: var(--input);
    font-size: 12px;
  }
  .draft-fields input:focus {
    border-color: var(--ring);
    outline: 2px solid color-mix(in oklch, var(--ring) 22%, transparent);
  }
  .draft-fields input::placeholder {
    color: var(--muted-foreground);
    opacity: 0.8;
  }
  .remove-person {
    display: grid;
    width: 30px;
    height: 30px;
    place-items: center;
    border-radius: 8px;
    color: var(--muted-foreground);
    cursor: pointer;
  }
  .remove-person:hover {
    color: var(--destructive);
    background: color-mix(in oklch, var(--destructive) 8%, transparent);
  }
  .invite-later {
    display: flex;
    gap: 10px;
    align-items: start;
    margin-top: 16px;
    color: var(--muted-foreground);
    font-size: 11px;
    line-height: 1.55;
  }
  .invite-later :global(svg) {
    flex: none;
    margin-top: 1px;
    color: var(--primary);
  }
  .invite-later strong {
    display: block;
    margin-bottom: 1px;
    color: var(--foreground);
    font-size: 12px;
  }
  .shared-access {
    border-top: 1px solid var(--border);
    padding-top: 18px;
  }
  .shared-access button {
    display: flex;
    align-items: center;
    gap: 9px;
    color: var(--muted-foreground);
    font-size: 12px;
    cursor: pointer;
  }
  .shared-access button :global(svg:last-child) {
    transition: transform 160ms ease;
  }
  .shared-access button :global(svg.rotated) {
    transform: rotate(180deg);
  }
  .shared-access p {
    max-width: 68ch;
    padding: 16px 0 2px 25px;
    color: var(--muted-foreground);
    font-size: 12px;
    line-height: 1.65;
  }
  @media (min-width: 760px) {
    .household-layout {
      grid-template-columns: minmax(300px, 0.9fr) minmax(260px, 1.1fr);
    }
  }
  @media (max-width: 640px) {
    .section-heading {
      display: grid;
    }
    .add-person {
      justify-self: start;
    }
    .draft-row {
      grid-template-columns: 34px minmax(0, 1fr) 32px;
    }
    .draft-fields {
      grid-template-columns: 1fr;
    }
    .draft-row .role {
      display: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .profiles label,
    .shared-access button :global(svg:last-child) {
      transition: none;
    }
  }
</style>
