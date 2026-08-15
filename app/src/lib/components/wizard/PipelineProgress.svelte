<script lang="ts">
  import { createEventDispatcher, onMount } from "svelte";
  import {
    CheckCircle,
    Circle,
    Loader2,
    FileCode,
    Server,
    Network,
    Rocket,
    ChevronDown,
    ChevronUp,
  } from "@lucide/svelte";

  interface Phase {
    id: string;
    name: string;
    description: string;
    status: "pending" | "running" | "success" | "failed" | "skipped";
    duration?: number;
    output?: string[];
    expanded?: boolean;
  }

  interface Props {
    phases: Phase[];
    currentPhase?: string;
    showDetails?: boolean;
    class?: string;
  }

  let {
    phases = [],
    currentPhase = "",
    showDetails = true,
    class: className = "",
  }: Props = $props();

  const dispatch = createEventDispatcher<{
    phaseClick: { phase: Phase };
  }>();

  const phaseIcons = {
    "pre-validation": FileCode,
    "stackkit-resolution": Server,
    "addon-detection": Server,
    "unifier-engine": Server,
    "iac-generation": FileCode,
    "worker-validation": Server,
    "network-setup": Network,
    deployment: Rocket,
  } as const;

  const statusConfig = {
    pending: {
      icon: Circle,
      color: "text-slate-300",
      ringColor: "ring-slate-200",
    },
    running: {
      icon: Loader2,
      color: "text-blue-500",
      ringColor: "ring-blue-300",
      animate: true,
    },
    success: {
      icon: CheckCircle,
      color: "text-green-500",
      ringColor: "ring-green-300",
    },
    failed: { icon: Circle, color: "text-red-500", ringColor: "ring-red-300" },
    skipped: {
      icon: Circle,
      color: "text-slate-300",
      ringColor: "ring-slate-200",
    },
  };

  function getPhaseIcon(phaseId: string) {
    if (phaseId in phaseIcons) {
      return phaseIcons[phaseId as keyof typeof phaseIcons];
    }
    return Circle;
  }

  function formatDuration(ms?: number): string {
    if (!ms) return "";
    if (ms < 1000) return `${ms}ms`;
    return `${(ms / 1000).toFixed(1)}s`;
  }

  function togglePhase(phase: Phase) {
    phase.expanded = !phase.expanded;
    dispatch("phaseClick", { phase });
  }

  const completedCount = $derived.by(
    () => phases.filter((p) => p.status === "success").length,
  );
  const totalCount = $derived.by(
    () => phases.filter((p) => p.status !== "skipped").length,
  );
  const progress = $derived.by(() =>
    totalCount > 0 ? (completedCount / totalCount) * 100 : 0,
  );
</script>

<div class="rounded-xl border border-slate-200 p-4 bg-white {className}">
  <!-- Progress Header -->
  <div class="flex items-center justify-between mb-4">
    <div>
      <h3 class="font-semibold text-slate-900">Unifier Pipeline</h3>
      <p class="text-sm text-slate-500">
        {completedCount} of {totalCount} phases complete
      </p>
    </div>
    <div class="text-right">
      <span class="text-2xl font-bold text-blue-600"
        >{Math.round(progress)}%</span
      >
    </div>
  </div>

  <!-- Progress Bar -->
  <div class="w-full h-2 bg-slate-100 rounded-full mb-6 overflow-hidden">
    <div
      class="h-full bg-gradient-to-r from-blue-500 to-green-500 rounded-full transition-all duration-500"
      style="width: {progress}%"
    ></div>
  </div>

  <!-- Phase List -->
  <div class="space-y-2">
    {#each phases as phase, index}
      {@const config = statusConfig[phase.status]}
      {@const PhaseIcon = getPhaseIcon(phase.id)}
      {@const isActive = phase.id === currentPhase}

      <div
        class="phase-item rounded-lg border transition-all duration-200
					{isActive ? 'border-blue-300 bg-blue-50' : 'border-slate-200 bg-white'}
					{phase.status === 'failed' ? 'border-red-300 bg-red-50' : ''}
				"
      >
        <button
          type="button"
          onclick={() => togglePhase(phase)}
          class="w-full flex items-center gap-3 p-3 text-left"
        >
          <!-- Phase Number/Status -->
          <div class="flex-shrink-0 relative">
            <div
              class="w-8 h-8 rounded-full flex items-center justify-center ring-2 {config.ringColor}
								{phase.status === 'success' ? 'bg-green-100' : ''}
								{phase.status === 'running' ? 'bg-blue-100' : ''}
								{phase.status === 'failed' ? 'bg-red-100' : ''}
								{phase.status === 'pending' ? 'bg-slate-50' : ''}
							"
            >
              {#if phase.status === "running"}
                <Loader2 class="w-4 h-4 {config.color} animate-spin" />
              {:else if phase.status === "success"}
                <CheckCircle class="w-4 h-4 {config.color}" />
              {:else}
                <span class="text-sm font-medium text-slate-500"
                  >{index + 1}</span
                >
              {/if}
            </div>

            <!-- Connector Line -->
            {#if index < phases.length - 1}
              <div
                class="absolute top-8 left-1/2 -translate-x-1/2 w-0.5 h-4
									{phases[index + 1].status !== 'pending' ? 'bg-green-300' : 'bg-slate-200'}
								"
              ></div>
            {/if}
          </div>

          <!-- Phase Info -->
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2">
              <PhaseIcon class="w-4 h-4 text-slate-400" />
              <span class="font-medium text-slate-800">{phase.name}</span>
              {#if phase.duration}
                <span class="text-xs text-slate-400"
                  >{formatDuration(phase.duration)}</span
                >
              {/if}
            </div>
            <p class="text-sm text-slate-500 truncate">{phase.description}</p>
          </div>

          <!-- Expand/Collapse -->
          {#if showDetails && phase.output?.length}
            <div class="flex-shrink-0">
              {#if phase.expanded}
                <ChevronUp class="w-4 h-4 text-slate-400" />
              {:else}
                <ChevronDown class="w-4 h-4 text-slate-400" />
              {/if}
            </div>
          {/if}
        </button>

        <!-- Phase Details (Expanded) -->
        {#if showDetails && phase.expanded && phase.output?.length}
          <div class="px-3 pb-3 pt-0">
            <div
              class="ml-11 p-2 bg-slate-900 rounded-lg text-xs font-mono text-slate-300 overflow-x-auto max-h-32 overflow-y-auto"
            >
              {#each phase.output as line}
                <div class="whitespace-pre">{line}</div>
              {/each}
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
</div>
