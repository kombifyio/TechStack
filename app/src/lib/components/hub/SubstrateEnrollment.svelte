<script lang="ts">
  import { API_BASE, fetchApi } from "#lib/api/client.js";
  import { parseApiError } from "#lib/api/errors.js";

  // A hypervisor belongs to the owner's infrastructure and may precede the
  // first deployment. Keep the old prop accepted for existing Hub callers.
  let {
    deploymentId: _deploymentId,
    flat = false,
  }: { deploymentId?: string; flat?: boolean } = $props();
  let command = $state("");
  let expiresAt = $state("");
  let pending = $state(false);
  let error = $state("");

  async function prepare() {
    pending = true;
    error = "";
    command = "";
    try {
      const origin = new URL(API_BASE || window.location.origin).origin;
      const shellOrigin = origin.replaceAll("'", "'\\''");
      const result = await fetchApi<{ token: string; expires_at: string }>(
        "/api/v1/trust/pairing-tokens",
        {
          method: "POST",
          body: JSON.stringify({
            name: "Proxmox hypervisor connection",
            server_provisioning_mode: "install-command",
            environment_class: "local",
            node_role: "substrate",
            services: [],
          }),
        },
      );
      if (
        !result.data?.token ||
        !/^kpt1\.[A-Za-z0-9_.-]+$/.test(result.data.token)
      ) {
        throw new Error("A connection command could not be prepared.");
      }
      // Only URL origins and the closed token alphabet enter the shell command.
      command = `curl -fsSL '${shellOrigin}/install.sh' | KOMBI_SERVER='${shellOrigin}' KOMBI_TOKEN='${result.data.token}' TECHSTACK_AS_SERVICE=1 bash`;
      expiresAt = result.data.expires_at;
    } catch (cause) {
      error =
        parseApiError(cause).message || "Could not prepare the connection.";
    } finally {
      pending = false;
    }
  }
</script>

<section
  class={flat
    ? "space-y-4 border-t border-border py-5"
    : "space-y-4 rounded-lg border border-border bg-card p-6"}
  data-testid="substrate-enrollment"
>
  <h2 class="text-lg font-semibold">Connect a Proxmox hypervisor</h2>
  <p>
    Run the connection command in the Proxmox host shell as an administrator.
    Its Guard reports health to your Homelab through an outbound connection.
  </p>
  <p class="text-sm text-muted-foreground">
    The hypervisor hosts your Nodes. StackKits run inside Ubuntu guests. Guest
    management is enabled after its local Proxmox client is configured and you
    authorize the connection. Existing guests stay outside deletion custody.
  </p>
  <button
    class="rounded-lg bg-primary px-4 py-2 text-primary-foreground disabled:opacity-50"
    disabled={pending}
    onclick={prepare}
  >
    {pending
      ? "Preparing..."
      : command
        ? "Generate a new command"
        : "Generate connection command"}
  </button>
  {#if error}<p role="alert" class="text-destructive">{error}</p>{/if}
  {#if command}
    <pre
      class="overflow-x-auto rounded bg-muted p-4 text-sm"
      data-testid="substrate-registration-command">{command}</pre>
    <p class="text-sm text-muted-foreground">
      Expires: {expiresAt}. The command is shown only here and is not retained
      in job reports.
    </p>
    <a class="underline" href="/dashboard">View connected Nodes</a>
  {/if}
</section>
