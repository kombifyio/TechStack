<script lang="ts">
  /**
   * The kombify Wallet in Techstack: every item the signed-in person keeps in
   * their Wallet, drawn as Brand entity cards, with add, new version, reveal,
   * revoke and delete. A card opens the one detail sheet of the card family,
   * deep-linked as `?item=<id>`. Values are encrypted and decrypted in this
   * browser (`../transport.ts`); the Gateway relays only JWEs. An SSH
   * certificate authority is never revealed or versioned: it shows its public
   * key and signs certificates inside its sign policy. The owner also decides
   * here what Techstack may do with an item (the `use` right) and whether it
   * may save credentials in the Wallet.
   *
   * `#access` and `#recovery` (the stack handoff links) preset the type
   * filter to the items each of them means.
   */
  import { onDestroy, onMount, untrack } from "svelte";
  import { goto, replaceState } from "$app/navigation";
  import { page } from "$app/state";
  import { Select } from "@kombiverselabs/ui/primitives";
  import {
    DEFAULT_ENTITY_LABELS,
    EntityCard,
    EntityCollection,
    EntityDetailSheet,
    type EntityAction,
    type EntityCardSize,
    type EntityFact,
    type EntityFactGroup,
    type EntityIcon,
    type EntityLabels,
  } from "@kombiverselabs/ui/entity";
  import {
    Ban,
    Bot,
    Eye,
    FileCog,
    Key,
    KeySquare,
    LockKeyhole,
    RefreshCw,
    ServerCog,
    Settings2,
    ShieldCheck,
    Signature,
    Trash2,
    UserRound,
    Variable,
  } from "@lucide/svelte";
  import { tr, getLocale } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { confirmInApp } from "#lib/dialogs/in-app-dialog.js";
  import { authHandler } from "#lib/stores/authHandler.svelte.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { listKitDeployments } from "#lib/api/stacks.js";
  import { canAddVersion, EDITOR_ITEM_TYPES } from "../item-types.js";
  import { isSshCa, itemCaPublicKey, itemSignPolicy } from "../ssh-ca.js";
  import {
    disclosuresSinceRotation,
    itemValueAccess,
  } from "../item-settings.js";
  import {
    deleteItem,
    listItems,
    listServiceAccess,
    listServiceCreators,
    ownerSubject,
    revokeItem,
    setServiceAccess,
    setServiceCreator,
    WalletApiError,
    type WalletItem,
  } from "../wallet-api.js";
  import { TECHSTACK_SERVICE, techstackMayUse } from "../service-access.js";
  import {
    itemStateLabel,
    itemTypeLabel,
    scopeLabel,
    walletErrorMessage,
  } from "../wallet-text.js";
  import WalletItemDialog, {
    type WalletItemDialogResult,
  } from "./WalletItemDialog.svelte";
  import WalletRevealDialog from "./WalletRevealDialog.svelte";
  import WalletSettingsDialog from "./WalletSettingsDialog.svelte";
  import WalletSshCaKey from "./WalletSshCaKey.svelte";
  import WalletSshCertificateDialog from "./WalletSshCertificateDialog.svelte";
  import WalletImportDialog from "./WalletImportDialog.svelte";
  import "./wallet.css";

  interface Props {
    /** List and create only inside this scope (a stack's credentials). */
    fixedScope?: string;
    /** Items created here are made available to Techstack at once. */
    allowTechstackOnCreate?: boolean;
    /** Card size of the list. */
    view?: EntityCardSize;
  }

  let {
    fixedScope,
    allowTechstackOnCreate = false,
    view = "row",
  }: Props = $props();

  type Grant = "loading" | "on" | "off" | "error";
  type Preset = "access" | "recovery";

  /**
   * What the stack handoff links mean here. Access: what lets people and
   * Techstack into a stack's servers and apps. Recovery: the secrets and
   * configurations recovery material and break-glass credentials are kept in.
   */
  const PRESETS: Record<Preset, readonly string[]> = {
    access: ["login", "node_access_profile", "ssh_ca", "parent_credential"],
    recovery: ["generic_secret", "configuration"],
  };
  const PRESET_PREFIX = "@";

  const TYPE_ICONS: Record<string, EntityIcon> = {
    generic_secret: LockKeyhole,
    login: UserRound,
    variable: Variable,
    configuration: FileCog,
    node_access_profile: ServerCog,
    parent_credential: KeySquare,
    ssh_ca: ShieldCheck,
    ai_credential: Bot,
  };

  let items = $state<WalletItem[]>([]);
  let loadState = $state<"loading" | "ready" | "error">("loading");
  let loadError = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let noticeWarning = $state<string | null>(null);
  let actionError = $state<string | null>(null);
  let owner = $state<string | null>(null);
  let ownerChecked = $state(false);
  let creating = $state(false);
  let generatingKey = $state(false);
  let importing = $state(false);
  let confirming = $state(false);
  let versionOf = $state<WalletItem | null>(null);
  let revealOf = $state<WalletItem | null>(null);
  let settingsOf = $state<WalletItem | null>(null);
  let signOf = $state<WalletItem | null>(null);
  let stackNames = $state<Map<string, string>>(new Map());

  let search = $state("");
  let typeFilter = $state("all");
  let scopeFilter = $state("all");

  let grants = $state<Record<string, Grant>>({});
  let creatorState = $state<"loading" | "on" | "off" | "error">("loading");
  let creatorBusy = $state(false);

  const canUseValues = $derived(Boolean(owner));
  let destroyed = false;

  async function load() {
    loadState = items.length ? loadState : "loading";
    loadError = null;
    try {
      items = await listItems();
      loadState = "ready";
    } catch (error) {
      if (
        error instanceof WalletApiError &&
        error.status === 401 &&
        (await authHandler.handleUnauthorized(
          () => load(),
          undefined,
          error,
        )) !== "reauth_required"
      )
        return;
      loadError = walletErrorMessage(error);
      loadState = "error";
    }
  }

  async function loadOwner() {
    try {
      owner = await ownerSubject();
    } catch {
      owner = null;
    } finally {
      ownerChecked = true;
    }
  }

  async function loadStackNames() {
    try {
      const stacks = await listKitDeployments();
      stackNames = new Map(
        stacks.flatMap((stack) =>
          [stack.kit_deployment_id, stack.id]
            .filter((id): id is string => Boolean(id))
            .map((id) => [id, stack.name] as const),
        ),
      );
    } catch {
      /* the Wallet's own wording is shown instead */
    }
  }

  async function loadCreator() {
    creatorState = "loading";
    try {
      creatorState = (await listServiceCreators()).includes(TECHSTACK_SERVICE)
        ? "on"
        : "off";
    } catch {
      creatorState = "error";
    }
  }

  /** Techstack's `use` right per visible active item, a few requests at a time. */
  async function loadGrants() {
    const wanted = visible.filter(
      (item) => item.state === "active" && grants[item.itemId] === undefined,
    );
    for (const item of wanted) grants[item.itemId] = "loading";
    const queue = [...wanted];
    const worker = async () => {
      for (let item = queue.shift(); item; item = queue.shift()) {
        try {
          grants[item.itemId] = techstackMayUse(
            await listServiceAccess(item.itemId),
          )
            ? "on"
            : "off";
        } catch {
          grants[item.itemId] = "error";
        }
        if (destroyed) return;
      }
    };
    await Promise.all(Array.from({ length: 4 }, worker));
  }

  async function toggleGrant(item: WalletItem, on: boolean) {
    const before = grants[item.itemId];
    grants[item.itemId] = "loading";
    actionError = null;
    try {
      const held = await setServiceAccess(
        item.itemId,
        TECHSTACK_SERVICE,
        "use",
        on,
      );
      grants[item.itemId] = held ? "on" : "off";
    } catch (error) {
      grants[item.itemId] = before ?? "error";
      actionError = walletErrorMessage(error);
    }
  }

  async function toggleCreator(on: boolean) {
    if (creatorBusy) return;
    creatorBusy = true;
    actionError = null;
    try {
      creatorState = (await setServiceCreator(TECHSTACK_SERVICE, on))
        ? "on"
        : "off";
    } catch (error) {
      actionError = walletErrorMessage(error);
    } finally {
      creatorBusy = false;
    }
  }

  // --- the handoff presets, kept in the URL hash ------------------------------
  function presetOf(hash: string): Preset | null {
    const name = hash.replace(/^#/, "");
    return name === "access" || name === "recovery" ? name : null;
  }

  function syncPresetFromHash() {
    if (fixedScope) return;
    const preset = presetOf(window.location.hash);
    if (preset) typeFilter = `${PRESET_PREFIX}${preset}`;
    else if (typeFilter.startsWith(PRESET_PREFIX)) typeFilter = "all";
  }

  function setTypeFilter(value: string) {
    typeFilter = value;
    if (fixedScope) return;
    const url = new URL(page.url.href);
    const hash = value.startsWith(PRESET_PREFIX)
      ? value.slice(PRESET_PREFIX.length)
      : "";
    if (url.hash.replace(/^#/, "") === hash) return;
    url.hash = hash;
    // Before the router is ready the filter still applies; only the URL lags.
    void replaceState(url, page.state).catch(() => undefined);
  }

  onMount(() => {
    void load();
    void loadOwner();
    if (!fixedScope) {
      syncPresetFromHash();
      window.addEventListener("hashchange", syncPresetFromHash);
      void loadStackNames();
      void loadCreator();
    }
  });
  onDestroy(() => {
    destroyed = true;
    if (typeof window !== "undefined")
      window.removeEventListener("hashchange", syncPresetFromHash);
  });

  const inScope = $derived(
    fixedScope ? items.filter((item) => item.scope === fixedScope) : items,
  );
  const typesPresent = $derived([
    ...new Set([...EDITOR_ITEM_TYPES, ...inScope.map((item) => item.itemType)]),
  ]);
  const scopesPresent = $derived([
    ...new Set(inScope.map((item) => item.scope)),
  ]);
  const activePreset = $derived<Preset | null>(
    typeFilter.startsWith(PRESET_PREFIX)
      ? (typeFilter.slice(PRESET_PREFIX.length) as Preset)
      : null,
  );
  const typeOptions = $derived([
    { value: "all", label: tr("kwallet.filter.all") },
    ...(fixedScope
      ? []
      : (Object.keys(PRESETS) as Preset[]).map((preset) => ({
          value: `${PRESET_PREFIX}${preset}`,
          label: tr(`kwallet.preset.${preset}`),
        }))),
    ...typesPresent.map((type) => ({ value: type, label: itemTypeLabel(type) })),
  ]);
  const scopeOptions = $derived([
    { value: "all", label: tr("kwallet.filter.all") },
    ...scopesPresent.map((scope) => ({
      value: scope,
      label: scopeLabel(scope, stackNames),
    })),
  ]);
  const visible = $derived(
    inScope.filter((item) => {
      if (activePreset) {
        if (!PRESETS[activePreset].includes(item.itemType)) return false;
      } else if (typeFilter !== "all" && item.itemType !== typeFilter)
        return false;
      if (scopeFilter !== "all" && item.scope !== scopeFilter) return false;
      const query = search.trim().toLowerCase();
      if (!query) return true;
      return [
        item.displayName,
        item.description,
        item.name,
        scopeLabel(item.scope, stackNames),
      ]
        .filter((text): text is string => Boolean(text))
        .some((text) => text.toLowerCase().includes(query));
    }),
  );

  $effect(() => {
    // Newly visible items (a filter changed) need their Techstack state too.
    void visible;
    if (loadState === "ready") untrack(() => void loadGrants());
  });

  // --- selection, deep-linked as ?item= ---------------------------------------
  const selectedId = $derived(page.url.searchParams.get("item"));
  const selected = $derived(
    inScope.find((item) => item.itemId === selectedId) ?? null,
  );
  const selectedMissing = $derived(
    Boolean(selectedId) && loadState === "ready" && !selected,
  );
  /** A Wallet dialog is a modal of its own; the sheet steps aside meanwhile. */
  const dialogOpen = $derived(
    Boolean(
      creating ||
        generatingKey ||
        importing ||
        confirming ||
        versionOf ||
        revealOf ||
        settingsOf ||
        signOf,
    ),
  );

  function select(itemId: string | null) {
    const url = new URL(page.url.href);
    url.searchParams.delete("item");
    if (itemId) url.searchParams.set("item", itemId);
    void goto(`${url.pathname}${url.search}${url.hash}`, { reset: false });
  }

  function announce(
    text: string,
    pending: boolean,
    warning: string | null = null,
  ) {
    notice = text;
    noticeWarning = pending ? tr("kwallet.notice.audit_pending") : warning;
    actionError = null;
  }

  function onItemSaved(result: WalletItemDialogResult) {
    creating = false;
    generatingKey = false;
    versionOf = null;
    announce(
      result.kind === "created"
        ? tr("kwallet.notice.created", { name: result.name })
        : tr("kwallet.notice.versioned", { name: result.name }),
      result.auditPending,
      result.techstackAccess === "failed"
        ? tr("kwallet.techstack.grant_failed")
        : null,
    );
    void load();
  }

  function onSettingsSaved(result: { name: string; auditPending: boolean }) {
    settingsOf = null;
    announce(
      tr("kwallet.notice.configured", { name: result.name }),
      result.auditPending,
    );
    void load();
  }

  async function runConfirmed(kind: "revoke" | "delete", item: WalletItem) {
    confirming = true;
    let confirmed = false;
    try {
      confirmed = await confirmInApp({
        title: tr(
          kind === "revoke" ? "kwallet.revoke.title" : "kwallet.delete.title",
          { name: item.displayName },
        ),
        message: tr(
          kind === "revoke" ? "kwallet.revoke.body" : "kwallet.delete.body",
        ),
        confirmText: tr(
          kind === "revoke"
            ? "kwallet.revoke.confirm"
            : "kwallet.delete.confirm",
        ),
        tone: "danger",
      });
    } finally {
      confirming = false;
    }
    if (!confirmed) return;
    try {
      const result =
        kind === "revoke"
          ? await revokeItem(item.itemId)
          : await deleteItem(item.itemId);
      announce(
        tr(
          kind === "revoke"
            ? "kwallet.notice.revoked"
            : "kwallet.notice.deleted",
          { name: item.displayName },
        ),
        result.auditPending,
      );
      if (kind === "delete" && selectedId === item.itemId) select(null);
      void load();
    } catch (error) {
      actionError = walletErrorMessage(error);
    }
  }

  // --- card and sheet content -------------------------------------------------
  function formatInstant(value: unknown): string {
    const at = Number(value);
    if (!Number.isFinite(at) || at <= 0) return "";
    return new Intl.DateTimeFormat(getLocale(), {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(at));
  }

  /** The Wallet records release times as epoch milliseconds or ISO text. */
  function lastReleasedAt(item: WalletItem): string {
    const disclosure = item.disclosure;
    if (!disclosure || typeof disclosure !== "object") return "";
    const at = (disclosure as { lastAt?: unknown }).lastAt;
    if (typeof at === "string" && at && !/^\d+$/.test(at))
      return formatInstant(Date.parse(at));
    return formatInstant(at);
  }

  function useTargetHosts(item: WalletItem): string[] {
    if (!Array.isArray(item.useTargets)) return [];
    return item.useTargets
      .map((target) =>
        target && typeof target === "object"
          ? (target as { host?: unknown }).host
          : undefined,
      )
      .filter((host): host is string => typeof host === "string" && !!host);
  }

  function itemIcon(item: WalletItem): EntityIcon {
    return TYPE_ICONS[item.itemType] ?? Key;
  }

  function itemAccent(item: WalletItem) {
    if (item.state === "active") return "primary" as const;
    if (item.state === "revoked") return "warning" as const;
    return "muted" as const;
  }

  function itemMeta(item: WalletItem): string {
    return [
      item.name ?? item.description,
      fixedScope ? undefined : scopeLabel(item.scope, stackNames),
    ]
      .filter(Boolean)
      .join(" · ");
  }

  function itemTags(item: WalletItem): string[] {
    const active = item.state === "active";
    return [
      itemValueAccess(item) === "use_only" && tr("kwallet.badge.use_only"),
      active && grants[item.itemId] === "on" && tr("kwallet.col.techstack"),
      active && disclosuresSinceRotation(item) > 0 && tr("kwallet.tag.rotate"),
      active &&
        isSshCa(item.itemType) &&
        !itemSignPolicy(item) &&
        tr("kwallet.tag.no_policy"),
    ].filter((tag): tag is string => Boolean(tag));
  }

  function cardFacts(item: WalletItem): EntityFact[] {
    return [
      { label: tr("kwallet.col.type"), value: itemTypeLabel(item.itemType) },
      !fixedScope && {
        label: tr("kwallet.col.scope"),
        value: scopeLabel(item.scope, stackNames),
      },
      item.currentVersion && {
        label: tr("kwallet.fact.version"),
        value: item.currentVersion,
      },
      formatInstant(item.updatedAt) && {
        label: tr("kwallet.col.updated"),
        value: formatInstant(item.updatedAt),
      },
    ].filter((fact): fact is EntityFact => Boolean(fact));
  }

  function detailFacts(item: WalletItem): EntityFactGroup[] {
    const hosts = useTargetHosts(item);
    const policy = isSshCa(item.itemType) ? itemSignPolicy(item) : null;
    const releases = disclosuresSinceRotation(item);
    const groups: EntityFactGroup[] = [
      {
        title: tr("kwallet.detail.item"),
        facts: [
          {
            label: tr("kwallet.col.type"),
            value: itemTypeLabel(item.itemType),
          },
          {
            label: tr("kwallet.col.scope"),
            value: scopeLabel(item.scope, stackNames),
          },
          { label: tr("kwallet.col.state"), value: itemStateLabel(item.state) },
          item.name && {
            label: tr("kwallet.field.variable_name"),
            value: item.name,
          },
          item.currentVersion && {
            label: tr("kwallet.fact.version"),
            value: item.currentVersion,
          },
        ].filter((fact): fact is EntityFact => Boolean(fact)),
      },
      {
        title: tr("kwallet.detail.use"),
        facts: [
          {
            label: tr("kwallet.field.value_access"),
            value: tr(`kwallet.value_access.${itemValueAccess(item)}`),
          },
          ...(isSshCa(item.itemType)
            ? []
            : [
                {
                  label: tr("kwallet.fact.use_targets"),
                  value: hosts.length
                    ? hosts.join(", ")
                    : tr("kwallet.fact.none"),
                },
              ]),
        ],
      },
    ];
    if (policy)
      groups.push({
        title: tr("kwallet.ssh_ca.policy.legend"),
        facts: [
          {
            label: tr("kwallet.ssh_ca.policy.kinds"),
            value: policy.kinds
              .map((kind) => tr(`kwallet.ssh_ca.policy.kind_${kind}`))
              .join(", "),
          },
          {
            label: tr("kwallet.ssh_ca.policy.principals"),
            value: policy.principals.join(", "),
          },
        ],
      });
    groups.push({
      title: tr("kwallet.detail.history"),
      facts: [
        formatInstant(item.createdAt) && {
          label: tr("kwallet.fact.created"),
          value: formatInstant(item.createdAt),
        },
        formatInstant(item.updatedAt) && {
          label: tr("kwallet.col.updated"),
          value: formatInstant(item.updatedAt),
        },
        lastReleasedAt(item) && {
          label: tr("kwallet.fact.last_release"),
          value: lastReleasedAt(item),
        },
        releases > 0 && {
          label: tr("kwallet.fact.releases"),
          value: String(releases),
        },
      ].filter((fact): fact is EntityFact => Boolean(fact)),
    });
    return groups.filter((group) => group.facts.length > 0);
  }

  function openDialog(dialog: "reveal" | "version" | "settings" | "sign", item: WalletItem) {
    actionError = null;
    if (dialog === "reveal") revealOf = item;
    else if (dialog === "version") versionOf = item;
    else if (dialog === "settings") settingsOf = item;
    else signOf = item;
  }

  /** Owner actions on an item; `named` words them for a card's icon buttons. */
  function itemActions(item: WalletItem, named: boolean): EntityAction[] {
    const active = item.state === "active";
    const sshCa = isSshCa(item.itemType);
    const useOnly = itemValueAccess(item) === "use_only";
    const word = (key: string) =>
      named ? tr(`${key}_named`, { name: item.displayName }) : tr(key);
    const ownerOnly = canUseValues
      ? {}
      : {
          state: "disabled" as const,
          disabledReason: tr("kwallet.denied.owner_unknown"),
        };
    const actions: EntityAction[] = [];
    if (active && sshCa)
      actions.push({
        id: "sign",
        label: word("kwallet.action.sign"),
        icon: Signature,
        onSelect: () => openDialog("sign", item),
      });
    else if (active && !useOnly)
      actions.push({
        id: "reveal",
        label: word("kwallet.action.reveal"),
        icon: Eye,
        onSelect: () => openDialog("reveal", item),
        ...ownerOnly,
      });
    if (active)
      actions.push({
        id: "settings",
        label: word("kwallet.action.settings"),
        icon: Settings2,
        onSelect: () => openDialog("settings", item),
      });
    if (active && canAddVersion(item.itemType))
      actions.push({
        id: "version",
        label: word("kwallet.action.new_version"),
        icon: RefreshCw,
        onSelect: () => openDialog("version", item),
        ...ownerOnly,
      });
    if (named) return actions;
    if (active)
      actions.push({
        id: "revoke",
        label: tr("kwallet.action.revoke"),
        icon: Ban,
        onSelect: () => void runConfirmed("revoke", item),
      });
    actions.push({
      id: "delete",
      label: tr("kwallet.action.delete"),
      icon: Trash2,
      onSelect: () => void runConfirmed("delete", item),
    });
    return actions;
  }

  /** The item's type names the card's kind, so the tile reads "Login", not "Device". */
  function itemLabels(item: WalletItem): Partial<EntityLabels> {
    return {
      ...entityLabels,
      kind: { ...DEFAULT_ENTITY_LABELS.kind, device: itemTypeLabel(item.itemType) },
    };
  }

  const entityLabels = $derived<Partial<EntityLabels>>({
    views: {
      ...DEFAULT_ENTITY_LABELS.views,
      label: tr("kwallet.view.label"),
      mark: tr("kwallet.view.mark"),
      tile: tr("kwallet.view.tile"),
      row: tr("kwallet.view.row"),
      card: tr("kwallet.view.card"),
    },
    detail: { ...DEFAULT_ENTITY_LABELS.detail, facts: tr("kwallet.detail.facts") },
  });

  const cloudWalletUrl = $derived.by(() => {
    try {
      const origin = authStore.portalUrl
        ? new URL(authStore.portalUrl).origin
        : "https://kombify.io";
      return `${origin}/dashboard/settings#wallet`;
    } catch {
      return "https://kombify.io/dashboard/settings#wallet";
    }
  });
</script>

<div class="flex flex-col gap-5" data-testid="kombify-wallet-panel">
  <div class="flex flex-wrap items-center gap-3">
    <Button
      variant="primary"
      size="sm"
      disabled={!canUseValues}
      testId="wallet-add"
      onclick={() => {
        actionError = null;
        creating = true;
      }}
    >
      {tr("kwallet.add")}
    </Button>
    {#if !fixedScope}
      <Button
        variant="secondary"
        size="sm"
        disabled={!canUseValues}
        testId="wallet-generate"
        onclick={() => {
          actionError = null;
          generatingKey = true;
        }}
      >
        {tr("kwallet.keygen.open")}
      </Button>
      <Button
        variant="secondary"
        size="sm"
        disabled={!canUseValues}
        testId="wallet-import"
        onclick={() => (importing = true)}
      >
        {tr("kwallet.import.open")}
      </Button>
      <a
        class="text-primary text-sm underline"
        href={cloudWalletUrl}
        target="_blank"
        rel="noopener noreferrer"
        data-testid="wallet-manage-in-cloud"
      >
        {tr("kwallet.manage_in_cloud")}
      </a>
    {/if}
  </div>

  {#if !fixedScope}
    <div class="grid gap-2">
      <p class="text-muted-foreground text-xs" data-testid="wallet-no-export">
        {tr("kwallet.import.no_export")}
      </p>
      <label
        class="inline-flex items-start gap-2 text-sm"
        data-testid="wallet-creator-grant"
      >
        <input
          type="checkbox"
          class="mt-0.5"
          checked={creatorState === "on"}
          disabled={creatorState === "loading" ||
            creatorState === "error" ||
            creatorBusy}
          data-testid="wallet-creator-toggle"
          onchange={(event) => void toggleCreator(event.currentTarget.checked)}
        />
        <span>
          {tr("kwallet.creator.label")}
          <span class="text-muted-foreground mt-0.5 block text-xs"
            >{tr("kwallet.creator.hint")}</span
          >
        </span>
      </label>
    </div>
  {/if}

  {#if ownerChecked && !canUseValues}
    <p role="alert" class="text-warning text-sm">
      {tr("kwallet.denied.owner_unknown")}
    </p>
  {/if}

  <div role="status" aria-live="polite" class="grid gap-1 empty:hidden">
    {#if notice}<p class="text-success text-sm">{notice}</p>{/if}
    {#if notice && noticeWarning}
      <p class="text-warning text-sm">{noticeWarning}</p>
    {/if}
  </div>
  {#if actionError}
    <p role="alert" class="text-destructive text-sm">{actionError}</p>
  {/if}

  {#if loadState === "ready" && inScope.length > 0}
    <div class="wallet-filters">
      <label class="wallet-search">
        <span>{tr("kwallet.filter.search")}</span>
        <input
          type="search"
          autocomplete="off"
          data-kx="control"
          data-testid="wallet-search"
          placeholder={tr("kwallet.filter.search_placeholder")}
          bind:value={search}
        />
      </label>
      <Select
        class="wallet-filter"
        label={tr("kwallet.filter.type")}
        options={typeOptions}
        value={typeFilter}
        onchange={setTypeFilter}
      />
      {#if !fixedScope}
        <Select
          class="wallet-filter"
          label={tr("kwallet.filter.scope")}
          options={scopeOptions}
          value={scopeFilter}
          onchange={(value) => (scopeFilter = value)}
        />
      {/if}
    </div>
    {#if activePreset}
      <p
        id={activePreset}
        class="text-muted-foreground -mt-2 max-w-3xl text-sm"
        data-testid="wallet-preset-hint"
      >
        {tr(`kwallet.preset.${activePreset}_hint`)}
      </p>
    {/if}
  {/if}

  {#if selectedMissing}
    <p role="alert" class="text-warning text-sm" data-testid="wallet-item-missing">
      {tr("kwallet.detail.item_missing")}
    </p>
  {/if}

  {#if loadState === "loading"}
    <p role="status" class="text-muted-foreground text-sm">
      {tr("kwallet.loading")}
    </p>
  {:else if loadState === "error"}
    <div class="grid gap-2" role="alert">
      <p class="text-foreground text-sm font-semibold">
        {tr("kwallet.load_error_title")}
      </p>
      <p class="text-muted-foreground text-sm">{loadError}</p>
      <div>
        <Button variant="secondary" size="sm" onclick={() => void load()}>
          {tr("kwallet.retry")}
        </Button>
      </div>
    </div>
  {:else if inScope.length === 0}
    <div class="border-border border-y py-6">
      <h3 class="text-foreground text-sm font-semibold">
        {tr("kwallet.empty_title")}
      </h3>
      <p class="text-muted-foreground mt-1 max-w-xl text-sm">
        {tr(fixedScope ? "kwallet.empty_body_stack" : "kwallet.empty_body")}
      </p>
    </div>
  {:else if visible.length === 0}
    <p class="text-muted-foreground text-sm" data-testid="wallet-no-match">
      {tr("kwallet.filter.no_match")}
    </p>
  {:else}
    <EntityCollection
      items={visible}
      key={(item) => item.itemId}
      label={tr("kwallet.list_caption")}
      {view}
      views={[view]}
      labels={entityLabels}
    >
      {#snippet item(entry: WalletItem, size: EntityCardSize)}
        <EntityCard
          kind="device"
          shape="tile"
          {size}
          name={entry.displayName}
          meta={itemMeta(entry)}
          icon={itemIcon(entry)}
          accent={itemAccent(entry)}
          statusLabel={entry.state === "active"
            ? undefined
            : itemStateLabel(entry.state)}
          facts={cardFacts(entry)}
          tags={itemTags(entry)}
          actions={itemActions(entry, true)}
          selected={entry.itemId === selectedId}
          onselect={() => select(entry.itemId)}
          labels={itemLabels(entry)}
          data-testid="wallet-item"
          data-item-id={entry.itemId}
        />
      {/snippet}
    </EntityCollection>
  {/if}
</div>

{#if selected}
  {@const item = selected}
  {@const active = item.state === "active"}
  {@const caKey = isSshCa(item.itemType) ? itemCaPublicKey(item) : null}
  {@const releases = disclosuresSinceRotation(item)}
  <EntityDetailSheet
    open={!dialogOpen}
    onclose={() => {
      // Closing the sheet for a Wallet dialog keeps the item selected.
      if (!dialogOpen) select(null);
    }}
    kind="device"
    shape="tile"
    name={item.displayName}
    meta={item.name ?? item.description}
    icon={itemIcon(item)}
    statusLabel={itemStateLabel(item.state)}
    tags={itemTags(item)}
    facts={detailFacts(item)}
    actions={itemActions(item, false)}
    labels={itemLabels(item)}
  >
    <div class="grid gap-3" data-testid="wallet-item-detail">
      {#if item.name && item.description}
        <p class="text-muted-foreground text-sm">{item.description}</p>
      {/if}
      {#if active && releases > 0}
        <p class="text-warning text-sm" data-testid="wallet-rotate-hint">
          {tr("kwallet.rotate.hint", { count: String(releases) })}
        </p>
      {/if}
      {#if active && isSshCa(item.itemType) && !itemSignPolicy(item)}
        <p class="text-warning text-sm" data-testid="wallet-ca-no-policy">
          {tr("kwallet.ssh_ca.no_policy")}
        </p>
      {/if}
      {#if caKey}
        <WalletSshCaKey publicKey={caKey} name={item.displayName} />
      {/if}
      {#if active}
        <label class="inline-flex items-start gap-2 text-sm">
          <input
            type="checkbox"
            class="mt-0.5"
            data-testid="wallet-techstack-use"
            aria-label={tr("kwallet.techstack.toggle_named", {
              name: item.displayName,
            })}
            checked={grants[item.itemId] === "on"}
            disabled={grants[item.itemId] !== "on" &&
              grants[item.itemId] !== "off"}
            onchange={(event) =>
              void toggleGrant(item, event.currentTarget.checked)}
          />
          <span>
            {tr("kwallet.techstack.allow_label")}
            <span class="text-muted-foreground mt-0.5 block text-xs"
              >{tr("kwallet.techstack.allow_hint")}</span
            >
          </span>
        </label>
      {/if}
    </div>
  </EntityDetailSheet>
{/if}

{#if creating && owner}
  <WalletItemDialog
    mode="create"
    ownerSubject={owner}
    {fixedScope}
    allowTechstackByDefault={allowTechstackOnCreate || undefined}
    onclose={() => (creating = false)}
    ondone={onItemSaved}
    onerror={(message) => (actionError = message)}
  />
{/if}

{#if generatingKey && owner}
  <WalletItemDialog
    mode="create"
    ownerSubject={owner}
    allowedTypes={["node_access_profile"]}
    initialType="node_access_profile"
    generateKey
    allowTechstackByDefault={true}
    onclose={() => (generatingKey = false)}
    ondone={onItemSaved}
    onerror={(message) => (actionError = message)}
  />
{/if}

{#if versionOf && owner}
  <WalletItemDialog
    mode="version"
    item={versionOf}
    ownerSubject={owner}
    onclose={() => (versionOf = null)}
    ondone={onItemSaved}
    onerror={(message) => (actionError = message)}
  />
{/if}

{#if revealOf && owner}
  <WalletRevealDialog
    item={revealOf}
    ownerSubject={owner}
    onclose={() => (revealOf = null)}
  />
{/if}

{#if settingsOf}
  <WalletSettingsDialog
    item={settingsOf}
    onclose={() => (settingsOf = null)}
    ondone={onSettingsSaved}
    onerror={(message) => (actionError = message)}
  />
{/if}

{#if signOf}
  <WalletSshCertificateDialog item={signOf} onclose={() => (signOf = null)} />
{/if}

{#if importing && owner}
  <WalletImportDialog
    ownerSubject={owner}
    onclose={() => {
      importing = false;
      void load();
    }}
  />
{/if}

<style>
  .wallet-filters {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 0.75rem;
  }
  .wallet-search {
    display: grid;
    flex: 1 1 14rem;
    gap: 0.35rem;
    min-width: 0;
    color: var(--muted-foreground);
    font-size: 0.8rem;
  }
  .wallet-search input {
    min-height: 2.5rem;
    width: 100%;
    min-width: 0;
    padding: 0.4rem 0.75rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-button);
    background: var(--background);
    color: var(--foreground);
    font: inherit;
    font-size: 0.875rem;
  }
  .wallet-search input:focus-visible {
    outline: 2px solid var(--ring);
    outline-offset: 2px;
  }
  .wallet-filters :global(.wallet-filter) {
    flex: 1 1 10rem;
    min-width: 0;
  }
  .wallet-filters :global(.wallet-filter select) {
    width: 100%;
  }
</style>
