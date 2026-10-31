<!--
  New use cases, wherever they run: the creation wizard with the person's
  existing servers offered on its Server step. There is one homelab per
  person; the use cases go onto one of its servers or onto a new server.

  Guided answers handed over with a use case become settings against the
  catalog first, so the wizard opens with the same selection.
-->
<script lang="ts">
  import { onMount, untrack } from "svelte";
  import {
    resolveSetup,
    setupChanges,
    setupFlowFromCatalog,
  } from "@kombiverselabs/ui/usecase";
  import {
    loadUseCaseCatalog,
    type UseCaseCatalogView,
  } from "#lib/api/useCaseCatalog.js";
  import { supportsCreationChoice } from "#lib/wizard/creationGoals.js";
  import type { StackConfig } from "#lib/wizard/types.js";
  import type { OperatorSelfDisclosure } from "#lib/api/operatorProfile.js";
  import { useCreationEntry } from "#lib/creation/host.js";
  import { tr } from "#lib/i18n.svelte.js";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import ConfigurationFlow from "./ConfigurationFlow.svelte";

  let {
    initialConfig,
    selfDisclosure = null,
    onSelfDisclosureSettled,
    preferredServerId = "",
  }: {
    /** The use cases to place; without them the Goals step asks. */
    initialConfig?: StackConfig;
    selfDisclosure?: OperatorSelfDisclosure | null;
    onSelfDisclosureSettled?: () => void;
    /** The server the owner started from, chosen first when it can be used. */
    preferredServerId?: string;
  } = $props();

  const embedded = useCreationEntry() !== null;
  const needsResolution = untrack(
    () => Object.keys(initialConfig?.useCaseSetupAnswers ?? {}).length > 0,
  );
  let resolvedConfig = $state<StackConfig | undefined>(
    untrack(() => initialConfig),
  );
  let resolved = $state(!needsResolution);

  onMount(() => {
    if (!needsResolution) return;
    // A catalog that cannot be read leaves the answers to the wizard's own
    // Goals step; it never blocks the creation.
    void loadUseCaseCatalog()
      .then((catalog) => {
        if (initialConfig)
          resolvedConfig = resolveSetupAnswers(initialConfig, catalog);
      })
      .catch(() => undefined)
      .finally(() => {
        resolved = true;
      });
  });

  function resolveSetupAnswers(
    config: StackConfig,
    catalog: Map<string, UseCaseCatalogView>,
  ): StackConfig {
    const next = {
      ...config,
      useCaseSettings: { ...config.useCaseSettings },
      useCaseCapabilities: { ...config.useCaseCapabilities },
    };
    for (const [goal, answers] of Object.entries(
      config.useCaseSetupAnswers ?? {},
    )) {
      const view = catalog.get(goal);
      const flow = setupFlowFromCatalog(goal, view);
      if (!flow) continue;
      const changes = setupChanges(
        view,
        resolveSetup(flow, answers),
        next.useCaseSettings[goal],
      );
      next.useCaseSettings[goal] = { ...next.useCaseSettings[goal] };
      for (const [id, value] of changes.settings) {
        if (supportsCreationChoice(view, id, value))
          next.useCaseSettings[goal][id] = value;
      }
      if (view?.capabilities?.length)
        next.useCaseCapabilities[goal] = changes.capabilities;
    }
    return next;
  }
</script>

{#if !resolved}
  <section class="mx-auto max-w-6xl p-6 md:p-8">
    <LoadingNote label={tr("common.loading")} />
  </section>
{:else}
  <ConfigurationFlow
    initialConfig={resolvedConfig}
    {embedded}
    {selfDisclosure}
    {onSelfDisclosureSettled}
    chooseServer
    {preferredServerId}
  />
{/if}
