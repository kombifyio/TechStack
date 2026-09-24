<script lang="ts">
  import {
    generateSSHKey,
    downloadKey,
    isSSHKeyGenAvailable,
    type SSHKeyAlgorithm,
    type SSHKeyPair,
  } from "#lib/wallet/sshKeygen.js";
  import Modal from "./Modal.svelte";

  interface Props {
    onSave: (
      name: string,
      publicKey: string,
      privateKey: string,
    ) => Promise<void>;
    onClose: () => void;
  }

  let { onSave, onClose }: Props = $props();

  let name = $state("");
  let algorithm = $state<SSHKeyAlgorithm>("ed25519");
  let generating = $state(false);
  let saving = $state(false);
  let error = $state<string | null>(null);
  let keyPair = $state<SSHKeyPair | null>(null);
  let step = $state<"configure" | "generated">("configure");

  const algorithms: Array<{
    value: SSHKeyAlgorithm;
    label: string;
    description: string;
  }> = [
    {
      value: "ed25519",
      label: "Ed25519",
      description: "Modern, fast, secure (recommended)",
    },
    {
      value: "rsa-4096",
      label: "RSA 4096-bit",
      description: "Maximum compatibility, stronger",
    },
    {
      value: "rsa-2048",
      label: "RSA 2048-bit",
      description: "Wide compatibility, standard strength",
    },
  ];

  const cryptoAvailable = isSSHKeyGenAvailable();

  async function handleGenerate() {
    error = null;

    if (!name.trim()) {
      error = "Please enter a name for this key";
      return;
    }

    generating = true;
    try {
      keyPair = await generateSSHKey(algorithm);
      step = "generated";
    } catch (err) {
      error = err instanceof Error ? err.message : "Key generation failed";
    } finally {
      generating = false;
    }
  }

  function handleDownloadPrivate() {
    if (!keyPair) return;
    const filename =
      algorithm === "ed25519"
        ? `${name.toLowerCase().replace(/\s+/g, "_")}_ed25519`
        : `${name.toLowerCase().replace(/\s+/g, "_")}_rsa`;
    downloadKey(keyPair.privateKey, filename, true);
  }

  function handleDownloadPublic() {
    if (!keyPair) return;
    const filename =
      algorithm === "ed25519"
        ? `${name.toLowerCase().replace(/\s+/g, "_")}_ed25519.pub`
        : `${name.toLowerCase().replace(/\s+/g, "_")}_rsa.pub`;
    downloadKey(keyPair.publicKey, filename, false);
  }

  async function handleSaveToWallet() {
    if (!keyPair) return;
    saving = true;
    error = null;

    try {
      await onSave(name, keyPair.publicKey, keyPair.privateKey);
      onClose();
    } catch (err) {
      error = err instanceof Error ? err.message : "Failed to save key";
    } finally {
      saving = false;
    }
  }

  function handleBack() {
    keyPair = null;
    step = "configure";
  }

  let copiedPublic = $state(false);
  let copiedPrivate = $state(false);

  function copyToClipboard(text: string, isPrivate: boolean) {
    navigator.clipboard.writeText(text);
    if (isPrivate) {
      copiedPrivate = true;
      setTimeout(() => (copiedPrivate = false), 2000);
    } else {
      copiedPublic = true;
      setTimeout(() => (copiedPublic = false), 2000);
    }
  }
</script>

<Modal
  title={step === "configure" ? "Generate SSH Key" : "Key Generated"}
  {onClose}
  maxWidth="2xl"
