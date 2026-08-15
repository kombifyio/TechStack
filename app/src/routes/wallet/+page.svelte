<script lang="ts">
  import type { Component } from "svelte";
  import { onMount } from "svelte";
  import { replaceState } from "$app/navigation";
  import {
    Boxes,
    Copy,
    Eye,
    EyeOff,
    Search,
    ShieldAlert,
    ShieldCheck,
    X,
  } from "@lucide/svelte";
  import {
    getWalletItems,
    requestWalletRevealProof,
    revealWalletItem,
    createWalletItem,
    updateWalletItem,
    deleteWalletItem,
    rotateWalletItem,
    resetSystemAccountPassword,
    type SystemAccountRole,
  } from "$lib/api/wallet";
  import { isCancelledRequestError, parseApiError } from "$lib/api/errors";
  import type {
    CredentialType,
    PBWalletItem,
    WalletEntryArea,
  } from "$lib/stores/wallet";
  import { buildWalletEntryPayload } from "$lib/wallet/payload";
  import { authHandler } from "$lib/stores/authHandler.svelte";
  import SessionRenewalPanel from "$lib/components/SessionRenewalPanel.svelte";
  import { authStore } from "$lib/stores/auth.svelte";
  import { showInlineTabs } from "$lib/stores/deploymentMode";
  import Modal from "$lib/components/Modal.svelte";
  import CredentialForm from "$lib/components/CredentialForm.svelte";
  import ImportExportModal from "$lib/components/ImportExportModal.svelte";
  import SSHKeyGenerator from "$lib/components/SSHKeyGenerator.svelte";
  import ServiceDiscovery from "$lib/components/ServiceDiscovery.svelte";
  import RotationReminders from "$lib/components/RotationReminders.svelte";
  import type { ExportableCredential } from "$lib/wallet/export";
  import { triggerPasswordManagerSave } from "$lib/wallet/quicksync";
  import { FeatureGate } from "$lib/components";
  import { isNetworkDiscoveryEnabled } from "$lib/stores/features";
  import { confirmInApp, promptInApp } from "$lib/dialogs/in-app-dialog";

  let items = $state<PBWalletItem[]>([]);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let sessionRenewalRequired = $state(false);
  let saving = $state(false);

  // UI state
  let showAddForm = $state(false);
  let editingItem = $state<PBWalletItem | null>(null);
  let formArea = $state<WalletEntryArea>("recovery");
  let deletingId = $state<string | null>(null);
  let showImportExport = $state(false);
  let exportItems = $state<PBWalletItem[]>([]);
  let showSSHGenerator = $state(false);
  let showServiceDiscovery = $state(false);
  let showRotateModal = $state(false);
  let rotatingItem = $state<PBWalletItem | null>(null);
  let newSecret = $state("");
  let revealedWalletItems = $state<Record<string, PBWalletItem>>({});
  const isSaaSDeployment = $derived(authStore.deploymentMode === "saas");

  // Quick-Sync state
  let quickSyncStates = $state<
    Record<string, "idle" | "syncing" | "success" | "error">
  >({});
  let quickSyncToasts = $state<Record<string, string>>({});

  // Search and filter state
  let searchQuery = $state("");
  let filterType = $state<CredentialType | "all">("all");
  type WalletTab = WalletEntryArea;
  let activeTab = $state<WalletTab>("tools");

  let walletTabs = $derived.by(
    (): Array<{
      id: WalletTab;
      label: string;
      icon: Component;
    }> => [
      {
        id: "tools",
        label: "Tools",
        icon: Boxes,
      },
      {
        id: "access",
        label: "Access",
        icon: ShieldCheck,
      },
      {
        id: "recovery",
        label: "Recovery",
        icon: ShieldAlert,
      },
    ],
  );

  // Derived filtered items
  let filteredItems = $derived.by(() => {
    return items.filter((item) => {
      // Type filter
      if (filterType !== "all" && item.kind !== filterType) return false;

      // Search filter
      if (searchQuery.trim()) {
        const query = searchQuery.toLowerCase();
        return (
          item.name.toLowerCase().includes(query) ||
          item.username?.toLowerCase().includes(query) ||
          item.url?.toLowerCase().includes(query) ||
          item.notes?.toLowerCase().includes(query)
        );
      }
      return true;
    });
  });

  let toolItems = $derived.by(() => {
    return filteredItems.filter((item) => isToolItem(item));
  });

  let userAccountItems = $derived.by(() => {
    return filteredItems.filter(
      (item) => isAccessEntry(item) && item.item_class === "user_account",
    );
  });

  let accessEntryItems = $derived.by(() => {
    return filteredItems.filter(
      (item) => isAccessEntry(item) && item.item_class !== "user_account",
    );
  });

  let vaultItems = $derived.by(() => {
    return filteredItems.filter(
      (item) => !isToolItem(item) && !isAccessEntry(item),
    );
  });

  // Check for expiring credentials (within 30 days)
  let expiringCredentials = $derived.by(() => {
    const thirtyDaysFromNow = new Date();
    thirtyDaysFromNow.setDate(thirtyDaysFromNow.getDate() + 30);

    return items.filter((item) => {
      if (!item.expires_at) return false;
      const expiryDate = new Date(item.expires_at);
      return expiryDate <= thirtyDaysFromNow && expiryDate > new Date();
    });
  });

  let expiredCredentials = $derived.by(() => {
    return items.filter((item) => {
      if (!item.expires_at) return false;
      return new Date(item.expires_at) <= new Date();
    });
  });

  // Track which passwords are revealed
  let revealedPasswords = $state<Set<string>>(new Set());
  let copiedStates = $state<Record<string, boolean>>({});

  let systemResetting = $state<Record<string, boolean>>({});

  type SystemUser = {
    id: SystemAccountRole;
    email: string;
    password: string;
    walletItemId?: string;
    hasPassword?: boolean;
    role: string;
    description: string;
    securityLevel: string;
    features: string[];
  };

  // System users created during bootstrap.
  let systemUsers = $state<SystemUser[]>([
    {
      id: "superuser",
      email: "superuser@techstack.local",
      password: "",
      role: "Local backend superuser",
      description: "Self-hosted compatibility admin access",
      securityLevel: "critical",
      features: [
        "Local backend admin",
        "Database management",
        "Collection editing",
        "API rules",
      ],
    },
    {
      id: "admin",
      email: "admin@techstack.local",
      password: "",
      role: "kombify-TechStack Admin",
      description: "Application administrator with full stack management",
      securityLevel: "high",
      features: [
        "Stack management",
        "User management",
        "System settings",
        "Monitoring",
      ],
    },
    {
      id: "developer",
      email: "developer@techstack.local",
      password: "",
      role: "kombify-TechStack Developer",
      description: "Developer access for testing and development",
      securityLevel: "medium",
      features: [
        "Stack creation",
        "Simulation access",
        "API access",
        "Log viewing",
      ],
    },
  ]);

  function syncSystemUsersFromWallet(walletItems: PBWalletItem[]) {
    const currentUsers = new Map(
      systemUsers.map((user) => [user.id, user] as const),
    );
    const byServiceId = new Map(
      walletItems
        .filter(
          (i) => i.kind === "password" && i.service_id?.startsWith("system:"),
        )
        .map((i) => [i.service_id!, i] as const),
    );

    systemUsers = systemUsers.map((u) => {
      const entry = byServiceId.get(`system:${u.id}`);
      const current = currentUsers.get(u.id);
      const revealed = entry?.id ? revealedWalletItems[entry.id] : undefined;
      return {
        ...u,
        email: entry?.username || current?.email || u.email,
        password: revealed?.secret || current?.password || "",
        walletItemId: entry?.id,
        hasPassword: Boolean(
          entry?.has_secret || revealed?.secret || current?.hasPassword,
        ),
      };
    });
  }

  async function loadWalletItems() {
    loading = true;
    error = null;

    try {
      items = await getWalletItems();
      const currentIDs = new Set(items.map((item) => item.id));
      revealedWalletItems = Object.fromEntries(
        Object.entries(revealedWalletItems).filter(([id]) =>
          currentIDs.has(id),
        ),
      );
      syncSystemUsersFromWallet(items);
    } catch (err) {
      if (isCancelledRequestError(err)) return;
      const parsed = parseApiError(err);
      if (parsed.isAuthError) {
        const outcome = await authHandler.handleUnauthorized(
          () => loadWalletItems(),
          undefined,
          err,
        );
        if (outcome === "reauth_required") sessionRenewalRequired = true;
        return;
      }
      error = parsed.message;
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void loadWalletItems();
    syncWalletTabFromHash();

    const handleHashChange = () => syncWalletTabFromHash();
    window.addEventListener("hashchange", handleHashChange);

    return () => {
      window.removeEventListener("hashchange", handleHashChange);
    };
  });

  function schedulePasswordHide(id: string) {
    setTimeout(() => {
      revealedPasswords.delete(id);
      revealedPasswords = new Set(revealedPasswords);
    }, 30000);
  }

  function redirectToCloudWalletReauth(reason: string) {
    if (typeof window === "undefined") return;

    const returnTo = new URL(
      `${window.location.pathname}${window.location.search}${window.location.hash}`,
      window.location.origin,
    ).toString();
    const target = authStore.portalUrl
      ? new URL("/dashboard/techstack/wallet/reauth", authStore.portalUrl)
      : new URL(
          authStore.cloudAuthUrl || "/api/v2/auth/login",
          window.location.origin,
        );

    target.searchParams.set("return_to", returnTo);
    target.searchParams.set("reauth", "wallet_reveal");
    target.searchParams.set("prompt", "login");
    target.searchParams.set("max_age", "0");
    if (reason.trim()) {
      target.searchParams.set("reason", reason.trim());
    }
    window.location.href = target.toString();
  }

  async function loadWalletItemDetail(
    id: string,
    reason = "wallet item reveal",
  ): Promise<PBWalletItem | null> {
    const base = items.find((item) => item.id === id);
    try {
      const revealed = await revealWalletItemForCurrentLane(id, reason);
      return { ...(base || ({} as PBWalletItem)), ...revealed } as PBWalletItem;
    } catch (err) {
      const parsed = parseApiError(err);
      if (
        parsed.isForbidden &&
        isSaaSDeployment &&
        typeof window !== "undefined"
      ) {
        error =
          "Fresh kombify Cloud re-authentication is required before wallet material can be revealed.";
        redirectToCloudWalletReauth(reason);
        return null;
      }
      if (parsed.isForbidden && typeof window !== "undefined") {
        const currentPassword = await promptInApp({
          title: "Confirm credential reveal",
          message:
            "Enter your current local TechStack password to reveal this wallet entry.",
          inputLabel: "Current password",
          inputType: "password",
          confirmText: "Reveal",
        });
        if (currentPassword?.trim()) {
          try {
            const revealed = await revealWalletItem(id, {
              reason,
              currentPassword: currentPassword.trim(),
            });
            return {
              ...(base || ({} as PBWalletItem)),
              ...revealed,
            } as PBWalletItem;
          } catch (retryErr) {
            const retryParsed = parseApiError(retryErr);
            error = retryParsed.message;
            return null;
          }
        }
      }
      error = parsed.message;
      return null;
    }
  }

  async function revealWalletItemForCurrentLane(
    id: string,
    reason: string,
  ): Promise<Pick<PBWalletItem, "id" | "secret" | "totp">> {
    if (!isSaaSDeployment) {
      return await revealWalletItem(id, { reason });
    }
    const proof = await requestWalletRevealProof(id, reason);
    return await revealWalletItem(id, {
      reason,
      reauthTimestamp: proof.reauth_timestamp,
      reauthSignature: proof.reauth_signature,
    });
  }

  async function ensureWalletItemRevealed(
    id: string,
  ): Promise<PBWalletItem | null> {
    if (revealedWalletItems[id]) {
      return revealedWalletItems[id];
    }
    const detail = await loadWalletItemDetail(id);
    if (!detail) {
      return null;
    }
    revealedWalletItems = { ...revealedWalletItems, [id]: detail };
    syncSystemUsersFromWallet(items);
    return detail;
  }

  async function loadWalletItemsForSensitiveActions(
    sourceItems: PBWalletItem[],
  ): Promise<PBWalletItem[]> {
    return await Promise.all(
      sourceItems.map(async (item) => {
        if (!item.has_secret && !item.has_totp) {
          return item;
        }
        return (
          (await loadWalletItemDetail(item.id, "wallet export reveal")) || item
        );
      }),
    );
  }

  function getDisplaySecret(item: PBWalletItem): string {
    return revealedWalletItems[item.id]?.secret || item.secret || "";
  }

  function getSystemUserPassword(user: SystemUser): string {
    if (user.walletItemId) {
      return revealedWalletItems[user.walletItemId]?.secret || user.password;
    }
    return user.password;
  }

  function systemUserHasPassword(user: SystemUser): boolean {
    return Boolean(user.hasPassword || getSystemUserPassword(user));
  }

  async function ensureSystemUserPassword(
    role: SystemAccountRole,
  ): Promise<string> {
    const user = systemUsers.find((entry) => entry.id === role);
    if (!user) {
      return "";
    }
    if (!user.walletItemId) {
      return user.password;
    }
    const detail = await ensureWalletItemRevealed(user.walletItemId);
    const password = detail?.secret || user.password || "";
    if (password) {
      systemUsers = systemUsers.map((entry) =>
        entry.id === role ? { ...entry, password, hasPassword: true } : entry,
      );
    }
    return password;
  }

  async function togglePasswordVisibility(id: string) {
    if (revealedPasswords.has(id)) {
      revealedPasswords.delete(id);
      revealedPasswords = new Set(revealedPasswords);
      return;
    }

    if (id.startsWith("item-")) {
      const detail = await ensureWalletItemRevealed(id.slice(5));
      if (!detail?.secret && !detail?.totp) {
        return;
      }
    } else {
      const password = await ensureSystemUserPassword(id as SystemAccountRole);
      if (!password) {
        return;
      }
    }

    revealedPasswords.add(id);
    revealedPasswords = new Set(revealedPasswords);
    schedulePasswordHide(id);
  }

  function copyToClipboard(text: string, id: string) {
    navigator.clipboard.writeText(text);
    copiedStates[id] = true;
    setTimeout(() => {
      copiedStates[id] = false;
    }, 2000);
  }

  function maskPassword(password: string): string {
    return "•".repeat(Math.min(password.length, 16));
  }

  function hasRevealableSecret(item: PBWalletItem): boolean {
    return Boolean(
      item.has_secret || item.secret || item.has_totp || item.totp,
    );
  }

  function isToolItem(item: PBWalletItem): boolean {
    if (item.item_class === "launch") {
      return true;
    }

    if (item.access_mode === "open") {
      return Boolean(item.url);
    }

    return Boolean(
      item.url &&
      !hasRevealableSecret(item) &&
      item.item_class !== "user_account" &&
      item.access_mode !== "manage",
    );
  }

  function isAccessEntry(item: PBWalletItem): boolean {
    return item.item_class === "user_account" || item.access_mode === "manage";
  }

  function getWalletArea(item: PBWalletItem): WalletEntryArea {
    if (isToolItem(item)) {
      return "tools";
    }

    if (isAccessEntry(item)) {
      return "access";
    }

    return "recovery";
  }

  function getEntryActionLabel(item: PBWalletItem): string {
    return item.access_mode === "manage" ? "Manage" : "Open";
  }

  function openWalletLink(item: PBWalletItem) {
    if (!item.url) {
      return;
    }

    window.open(item.url, "_blank", "noopener,noreferrer");
  }

  function openAddForm(area: WalletEntryArea) {
    formArea = area;
    editingItem = null;
    showAddForm = true;
  }

  function getFormTitle(): string {
    const label =
      formArea === "tools"
        ? "Tool"
        : formArea === "access"
          ? "Access Entry"
          : "Recovery Item";

    return editingItem ? `Edit ${label}` : `Add ${label}`;
  }

  function getSecurityLevelColor(level: string): string {
    switch (level) {
      case "critical":
        return "bg-red-900/50 text-red-400 border-red-700/50";
      case "high":
        return "bg-orange-900/50 text-orange-400 border-orange-700/50";
      case "medium":
        return "bg-yellow-900/50 text-yellow-400 border-yellow-700/50";
      default:
        return "bg-muted/50 text-muted-foreground border-border/50";
    }
  }

  function getCredentialTypeIcon(kind: CredentialType): string {
    switch (kind) {
      case "password":
        return "PW";
      case "api_key":
        return "API";
      case "ssh_key":
        return "SSH";
      case "oauth_token":
        return "OAuth";
      case "certificate":
        return "Cert";
      default:
        return "Other";
    }
  }

  async function handleResetSystemPassword(role: SystemAccountRole) {
    systemResetting[role] = true;
    error = null;
    try {
      await resetSystemAccountPassword(role);
      await loadWalletItems();
    } catch (err) {
      const parsed = parseApiError(err);
      error = parsed.message;
    } finally {
      systemResetting[role] = false;
    }
  }

  async function handleSaveCredential(data: Partial<PBWalletItem>) {
    saving = true;
    error = null;
    try {
      if (editingItem?.id) {
        await updateWalletItem(editingItem.id, data);
      } else {
        await createWalletItem(data);
      }
      await loadWalletItems();
      showAddForm = false;
      editingItem = null;
      formArea = "recovery";
    } catch (err) {
      if (isCancelledRequestError(err)) return;
      const parsed = parseApiError(err);
      if (parsed.isAuthError) {
        // No auto-redirect: the form holds unsaved credential input.
        const outcome = await authHandler.handleUnauthorized(
          () => handleSaveCredential(data),
          undefined,
          err,
          { allowAutoRedirect: false },
        );
        if (outcome === "reauth_required") sessionRenewalRequired = true;
        return;
      }
      error = parsed.isForbidden
        ? "You don't have permission to add wallet entries."
        : parsed.isValidationError && Object.keys(parsed.fieldErrors).length > 0
          ? Object.entries(parsed.fieldErrors)
              .map(([f, { message }]) => `${f}: ${message}`)
              .join("; ")
          : parsed.message;
    } finally {
      saving = false;
    }
  }

  async function handleEditCredential(item: PBWalletItem) {
    formArea = getWalletArea(item);
    if (item.has_secret || item.has_totp) {
      editingItem = (await ensureWalletItemRevealed(item.id)) || item;
    } else {
      editingItem = item;
    }
    showAddForm = true;
  }

  async function handleDeleteCredential(id: string) {
    if (
      !(await confirmInApp({
        title: "Delete wallet entry?",
        message: "This permanently removes the selected wallet entry.",
        confirmText: "Delete",
        tone: "danger",
      }))
    ) {
      return;
    }
    deletingId = id;
    try {
      await deleteWalletItem(id);
      await loadWalletItems();
    } catch (err) {
      const parsed = parseApiError(err);
      if (parsed.isAuthError) {
        const outcome = await authHandler.handleUnauthorized(
          () => handleDeleteCredential(id),
          undefined,
          err,
        );
        if (outcome === "reauth_required") sessionRenewalRequired = true;
        return;
      }
      error = parsed.isForbidden
        ? "You don't have permission to delete this credential."
        : parsed.message;
    } finally {
      deletingId = null;
    }
  }

  function handleCancelForm() {
    showAddForm = false;
    editingItem = null;
    formArea = "recovery";
  }

  async function handleImportCredentials(credentials: ExportableCredential[]) {
    for (const cred of credentials) {
      await createWalletItem(
        buildWalletEntryPayload(
          "recovery",
          {
            name: cred.name,
            kind: cred.kind,
            username: cred.username,
            secret: cred.secret,
            totp: cred.totp,
            url: cred.url,
            notes: cred.notes,
            expires_at: cred.expires_at,
            auto_generated: cred.auto_generated,
          },
          { sourceRef: "wallet-import" },
        ),
      );
    }
    await loadWalletItems();
  }

  async function handleSaveSSHKey(
    name: string,
    publicKey: string,
    privateKey: string,
  ) {
    await createWalletItem(
      buildWalletEntryPayload(
        "recovery",
        {
          name: name,
          kind: "ssh_key",
          secret: privateKey,
          notes: `Public Key:\n${publicKey}`,
          auto_generated: true,
        },
        { sourceRef: "ssh-key-generator" },
      ),
    );
    await loadWalletItems();
  }

  function getDaysUntilExpiry(expiresAt: string): number {
    const expiry = new Date(expiresAt);
    const now = new Date();
    const diffTime = expiry.getTime() - now.getTime();
    return Math.ceil(diffTime / (1000 * 60 * 60 * 24));
  }

  function getExpiryBadgeColor(expiresAt: string): string {
    const days = getDaysUntilExpiry(expiresAt);
    if (days <= 0) return "bg-red-900/50 text-red-400 border-red-700/50";
    if (days <= 7)
      return "bg-orange-900/50 text-orange-400 border-orange-700/50";
    if (days <= 30)
      return "bg-yellow-900/50 text-yellow-400 border-yellow-700/50";
    return "bg-muted/50 text-muted-foreground border-border/50";
  }

  function handleRotateCredential(item: PBWalletItem) {
    rotatingItem = item;
    newSecret = "";
    showRotateModal = true;
  }

  async function handleConfirmRotation() {
    if (!rotatingItem || !newSecret.trim()) return;

    saving = true;
    try {
      await rotateWalletItem(rotatingItem.id, newSecret.trim());
      await loadWalletItems();
      showRotateModal = false;
      rotatingItem = null;
      newSecret = "";
    } catch (err) {
      const parsed = parseApiError(err);
      if (parsed.isAuthError) {
        // No auto-redirect: the rotation modal holds the unsaved new secret.
        const outcome = await authHandler.handleUnauthorized(
          () => handleConfirmRotation(),
          undefined,
          err,
          { allowAutoRedirect: false },
        );
        if (outcome === "reauth_required") sessionRenewalRequired = true;
        return;
      }
      error = parsed.message;
    } finally {
      saving = false;
    }
  }

  async function handleQuickSync(item: PBWalletItem) {
    quickSyncStates[item.id] = "syncing";

    try {
      const resolvedItem =
        item.has_secret || item.has_totp
          ? ((await ensureWalletItemRevealed(item.id)) ?? item)
          : item;
      const success = await triggerPasswordManagerSave(resolvedItem);

      if (success) {
        quickSyncStates[item.id] = "success";
        quickSyncToasts[item.id] =
          "Browser extension triggered! Check for save prompt.";
      } else {
        quickSyncStates[item.id] = "error";
        quickSyncToasts[item.id] = "Could not trigger browser extension.";
      }

      // Reset state after 3 seconds
      setTimeout(() => {
        quickSyncStates[item.id] = "idle";
        delete quickSyncToasts[item.id];
        quickSyncStates = { ...quickSyncStates };
        quickSyncToasts = { ...quickSyncToasts };
      }, 3000);
    } catch (err) {
      quickSyncStates[item.id] = "error";
      quickSyncToasts[item.id] = "Quick-Sync failed. Try copying manually.";

      setTimeout(() => {
        quickSyncStates[item.id] = "idle";
        delete quickSyncToasts[item.id];
        quickSyncStates = { ...quickSyncStates };
        quickSyncToasts = { ...quickSyncToasts };
      }, 3000);
    }
  }

  async function copySystemUserPassword(user: SystemUser) {
    const password =
      getSystemUserPassword(user) || (await ensureSystemUserPassword(user.id));
    if (password) {
      copyToClipboard(password, `pass-${user.id}`);
    }
  }

  async function copyWalletItemSecret(item: PBWalletItem) {
    const detail =
      item.has_secret || item.has_totp
        ? await ensureWalletItemRevealed(item.id)
        : item;
    const secret = detail?.secret || item.secret || "";
    if (secret) {
      copyToClipboard(secret, `secret-${item.id}`);
    }
  }

  async function openImportExportModal() {
    exportItems = await loadWalletItemsForSensitiveActions(items);
    showImportExport = true;
  }

  function syncWalletTabFromHash() {
    if (typeof window === "undefined") {
      return;
    }

    const hash = window.location.hash.replace("#", "");
    if (hash === "tools" || hash === "access" || hash === "recovery") {
      activeTab = hash;
      return;
    }

    activeTab = "tools";
  }

  function selectWalletTab(tab: WalletTab) {
    activeTab = tab;

    if (tab !== "recovery") {
      searchQuery = "";
      filterType = "all";
    }

    if (typeof window === "undefined") {
      return;
    }

    const url = new URL(window.location.href);
    url.hash = tab === "tools" ? "" : tab;
    replaceState(url, {});
  }
