<script lang="ts">
  import { Bell, Check, CheckCheck, Settings, X } from "@lucide/svelte";
  import { onMount } from "svelte";

  type NotificationRequest = {
    body?: string;
    credentials?: "include";
    headers?: Record<string, string>;
    method?: string;
  };
  type NotificationFetch = (
    input: string,
    init?: NotificationRequest,
  ) => Promise<Response>;
  type FeedItem = {
    id: string;
    subject: string;
    body_markdown?: string | null;
    link_url?: string | null;
    read_at?: string | null;
    created_at: string;
  };
  type MutationErrorContext = {
    action: "read" | "dismiss" | "read-all";
    itemId?: string;
    linkUrl?: string;
  };

  interface Props {
    apiBase?: string;
    pollMs?: number;
    settingsHref?: string;
    fetchImpl?: NotificationFetch;
    mutationErrorMessage?: string;
    onMutationError?: (message: string, context: MutationErrorContext) => void;
  }

  let {
    apiBase = "/api/v1/notifications",
    pollMs = 30_000,
    settingsHref = "/dashboard/settings?tab=notifications",
    fetchImpl,
    mutationErrorMessage,
    onMutationError,
  }: Props = $props();

  let open = $state(false);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let items = $state<FeedItem[]>([]);
  let unreadCount = $state(0);
  let root = $state<HTMLElement>();

  const base = $derived(apiBase.replace(/\/$/, ""));

  async function request<T>(
    path: string,
    init?: NotificationRequest,
  ): Promise<T> {
    const response = await (fetchImpl ?? fetch)(`${base}${path}`, {
      credentials: "include",
      ...init,
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok || body?.success === false) {
      throw new Error(
        body?.error || `Notifications request failed (${response.status})`,
      );
    }
    return body as T;
  }

  async function refresh() {
    loading = true;
    try {
      const result = await request<{
        feed?: FeedItem[];
        unread_count?: number;
      }>("/feed?limit=30");
      items = Array.isArray(result.feed) ? result.feed : [];
      unreadCount =
        typeof result.unread_count === "number"
          ? result.unread_count
          : items.filter((item) => !item.read_at).length;
      error = null;
    } catch (cause) {
      error =
        cause instanceof Error
          ? cause.message
          : "Notifications are unavailable.";
    } finally {
      loading = false;
    }
  }

  async function mutate(
    path: string,
    context: MutationErrorContext,
  ): Promise<boolean> {
    try {
      await request(path, { method: "POST" });
      await refresh();
      return true;
    } catch (cause) {
      const message =
        mutationErrorMessage ||
        (cause instanceof Error
          ? cause.message
          : "Notification update failed.");
      error = message;
      onMutationError?.(message, context);
      return false;
    }
  }

  function relativeTime(value: string): string {
    const age = Math.max(0, Date.now() - new Date(value).getTime());
    const minutes = Math.floor(age / 60_000);
    if (minutes < 1) return "just now";
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.floor(hours / 24)}d ago`;
  }

  async function openItem(item: FeedItem) {
    if (!item.read_at) {
      await mutate(`/feed/${encodeURIComponent(item.id)}/read`, {
        action: "read",
        itemId: item.id,
        linkUrl: item.link_url ?? undefined,
      });
    }
    if (item.link_url) window.location.href = item.link_url;
  }

  onMount(() => {
    void refresh();
    const poller = setInterval(() => {
      if (!open) void refresh();
    }, pollMs);
    const closeOnOutsidePress = (event: PointerEvent) => {
      const target = event.target;
      if (open && target instanceof Node && !root?.contains(target))
        open = false;
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") open = false;
    };
    document.addEventListener("pointerdown", closeOnOutsidePress, true);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      clearInterval(poller);
      document.removeEventListener("pointerdown", closeOnOutsidePress, true);
      document.removeEventListener("keydown", closeOnEscape);
    };
  });
</script>

<div class="relative" bind:this={root}>
  <button
    type="button"
    onclick={() => {
      open = !open;
      if (open) void refresh();
    }}
    aria-label="Notifications"
    aria-expanded={open}
    class="relative flex h-9 w-9 items-center justify-center rounded-lg transition-colors hover:bg-muted"
  >
    <Bell class="h-5 w-5" />
    {#if unreadCount > 0}<span
        class="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[10px] font-semibold leading-none text-primary-foreground"
        >{unreadCount > 99 ? "99+" : unreadCount}</span
      >{/if}
  </button>

  {#if open}
    <section
      class="absolute right-0 z-50 mt-2 flex max-h-[70vh] w-80 flex-col overflow-hidden rounded-xl border border-border bg-popover shadow-lg sm:w-96"
    >
      <header
        class="flex items-center justify-between border-b border-border px-4 py-3"
      >
        <p class="text-sm font-semibold text-foreground">Notifications</p>
        <div class="flex items-center gap-2">
          <button
            type="button"
            onclick={() =>
              void mutate("/feed/read-all", { action: "read-all" })}
            disabled={unreadCount === 0}
            class="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground disabled:opacity-40"
            ><CheckCheck class="h-3.5 w-3.5" />Mark all read</button
          ><a
            href={settingsHref}
            aria-label="Notification settings"
            title="Notification settings"
            onclick={() => (open = false)}
            class="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            ><Settings class="h-3.5 w-3.5" /></a
          >
        </div>
      </header>
      <div class="flex-1 overflow-y-auto">
        {#if loading && items.length === 0}<p
            class="px-4 py-8 text-center text-sm text-muted-foreground"
          >
            Loading…
          </p>
        {:else if error && items.length === 0}<p
            role="alert"
            class="px-4 py-8 text-center text-sm text-destructive"
          >
            {error}
          </p>
        {:else if items.length === 0}<div
            class="flex flex-col items-center gap-2 px-4 py-10 text-center text-muted-foreground"
          >
            <Bell class="h-6 w-6 opacity-40" />
            <p class="text-sm">You're all caught up.</p>
          </div>
        {:else}<ul>
            {#each items as item (item.id)}<li
                class="border-b border-border/60 last:border-b-0"
              >
                <div
                  role="button"
                  tabindex="0"
                  onclick={() => void openItem(item)}
                  onkeydown={(event) => {
                    if (event.key === "Enter") void openItem(item);
                  }}
                  class={`flex w-full items-start gap-3 px-4 py-3 text-left transition-colors hover:bg-muted/60 ${item.read_at ? "" : "bg-primary/5"}`}
                >
                  <span
                    class={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${item.read_at ? "opacity-0" : "bg-primary"}`}
                  ></span>
                  <div class="min-w-0 flex-1">
                    <p class="truncate text-sm font-medium text-foreground">
                      {item.subject}
                    </p>
                    {#if item.body_markdown}<p
                        class="mt-0.5 line-clamp-2 text-xs text-muted-foreground"
                      >
                        {item.body_markdown}
                      </p>{/if}
                    <p class="mt-1 text-[11px] text-muted-foreground">
                      {relativeTime(item.created_at)}
                    </p>
                  </div>
                  <div class="flex shrink-0 items-center gap-1">
                    {#if !item.read_at}<button
                        type="button"
                        aria-label="Mark read"
                        onclick={(event) => {
                          event.stopPropagation();
                          void mutate(
                            `/feed/${encodeURIComponent(item.id)}/read`,
                            { action: "read", itemId: item.id },
                          );
                        }}
                        class="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                        ><Check class="h-3.5 w-3.5" /></button
                      >{/if}<button
                      type="button"
                      aria-label="Dismiss"
                      onclick={(event) => {
                        event.stopPropagation();
                        void mutate(
                          `/feed/${encodeURIComponent(item.id)}/dismiss`,
                          { action: "dismiss", itemId: item.id },
                        );
                      }}
                      class="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
                      ><X class="h-3.5 w-3.5" /></button
                    >
                  </div>
                </div>
              </li>{/each}
          </ul>{/if}
      </div>
    </section>
  {/if}
</div>