>
  {#if step === "configure"}
    {#if !cryptoAvailable}
      <div
        class="mb-6 p-4 rounded-xl border border-destructive/30 bg-destructive/5 text-destructive"
      >
        SSH key generation requires HTTPS and a modern browser with Web Crypto
        API support.
      </div>
    {/if}

    {#if error}
      <div
        class="mb-4 p-3 rounded-xl border border-destructive/30 bg-destructive/5 text-destructive text-sm"
      >
        {error}
      </div>
    {/if}

    <div class="space-y-6">
      <!-- Key Name -->
      <div>
        <label
          for="key-name"
          class="block text-sm font-medium text-foreground mb-2"
        >
          Key Name <span class="text-destructive">*</span>
        </label>
        <input
          id="key-name"
          type="text"
          bind:value={name}
          placeholder="e.g., Production Server, GitHub Deploy"
          class="w-full px-4 py-3 bg-input border border-border rounded-lg text-foreground placeholder-muted-foreground focus:border-primary focus:outline-none"
        />
      </div>

      <!-- Algorithm Selection -->
      <fieldset>
        <legend class="block text-sm font-medium text-foreground mb-3">
          Algorithm
        </legend>
        <div class="space-y-2">
          {#each algorithms as algo}
            <button
              type="button"
              onclick={() => (algorithm = algo.value)}
              disabled={!cryptoAvailable}
              class="w-full p-4 rounded-lg border text-left transition-colors disabled:opacity-50 {algorithm ===
              algo.value
                ? 'bg-primary/10 border-primary/50'
                : 'bg-card border-border hover:border-muted-foreground'}"
            >
              <div class="flex items-center justify-between">
                <div>
                  <span class="text-foreground font-medium">{algo.label}</span>
                  <p class="text-sm text-muted-foreground mt-0.5">
                    {algo.description}
                  </p>
                </div>
                {#if algorithm === algo.value}
                  <span class="text-primary">✓</span>
                {/if}
              </div>
            </button>
          {/each}
        </div>
      </fieldset>

      <div
        class="p-4 rounded-xl border border-info/30 bg-info/5 text-sm text-info"
      >
        <strong>Tip:</strong> Ed25519 is recommended for most use cases. Use RSA only
        if you need compatibility with older systems.
      </div>
    </div>

    <div class="mt-6 flex justify-end gap-3">
      <button
        onclick={onClose}
        data-kx="control"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        Cancel
      </button>
      <button
        onclick={handleGenerate}
        disabled={generating || !cryptoAvailable || !name.trim()}
        data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        {#if generating}
          <span class="flex items-center gap-2">
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
            Generating...
          </span>
        {:else}
          Generate
        {/if}
      </button>
    </div>
  {:else if step === "generated" && keyPair}
    {#if error}
      <div
        class="mb-4 p-3 rounded-xl border border-destructive/30 bg-destructive/5 text-destructive text-sm"
      >
        {error}
      </div>
    {/if}

    <div class="space-y-4">
      <!-- Key Info -->
      <div
        class="p-4 rounded-xl border border-success/30 bg-success/5 text-success"
      >
        <div class="flex items-center gap-2 mb-2">
          <svg
            class="w-5 h-5"
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
          <span class="font-medium">Key pair generated successfully</span>
        </div>
        <div class="text-sm text-success/80 space-y-1">
          <p><strong>Name:</strong> {name}</p>
          <p>
            <strong>Algorithm:</strong>
            {algorithms.find((a) => a.value === keyPair?.algorithm)?.label}
          </p>
          <p><strong>Fingerprint:</strong> {keyPair.fingerprint}</p>
        </div>
      </div>

      <!-- Public Key -->
      <div>
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-foreground">Public Key</span>
          <div class="flex gap-2">
            <button
              onclick={() => copyToClipboard(keyPair!.publicKey, false)}
              class="text-xs text-muted-foreground hover:text-foreground"
            >
              {copiedPublic ? "✓ Copied" : "Copy"}
            </button>
            <button
              onclick={handleDownloadPublic}
              class="text-xs text-primary hover:text-primary"
            >
              Download .pub
            </button>
          </div>
        </div>
        <div
          class="p-3 rounded-lg bg-muted/30 border border-border font-mono text-xs text-foreground break-all max-h-24 overflow-y-auto"
        >
          {keyPair.publicKey}
        </div>
        <p class="text-xs text-muted-foreground mt-1">
          Add this to ~/.ssh/authorized_keys on your servers
        </p>
      </div>

      <!-- Private Key -->
      <div>
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm font-medium text-foreground">Private Key</span>
          <div class="flex gap-2">
            <button
              onclick={() => copyToClipboard(keyPair!.privateKey, true)}
              class="text-xs text-muted-foreground hover:text-foreground"
            >
              {copiedPrivate ? "✓ Copied" : "Copy"}
            </button>
            <button
              onclick={handleDownloadPrivate}
              class="text-xs text-primary hover:text-primary"
            >
              Download
            </button>
          </div>
        </div>
        <div
          class="p-3 rounded-lg bg-muted/30 border border-border font-mono text-xs text-foreground break-all max-h-32 overflow-y-auto"
        >
          {keyPair.privateKey}
        </div>
      </div>

      <div
        class="p-3 rounded-xl border border-destructive/30 bg-destructive/5 text-sm text-destructive"
      >
        <strong>Important:</strong> Download and securely store your private key now.
        It cannot be recovered if lost. Never share your private key.
      </div>
    </div>

    <div class="mt-6 flex justify-end gap-3">
      <button
        onclick={onClose}
        data-kx="control"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        Close
      </button>
      <button
        onclick={handleSaveToWallet}
        disabled={saving}
        data-kx="control"
        data-variant="primary"
        class="inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg px-4 py-2 text-sm font-medium disabled:pointer-events-none disabled:opacity-50"
      >
        {#if saving}
          Saving...
        {:else}
          Save to Wallet
        {/if}
      </button>
    </div>
  {/if}
</Modal>
