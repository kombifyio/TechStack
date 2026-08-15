<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import {
    CheckCircle,
    XCircle,
    AlertTriangle,
    Loader2,
    Server,
    Cpu,
    Network,
  } from "@lucide/svelte";

  interface Requirement {
    name: string;
    category: "hardware" | "software" | "network";
    minimum: string | number;
    recommended?: string | number;
    current?: string | number;
    status: "pending" | "checking" | "pass" | "warn" | "fail";
    message?: string;
  }

  interface Props {
    requirements: Requirement[];
    workerConnected?: boolean;
    onRetryCheck?: () => void;
    class?: string;
  }

  let {
    requirements = [],
    workerConnected = false,
    onRetryCheck,
    class: className = "",
  }: Props = $props();

  const dispatch = createEventDispatcher<{
    adjust: { requirement: Requirement };
    retry: void;
  }>();

  const categoryIcons = {
    hardware: Server,
    software: Cpu,
    network: Network,
  } as const;
  const categories: Requirement["category"][] = [
    "hardware",
    "software",
    "network",
  ];

  const statusConfig = {
    pending: {
      icon: Loader2,
      color: "text-slate-400",
      bg: "bg-slate-100",
      animate: false,
    },
    checking: {
      icon: Loader2,
      color: "text-blue-500",
      bg: "bg-blue-50",
      animate: true,
    },
    pass: {
      icon: CheckCircle,
      color: "text-green-500",
      bg: "bg-green-50",
      animate: false,
    },
    warn: {
      icon: AlertTriangle,
      color: "text-yellow-500",
      bg: "bg-yellow-50",
      animate: false,
    },
    fail: {
      icon: XCircle,
      color: "text-red-500",
      bg: "bg-red-50",
      animate: false,
    },
  } as const;

  const allPassing = $derived.by(() =>
    requirements.every((r) => r.status === "pass"),
  );
  const hasFailures = $derived.by(() =>
    requirements.some((r) => r.status === "fail"),
  );
  const hasWarnings = $derived.by(() =>
    requirements.some((r) => r.status === "warn"),
  );
  const isChecking = $derived.by(() =>
    requirements.some((r) => r.status === "checking"),
  );

  const overallStatus = $derived.by<keyof typeof statusConfig>(() =>
    isChecking
      ? "checking"
      : hasFailures
        ? "fail"
        : hasWarnings
          ? "warn"
          : allPassing
            ? "pass"
            : "pending",
  );
  const OverallIcon = $derived(statusConfig[overallStatus].icon);

  function getCategoryRequirements(category: Requirement["category"]) {
    return requirements.filter((r) => r.category === category);
  }

  function handleAdjust(req: Requirement) {
    dispatch("adjust", { requirement: req });
  }
</script>

<div class="rounded-xl border border-slate-200 p-4 bg-white {className}">
  <!-- Overall Status Banner -->
  <div class="mb-4 p-4 rounded-lg {statusConfig[overallStatus].bg}">
    <div class="flex items-center gap-3">
      <OverallIcon
        class="w-6 h-6 {statusConfig[overallStatus].color} {statusConfig[
          overallStatus
        ].animate
          ? 'animate-spin'
          : ''}"
      />
      <div>
        <h3 class="font-semibold text-slate-900">
          {#if isChecking}
            Checking Requirements...
          {:else if allPassing}
            All Requirements Met
          {:else if hasFailures}
            Some Requirements Not Met
          {:else if hasWarnings}
            Requirements Met with Warnings
          {:else}
            Requirements Not Yet Checked
          {/if}
        </h3>
        <p class="text-sm text-slate-600">
          {#if !workerConnected}
            Connect a worker to check hardware requirements
          {:else if isChecking}
            Validating your system configuration...
          {:else if hasFailures}
            Please review the failed requirements below
          {:else if allPassing}
            Your system is ready for deployment
          {/if}
        </p>
      </div>
    </div>
  </div>

  <!-- Requirements by Category -->
  {#each categories as category}
    {@const categoryReqs = getCategoryRequirements(category)}
    {@const CategoryIcon = categoryIcons[category]}
    {#if categoryReqs.length > 0}
      <div class="mb-4">
        <div class="flex items-center gap-2 mb-2">
          <CategoryIcon class="w-4 h-4 text-slate-500" />
          <h4 class="font-medium text-slate-700 capitalize">{category}</h4>
        </div>

        <div class="space-y-2">
          {#each categoryReqs as req}
            {@const config = statusConfig[req.status]}
            {@const RequirementIcon = config.icon}
            <div
              class="flex items-center justify-between p-3 rounded-lg border {config.bg} border-slate-200"
            >
              <div class="flex items-center gap-3">
                <RequirementIcon
                  class="w-5 h-5 {config.color} {config.animate
                    ? 'animate-spin'
                    : ''}"
                />
                <div>
                  <span class="font-medium text-slate-800">{req.name}</span>
                  <div class="text-sm text-slate-500">
                    {#if req.current !== undefined}
                      Current: <span class="font-mono">{req.current}</span>
                    {/if}
                    {#if req.minimum}
                      <span class="mx-1">•</span>
                      Required: <span class="font-mono">{req.minimum}</span>
                    {/if}
                    {#if req.recommended}
                      <span class="mx-1">•</span>
                      Recommended:
                      <span class="font-mono">{req.recommended}</span>
                    {/if}
                  </div>
                  {#if req.message}
                    <p class="text-xs {config.color} mt-1">{req.message}</p>
                  {/if}
                </div>
              </div>

              {#if req.status === "fail" || req.status === "warn"}
                <button
                  onclick={() => handleAdjust(req)}
                  class="px-3 py-1 text-sm font-medium text-blue-600 hover:bg-blue-50 rounded-lg transition-colors"
                >
                  Adjust Config
                </button>
              {/if}
            </div>
          {/each}
        </div>
      </div>
    {/if}
  {/each}

  <!-- Action Buttons -->
  {#if !isChecking && (hasFailures || !workerConnected)}
    <div class="flex gap-3 mt-4">
      {#if onRetryCheck}
        <button
          onclick={() => dispatch("retry")}
          class="flex items-center gap-2 px-4 py-2 text-sm font-medium text-blue-600 bg-blue-50 hover:bg-blue-100 rounded-lg transition-colors"
        >
          <Loader2 class="w-4 h-4" />
          Retry Check
        </button>
      {/if}
    </div>
  {/if}
</div>
