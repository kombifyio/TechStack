<script lang="ts">
  import { createEventDispatcher } from "svelte";
  import {
    FileCode,
    Copy,
    Download,
    CheckCircle,
    Eye,
    EyeOff,
    ChevronDown,
    ChevronUp,
  } from "@lucide/svelte";
  import { toast } from "$lib/stores/toast";

  interface GeneratedFile {
    name: string;
    content: string;
    language: "hcl" | "json" | "yaml";
    size: number;
  }

  interface Props {
    files: GeneratedFile[];
    mode?: "simple" | "advanced";
    stackName?: string;
    showContent?: boolean;
    class?: string;
  }

  let {
    files = [],
    mode = "simple",
    stackName = "stack",
    showContent = true,
    class: className = "",
  }: Props = $props();

  const dispatch = createEventDispatcher<{
    download: { files: GeneratedFile[] };
    copy: { file: GeneratedFile };
  }>();

  let expandedFile = $state<string | null>(null);
  let showAllContent = $derived(showContent);

  const fileCategories: Record<
    string,
    { category: string; description: string }
  > = {
    "main.tf": {
      category: "Core",
      description: "Provider and locals configuration",
    },
    "variables.tf": {
      category: "Core",
      description: "Input variables definition",
    },
    "bootstrap.tf": {
      category: "Bootstrap",
      description: "OS preparation and Docker setup",
    },
    "network.tf": {
      category: "Network",
      description: "Network configuration and Traefik",
    },
    "network_local.tf": {
      category: "Network",
      description: "Local network mode (self-signed TLS)",
    },
    "network_public.tf": {
      category: "Network",
      description: "Public network mode (ACME TLS)",
    },
    "network_hybrid.tf": {
      category: "Network",
      description: "Hybrid network mode",
    },
    "services.tf": {
      category: "Services",
      description: "Docker container definitions",
    },
    "outputs.tf": {
      category: "Outputs",
      description: "Service URLs and deployment info",
    },
    "terraform.tfvars.json": {
      category: "Variables",
      description: "Resolved variable values",
    },
    "terramate.tm.hcl": {
      category: "Orchestration",
      description: "Terramate stack configuration",
    },
  };

  function getFileInfo(name: string) {
    return (
      fileCategories[name] ?? {
        category: "Other",
        description: "Generated configuration",
      }
    );
  }

  function formatSize(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    return `${(bytes / 1024).toFixed(1)} KB`;
  }

  async function copyToClipboard(file: GeneratedFile) {
    try {
      await navigator.clipboard.writeText(file.content);
      toast.success(`Copied ${file.name} to clipboard`);
      dispatch("copy", { file });
    } catch (err) {
      toast.error("Failed to copy to clipboard");
    }
  }

  function downloadFile(file: GeneratedFile) {
    const blob = new Blob([file.content], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = file.name;
    a.click();
    URL.revokeObjectURL(url);
  }

  function downloadAll() {
    dispatch("download", { files });
    // Could use JSZip here for a zip download
    files.forEach((file) => downloadFile(file));
  }

  function toggleFile(name: string) {
    expandedFile = expandedFile === name ? null : name;
  }

  const totalSize = $derived.by(() =>
    files.reduce((sum, f) => sum + f.size, 0),
  );
  const categorizedFiles = $derived.by(() =>
    files.reduce(
      (acc, file) => {
        const info = getFileInfo(file.name);
        if (!acc[info.category]) acc[info.category] = [];
        acc[info.category].push(file);
        return acc;
      },
      {} as Record<string, GeneratedFile[]>,
    ),
  );
</script>

<div class="rounded-xl border border-slate-200 p-4 bg-white {className}">
  <!-- Header -->
  <div class="flex items-center justify-between mb-4">
    <div>
      <h3 class="font-semibold text-slate-900 flex items-center gap-2">
        <FileCode class="w-5 h-5 text-blue-500" />
        Generated IaC Files
      </h3>
      <p class="text-sm text-slate-500">
        {files.length} files • {formatSize(totalSize)} total • Mode: {mode}
      </p>
    </div>
    <div class="flex items-center gap-2">
      <button
        type="button"
        onclick={() => (showAllContent = !showAllContent)}
        class="p-2 text-slate-500 hover:text-slate-700 hover:bg-slate-100 rounded-lg transition-colors"
        title={showAllContent ? "Hide content" : "Show content"}
      >
        {#if showAllContent}
          <EyeOff class="w-4 h-4" />
        {:else}
          <Eye class="w-4 h-4" />
        {/if}
      </button>
      <button
        type="button"
        onclick={downloadAll}
        class="flex items-center gap-2 px-3 py-2 text-sm font-medium text-blue-600 bg-blue-50 hover:bg-blue-100 rounded-lg transition-colors"
      >
        <Download class="w-4 h-4" />
        Download All
      </button>
    </div>
  </div>

  <!-- Files by Category -->
  {#each Object.entries(categorizedFiles) as [category, categoryFiles]}
    <div class="mb-4">
      <h4 class="text-sm font-medium text-slate-600 mb-2">{category}</h4>
      <div class="space-y-2">
        {#each categoryFiles as file}
          {@const info = getFileInfo(file.name)}
          {@const isExpanded = expandedFile === file.name}

          <div
            class="file-card rounded-lg border border-slate-200 overflow-hidden"
          >
            <!-- File Header -->
            <div
              class="w-full flex items-center justify-between p-3 bg-slate-50 hover:bg-slate-100 transition-colors"
            >
              <button
                type="button"
                onclick={() => toggleFile(file.name)}
                class="flex min-w-0 flex-1 items-center justify-between gap-3 text-left"
              >
                <div class="flex min-w-0 items-center gap-3">
                  <FileCode class="w-4 h-4 text-slate-400 flex-shrink-0" />
                  <div class="min-w-0">
                    <span class="font-mono text-sm font-medium text-slate-800"
                      >{file.name}</span
                    >
                    <span class="text-xs text-slate-400 ml-2"
                      >{formatSize(file.size)}</span
                    >
                  </div>
                </div>
                {#if showAllContent}
                  {#if isExpanded}
                    <ChevronUp class="w-4 h-4 text-slate-400 flex-shrink-0" />
                  {:else}
                    <ChevronDown class="w-4 h-4 text-slate-400 flex-shrink-0" />
                  {/if}
                {/if}
              </button>

              <div class="flex items-center gap-2 pl-2">
                <button
                  type="button"
                  onclick={() => copyToClipboard(file)}
                  class="p-1.5 text-slate-400 hover:text-slate-600 hover:bg-slate-200 rounded transition-colors"
                  title="Copy to clipboard"
                  aria-label="Copy to clipboard"
                >
                  <Copy class="w-4 h-4" />
                </button>
                <button
                  type="button"
                  onclick={() => downloadFile(file)}
                  class="p-1.5 text-slate-400 hover:text-slate-600 hover:bg-slate-200 rounded transition-colors"
                  title="Download file"
                  aria-label="Download file"
                >
                  <Download class="w-4 h-4" />
                </button>
              </div>
            </div>

            <!-- File Content (when expanded) -->
            {#if showAllContent && isExpanded}
              <div class="p-3 bg-slate-900 overflow-x-auto">
                <pre
                  class="text-xs font-mono text-slate-300 whitespace-pre-wrap max-h-96 overflow-y-auto">{file.content}</pre>
              </div>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/each}

  <!-- Summary -->
  <div class="mt-4 p-3 bg-blue-50 rounded-lg">
    <div class="flex items-start gap-2">
      <CheckCircle class="w-5 h-5 text-blue-500 flex-shrink-0 mt-0.5" />
      <div class="text-sm text-blue-800">
        <p class="font-medium">Ready for Deployment</p>
        <p class="text-blue-600">
          These files will be applied to your worker using OpenTofu.
          {#if mode === "advanced"}
            Terramate orchestration is enabled for advanced stack management.
          {/if}
        </p>
      </div>
    </div>
  </div>
</div>