</script>

<div
  class="mx-auto flex w-full max-w-7xl flex-col gap-6 {$showInlineTabs
    ? 'h-full min-h-0'
    : 'px-4 py-6 md:px-6 md:py-8'}"
>
  <div class="flex flex-col gap-3">
    <div
      class="inline-flex max-w-full rounded-2xl border border-border/70 bg-card/70 p-1 shadow-sm shadow-black/10 backdrop-blur-sm"
    >
      <nav
        class="flex min-h-12 flex-wrap items-center gap-1"
        aria-label="Wallet sections"
        data-wallet-tab-nav
      >
        {#each walletTabs as tab}
          {@const Icon = tab.icon}

          <button
            type="button"
            data-wallet-tab={tab.id}
            aria-pressed={activeTab === tab.id}
            onclick={() => selectWalletTab(tab.id)}
            class="flex min-h-10 items-center gap-2 rounded-xl px-4 py-2 text-sm font-medium whitespace-nowrap transition-all {activeTab ===
            tab.id
              ? 'bg-background text-foreground shadow-sm ring-1 ring-border/80'
              : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'}"
          >
            <Icon class="h-4 w-4" />
            <span>{tab.label}</span>
          </button>
        {/each}
      </nav>
    </div>

    <p class="text-xs text-muted-foreground/80">
      Wallet stays split into launch surfaces, operational access, and
      recovery-only material.
    </p>
  </div>

  <section
    class="rounded-3xl border border-border/60 bg-card/35 p-5 shadow-sm shadow-black/10 backdrop-blur-sm md:p-6 {$showInlineTabs
      ? 'flex-1 min-h-0 overflow-y-auto'
      : 'min-h-[42rem]'}"
    data-wallet-canvas
  >
    <!-- Import/Export Modal -->
    {#if showImportExport}
      <ImportExportModal
        items={exportItems}
        onImport={handleImportCredentials}
        onClose={() => {
          showImportExport = false;
          exportItems = [];
        }}
      />
    {/if}

    <!-- SSH Key Generator -->
    {#if showSSHGenerator}
      <SSHKeyGenerator
        onSave={handleSaveSSHKey}
        onClose={() => (showSSHGenerator = false)}
      />
    {/if}

    <!-- Service Discovery (only show if feature enabled) -->
    {#if showServiceDiscovery && $isNetworkDiscoveryEnabled}
      <ServiceDiscovery
        onCredentialsAdded={loadWalletItems}
        onClose={() => (showServiceDiscovery = false)}
      />
    {/if}

    <!-- Rotation Modal -->
    {#if showRotateModal && rotatingItem}
      <Modal
        title="Rotate Credential"
        onClose={() => {
          showRotateModal = false;
          rotatingItem = null;
          newSecret = "";
        }}
        maxWidth="md"
      >
        <p class="text-muted-foreground text-sm mb-6">
          Rotating: <strong class="text-white">{rotatingItem.name}</strong>
        </p>

        <div class="mb-6">
          <label
            for="new-secret"
            class="block text-sm font-medium text-foreground/80 mb-2"
          >
            New Secret <span class="text-red-400">*</span>
          </label>
          <input
            id="new-secret"
            type="password"
            bind:value={newSecret}
            placeholder="Enter new secret value"
            class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
          />
        </div>

        <div
          class="p-3 rounded-lg bg-info/10 border border-info/30 text-sm text-muted-foreground mb-6"
        >
          This will update the secret and record the rotation date for tracking.
        </div>

        <div class="flex justify-end gap-3">
          <button
            onclick={() => {
              showRotateModal = false;
              rotatingItem = null;
              newSecret = "";
            }}
            class="btn btn-secondary"
          >
            Cancel
          </button>
          <button
            onclick={handleConfirmRotation}
            disabled={saving || !newSecret.trim()}
            class="btn btn-primary disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {saving ? "Rotating..." : "Save"}
          </button>
        </div>
      </Modal>
    {/if}

    <!-- Add/Edit Form Modal -->
    {#if showAddForm}
      <Modal title={getFormTitle()} onClose={handleCancelForm} maxWidth="2xl">
        <CredentialForm
          credential={editingItem || undefined}
          mode={formArea}
          onSave={handleSaveCredential}
          onCancel={handleCancelForm}
          {saving}
        />
      </Modal>
    {/if}

    <!-- Expiry Warnings -->
    {#if activeTab === "recovery"}
      {#if expiredCredentials.length > 0 || expiringCredentials.length > 0}
        <div class="mb-8 space-y-3">
          {#if expiredCredentials.length > 0}
            <div class="card border-destructive/30 bg-destructive/5">
              <div class="card-content flex items-start gap-3">
                <div
                  class="w-10 h-10 rounded-lg bg-destructive/10 flex items-center justify-center shrink-0"
                >
                  <svg
                    class="w-5 h-5 text-destructive"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="2"
                      d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
                    />
                  </svg>
                </div>
                <div>
                  <p class="font-medium text-foreground">
                    {expiredCredentials.length} Expired Credential{expiredCredentials.length !==
                    1
                      ? "s"
                      : ""}
                  </p>
                  <p class="text-sm text-muted-foreground mt-1">
                    {expiredCredentials.map((c) => c.name).join(", ")} — consider
                    rotating immediately.
                  </p>
                </div>
              </div>
            </div>
          {/if}
          {#if expiringCredentials.length > 0}
            <div class="card border-warning/30 bg-warning/5">
              <div class="card-content flex items-start gap-3">
                <div
                  class="w-10 h-10 rounded-lg bg-warning/10 flex items-center justify-center shrink-0"
                >
                  <svg
                    class="w-5 h-5 text-warning"
                    fill="none"
                    stroke="currentColor"
                    viewBox="0 0 24 24"
                  >
                    <path
                      stroke-linecap="round"
                      stroke-linejoin="round"
                      stroke-width="2"
                      d="M15 17h5l-1.405-1.405A2.032 2.032 0 0118 14.158V11a6.002 6.002 0 00-4-5.659V5a2 2 0 10-4 0v.341C7.67 6.165 6 8.388 6 11v3.159c0 .538-.214 1.055-.595 1.436L4 17h5m6 0v1a3 3 0 11-6 0v-1m6 0H9"
                    />
                  </svg>
                </div>
                <div>
                  <p class="font-medium text-foreground">
                    {expiringCredentials.length} Credential{expiringCredentials.length !==
                    1
                      ? "s"
                      : ""} Expiring Soon
                  </p>
                  <p class="text-sm text-muted-foreground mt-1">
                    {expiringCredentials
                      .map(
                        (c) =>
                          `${c.name} (${getDaysUntilExpiry(c.expires_at ?? "")}d)`,
                      )
                      .join(", ")}
                  </p>
                </div>
              </div>
            </div>
          {/if}
        </div>
      {/if}
    {/if}

    {#if sessionRenewalRequired}
      <div class="mb-8">
        <SessionRenewalPanel
          onretry={() => {
            sessionRenewalRequired = false;
            loadWalletItems();
          }}
          busy={loading}
          returnTo="/wallet"
        />
      </div>
    {/if}

    {#if loading}
      <div class="space-y-4 mb-8" data-wallet-state="loading">
        {#each Array(3) as _}
          <div class="card animate-pulse h-28"></div>
        {/each}
      </div>
    {:else if error}
      <div
        class="card border-destructive/30 bg-destructive/5 mb-8"
        data-wallet-state="error"
      >
        <div class="card-content flex items-center justify-between gap-4">
          <span class="text-foreground">{error}</span>
          <button
            onclick={() => (error = null)}
            class="btn btn-ghost btn-sm text-muted-foreground hover:text-foreground"
          >
            Dismiss
          </button>
        </div>
      </div>
    {/if}

    {#if activeTab === "tools" && !loading && !error}
      <div class="mb-8">
        <div class="flex items-center justify-between gap-4 mb-4">
          <div>
            <h2 class="text-xl font-semibold text-foreground">Tools</h2>
            <p class="text-sm text-muted-foreground mt-1">
              Wallet should be the first place to launch tools, services, and
              operational surfaces without dropping into reveal flows.
            </p>
          </div>
          <div class="flex items-center gap-2">
            <span class="badge badge-secondary">{toolItems.length}</span>
            <button
              onclick={() => openAddForm("tools")}
              class="btn btn-secondary btn-sm"
            >
              + Add Tool
            </button>
          </div>
        </div>

        {#if toolItems.length === 0}
          <div class="card border-border/60 bg-muted/10">
            <div class="card-content py-6">
              <p class="text-foreground font-medium mb-1">No tools yet</p>
              <p class="text-sm text-muted-foreground">
                Launch-ready tools and URL-backed service entries will surface
                here as Wallet becomes the central starting point.
              </p>
              <div class="mt-4">
                <button
                  onclick={() => openAddForm("tools")}
                  class="btn btn-primary btn-sm"
                >
                  + Add Tool
                </button>
              </div>
            </div>
          </div>
        {:else}
          <div class="grid md:grid-cols-2 xl:grid-cols-3 gap-4">
            {#each toolItems as item}
              <div class="card card-hover">
                <div class="card-content">
                  <div class="flex items-start justify-between gap-4 mb-4">
                    <div class="min-w-0 flex items-center gap-3">
                      <div
                        class="w-10 h-10 rounded-lg bg-primary/10 flex items-center justify-center shrink-0"
                      >
                        <span class="text-lg"
                          >{getCredentialTypeIcon(item.kind)}</span
                        >
                      </div>
                      <div class="min-w-0">
                        <div class="flex items-center gap-2">
                          <h3
                            class="text-base font-semibold text-foreground truncate"
                          >
                            {item.name}
                          </h3>
                          <span class="badge badge-outline text-xs">Tool</span>
                        </div>
                        <p class="text-xs text-muted-foreground truncate">
                          {item.url || item.source_ref || "Tool surface"}
                        </p>
                      </div>
                    </div>
                    <div class="flex gap-1 shrink-0">
                      <button
                        onclick={() => handleEditCredential(item)}
                        class="btn btn-ghost btn-sm p-2"
                        title="Edit"
                      >
                        <svg
                          class="w-4 h-4"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            stroke-width="2"
                            d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z"
                          />
                        </svg>
                      </button>
                      <button
                        onclick={() => handleDeleteCredential(item.id)}
                        disabled={deletingId === item.id}
                        class="btn btn-ghost btn-sm p-2 hover:text-destructive disabled:opacity-50"
                        title="Delete"
                      >
                        <svg
                          class="w-4 h-4"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            stroke-width="2"
                            d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                          />
                        </svg>
                      </button>
                    </div>
                  </div>

                  <div class="space-y-3 text-sm">
                    {#if item.username}
                      <div class="flex justify-between gap-3">
                        <span class="text-muted-foreground">Account</span>
                        <span class="text-foreground/80 truncate"
                          >{item.username}</span
                        >
                      </div>
                    {/if}

                    {#if item.source_type}
                      <div class="flex justify-between gap-3">
                        <span class="text-muted-foreground">Source</span>
                        <span class="text-foreground/80 truncate"
                          >{item.source_type}</span
                        >
                      </div>
                    {/if}

                    {#if item.notes}
                      <div
                        class="rounded-lg bg-muted/30 border border-border px-3 py-2"
                      >
                        <div class="text-muted-foreground mb-1 text-xs">
                          Context
                        </div>
                        <div
                          class="text-foreground/80 whitespace-pre-wrap text-xs"
                        >
                          {item.notes}
                        </div>
                      </div>
                    {/if}

                    {#if hasRevealableSecret(item)}
                      <div>
                        <div class="flex justify-between gap-3 mb-1">
                          <span class="text-muted-foreground">Secret</span>
                          <div class="flex gap-1">
                            <button
                              onclick={() =>
                                togglePasswordVisibility(`item-${item.id}`)}
                              class="text-muted-foreground hover:text-white text-xs"
                            >
                              {revealedPasswords.has(`item-${item.id}`)
                                ? "Hide"
                                : "Reveal"}
                            </button>
                            <button
                              onclick={() => copyWalletItemSecret(item)}
                              class="text-muted-foreground hover:text-white text-xs"
                            >
                              {copiedStates[`secret-${item.id}`] ? "✓" : "Copy"}
                            </button>
                          </div>
                        </div>
                        <div
                          class="rounded-lg bg-muted/50 border border-border px-3 py-2"
                        >
                          <div
                            class="text-foreground font-mono text-xs break-all"
                          >
                            {#if revealedPasswords.has(`item-${item.id}`)}
                              {getDisplaySecret(item) || "-"}
                            {:else}
                              ••••••••
                            {/if}
                          </div>
                        </div>
                      </div>
                    {/if}

                    <div class="pt-3 border-t border-border flex gap-2">
                      {#if item.url}
                        <button
                          onclick={() => openWalletLink(item)}
                          class="btn btn-primary btn-sm flex-1"
                        >
                          {getEntryActionLabel(item)}
                        </button>
                      {/if}
                      {#if item.kind === "password" || item.kind === "api_key"}
                        <button
                          onclick={() => handleQuickSync(item)}
                          disabled={quickSyncStates[item.id] === "syncing"}
                          class="btn btn-secondary btn-sm flex-1 disabled:opacity-50"
                        >
                          {quickSyncStates[item.id] === "syncing"
                            ? "Syncing..."
                            : "Quick-Sync"}
                        </button>
                      {/if}
                    </div>
                  </div>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </div>
    {/if}

    {#if activeTab === "access" && !loading && !error}
      <div class="mb-6">
        <div class="flex items-center justify-between gap-4 mb-3">
          <div>
            <h2 class="text-xl font-semibold text-foreground">Access</h2>
            <p class="text-sm text-muted-foreground mt-1">
              Human users, admin identities, and system-owned login surfaces
              live here as the operational access layer.
            </p>
          </div>
          <div class="flex items-center gap-2">
            <span class="badge badge-secondary"
              >{systemUsers.length +
                userAccountItems.length +
                accessEntryItems.length}</span
            >
            <button
              onclick={() => openAddForm("access")}
              class="btn btn-secondary btn-sm"
            >
              + Add Access
            </button>
          </div>
        </div>
      </div>

      {#if accessEntryItems.length > 0}
        <div class="mb-6">
          <div class="flex items-center gap-3 mb-3">
            <h3 class="text-lg font-semibold text-foreground">
              Access Controls
            </h3>
            <span class="badge badge-secondary">{accessEntryItems.length}</span>
          </div>

          <div class="grid md:grid-cols-2 xl:grid-cols-3 gap-4">
            {#each accessEntryItems as item}
              <div class="card card-hover">
                <div class="card-content">
                  <div class="flex items-start justify-between gap-4 mb-4">
                    <div class="min-w-0">
                      <div class="flex items-center gap-2">
                        <h3
                          class="text-base font-semibold text-foreground truncate"
                        >
                          {item.name}
                        </h3>
                        <span class="badge badge-outline text-xs">Access</span>
                      </div>
                      <p class="text-xs text-muted-foreground">
                        {item.username ||
                          item.url ||
                          item.source_ref ||
                          "Access control"}
                      </p>
                    </div>
                    <div class="flex gap-1 shrink-0">
                      <button
                        onclick={() => handleEditCredential(item)}
                        class="btn btn-ghost btn-sm p-2"
                        title="Edit"
                      >
                        <svg
                          class="w-4 h-4"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            stroke-width="2"
                            d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z"
                          />
                        </svg>
                      </button>
                      <button
                        onclick={() => handleDeleteCredential(item.id)}
                        disabled={deletingId === item.id}
                        class="btn btn-ghost btn-sm p-2 hover:text-destructive disabled:opacity-50"
                        title="Delete"
                      >
                        <svg
                          class="w-4 h-4"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            stroke-width="2"
                            d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                          />
                        </svg>
                      </button>
                    </div>
                  </div>

                  <div class="space-y-3 text-sm">
                    {#if item.url}
                      <div class="flex justify-between gap-3">
                        <span class="text-muted-foreground">Surface</span>
                        <a
                          href={item.url}
                          target="_blank"
                          class="text-primary hover:text-primary/80 truncate max-w-50"
                        >
                          {item.url}
                        </a>
                      </div>
                    {/if}

                    {#if item.source_type}
                      <div class="flex justify-between gap-3">
                        <span class="text-muted-foreground">Source</span>
                        <span class="text-foreground/80 truncate"
                          >{item.source_type}</span
                        >
                      </div>
                    {/if}

                    {#if item.notes}
                      <div
                        class="rounded-lg bg-muted/30 border border-border px-3 py-2"
                      >
                        <div class="text-muted-foreground mb-1 text-xs">
                          Notes
                        </div>
                        <div
                          class="text-foreground/80 whitespace-pre-wrap text-xs"
                        >
                          {item.notes}
                        </div>
                      </div>
                    {/if}

                    {#if hasRevealableSecret(item)}
                      <div>
                        <div class="flex justify-between gap-3 mb-1">
                          <span class="text-muted-foreground">Secret</span>
                          <div class="flex gap-1">
                            <button
                              onclick={() =>
                                togglePasswordVisibility(`item-${item.id}`)}
                              class="text-muted-foreground hover:text-white text-xs"
                            >
                              {revealedPasswords.has(`item-${item.id}`)
                                ? "Hide"
                                : "Reveal"}
                            </button>
                            <button
                              onclick={() => copyWalletItemSecret(item)}
                              class="text-muted-foreground hover:text-white text-xs"
                            >
                              {copiedStates[`secret-${item.id}`] ? "✓" : "Copy"}
                            </button>
                          </div>
                        </div>
                        <div
                          class="rounded-lg bg-muted/50 border border-border px-3 py-2"
                        >
                          <div
                            class="text-foreground font-mono text-xs break-all"
                          >
                            {#if revealedPasswords.has(`item-${item.id}`)}
                              {getDisplaySecret(item) || "-"}
                            {:else}
                              ••••••••
                            {/if}
                          </div>
                        </div>
                      </div>
                    {/if}

                    {#if item.url}
                      <div class="pt-3 border-t border-border">
                        <button
                          onclick={() => openWalletLink(item)}
                          class="w-full btn btn-secondary btn-sm"
                        >
                          {getEntryActionLabel(item)}
                        </button>
                      </div>
                    {/if}
                  </div>
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      {#if userAccountItems.length > 0}
        <div class="mb-6">
          <div class="flex items-center gap-3 mb-3">
            <h3 class="text-lg font-semibold text-foreground">Managed Users</h3>
            <span class="badge badge-secondary">{userAccountItems.length}</span>
          </div>

          <div class="grid md:grid-cols-2 xl:grid-cols-3 gap-4">
            {#each userAccountItems as item}
              <div class="card card-hover">
                <div class="card-content">
                  <div class="flex items-start justify-between gap-4 mb-4">
                    <div class="min-w-0">
                      <h3
                        class="text-base font-semibold text-foreground truncate"
                      >
                        {item.name}
                      </h3>
                      <p class="text-xs text-muted-foreground">
                        {item.username ||
                          item.url ||
                          item.source_ref ||
                          "Managed user"}
                      </p>
                    </div>
                    <div class="flex gap-1 shrink-0">
                      <button
                        onclick={() => handleEditCredential(item)}
                        class="btn btn-ghost btn-sm p-2"
                        title="Edit"
                      >
                        <svg
                          class="w-4 h-4"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            stroke-width="2"
                            d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z"
                          />
                        </svg>
                      </button>
                      <button
                        onclick={() => handleDeleteCredential(item.id)}
                        disabled={deletingId === item.id}
                        class="btn btn-ghost btn-sm p-2 hover:text-destructive disabled:opacity-50"
                        title="Delete"
                      >
                        <svg
                          class="w-4 h-4"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            stroke-width="2"
                            d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                          />
                        </svg>
                      </button>
                    </div>
                  </div>

                  <div class="space-y-3 text-sm">
                    {#if item.username}
                      <div class="flex justify-between gap-3">
                        <span class="text-muted-foreground">Identity</span>
                        <span class="text-foreground/80 truncate"
                          >{item.username}</span
                        >
                      </div>
                    {/if}

                    {#if item.url}
                      <div class="flex justify-between gap-3">
                        <span class="text-muted-foreground">Manage</span>
                        <a
                          href={item.url}
                          target="_blank"
                          class="text-primary hover:text-primary/80 truncate max-w-50"
                        >
                          {item.url}
                        </a>
                      </div>
                    {/if}

                    {#if item.notes}
                      <div
                        class="rounded-lg bg-muted/30 border border-border px-3 py-2"
                      >
                        <div class="text-muted-foreground mb-1 text-xs">
                          Notes
                        </div>
                        <div
                          class="text-foreground/80 whitespace-pre-wrap text-xs"
                        >
                          {item.notes}
                        </div>
                      </div>
                    {/if}

                    {#if hasRevealableSecret(item)}
                      <div>
                        <div class="flex justify-between gap-3 mb-1">
                          <span class="text-muted-foreground">Secret</span>
                          <div class="flex gap-1">
                            <button
                              onclick={() =>
                                togglePasswordVisibility(`item-${item.id}`)}
                              class="text-muted-foreground hover:text-white text-xs"
                            >
                              {revealedPasswords.has(`item-${item.id}`)
                                ? "Hide"
                                : "Reveal"}
                            </button>
                            <button
                              onclick={() => copyWalletItemSecret(item)}
                              class="text-muted-foreground hover:text-white text-xs"
                            >
                              {copiedStates[`secret-${item.id}`] ? "✓" : "Copy"}
                            </button>
                          </div>
                        </div>
                        <div
                          class="rounded-lg bg-muted/50 border border-border px-3 py-2"
                        >
                          <div
                            class="text-foreground font-mono text-xs break-all"
                          >
                            {#if revealedPasswords.has(`item-${item.id}`)}
                              {getDisplaySecret(item) || "-"}
                            {:else}
                              ••••••••
                            {/if}
                          </div>
                        </div>
                      </div>
                    {/if}
                  </div>
                </div>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- System Users Section -->
      <div class="mb-6">
        <div class="flex items-center gap-3 mb-3">
          <h3 class="text-lg font-semibold text-foreground">System Users</h3>
          <span class="badge badge-secondary">{systemUsers.length}</span>
        </div>

        <div class="grid md:grid-cols-2 lg:grid-cols-3 gap-3">
          {#each systemUsers as user}
            <div class="card card-hover">
              <div class="card-content py-3 px-4">
                <!-- Compact Header -->
                <div class="flex items-center justify-between gap-2 mb-2">
                  <div class="flex items-center gap-2 min-w-0">
                    <div
                      class="w-8 h-8 rounded-lg bg-primary/10 flex items-center justify-center shrink-0"
                    >
                      <svg
                        class="w-4 h-4 text-primary"
                        fill="none"
                        stroke="currentColor"
                        viewBox="0 0 24 24"
                      >
                        <path
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          stroke-width="2"
                          d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"
                        />
                      </svg>
                    </div>
                    <div class="min-w-0">
                      <h3 class="font-medium text-foreground text-sm truncate">
                        {user.role}
                      </h3>
                      <span class="badge badge-outline text-xs"
                        >{user.securityLevel}</span
                      >
                    </div>
                  </div>
                  <span class="badge badge-info text-xs shrink-0">Auto</span>
                </div>

                <!-- Compact credentials -->
                <div class="space-y-1.5 text-xs">
                  <div
                    class="flex items-center justify-between gap-2 rounded bg-muted/50 px-2 py-1.5"
                  >
                    <span class="text-muted-foreground">User</span>
                    <div class="flex items-center gap-1.5">
                      <code class="text-primary truncate max-w-32"
                        >{user.email}</code
                      >
                      <button
                        onclick={() =>
                          copyToClipboard(user.email, `email-${user.id}`)}
                        class="text-muted-foreground hover:text-foreground"
                        aria-label={`Copy ${user.role} email`}
                      >
                        <Copy class="h-3.5 w-3.5" />
                      </button>
                    </div>
                  </div>
                  <div
                    class="flex items-center justify-between gap-2 rounded bg-muted/50 px-2 py-1.5"
                  >
                    <span class="text-muted-foreground">Secret</span>
                    <div class="flex items-center gap-1.5">
                      {#if systemUserHasPassword(user)}
                        <code class="text-primary font-mono truncate max-w-20">
                          {revealedPasswords.has(user.id)
                            ? getSystemUserPassword(user)
                            : "••••••••"}
                        </code>
                        <button
                          onclick={() => togglePasswordVisibility(user.id)}
                          class="text-muted-foreground hover:text-foreground"
                          aria-label={revealedPasswords.has(user.id)
                            ? `Hide ${user.role} secret`
                            : `Reveal ${user.role} secret`}
                        >
                          {#if revealedPasswords.has(user.id)}
                            <EyeOff class="h-3.5 w-3.5" />
                          {:else}
                            <Eye class="h-3.5 w-3.5" />
                          {/if}
                        </button>
                        <button
                          onclick={() => copySystemUserPassword(user)}
                          class="text-muted-foreground hover:text-foreground"
                          aria-label={`Copy ${user.role} secret`}
                        >
                          <Copy class="h-3.5 w-3.5" />
                        </button>
                      {:else}
                        <span class="text-muted-foreground italic">Not set</span
                        >
                        <button
                          onclick={() => handleResetSystemPassword(user.id)}
                          class="text-primary"
                          disabled={systemResetting[user.id]}
                        >
                          {systemResetting[user.id] ? "..." : "Reset"}
                        </button>
                      {/if}
                    </div>
                  </div>
                </div>
              </div>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    {#if activeTab === "recovery" && !loading && !error}
      <div class="mb-6">
        <div class="flex items-center justify-between gap-4 mb-3">
          <div>
            <h2 class="text-xl font-semibold text-foreground">Recovery</h2>
            <p class="text-sm text-muted-foreground mt-1">
              Break-glass material, credentials, and reveal-only secrets stay in
              the recovery zone.
            </p>
          </div>
          <span class="badge badge-secondary">{vaultItems.length}</span>
        </div>
      </div>

      <!-- Rotation Reminders -->
      {#if items.length > 0}
        <div class="mb-8">
          <RotationReminders {items} onRotate={handleRotateCredential} />
        </div>
      {/if}

      <!-- Wallet Items Section -->
      <div class="mb-4">
        <div
          class="flex flex-col md:flex-row md:items-center justify-between gap-4 mb-4"
        >
          <div class="flex items-center gap-3">
            <h2 class="text-xl font-semibold text-white">Recovery Items</h2>
            <span
              class="text-xs px-2 py-0.5 rounded-full bg-muted text-muted-foreground border border-border"
            >
              {vaultItems.length} stored
            </span>
          </div>

          <!-- Search and Filter -->
          <div class="flex items-center gap-3">
            <div class="relative">
              <input
                type="text"
                bind:value={searchQuery}
                placeholder="Search credentials..."
                data-search-input
                class="w-64 px-4 py-2 pl-10 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none text-sm"
              />
              <Search
                class="w-4 h-4 absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground"
              />
              {#if searchQuery}
                <button
                  onclick={() => (searchQuery = "")}
                  class="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-white"
                  aria-label="Clear wallet search"
                >
                  <X class="h-4 w-4" />
                </button>
              {/if}
            </div>

            <select
              bind:value={filterType}
              class="px-4 py-2 bg-input border border-border rounded-lg text-foreground text-sm focus:border-primary focus:outline-none"
            >
              <option value="all">All Types</option>
              <option value="password">Passwords</option>
              <option value="api_key">API Keys</option>
              <option value="ssh_key">SSH Keys</option>
              <option value="oauth_token">OAuth Tokens</option>
              <option value="certificate">Certificates</option>
              <option value="other">Other</option>
            </select>
          </div>
        </div>

        <!-- Action Buttons -->
        <div class="flex items-center gap-2 flex-wrap">
          <FeatureGate feature="network_discovery" showDisabledHint={true}>
            {#snippet children()}
              <button
                onclick={() => (showServiceDiscovery = true)}
                class="btn btn-secondary text-sm"
                title="Discover service credentials"
              >
                Discover
              </button>
            {/snippet}
          </FeatureGate>
          <button
            onclick={() => (showSSHGenerator = true)}
            class="btn btn-secondary text-sm"
          >
            Generate SSH Key
          </button>
          <button
            onclick={openImportExportModal}
            class="btn btn-secondary text-sm"
          >
            Import / Export
          </button>
          <button
            onclick={() => openAddForm("recovery")}
            class="btn btn-primary text-sm"
          >
            + Recovery
          </button>
        </div>
      </div>

      {#if items.length === 0}
        <div class="card">
          <div class="card-content text-center py-8">
            <div
              class="w-12 h-12 mx-auto rounded-lg bg-muted/50 flex items-center justify-center mb-4"
            >
              <svg
                class="w-6 h-6 text-muted-foreground"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="1.5"
                  d="M12 15v2m-6 4h12a2 2 0 002-2v-6a2 2 0 00-2-2H6a2 2 0 00-2 2v6a2 2 0 002 2zm10-10V7a4 4 0 00-8 0v4h8z"
                />
              </svg>
            </div>
            <p class="text-foreground font-medium mb-2">
              No recovery items yet
            </p>
            <p class="text-sm text-muted-foreground mb-4">
              Add break-glass credentials, fallback material, or reveal-only
              secrets here.
            </p>
            <div class="flex flex-wrap items-center justify-center gap-2">
              <button
                onclick={() => openAddForm("recovery")}
                class="btn btn-primary"
              >
                + Recovery
              </button>
            </div>
          </div>
        </div>
      {:else if filteredItems.length === 0}
        <div class="card">
          <div class="card-content text-center py-8">
            <div
              class="w-12 h-12 mx-auto rounded-lg bg-muted/50 flex items-center justify-center mb-4"
            >
              <svg
                class="w-6 h-6 text-muted-foreground"
                fill="none"
                stroke="currentColor"
                viewBox="0 0 24 24"
              >
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="1.5"
                  d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
                />
              </svg>
            </div>
            <p class="text-foreground font-medium mb-2">
              No matching recovery items
            </p>
            <p class="text-sm text-muted-foreground mb-4">
              Try adjusting your search query or filter for reveal-only entries.
            </p>
            <button
              onclick={() => {
                searchQuery = "";
                filterType = "all";
              }}
              class="btn btn-secondary"
            >
              Clear Filters
            </button>
          </div>
        </div>
      {:else if vaultItems.length === 0}
        <div class="card border-border/60 bg-muted/10">
          <div class="card-content text-center py-8">
            <p class="text-foreground font-medium mb-2">
              No recovery items in this view
            </p>
            <p class="text-sm text-muted-foreground mb-4">
              Break-glass material, reveal-only credentials, and fallback
              secrets will appear here.
            </p>
          </div>
        </div>
      {:else}
        <div class="grid md:grid-cols-2 gap-4">
          {#each vaultItems as item}
            <div
              class="card card-hover {item.expires_at &&
              getDaysUntilExpiry(item.expires_at) <= 0
                ? 'border-destructive/50'
                : item.expires_at && getDaysUntilExpiry(item.expires_at) <= 7
                  ? 'border-warning/50'
                  : ''}"
            >
              <div class="card-content">
                <div class="flex items-start justify-between gap-4 mb-4">
                  <div class="min-w-0 flex items-center gap-3">
                    <div
                      class="w-10 h-10 rounded-lg bg-primary/10 flex items-center justify-center shrink-0"
                    >
                      <span class="text-lg"
                        >{getCredentialTypeIcon(item.kind)}</span
                      >
                    </div>
                    <div>
                      <div class="flex items-center gap-2">
                        <h3
                          class="text-base font-semibold text-foreground truncate"
                        >
                          {item.name}
                        </h3>
                        {#if item.expires_at}
                          {@const days = getDaysUntilExpiry(item.expires_at)}
                          <span
                            class="badge {days <= 0
                              ? 'badge-destructive'
                              : days <= 7
                                ? 'badge-warning'
                                : 'badge-secondary'}"
                          >
                            {#if days <= 0}
                              Expired
                            {:else if days <= 7}
                              {days}d left
                            {:else if days <= 30}
                              {days}d
                            {/if}
                          </span>
                        {/if}
                      </div>
                      <p class="text-xs text-muted-foreground capitalize">
                        {item.kind.replace("_", " ")}
                      </p>
                    </div>
                  </div>
                  <div class="flex gap-1">
                    <button
                      onclick={() => handleEditCredential(item)}
                      class="btn btn-ghost btn-sm p-2"
                      title="Edit"
                    >
                      <svg
                        class="w-4 h-4"
                        fill="none"
                        stroke="currentColor"
                        viewBox="0 0 24 24"
                      >
                        <path
                          stroke-linecap="round"
                          stroke-linejoin="round"
                          stroke-width="2"
                          d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z"
                        />
                      </svg>
                    </button>
                    <button
                      onclick={() => handleDeleteCredential(item.id)}
                      disabled={deletingId === item.id}
                      class="btn btn-ghost btn-sm p-2 hover:text-destructive disabled:opacity-50"
                      title="Delete"
                    >
                      {#if deletingId === item.id}
                        <svg class="w-4 h-4 animate-spin" viewBox="0 0 24 24">
                          <circle
                            class="opacity-25"
                            cx="12"
                            cy="12"
                            r="10"
                            stroke="currentColor"
                            stroke-width="4"
                            fill="none"
                          />
                          <path
                            class="opacity-75"
                            fill="currentColor"
                            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
                          />
                        </svg>
                      {:else}
                        <svg
                          class="w-4 h-4"
                          fill="none"
                          stroke="currentColor"
                          viewBox="0 0 24 24"
                        >
                          <path
                            stroke-linecap="round"
                            stroke-linejoin="round"
                            stroke-width="2"
                            d="M19 7l-.867 12.142A2 2 0 0116.138 21H7.862a2 2 0 01-1.995-1.858L5 7m5 4v6m4-6v6m1-10V4a1 1 0 00-1-1h-4a1 1 0 00-1 1v3M4 7h16"
                          />
                        </svg>
                      {/if}
                    </button>
                  </div>
                </div>

                <div class="space-y-3 text-sm">
                  {#if item.username}
                    <div class="flex justify-between gap-3">
                      <span class="text-muted-foreground">Username</span>
                      <div class="flex items-center gap-1">
                        <span class="text-foreground/80 truncate"
                          >{item.username}</span
                        >
                        <button
                          onclick={() =>
                            copyToClipboard(
                              item.username || "",
                              `user-${item.id}`,
                            )}
                          class="text-muted-foreground hover:text-white text-xs"
                        >
                          {copiedStates[`user-${item.id}`] ? "✓" : "Copy"}
                        </button>
                      </div>
                    </div>
                  {/if}

                  {#if item.url}
                    <div class="flex justify-between gap-3">
                      <span class="text-muted-foreground">URL</span>
                      <a
                        href={item.url}
                        target="_blank"
                        class="text-primary hover:text-primary/80 truncate max-w-50"
                      >
                        {item.url}
                      </a>
                    </div>
                  {/if}

                  <div>
                    <div class="flex justify-between gap-3 mb-1">
                      <span class="text-muted-foreground">Secret</span>
                      <div class="flex gap-1">
                        {#if item.has_secret || item.secret}
                          <button
                            onclick={() =>
                              togglePasswordVisibility(`item-${item.id}`)}
                            class="text-muted-foreground hover:text-white text-xs"
                          >
                            {revealedPasswords.has(`item-${item.id}`)
                              ? "Hide"
                              : "Show"}
                          </button>
                          <button
                            onclick={() => copyWalletItemSecret(item)}
                            class="text-muted-foreground hover:text-white text-xs"
                          >
                            {copiedStates[`secret-${item.id}`] ? "✓" : "Copy"}
                          </button>
                        {/if}
                      </div>
                    </div>
                    <div
                      class="rounded-lg bg-muted/50 border border-border px-3 py-2"
                    >
                      <div class="text-foreground font-mono text-xs break-all">
                        {#if item.has_secret || item.secret}
                          {revealedPasswords.has(`item-${item.id}`)
                            ? getDisplaySecret(item) || "-"
                            : "••••••••"}
                        {:else}
                          -
                        {/if}
                      </div>
                    </div>
                  </div>

                  {#if item.expires_at}
                    <div class="flex justify-between gap-3">
                      <span class="text-muted-foreground">Expires</span>
                      <span class="text-foreground/80">
                        {new Date(item.expires_at).toLocaleDateString()}
                      </span>
                    </div>
                  {/if}

                  {#if item.notes}
                    <div class="pt-3 border-t border-border">
                      <div class="text-muted-foreground mb-1 text-xs">
                        Notes
                      </div>
                      <div
                        class="text-foreground/80 whitespace-pre-wrap text-xs"
                      >
                        {item.notes}
                      </div>
                    </div>
                  {/if}

                  <!-- Quick-Sync Button -->
                  {#if item.kind === "password" || item.kind === "api_key"}
                    <div class="pt-3 border-t border-border">
                      <button
                        onclick={() => handleQuickSync(item)}
                        disabled={quickSyncStates[item.id] === "syncing"}
                        class="w-full btn {quickSyncStates[item.id] ===
                        'success'
                          ? 'btn-success'
                          : quickSyncStates[item.id] === 'error'
                            ? 'btn-destructive'
                            : 'btn-secondary'}
                    disabled:opacity-50 disabled:cursor-not-allowed"
                        title="Save to external password manager (1Password, Bitwarden, etc.)"
                      >
                        {#if quickSyncStates[item.id] === "syncing"}
                          <svg class="w-4 h-4 animate-spin" viewBox="0 0 24 24">
                            <circle
                              class="opacity-25"
                              cx="12"
                              cy="12"
                              r="10"
                              stroke="currentColor"
                              stroke-width="4"
                              fill="none"
                            />
                            <path
                              class="opacity-75"
                              fill="currentColor"
                              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
                            />
                          </svg>
                          <span>Triggering...</span>
                        {:else if quickSyncStates[item.id] === "success"}
                          <svg
                            class="w-4 h-4"
                            fill="none"
                            stroke="currentColor"
                            viewBox="0 0 24 24"
                          >
                            <path
                              stroke-linecap="round"
                              stroke-linejoin="round"
                              stroke-width="2"
                              d="M5 13l4 4L19 7"
                            />
                          </svg>
                          <span>Extension Triggered!</span>
                        {:else if quickSyncStates[item.id] === "error"}
                          <svg
                            class="w-4 h-4"
                            fill="none"
                            stroke="currentColor"
                            viewBox="0 0 24 24"
                          >
                            <path
                              stroke-linecap="round"
                              stroke-linejoin="round"
                              stroke-width="2"
                              d="M6 18L18 6M6 6l12 12"
                            />
                          </svg>
                          <span>Failed</span>
                        {:else}
                          <svg
                            class="w-4 h-4"
                            fill="none"
                            stroke="currentColor"
                            viewBox="0 0 24 24"
                          >
                            <path
                              stroke-linecap="round"
                              stroke-linejoin="round"
                              stroke-width="2"
                              d="M8 7H5a2 2 0 00-2 2v9a2 2 0 002 2h14a2 2 0 002-2V9a2 2 0 00-2-2h-3m-1 4l-3 3m0 0l-3-3m3 3V4"
                            />
                          </svg>
                          <span>Quick-Sync to Password Manager</span>
                        {/if}
                      </button>
                      {#if quickSyncToasts[item.id]}
                        <p
                          class="text-xs text-center mt-1 {quickSyncStates[
                            item.id
                          ] === 'success'
                            ? 'text-green-400'
                            : 'text-red-400'}"
                        >
                          {quickSyncToasts[item.id]}
                        </p>
                      {/if}
                    </div>
                  {/if}
                </div>

                {#if item.auto_generated}
                  <div class="mt-3 pt-3 border-t border-border">
                    <span class="badge badge-info">Auto-generated</span>
                  </div>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      {/if}
    {/if}
  </section>
</div>
