<script lang="ts">
  import {
    getAvailableLocales,
    getLocale,
    isLocale,
    setLocale,
    tr,
  } from "#lib/i18n.svelte.js";

  let {
    label = tr("language.switch"),
    class: className = "",
  }: { label?: string; class?: string } = $props();
</script>

<select
  class={className}
  aria-label={label}
  value={getLocale()}
  onchange={(event) => {
    const value = event.currentTarget.value;
    if (isLocale(value)) setLocale(value);
  }}
  data-testid="language-switcher"
>
  {#each getAvailableLocales() as locale (locale.code)}
    <option value={locale.code} lang={locale.code}>{locale.name}</option>
  {/each}
</select>
