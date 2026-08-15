<script lang="ts">
  import { Check, Loader2, Lock } from "@lucide/svelte";
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
  type ChannelPreference = { enabled: boolean; locked?: boolean };
  type Topic = {
    topic_key: string;
    category: string;
    channels: Record<string, ChannelPreference>;
  };
  type PreferenceMatrix = { topics: Topic[] };

  interface Props {
    apiBase?: string;
    fetchImpl?: NotificationFetch;
  }

  let { apiBase = "/api/v1/notifications", fetchImpl }: Props = $props();
  let matrix = $state<PreferenceMatrix>({ topics: [] });
  let loading = $state(true);
  let saving = $state(false);
  let dirty = $state(false);
  let saved = $state(false);
  let error = $state<string | null>(null);
  const base = $derived(apiBase.replace(/\/$/, ""));
  const groups = $derived.by(() => {
    const entries: Record<string, Topic[]> = Object.create(null);
    for (const topic of matrix.topics) {
      (entries[topic.category] ??= []).push(topic);
    }
    return Object.entries(entries).map(([category, topics]) => ({
      category,
      topics,
    }));
  });

  async function request<T>(
    path: string,
    init?: NotificationRequest,
  ): Promise<T> {
    const response = await (fetchImpl ?? fetch)(`${base}${path}`, {
      credentials: "include",
      ...init,
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok || body?.success === false)
      throw new Error(
        body?.error || `Notifications request failed (${response.status})`,
      );
    return body as T;
  }

  async function load() {
    loading = true;
    try {
      const result = await request<PreferenceMatrix>("/preferences");
      matrix = { topics: Array.isArray(result.topics) ? result.topics : [] };
      dirty = false;
      error = null;
    } catch (cause) {
      error =
        cause instanceof Error
          ? cause.message
          : "Notification preferences are unavailable.";
    } finally {
      loading = false;
    }
  }

  function toggle(topicKey: string, channel: string) {
    matrix = {
      topics: matrix.topics.map((topic) => {
        if (topic.topic_key !== topicKey) return topic;
        const preference = topic.channels[channel];
        if (!preference || preference.locked) return topic;
        return {
          ...topic,
          channels: {
            ...topic.channels,
            [channel]: { ...preference, enabled: !preference.enabled },
          },
        };
      }),
    };
    dirty = true;
    saved = false;
  }

  async function save() {
    saving = true;
    try {
      const result = await request<PreferenceMatrix>("/preferences", {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(matrix),
      });
      matrix = { topics: Array.isArray(result.topics) ? result.topics : [] };
      dirty = false;
      saved = true;
      error = null;
    } catch (cause) {
      error =
        cause instanceof Error
          ? cause.message
          : "Notification preferences could not be saved.";
    } finally {
      saving = false;
    }
  }

  function topicLabel(value: string): string {
    const tail = value.includes(".")
      ? value.split(".").slice(1).join(".")
      : value;
    return tail
      .replace(/[._-]+/g, " ")
      .replace(/\b\w/g, (letter) => letter.toUpperCase());
  }

  onMount(() => {
    void load();
  });
</script>

<section class="space-y-6">
  <div>
    <h3 class="text-lg font-semibold text-foreground">Notifications</h3>
    <p class="mt-1 text-sm text-muted-foreground">
      Choose what you get notified about and where. Required security
      notifications stay enabled.
    </p>
  </div>
  {#if loading}<div
      class="flex items-center gap-2 py-8 text-sm text-muted-foreground"
    >
      <Loader2 class="h-4 w-4 animate-spin" />Loading your preferences…
    </div>
  {:else if error && matrix.topics.length === 0}<div
      role="alert"
      class="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
    >
      {error}
    </div>
  {:else if matrix.topics.length === 0}<p
      class="py-8 text-sm text-muted-foreground"
    >
      No notification topics are available yet.
    </p>
  {:else}{#each groups as group (group.category)}<section
        class="rounded-xl border border-border"
      >
        <header class="border-b border-border px-4 py-3">
          <h4 class="text-sm font-semibold text-foreground">
            {group.category.replace(/\b\w/g, (letter) => letter.toUpperCase())}
          </h4>
        </header>
        <div class="divide-y divide-border">
          {#each group.topics as topic (topic.topic_key)}<div
              class="flex flex-col gap-3 px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
            >
              <p class="text-sm text-foreground">
                {topicLabel(topic.topic_key)}
              </p>
              <div class="flex flex-wrap gap-2">
                {#each Object.entries(topic.channels) as [channel, preference] (channel)}<button
                    type="button"
                    onclick={() => toggle(topic.topic_key, channel)}
                    disabled={preference.locked}
                    aria-pressed={preference.enabled}
                    title={preference.locked
                      ? "Required — cannot be changed"
                      : ""}
                    class={`flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors ${preference.enabled ? "border-primary bg-primary/10 text-primary" : "border-border text-muted-foreground hover:text-foreground"} ${preference.locked ? "cursor-not-allowed opacity-70" : ""}`}
                    >{#if preference.locked}<Lock
                        class="h-3 w-3"
                      />{/if}{channel.replace(/_/g, " ")}</button
                  >{/each}
              </div>
            </div>{/each}
        </div>
      </section>{/each}
    <div class="flex items-center gap-3">
      <button
        type="button"
        class="btn btn-primary gap-2"
        onclick={() => void save()}
        disabled={saving || !dirty}
        >{#if saving}<Loader2 class="h-4 w-4 animate-spin" />Saving…{:else}Save
          preferences{/if}</button
      >{#if saved && !dirty}<span
          class="flex items-center gap-1 text-xs text-success"
          ><Check class="h-3.5 w-3.5" />Saved</span
        >{:else if dirty}<span class="text-xs text-muted-foreground"
          >Unsaved changes</span
        >{/if}{#if error}<span class="text-xs text-destructive">{error}</span
        >{/if}
    </div>{/if}
</section>
