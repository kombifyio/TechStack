import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const coreEnglishFiles = [
  "src/routes/stacks/+page.svelte",
  "src/routes/stacks/creating/+page.svelte",
  "src/routes/stacks/[id]/servers/new/+page.svelte",
  "src/routes/settings/+page.svelte",
  "src/lib/components/wizard/EasyWizard.svelte",
  "src/lib/wizard/provider-errors.ts",
  "src/lib/wizard/requirements.ts",
  "src/lib/wizard/task-updates.ts",
];

const germanLanguageMarker =
  /[äöüÄÖÜß]|Heartbeat und Metriken|Alle anzeigen|Hilfe & Ressourcen|\b(?:Die|Der|Das|Ein|Eine|Dieser|Diese|Warte|Prüfe|Starte|Lade|Sieh|Erstelle|Stelle|Mindestens|Lokaler|Bestehender|Vollständiges|Baut|Räume|Aufräumen|Löschen|Verwaiste|Entfernt|Aktueller|Serverzugriff|Fehlercode|Nächster Schritt|gemeldet)\b/;

describe("core product language contract", () => {
  for (const file of coreEnglishFiles) {
    it(`${file} contains no hard-coded German copy`, () => {
      const source = readFileSync(resolve(process.cwd(), file), "utf8");
      expect(source).not.toMatch(germanLanguageMarker);
    });
  }
});
