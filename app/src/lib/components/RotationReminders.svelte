<script lang="ts">
  import type { PBWalletItem } from "$lib/stores/wallet";
  import {
    getRotationInterval,
    shouldRotate,
    getDaysUntilRotation,
  } from "$lib/wallet/integration";

  interface Props {
    items: PBWalletItem[];
    onRotate: (item: any) => void;
  }

  let { items, onRotate }: Props = $props();

  // Filter items that need rotation or have rotation tracking
  let rotationItems = $derived.by(() => {
    return items
      .filter((item) => {
        const interval = getRotationInterval(item.kind);
        return interval !== null;
      })
      .map((item) => {
        const interval = getRotationInterval(item.kind);
        const needsRotation = shouldRotate(item.last_rotated, item.kind);
        const daysUntil = getDaysUntilRotation(item.last_rotated, item.kind);

        return {
          item,
          interval,
          needsRotation,
          daysUntil,
          neverRotated: !item.last_rotated,
        };
      })
      .sort((a, b) => {
        // Sort by urgency: needs rotation first, then by days until
        if (a.needsRotation && !b.needsRotation) return -1;
        if (!a.needsRotation && b.needsRotation) return 1;
        if (a.neverRotated && !b.neverRotated) return -1;
        if (!a.neverRotated && b.neverRotated) return 1;
        if (a.daysUntil !== null && b.daysUntil !== null) {
          return a.daysUntil - b.daysUntil;
        }
        return 0;
      });
  });

  let needsAttention = $derived(
    rotationItems.filter((r) => r.needsRotation || r.neverRotated).length,
  );

  function getCredentialTypeLabel(kind: string): string {
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
        return "TLS";
      default:
        return "Secret";
    }
  }

  function formatLastRotated(date: string | undefined): string {
    if (!date) return "Never";
    const d = new Date(date);
    const now = new Date();
    const days = Math.floor(
      (now.getTime() - d.getTime()) / (1000 * 60 * 60 * 24),
    );
    if (days === 0) return "Today";
    if (days === 1) return "Yesterday";
    if (days < 30) return `${days} days ago`;
    if (days < 60) return "1 month ago";
    return `${Math.floor(days / 30)} months ago`;
  }

  function getStatusBadge(
    needsRotation: boolean,
    neverRotated: boolean,
    daysUntil: number | null,
  ): string {
    if (needsRotation) return "Overdue";
    if (neverRotated) return "Setup needed";
    if (daysUntil !== null && daysUntil <= 14) return `${daysUntil}d left`;
    return "OK";
  }
</script>

{#if rotationItems.length > 0}
  <div class="card">
    <div class="card-content">
      <div class="flex items-center justify-between mb-4">
        <div class="flex items-center gap-3">
          <div
            class="w-10 h-10 rounded-lg bg-primary/10 flex items-center justify-center"
          >
            <svg
              class="h-5 w-5 text-primary"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="1.8"
                d="M4 4v6h6M20 20v-6h-6"
              />
              <path
                stroke-linecap="round"
                stroke-linejoin="round"
                stroke-width="1.8"
                d="M20 9A8 8 0 0 0 6.34 5.34L4 10m16 4-2.34 4.66A8 8 0 0 1 4 15"
              />
            </svg>
          </div>
          <h3 class="text-foreground font-semibold">Rotation Schedule</h3>
        </div>
        {#if needsAttention > 0}
          <span class="badge badge-destructive"
            >{needsAttention} need attention</span
          >
        {/if}
      </div>

      <div class="space-y-2 max-h-64 overflow-y-auto">
        {#each rotationItems as { item, interval, needsRotation, daysUntil, neverRotated }}
          <div
            class="flex items-center justify-between p-3 rounded-lg bg-muted/50 border border-border"
          >
            <div class="flex items-center gap-3 min-w-0">
              <div
                class="w-8 h-8 rounded-lg bg-primary/10 flex items-center justify-center shrink-0"
              >
                <span
                  class="text-[10px] font-semibold uppercase tracking-[0.18em] text-primary"
                >
                  {getCredentialTypeLabel(item.kind)}
                </span>
              </div>
              <div class="min-w-0">
                <p class="text-foreground text-sm truncate">{item.name}</p>
                <p class="text-xs text-muted-foreground">
                  Last: {formatLastRotated(item.last_rotated)} • Every {interval}d
                </p>
              </div>
            </div>

            <div class="flex items-center gap-3 flex-shrink-0">
              <span
                class="badge {needsRotation
                  ? 'badge-destructive'
                  : neverRotated
                    ? 'badge-warning'
                    : daysUntil !== null && daysUntil <= 14
                      ? 'badge-warning'
                      : 'badge-success'}"
              >
                {getStatusBadge(needsRotation, neverRotated, daysUntil)}
              </span>

              {#if needsRotation || neverRotated}
                <button
                  onclick={() => onRotate(item)}
                  class="btn btn-ghost btn-sm text-primary"
                >
                  Rotate Now
                </button>
              {/if}
            </div>
          </div>
        {/each}
      </div>

      <div class="mt-4 pt-4 border-t border-border">
        <p class="text-xs text-muted-foreground">
          Recommended rotation: API Keys (90d), Passwords (180d), OAuth Tokens
          (30d), Certificates (365d)
        </p>
      </div>
    </div>
  </div>
{/if}
