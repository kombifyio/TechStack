// Customer-visible strings of the layout recommendation: the "Recommended
// distribution" panel, one message per reason code the Layout Planner can emit
// (`pkg/layout/explain.go` reasonTemplates, same {placeholders}), the
// "Add use cases" flow and the "Why this setup" reasons of the wizard's hint
// zone. The reason code is the localization authority; an unknown code renders
// the server's English text. Merged into every locale by i18n.ts.
export const layoutMessages = {
  en: {
    "layout.panel.title": "Recommended distribution",
    "layout.panel.intro":
      "kombify suggests which server runs each app. Finishing this setup accepts it.",
    "layout.panel.loading": "Working out where each app should run…",
    "layout.panel.updating": "Updating…",
    "layout.panel.mainServer": "Main server",
    "layout.panel.newServer": "New server",
    "layout.panel.reserved": "{used} of {total} GB of memory reserved",
    "layout.panel.nothingNew": "No new apps run here.",
    "layout.panel.moreReasons": "More about this choice",
    "layout.panel.staysTitle": "Staying where they are",
    "layout.panel.staysLine": "{app} on {node}",
    "layout.panel.notesTitle": "Good to know",
    "layout.panel.conflictsTitle": "What doesn't fit yet",
    "layout.panel.notCurrent": "This recommendation is no longer current.",
    "layout.availability.always-on": "Always on",
    "layout.availability.part-time": "Part-time",
    "layout.verification.verified": "Checked against your hardware",
    "layout.verification.infeasible":
      "This doesn't fit your hardware as it is, so it isn't offered as a recommendation.",
    "layout.verification.unknown":
      "We couldn't confirm yet that this fits your hardware, so it isn't offered as a recommendation.",
    "layout.verification.unavailable":
      "The hardware check isn't available right now, so this is for information only.",
    "layout.verification.skipped":
      "The hardware check was skipped, so this is for information only.",
    "layout.verification.details": "Details",
    "layout.reason.data_budget_unset":
      "How much space your data needs on {node} isn't set yet, so free disk space was not checked for it.",
    "layout.outcome.proven-infeasible":
      "Not everything you picked fits your servers as they are.",
    "layout.outcome.unknown":
      "We can't tell yet whether everything you picked fits.",
    "layout.outcome.budget-exhausted":
      "We couldn't finish checking every way to distribute your apps.",
    "layout.state.noSetup":
      "A distribution can be recommended once your homelab has finished its first setup.",
    "layout.state.invalid":
      "Some of your choices can't be distributed together yet.",
    "layout.state.unavailable":
      "A recommended distribution isn't available right now. You can still continue.",
    "layout.summary.title": "Where your apps will run",
    "layout.summary.applies": "Finishing this setup applies this distribution.",
    "layout.summary.line": "{node}: {apps}",

    "layout.axis.cpu": "processor",
    "layout.axis.ram": "memory",
    "layout.axis.storage": "storage",
    "layout.axis.cpu_reservation": "processor needs",
    "layout.axis.roles": "server role",
    "layout.axis.cpu_microarchitecture": "processor generation",
    "layout.axis.cpu_architecture": "processor type",
    "layout.axis.accelerator_vendor": "graphics card brand",

    "layout.reason.runs_on_always_on":
      "{title} is one of your most important apps, so it runs on {node}, which has been on around the clock.",
    "layout.reason.prefers_always_on":
      "{title} runs on {node} because it has been on around the clock.",
    "layout.reason.avoids_part_time":
      "{title} avoids {rival}, which is often switched off.",
    "layout.reason.important_tight_fit":
      "{title} is important and small, so it shares {node} with your other important apps and leaves {rival} free for heavier ones.",
    "layout.reason.kept_apart":
      "{title} runs on {node}, apart from your important apps, so its load cannot slow them down.",
    "layout.reason.tight_fit":
      "{node} has room for {title} and keeps more space free on {rival}.",
    "layout.reason.verified_hardware":
      "{node} has confirmed the hardware that {title} depends on.",
    "layout.reason.equal_choice":
      "{node} and {rival} suit {title} equally well, so kombify picked {node}.",
    "layout.reason.only_fitting_node":
      "{node} is the only server that has what {title} needs.",
    "layout.reason.profile_not_measured":
      "kombify has not measured {title} yet, so it stays on your main server, {node}.",
    "layout.reason.pinned": "{title} stays on {node}.",
    "layout.reason.with_use_case":
      "{title} adds to the use case that already runs on {node}, so it runs there too.",
    "layout.reason.repacked":
      "{title} runs on {node} so that every app you picked fits.",
    "layout.reason.uses_accelerator":
      "{title} needs a {vendor} graphics card, and {node} has one.",
    "layout.reason.measured_reservation":
      "kombify measured {title}: it reserves {ram} GB of memory.",
    "layout.reason.estimated_profile":
      "kombify has not measured {title} yet, so its size is an estimate.",
    "layout.reason.evidence_unverified":
      "{node} has not reported everything yet ({missing}), so this part is not fully confirmed.",
    "layout.reason.node_not_owner_controlled":
      "{node} isn't one of your own servers, so no app is placed on it.",
    "layout.reason.node_not_connected":
      "{node} is not connected, so it is left out.",
    "layout.reason.node_capacity_unknown":
      "{node} has not reported its hardware yet, so it is left out.",
    "layout.reason.no_eligible_node":
      "None of your connected servers has reported its hardware yet.",
    "layout.reason.infeasibility_unproven_unknown_node":
      "A server that has not reported its hardware yet might still fit, so kombify can't say for sure yet.",
    "layout.reason.needs_more_ram":
      "{title} needs {need} GB of memory, but the most any server has free is {largest} GB.",
    "layout.reason.needs_more_cpu":
      "{title} needs {need} processor cores, but your largest server has {largest}.",
    "layout.reason.needs_more_storage":
      "{title} needs {need} GB of storage, but the most any server has free is {largest} GB.",
    "layout.reason.missing_accelerator":
      "{title} needs a {vendor} graphics card, and none of your servers has one.",
    "layout.reason.pinned_node_lacks_room":
      "{title} is tied to a server that has no room for it.",
    "layout.reason.no_node_meets_requirements":
      "None of your servers has what {title} needs.",
    "layout.reason.selection_exceeds_free_ram":
      "The apps you picked reserve {need} GB of memory, but your servers have {free} GB free.",
    "layout.reason.no_combination_fits":
      "There is no way to distribute the apps you picked so that all of them fit.",

    "layout.addUseCases.pageTitle": "Add use cases | kombify-Techstack",
    "layout.addUseCases.entry": "Add use cases",
    "layout.addUseCases.title": "Add use cases",
    "layout.addUseCases.subtitle":
      "Choose what else your homelab should do. kombify recommends which of your servers runs each app.",
    "layout.addUseCases.submit": "Add use cases",
    "layout.addUseCases.submitting": "Adding use cases…",
    "layout.addUseCases.backToHomelab": "Back to your homelab",
    "layout.addUseCases.accepted.title": "Added to your homelab's plan",
    "layout.addUseCases.accepted.body":
      "Your use cases are saved. They are installed on your servers once the change is applied.",
    "layout.addUseCases.provisioning.title": "Setting up your use cases",
    "layout.addUseCases.provisioning.body":
      "Your homelab is applying the change now.",
    "layout.addUseCases.provisioning.progress": "See progress",
    "layout.addUseCases.superseded.title":
      "Your homelab changed in the meantime",
    "layout.addUseCases.superseded.body":
      "Nothing was changed. The updated recommendation is shown above. Review it and add the use cases again to confirm.",
    "layout.addUseCases.stale.title": "The recommendation is out of date",
    "layout.addUseCases.stale.body":
      "Nothing was changed. Review the distribution again and add the use cases once more.",
    "layout.addUseCases.unavailable.title":
      "The recommendation can't be checked right now",
    "layout.addUseCases.unavailable.body":
      "Nothing was changed. Try again in a moment.",
    "layout.addUseCases.failed.title": "The use cases could not be added",
    "layout.addUseCases.failed.body":
      "Check your choices and try again. Your answers are still here.",
    "wizard.validation.goalsRequired":
      "Choose at least one use case to continue.",

    "wizard.hints.why": "Why this setup",
    "wizard.hints.reason.goal_alignment":
      "It matches the use cases you picked.",
    "wizard.hints.reason.service_alignment":
      "It matches the services you picked.",
    "wizard.hints.reason.provider_alignment":
      "It takes your chosen provider into account.",
    "wizard.hints.reason.safe_standard":
      "Until you choose a provider, kombify uses its standard managed setup.",
    "wizard.hints.reason.operator_capability_band":
      "The amount of detail follows how you use kombify.",
    "wizard.hints.reason.low_compute_profile":
      "One of your servers has little memory, so a lighter setup fits it better.",
    "wizard.hints.reason.cloud_owner_asset":
      "Your kombify account sets up the owner login for you.",
    "wizard.hints.reason.selection_capacity_shortfall":
      "The apps you picked need more than your servers have free right now.",
    "wizard.hints.reason.module_capacity_shortfall":
      "At least one app you picked needs more than any of your servers can give it right now.",
    "wizard.hints.reason.module_capacity_unverified":
      "Not every server has reported its hardware, so we can't tell for sure that everything fits.",
    "wizard.hints.reason.catalog_footprint_unavailable":
      "The size of the apps you picked isn't known yet, so the fit isn't checked.",
  },
  de: {
    "layout.panel.title": "Empfohlene Verteilung",
    "layout.panel.intro":
      "kombify schlägt vor, auf welchem Server welche App läuft. Wenn du diese Einrichtung abschließt, übernimmst du den Vorschlag.",
    "layout.panel.loading": "Es wird ermittelt, wo welche App laufen soll…",
    "layout.panel.updating": "Wird aktualisiert…",
    "layout.panel.mainServer": "Hauptserver",
    "layout.panel.newServer": "Neuer Server",
    "layout.panel.reserved": "{used} von {total} GB Arbeitsspeicher reserviert",
    "layout.panel.nothingNew": "Hier läuft keine neue App.",
    "layout.panel.moreReasons": "Mehr zu dieser Wahl",
    "layout.panel.staysTitle": "Bleiben, wo sie sind",
    "layout.panel.staysLine": "{app} auf {node}",
    "layout.panel.notesTitle": "Gut zu wissen",
    "layout.panel.conflictsTitle": "Was noch nicht passt",
    "layout.panel.notCurrent": "Diese Empfehlung ist nicht mehr aktuell.",
    "layout.availability.always-on": "Immer an",
    "layout.availability.part-time": "Zeitweise an",
    "layout.verification.verified": "Gegen deine Hardware geprüft",
    "layout.verification.infeasible":
      "Das passt so nicht auf deine Hardware und wird deshalb nicht als Empfehlung angeboten.",
    "layout.verification.unknown":
      "Wir konnten noch nicht bestätigen, dass das auf deine Hardware passt, und bieten es deshalb nicht als Empfehlung an.",
    "layout.verification.unavailable":
      "Die Hardware-Prüfung ist gerade nicht verfügbar, die Verteilung dient deshalb nur zur Information.",
    "layout.verification.skipped":
      "Die Hardware-Prüfung wurde übersprungen, die Verteilung dient deshalb nur zur Information.",
    "layout.verification.details": "Details",
    "layout.reason.data_budget_unset":
      "Wie viel Platz deine Daten auf {node} brauchen, ist noch nicht festgelegt, deshalb wurde der freie Speicherplatz dafür nicht geprüft.",
    "layout.outcome.proven-infeasible":
      "Nicht alles, was du ausgewählt hast, passt auf deine Server, so wie sie sind.",
    "layout.outcome.unknown":
      "Wir können noch nicht sagen, ob alles passt, was du ausgewählt hast.",
    "layout.outcome.budget-exhausted":
      "Wir konnten nicht jede Möglichkeit prüfen, deine Apps zu verteilen.",
    "layout.state.noSetup":
      "Eine Verteilung lässt sich empfehlen, sobald dein Homelab seine erste Einrichtung abgeschlossen hat.",
    "layout.state.invalid":
      "Einige deiner Auswahlen lassen sich noch nicht gemeinsam verteilen.",
    "layout.state.unavailable":
      "Eine empfohlene Verteilung ist gerade nicht verfügbar. Du kannst trotzdem weitermachen.",
    "layout.summary.title": "Wo deine Apps laufen werden",
    "layout.summary.applies":
      "Wenn du diese Einrichtung abschließt, wird diese Verteilung angewendet.",
    "layout.summary.line": "{node}: {apps}",

    "layout.axis.cpu": "Prozessor",
    "layout.axis.ram": "Arbeitsspeicher",
    "layout.axis.storage": "Speicherplatz",
    "layout.axis.cpu_reservation": "Prozessorbedarf",
    "layout.axis.roles": "Serverrolle",
    "layout.axis.cpu_microarchitecture": "Prozessorgeneration",
    "layout.axis.cpu_architecture": "Prozessortyp",
    "layout.axis.accelerator_vendor": "Grafikkarten-Hersteller",

    "layout.reason.runs_on_always_on":
      "{title} ist eine deiner wichtigsten Apps und läuft deshalb auf {node}, das rund um die Uhr an war.",
    "layout.reason.prefers_always_on":
      "{title} läuft auf {node}, weil das rund um die Uhr an war.",
    "layout.reason.avoids_part_time":
      "{title} meidet {rival}, das oft ausgeschaltet ist.",
    "layout.reason.important_tight_fit":
      "{title} ist wichtig und klein und teilt sich deshalb {node} mit deinen anderen wichtigen Apps. So bleibt {rival} für größere Apps frei.",
    "layout.reason.kept_apart":
      "{title} läuft auf {node}, getrennt von deinen wichtigen Apps, damit seine Last sie nicht ausbremst.",
    "layout.reason.tight_fit":
      "Auf {node} ist Platz für {title}, und auf {rival} bleibt mehr Raum frei.",
    "layout.reason.verified_hardware":
      "{node} hat die Hardware bestätigt, auf die {title} angewiesen ist.",
    "layout.reason.equal_choice":
      "{node} und {rival} passen gleich gut zu {title}. kombify hat {node} gewählt.",
    "layout.reason.only_fitting_node":
      "{node} ist der einzige Server, der bietet, was {title} braucht.",
    "layout.reason.profile_not_measured":
      "kombify hat {title} noch nicht ausgemessen und lässt es deshalb auf deinem Hauptserver {node}.",
    "layout.reason.pinned": "{title} bleibt auf {node}.",
    "layout.reason.with_use_case":
      "{title} erweitert den Anwendungsfall, der schon auf {node} läuft, und läuft deshalb auch dort.",
    "layout.reason.repacked":
      "{title} läuft auf {node}, damit alle ausgewählten Apps Platz haben.",
    "layout.reason.uses_accelerator":
      "{title} braucht eine {vendor}-Grafikkarte, und {node} hat eine.",
    "layout.reason.measured_reservation":
      "kombify hat {title} ausgemessen: Die App reserviert {ram} GB Arbeitsspeicher.",
    "layout.reason.estimated_profile":
      "kombify hat {title} noch nicht ausgemessen, die Größe ist daher geschätzt.",
    "layout.reason.evidence_unverified":
      "{node} hat noch nicht alles gemeldet ({missing}), dieser Teil ist daher nicht vollständig bestätigt.",
    "layout.reason.node_not_owner_controlled":
      "{node} ist keiner deiner eigenen Server, deshalb wird dort keine App platziert.",
    "layout.reason.node_not_connected":
      "{node} ist nicht verbunden und bleibt deshalb außen vor.",
    "layout.reason.node_capacity_unknown":
      "{node} hat seine Hardware noch nicht gemeldet und bleibt deshalb außen vor.",
    "layout.reason.no_eligible_node":
      "Noch kein verbundener Server hat seine Hardware gemeldet.",
    "layout.reason.infeasibility_unproven_unknown_node":
      "Ein Server, der seine Hardware noch nicht gemeldet hat, könnte trotzdem passen. kombify kann das noch nicht sicher sagen.",
    "layout.reason.needs_more_ram":
      "{title} braucht {need} GB Arbeitsspeicher, aber kein Server hat mehr als {largest} GB frei.",
    "layout.reason.needs_more_cpu":
      "{title} braucht {need} Prozessorkerne, dein größter Server hat {largest}.",
    "layout.reason.needs_more_storage":
      "{title} braucht {need} GB Speicherplatz, aber kein Server hat mehr als {largest} GB frei.",
    "layout.reason.missing_accelerator":
      "{title} braucht eine {vendor}-Grafikkarte, und keiner deiner Server hat eine.",
    "layout.reason.pinned_node_lacks_room":
      "{title} ist an einen Server gebunden, der keinen Platz dafür hat.",
    "layout.reason.no_node_meets_requirements":
      "Keiner deiner Server bietet, was {title} braucht.",
    "layout.reason.selection_exceeds_free_ram":
      "Die ausgewählten Apps reservieren {need} GB Arbeitsspeicher, deine Server haben {free} GB frei.",
    "layout.reason.no_combination_fits":
      "Es gibt keine Verteilung, auf der alle ausgewählten Apps Platz haben.",

    "layout.addUseCases.pageTitle": "Use Cases hinzufügen | kombify-Techstack",
    "layout.addUseCases.entry": "Use Cases hinzufügen",
    "layout.addUseCases.title": "Use Cases hinzufügen",
    "layout.addUseCases.subtitle":
      "Wähle, was dein Homelab noch können soll. kombify empfiehlt, auf welchem deiner Server welche App läuft.",
    "layout.addUseCases.submit": "Use Cases hinzufügen",
    "layout.addUseCases.submitting": "Use Cases werden hinzugefügt…",
    "layout.addUseCases.backToHomelab": "Zurück zu deinem Homelab",
    "layout.addUseCases.accepted.title": "Zum Plan deines Homelabs hinzugefügt",
    "layout.addUseCases.accepted.body":
      "Deine Use Cases sind gespeichert. Sie werden auf deinen Servern installiert, sobald die Änderung angewendet wird.",
    "layout.addUseCases.provisioning.title":
      "Deine Use Cases werden eingerichtet",
    "layout.addUseCases.provisioning.body":
      "Dein Homelab wendet die Änderung gerade an.",
    "layout.addUseCases.provisioning.progress": "Fortschritt ansehen",
    "layout.addUseCases.superseded.title":
      "Dein Homelab hat sich in der Zwischenzeit verändert",
    "layout.addUseCases.superseded.body":
      "Es wurde nichts geändert. Oben siehst du die aktualisierte Empfehlung. Sieh sie dir an und füge die Use Cases erneut hinzu, um sie zu bestätigen.",
    "layout.addUseCases.stale.title": "Die Empfehlung ist nicht mehr aktuell",
    "layout.addUseCases.stale.body":
      "Es wurde nichts geändert. Sieh dir die Verteilung noch einmal an und füge die Use Cases erneut hinzu.",
    "layout.addUseCases.unavailable.title":
      "Die Empfehlung lässt sich gerade nicht prüfen",
    "layout.addUseCases.unavailable.body":
      "Es wurde nichts geändert. Versuche es gleich noch einmal.",
    "layout.addUseCases.failed.title":
      "Die Use Cases konnten nicht hinzugefügt werden",
    "layout.addUseCases.failed.body":
      "Prüfe deine Auswahl und versuche es erneut. Deine Antworten sind noch da.",
    "wizard.validation.goalsRequired":
      "Wähle mindestens einen Use Case, um weiterzumachen.",

    "wizard.hints.why": "Warum diese Einrichtung",
    "wizard.hints.reason.goal_alignment":
      "Sie passt zu den Use Cases, die du gewählt hast.",
    "wizard.hints.reason.service_alignment":
      "Sie passt zu den Diensten, die du gewählt hast.",
    "wizard.hints.reason.provider_alignment":
      "Sie berücksichtigt den Anbieter, den du gewählt hast.",
    "wizard.hints.reason.safe_standard":
      "Bis du einen Anbieter wählst, nutzt kombify seine verwaltete Standardeinrichtung.",
    "wizard.hints.reason.operator_capability_band":
      "Wie viele Details du siehst, richtet sich danach, wie du kombify nutzt.",
    "wizard.hints.reason.low_compute_profile":
      "Einer deiner Server hat wenig Arbeitsspeicher, deshalb passt eine schlankere Einrichtung besser.",
    "wizard.hints.reason.cloud_owner_asset":
      "Dein kombify-Konto richtet die Anmeldung als Eigentümer für dich ein.",
    "wizard.hints.reason.selection_capacity_shortfall":
      "Die gewählten Apps brauchen mehr, als deine Server gerade frei haben.",
    "wizard.hints.reason.module_capacity_shortfall":
      "Mindestens eine gewählte App braucht mehr, als jeder deiner Server ihr gerade geben kann.",
    "wizard.hints.reason.module_capacity_unverified":
      "Nicht jeder Server hat seine Hardware gemeldet, deshalb können wir nicht sicher sagen, dass alles passt.",
    "wizard.hints.reason.catalog_footprint_unavailable":
      "Die Größe der gewählten Apps ist noch nicht bekannt, deshalb wird nicht geprüft, ob alles passt.",
  },
  es: {
    "layout.panel.title": "Distribución recomendada",
    "layout.panel.intro":
      "kombify te sugiere qué servidor ejecuta cada app. Al terminar esta configuración, aceptas la sugerencia.",
    "layout.panel.loading": "Calculando dónde debería ejecutarse cada app…",
    "layout.panel.updating": "Actualizando…",
    "layout.panel.mainServer": "Servidor principal",
    "layout.panel.newServer": "Servidor nuevo",
    "layout.panel.reserved": "{used} de {total} GB de memoria reservados",
    "layout.panel.nothingNew": "Aquí no se ejecuta ninguna app nueva.",
    "layout.panel.moreReasons": "Más sobre esta elección",
    "layout.panel.staysTitle": "Se quedan donde están",
    "layout.panel.staysLine": "{app} en {node}",
    "layout.panel.notesTitle": "Conviene saber",
    "layout.panel.conflictsTitle": "Lo que aún no encaja",
    "layout.panel.notCurrent": "Esta recomendación ya no está al día.",
    "layout.availability.always-on": "Siempre encendido",
    "layout.availability.part-time": "A ratos encendido",
    "layout.verification.verified": "Comprobado con tu hardware",
    "layout.verification.infeasible":
      "Esto no cabe en tu hardware tal como está, así que no se ofrece como recomendación.",
    "layout.verification.unknown":
      "Todavía no hemos podido confirmar que esto encaje en tu hardware, así que no se ofrece como recomendación.",
    "layout.verification.unavailable":
      "La comprobación del hardware no está disponible ahora, así que esto es solo informativo.",
    "layout.verification.skipped":
      "Se omitió la comprobación del hardware, así que esto es solo informativo.",
    "layout.verification.details": "Detalles",
    "layout.reason.data_budget_unset":
      "Aún no se ha indicado cuánto espacio necesitan tus datos en {node}, así que no se comprobó el espacio libre en disco para ellos.",
    "layout.outcome.proven-infeasible":
      "No todo lo que elegiste cabe en tus servidores tal como están.",
    "layout.outcome.unknown":
      "Todavía no podemos saber si todo lo que elegiste encaja.",
    "layout.outcome.budget-exhausted":
      "No pudimos terminar de comprobar todas las formas de distribuir tus apps.",
    "layout.state.noSetup":
      "Se podrá recomendar una distribución cuando tu Homelab termine su primera configuración.",
    "layout.state.invalid":
      "Algunas de tus opciones todavía no se pueden distribuir juntas.",
    "layout.state.unavailable":
      "Ahora mismo no hay una distribución recomendada disponible. Puedes continuar de todos modos.",
    "layout.summary.title": "Dónde se ejecutarán tus apps",
    "layout.summary.applies":
      "Al terminar esta configuración se aplica esta distribución.",
    "layout.summary.line": "{node}: {apps}",

    "layout.axis.cpu": "procesador",
    "layout.axis.ram": "memoria",
    "layout.axis.storage": "almacenamiento",
    "layout.axis.cpu_reservation": "necesidades del procesador",
    "layout.axis.roles": "rol del servidor",
    "layout.axis.cpu_microarchitecture": "generación del procesador",
    "layout.axis.cpu_architecture": "tipo de procesador",
    "layout.axis.accelerator_vendor": "marca de la tarjeta gráfica",

    "layout.reason.runs_on_always_on":
      "{title} es una de tus apps más importantes, así que se ejecuta en {node}, que ha estado encendido las 24 horas.",
    "layout.reason.prefers_always_on":
      "{title} se ejecuta en {node} porque ha estado encendido las 24 horas.",
    "layout.reason.avoids_part_time":
      "{title} evita {rival}, que se apaga a menudo.",
    "layout.reason.important_tight_fit":
      "{title} es importante y pequeña, así que comparte {node} con tus otras apps importantes y deja {rival} libre para las más pesadas.",
    "layout.reason.kept_apart":
      "{title} se ejecuta en {node}, separada de tus apps importantes, para que su carga no las ralentice.",
    "layout.reason.tight_fit":
      "{node} tiene espacio para {title} y deja más espacio libre en {rival}.",
    "layout.reason.verified_hardware":
      "{node} ha confirmado el hardware del que depende {title}.",
    "layout.reason.equal_choice":
      "{node} y {rival} encajan igual de bien con {title}; kombify ha elegido {node}.",
    "layout.reason.only_fitting_node":
      "{node} es el único servidor que ofrece lo que necesita {title}.",
    "layout.reason.profile_not_measured":
      "kombify aún no ha medido {title}, así que se queda en tu servidor principal, {node}.",
    "layout.reason.pinned": "{title} se queda en {node}.",
    "layout.reason.with_use_case":
      "{title} amplía el caso de uso que ya se ejecuta en {node}, así que también se ejecuta allí.",
    "layout.reason.repacked":
      "{title} se ejecuta en {node} para que quepan todas las apps que elegiste.",
    "layout.reason.uses_accelerator":
      "{title} necesita una tarjeta gráfica {vendor}, y {node} la tiene.",
    "layout.reason.measured_reservation":
      "kombify ha medido {title}: reserva {ram} GB de memoria.",
    "layout.reason.estimated_profile":
      "kombify aún no ha medido {title}, así que su tamaño es una estimación.",
    "layout.reason.evidence_unverified":
      "{node} aún no ha informado de todo ({missing}), así que esta parte no está del todo confirmada.",
    "layout.reason.node_not_owner_controlled":
      "{node} no es uno de tus servidores, así que no se coloca ninguna app en él.",
    "layout.reason.node_not_connected":
      "{node} no está conectado, así que se deja fuera.",
    "layout.reason.node_capacity_unknown":
      "{node} aún no ha informado de su hardware, así que se deja fuera.",
    "layout.reason.no_eligible_node":
      "Ningún servidor conectado ha informado todavía de su hardware.",
    "layout.reason.infeasibility_unproven_unknown_node":
      "Un servidor que aún no ha informado de su hardware podría encajar; kombify todavía no puede asegurarlo.",
    "layout.reason.needs_more_ram":
      "{title} necesita {need} GB de memoria, pero lo máximo que tiene libre un servidor es {largest} GB.",
    "layout.reason.needs_more_cpu":
      "{title} necesita {need} núcleos de procesador, pero tu servidor más grande tiene {largest}.",
    "layout.reason.needs_more_storage":
      "{title} necesita {need} GB de almacenamiento, pero lo máximo que tiene libre un servidor es {largest} GB.",
    "layout.reason.missing_accelerator":
      "{title} necesita una tarjeta gráfica {vendor}, y ninguno de tus servidores la tiene.",
    "layout.reason.pinned_node_lacks_room":
      "{title} está ligada a un servidor que no tiene espacio para ella.",
    "layout.reason.no_node_meets_requirements":
      "Ninguno de tus servidores ofrece lo que necesita {title}.",
    "layout.reason.selection_exceeds_free_ram":
      "Las apps elegidas reservan {need} GB de memoria, pero tus servidores tienen {free} GB libres.",
    "layout.reason.no_combination_fits":
      "No hay ninguna forma de distribuir las apps elegidas para que quepan todas.",

    "layout.addUseCases.pageTitle": "Añadir casos de uso | kombify-Techstack",
    "layout.addUseCases.entry": "Añadir casos de uso",
    "layout.addUseCases.title": "Añadir casos de uso",
    "layout.addUseCases.subtitle":
      "Elige qué más debe hacer tu Homelab. kombify te recomienda qué servidor ejecuta cada app.",
    "layout.addUseCases.submit": "Añadir casos de uso",
    "layout.addUseCases.submitting": "Añadiendo casos de uso…",
    "layout.addUseCases.backToHomelab": "Volver a tu Homelab",
    "layout.addUseCases.accepted.title": "Añadido al plan de tu Homelab",
    "layout.addUseCases.accepted.body":
      "Tus casos de uso están guardados. Se instalarán en tus servidores cuando se aplique el cambio.",
    "layout.addUseCases.provisioning.title": "Configurando tus casos de uso",
    "layout.addUseCases.provisioning.body":
      "Tu Homelab está aplicando el cambio ahora mismo.",
    "layout.addUseCases.provisioning.progress": "Ver el progreso",
    "layout.addUseCases.superseded.title":
      "Tu Homelab ha cambiado mientras tanto",
    "layout.addUseCases.superseded.body":
      "No se cambió nada. Arriba ves la recomendación actualizada. Revísala y vuelve a añadir los casos de uso para confirmarla.",
    "layout.addUseCases.stale.title": "La recomendación ya no está al día",
    "layout.addUseCases.stale.body":
      "No se cambió nada. Revisa de nuevo la distribución y vuelve a añadir los casos de uso.",
    "layout.addUseCases.unavailable.title":
      "Ahora mismo no se puede comprobar la recomendación",
    "layout.addUseCases.unavailable.body":
      "No se cambió nada. Inténtalo de nuevo en un momento.",
    "layout.addUseCases.failed.title": "No se pudieron añadir los casos de uso",
    "layout.addUseCases.failed.body":
      "Revisa tu selección e inténtalo de nuevo. Tus respuestas siguen aquí.",
    "wizard.validation.goalsRequired":
      "Elige al menos un caso de uso para continuar.",

    "wizard.hints.why": "Por qué esta configuración",
    "wizard.hints.reason.goal_alignment":
      "Encaja con los casos de uso que elegiste.",
    "wizard.hints.reason.service_alignment":
      "Encaja con los servicios que elegiste.",
    "wizard.hints.reason.provider_alignment":
      "Tiene en cuenta el proveedor que elegiste.",
    "wizard.hints.reason.safe_standard":
      "Hasta que elijas un proveedor, kombify usa su configuración gestionada estándar.",
    "wizard.hints.reason.operator_capability_band":
      "El nivel de detalle sigue la forma en que usas kombify.",
    "wizard.hints.reason.low_compute_profile":
      "Uno de tus servidores tiene poca memoria, así que le va mejor una configuración más ligera.",
    "wizard.hints.reason.cloud_owner_asset":
      "Tu cuenta de kombify configura por ti el acceso como propietario.",
    "wizard.hints.reason.selection_capacity_shortfall":
      "Las apps que elegiste necesitan más de lo que tus servidores tienen libre ahora mismo.",
    "wizard.hints.reason.module_capacity_shortfall":
      "Al menos una app que elegiste necesita más de lo que cualquiera de tus servidores puede darle ahora mismo.",
    "wizard.hints.reason.module_capacity_unverified":
      "No todos los servidores han informado de su hardware, así que no podemos asegurar que todo encaje.",
    "wizard.hints.reason.catalog_footprint_unavailable":
      "Todavía no se conoce el tamaño de las apps que elegiste, así que no se comprueba si encajan.",
  },
  "zh-Hans": {
    "layout.panel.title": "推荐的分配方案",
    "layout.panel.intro":
      "kombify 会建议每个应用运行在哪台服务器上。完成本次设置即表示接受该建议。",
    "layout.panel.loading": "正在判断每个应用应该运行在哪里…",
    "layout.panel.updating": "正在更新…",
    "layout.panel.mainServer": "主服务器",
    "layout.panel.newServer": "新服务器",
    "layout.panel.reserved": "已预留 {used} GB，共 {total} GB 内存",
    "layout.panel.nothingNew": "这里不会运行新的应用。",
    "layout.panel.moreReasons": "了解这一选择的更多信息",
    "layout.panel.staysTitle": "保持原位",
    "layout.panel.staysLine": "{node} 上的 {app}",
    "layout.panel.notesTitle": "温馨提示",
    "layout.panel.conflictsTitle": "目前还放不下的内容",
    "layout.panel.notCurrent": "此推荐已不是最新的。",
    "layout.availability.always-on": "始终开机",
    "layout.availability.part-time": "不定时开机",
    "layout.verification.verified": "已根据你的硬件检查",
    "layout.verification.infeasible": "按现有硬件放不下，因此不作为推荐提供。",
    "layout.verification.unknown":
      "我们还无法确认这能放进你的硬件，因此不作为推荐提供。",
    "layout.verification.unavailable": "硬件检查目前不可用，以下内容仅供参考。",
    "layout.verification.skipped": "已跳过硬件检查，以下内容仅供参考。",
    "layout.verification.details": "详情",
    "layout.reason.data_budget_unset":
      "尚未设置你的数据在 {node} 上需要多少空间，因此没有为其检查可用磁盘空间。",
    "layout.outcome.proven-infeasible":
      "你选择的内容并不能全部放进现有服务器。",
    "layout.outcome.unknown": "我们暂时还无法判断你选择的内容是否都放得下。",
    "layout.outcome.budget-exhausted": "我们没能检查完所有分配应用的方式。",
    "layout.state.noSetup": "待你的 Homelab 完成首次设置后，才能推荐分配方案。",
    "layout.state.invalid": "你的部分选择目前还无法一起分配。",
    "layout.state.unavailable": "目前无法提供推荐的分配方案。你仍可继续。",
    "layout.summary.title": "你的应用将运行在哪里",
    "layout.summary.applies": "完成本次设置时将应用此分配方案。",
    "layout.summary.line": "{node}：{apps}",

    "layout.axis.cpu": "处理器",
    "layout.axis.ram": "内存",
    "layout.axis.storage": "存储空间",
    "layout.axis.cpu_reservation": "处理器需求",
    "layout.axis.roles": "服务器角色",
    "layout.axis.cpu_microarchitecture": "处理器代际",
    "layout.axis.cpu_architecture": "处理器类型",
    "layout.axis.accelerator_vendor": "显卡品牌",

    "layout.reason.runs_on_always_on":
      "{title} 是你最重要的应用之一，所以运行在一直开机的 {node} 上。",
    "layout.reason.prefers_always_on":
      "{title} 运行在 {node} 上，因为它一直全天开机。",
    "layout.reason.avoids_part_time": "{title} 避开经常关机的 {rival}。",
    "layout.reason.important_tight_fit":
      "{title} 既重要又小巧，所以与你的其他重要应用共用 {node}，让 {rival} 留给更吃资源的应用。",
    "layout.reason.kept_apart":
      "{title} 运行在 {node} 上，与你的重要应用分开，这样它的负载不会拖慢它们。",
    "layout.reason.tight_fit":
      "{node} 有足够空间运行 {title}，同时让 {rival} 保留更多空闲空间。",
    "layout.reason.verified_hardware": "{node} 已确认 {title} 所依赖的硬件。",
    "layout.reason.equal_choice":
      "{node} 和 {rival} 同样适合 {title}，kombify 选择了 {node}。",
    "layout.reason.only_fitting_node":
      "{node} 是唯一满足 {title} 需求的服务器。",
    "layout.reason.profile_not_measured":
      "kombify 尚未测量 {title}，所以它保留在你的主服务器 {node} 上。",
    "layout.reason.pinned": "{title} 保留在 {node} 上。",
    "layout.reason.with_use_case":
      "{title} 扩展了已在 {node} 上运行的用例，因此也在那里运行。",
    "layout.reason.repacked":
      "{title} 运行在 {node} 上，以便你选择的所有应用都能放下。",
    "layout.reason.uses_accelerator":
      "{title} 需要一块 {vendor} 显卡，而 {node} 有。",
    "layout.reason.measured_reservation":
      "kombify 已测量 {title}：它会预留 {ram} GB 内存。",
    "layout.reason.estimated_profile":
      "kombify 尚未测量 {title}，其大小为估算值。",
    "layout.reason.evidence_unverified":
      "{node} 尚未报告全部信息（{missing}），所以这一部分尚未完全确认。",
    "layout.reason.node_not_owner_controlled":
      "{node} 不是你自己的服务器，所以不会在其上放置任何应用。",
    "layout.reason.node_not_connected": "{node} 未连接，所以被排除在外。",
    "layout.reason.node_capacity_unknown":
      "{node} 尚未报告其硬件，所以被排除在外。",
    "layout.reason.no_eligible_node": "还没有已连接的服务器报告其硬件。",
    "layout.reason.infeasibility_unproven_unknown_node":
      "尚未报告硬件的服务器仍可能满足需求，kombify 目前还无法确定。",
    "layout.reason.needs_more_ram":
      "{title} 需要 {need} GB 内存，但任何服务器的最大空闲内存只有 {largest} GB。",
    "layout.reason.needs_more_cpu":
      "{title} 需要 {need} 个处理器核心，而你最大的服务器只有 {largest} 个。",
    "layout.reason.needs_more_storage":
      "{title} 需要 {need} GB 存储空间，但任何服务器的最大空闲空间只有 {largest} GB。",
    "layout.reason.missing_accelerator":
      "{title} 需要一块 {vendor} 显卡，而你的服务器都没有。",
    "layout.reason.pinned_node_lacks_room":
      "{title} 绑定的服务器没有足够空间容纳它。",
    "layout.reason.no_node_meets_requirements":
      "你的服务器都不满足 {title} 的需求。",
    "layout.reason.selection_exceeds_free_ram":
      "你选择的应用共预留 {need} GB 内存，而你的服务器只有 {free} GB 空闲。",
    "layout.reason.no_combination_fits":
      "没有任何分配方式能让你选择的所有应用都放得下。",

    "layout.addUseCases.pageTitle": "添加用例 | kombify-Techstack",
    "layout.addUseCases.entry": "添加用例",
    "layout.addUseCases.title": "添加用例",
    "layout.addUseCases.subtitle":
      "选择你的 Homelab 还应该做什么。kombify 会推荐由哪台服务器运行每个应用。",
    "layout.addUseCases.submit": "添加用例",
    "layout.addUseCases.submitting": "正在添加用例…",
    "layout.addUseCases.backToHomelab": "返回你的 Homelab",
    "layout.addUseCases.accepted.title": "已加入你的 Homelab 计划",
    "layout.addUseCases.accepted.body":
      "你的用例已保存。变更生效后，它们会安装到你的服务器上。",
    "layout.addUseCases.provisioning.title": "正在设置你的用例",
    "layout.addUseCases.provisioning.body": "你的 Homelab 正在应用这项变更。",
    "layout.addUseCases.provisioning.progress": "查看进度",
    "layout.addUseCases.superseded.title": "你的 Homelab 在此期间发生了变化",
    "layout.addUseCases.superseded.body":
      "未做任何更改。上方显示更新后的推荐。请查看后再次添加用例以确认。",
    "layout.addUseCases.stale.title": "推荐已过期",
    "layout.addUseCases.stale.body":
      "未做任何更改。请重新查看分配方案，然后再次添加用例。",
    "layout.addUseCases.unavailable.title": "目前无法检查推荐",
    "layout.addUseCases.unavailable.body": "未做任何更改。请稍后再试。",
    "layout.addUseCases.failed.title": "无法添加这些用例",
    "layout.addUseCases.failed.body":
      "请检查你的选择后重试。你的回答仍保留在这里。",
    "wizard.validation.goalsRequired": "请至少选择一个用例以继续。",

    "wizard.hints.why": "为什么这样设置",
    "wizard.hints.reason.goal_alignment": "它与你选择的用例相符。",
    "wizard.hints.reason.service_alignment": "它与你选择的服务相符。",
    "wizard.hints.reason.provider_alignment": "它考虑了你选择的提供商。",
    "wizard.hints.reason.safe_standard":
      "在你选择提供商之前，kombify 使用其标准托管设置。",
    "wizard.hints.reason.operator_capability_band":
      "显示的详细程度会随你使用 kombify 的方式而变化。",
    "wizard.hints.reason.low_compute_profile":
      "你的一台服务器内存较少，所以更轻量的设置更适合它。",
    "wizard.hints.reason.cloud_owner_asset":
      "你的 kombify 账户会为你设置所有者登录。",
    "wizard.hints.reason.selection_capacity_shortfall":
      "你选择的应用所需资源超过了你的服务器目前的空闲资源。",
    "wizard.hints.reason.module_capacity_shortfall":
      "你选择的至少一个应用需要的资源，超过了你的任何服务器目前能提供的。",
    "wizard.hints.reason.module_capacity_unverified":
      "并非每台服务器都已报告硬件，所以我们无法确定一切都放得下。",
    "wizard.hints.reason.catalog_footprint_unavailable":
      "你所选应用的大小暂时未知，所以尚未检查是否放得下。",
  },
  hi: {
    "layout.panel.title": "सुझाई गई व्यवस्था",
    "layout.panel.intro":
      "kombify सुझाव देता है कि हर ऐप किस सर्वर पर चले। यह सेटअप पूरा करने पर आप इसे स्वीकार कर लेते हैं।",
    "layout.panel.loading": "यह तय किया जा रहा है कि हर ऐप कहाँ चलना चाहिए…",
    "layout.panel.updating": "अपडेट हो रहा है…",
    "layout.panel.mainServer": "मुख्य सर्वर",
    "layout.panel.newServer": "नया सर्वर",
    "layout.panel.reserved": "{total} GB में से {used} GB मेमोरी आरक्षित",
    "layout.panel.nothingNew": "यहाँ कोई नया ऐप नहीं चलेगा।",
    "layout.panel.moreReasons": "इस चुनाव के बारे में और जानें",
    "layout.panel.staysTitle": "जहाँ हैं, वहीं रहेंगे",
    "layout.panel.staysLine": "{node} पर {app}",
    "layout.panel.notesTitle": "जानने लायक बातें",
    "layout.panel.conflictsTitle": "जो अभी फ़िट नहीं होता",
    "layout.panel.notCurrent": "यह सुझाव अब मौजूदा नहीं है।",
    "layout.availability.always-on": "हमेशा चालू",
    "layout.availability.part-time": "कुछ समय के लिए चालू",
    "layout.verification.verified": "आपके हार्डवेयर के अनुसार जाँचा गया",
    "layout.verification.infeasible":
      "यह आपके हार्डवेयर पर जैसा है वैसा फ़िट नहीं होता, इसलिए इसे सुझाव के रूप में नहीं दिया जा रहा।",
    "layout.verification.unknown":
      "हम अभी पुष्टि नहीं कर पाए कि यह आपके हार्डवेयर पर फ़िट होगा, इसलिए इसे सुझाव के रूप में नहीं दिया जा रहा।",
    "layout.verification.unavailable":
      "हार्डवेयर की जाँच अभी उपलब्ध नहीं है, इसलिए यह केवल जानकारी के लिए है।",
    "layout.verification.skipped":
      "हार्डवेयर की जाँच छोड़ दी गई, इसलिए यह केवल जानकारी के लिए है।",
    "layout.verification.details": "विवरण",
    "layout.reason.data_budget_unset":
      "{node} पर आपके डेटा को कितनी जगह चाहिए, यह अभी तय नहीं है, इसलिए उसके लिए खाली डिस्क स्थान की जाँच नहीं की गई।",
    "layout.outcome.proven-infeasible":
      "आपने जो चुना है, वह सब आपके सर्वरों पर मौजूदा रूप में फ़िट नहीं होता।",
    "layout.outcome.unknown":
      "हम अभी नहीं बता सकते कि आपका चुना हुआ सब कुछ फ़िट होगा या नहीं।",
    "layout.outcome.budget-exhausted":
      "हम आपके ऐप्स को बाँटने के सभी तरीके जाँच नहीं पाए।",
    "layout.state.noSetup":
      "आपके Homelab का पहला सेटअप पूरा होते ही एक व्यवस्था सुझाई जा सकेगी।",
    "layout.state.invalid": "आपके कुछ चुनाव अभी एक साथ नहीं बाँटे जा सकते।",
    "layout.state.unavailable":
      "सुझाई गई व्यवस्था अभी उपलब्ध नहीं है। आप फिर भी आगे बढ़ सकते हैं।",
    "layout.summary.title": "आपके ऐप्स कहाँ चलेंगे",
    "layout.summary.applies": "यह सेटअप पूरा करने पर यही व्यवस्था लागू होगी।",
    "layout.summary.line": "{node}: {apps}",

    "layout.axis.cpu": "प्रोसेसर",
    "layout.axis.ram": "मेमोरी",
    "layout.axis.storage": "स्टोरेज",
    "layout.axis.cpu_reservation": "प्रोसेसर की ज़रूरतें",
    "layout.axis.roles": "सर्वर की भूमिका",
    "layout.axis.cpu_microarchitecture": "प्रोसेसर की पीढ़ी",
    "layout.axis.cpu_architecture": "प्रोसेसर का प्रकार",
    "layout.axis.accelerator_vendor": "ग्राफ़िक्स कार्ड का ब्रांड",

    "layout.reason.runs_on_always_on":
      "{title} आपके सबसे ज़रूरी ऐप्स में से एक है, इसलिए यह {node} पर चलता है, जो चौबीसों घंटे चालू रहा है।",
    "layout.reason.prefers_always_on":
      "{title} {node} पर चलता है क्योंकि वह चौबीसों घंटे चालू रहा है।",
    "layout.reason.avoids_part_time":
      "{title} उस {rival} से बचता है, जो अक्सर बंद रहता है।",
    "layout.reason.important_tight_fit":
      "{title} ज़रूरी और छोटा है, इसलिए यह {node} को आपके अन्य ज़रूरी ऐप्स के साथ साझा करता है और {rival} को भारी ऐप्स के लिए खाली छोड़ता है।",
    "layout.reason.kept_apart":
      "{title} {node} पर आपके ज़रूरी ऐप्स से अलग चलता है, ताकि इसका लोड उन्हें धीमा न करे।",
    "layout.reason.tight_fit":
      "{node} में {title} के लिए जगह है, और {rival} पर ज़्यादा जगह खाली रहती है।",
    "layout.reason.verified_hardware":
      "{node} ने उस हार्डवेयर की पुष्टि की है, जिस पर {title} निर्भर है।",
    "layout.reason.equal_choice":
      "{node} और {rival} दोनों {title} के लिए समान रूप से उपयुक्त हैं; kombify ने {node} चुना।",
    "layout.reason.only_fitting_node":
      "{node} अकेला सर्वर है, जिसमें वह सब है जो {title} को चाहिए।",
    "layout.reason.profile_not_measured":
      "kombify ने {title} को अभी मापा नहीं है, इसलिए यह आपके मुख्य सर्वर {node} पर ही रहता है।",
    "layout.reason.pinned": "{title} {node} पर ही रहता है।",
    "layout.reason.with_use_case":
      "{title} उस उपयोग को बढ़ाता है जो पहले से {node} पर चलता है, इसलिए यह भी वहीं चलता है।",
    "layout.reason.repacked":
      "{title} {node} पर चलता है, ताकि आपके चुने सभी ऐप्स समा सकें।",
    "layout.reason.uses_accelerator":
      "{title} को {vendor} ग्राफ़िक्स कार्ड चाहिए, और {node} में वह है।",
    "layout.reason.measured_reservation":
      "kombify ने {title} को मापा है: यह {ram} GB मेमोरी आरक्षित करता है।",
    "layout.reason.estimated_profile":
      "kombify ने {title} को अभी मापा नहीं है, इसलिए इसका आकार अनुमानित है।",
    "layout.reason.evidence_unverified":
      "{node} ने अभी सारी जानकारी नहीं दी है ({missing}), इसलिए यह हिस्सा पूरी तरह पुष्ट नहीं है।",
    "layout.reason.node_not_owner_controlled":
      "{node} आपका अपना सर्वर नहीं है, इसलिए उस पर कोई ऐप नहीं रखा जाता।",
    "layout.reason.node_not_connected":
      "{node} जुड़ा हुआ नहीं है, इसलिए उसे छोड़ दिया गया है।",
    "layout.reason.node_capacity_unknown":
      "{node} ने अभी अपना हार्डवेयर नहीं बताया है, इसलिए उसे छोड़ दिया गया है।",
    "layout.reason.no_eligible_node":
      "अभी किसी जुड़े हुए सर्वर ने अपना हार्डवेयर नहीं बताया है।",
    "layout.reason.infeasibility_unproven_unknown_node":
      "जिस सर्वर ने अभी अपना हार्डवेयर नहीं बताया, वह फिर भी फ़िट हो सकता है; kombify अभी पक्का नहीं कह सकता।",
    "layout.reason.needs_more_ram":
      "{title} को {need} GB मेमोरी चाहिए, लेकिन किसी भी सर्वर पर सबसे ज़्यादा {largest} GB खाली है।",
    "layout.reason.needs_more_cpu":
      "{title} को {need} प्रोसेसर कोर चाहिए, लेकिन आपके सबसे बड़े सर्वर में {largest} हैं।",
    "layout.reason.needs_more_storage":
      "{title} को {need} GB स्टोरेज चाहिए, लेकिन किसी भी सर्वर पर सबसे ज़्यादा {largest} GB खाली है।",
    "layout.reason.missing_accelerator":
      "{title} को {vendor} ग्राफ़िक्स कार्ड चाहिए, और आपके किसी सर्वर में वह नहीं है।",
    "layout.reason.pinned_node_lacks_room":
      "{title} जिस सर्वर से बंधा है, उसमें इसके लिए जगह नहीं है।",
    "layout.reason.no_node_meets_requirements":
      "आपका कोई भी सर्वर वह पूरा नहीं करता जो {title} को चाहिए।",
    "layout.reason.selection_exceeds_free_ram":
      "आपके चुने ऐप्स {need} GB मेमोरी आरक्षित करते हैं, जबकि आपके सर्वरों पर {free} GB खाली है।",
    "layout.reason.no_combination_fits":
      "आपके चुने ऐप्स को इस तरह बाँटने का कोई तरीका नहीं है कि सभी समा जाएँ।",

    "layout.addUseCases.pageTitle": "यूज़ केस जोड़ें | kombify-Techstack",
    "layout.addUseCases.entry": "यूज़ केस जोड़ें",
    "layout.addUseCases.title": "यूज़ केस जोड़ें",
    "layout.addUseCases.subtitle":
      "चुनें कि आपका Homelab और क्या करे। kombify सुझाता है कि हर ऐप आपके किस सर्वर पर चले।",
    "layout.addUseCases.submit": "यूज़ केस जोड़ें",
    "layout.addUseCases.submitting": "यूज़ केस जोड़े जा रहे हैं…",
    "layout.addUseCases.backToHomelab": "अपने Homelab पर वापस जाएँ",
    "layout.addUseCases.accepted.title": "आपके Homelab की योजना में जोड़ा गया",
    "layout.addUseCases.accepted.body":
      "आपके यूज़ केस सहेज लिए गए हैं। बदलाव लागू होते ही वे आपके सर्वरों पर इंस्टॉल हो जाएँगे।",
    "layout.addUseCases.provisioning.title": "आपके यूज़ केस सेट अप हो रहे हैं",
    "layout.addUseCases.provisioning.body":
      "आपका Homelab अभी यह बदलाव लागू कर रहा है।",
    "layout.addUseCases.provisioning.progress": "प्रगति देखें",
    "layout.addUseCases.superseded.title": "इस बीच आपका Homelab बदल गया",
    "layout.addUseCases.superseded.body":
      "कुछ भी नहीं बदला गया। अपडेट किया हुआ सुझाव ऊपर दिखाया गया है। उसे देखें और पुष्टि के लिए यूज़ केस फिर से जोड़ें।",
    "layout.addUseCases.stale.title": "यह सुझाव पुराना हो गया है",
    "layout.addUseCases.stale.body":
      "कुछ भी नहीं बदला गया। व्यवस्था को फिर से देखें और यूज़ केस एक बार फिर जोड़ें।",
    "layout.addUseCases.unavailable.title": "सुझाव की जाँच अभी नहीं हो पा रही",
    "layout.addUseCases.unavailable.body":
      "कुछ भी नहीं बदला गया। थोड़ी देर बाद फिर कोशिश करें।",
    "layout.addUseCases.failed.title": "यूज़ केस जोड़े नहीं जा सके",
    "layout.addUseCases.failed.body":
      "अपने चुनाव जाँचें और फिर कोशिश करें। आपके जवाब अभी यहीं हैं।",
    "wizard.validation.goalsRequired":
      "आगे बढ़ने के लिए कम से कम एक यूज़ केस चुनें।",

    "wizard.hints.why": "यह सेटअप क्यों",
    "wizard.hints.reason.goal_alignment":
      "यह आपके चुने हुए यूज़ केस से मेल खाता है।",
    "wizard.hints.reason.service_alignment":
      "यह आपकी चुनी हुई सेवाओं से मेल खाता है।",
    "wizard.hints.reason.provider_alignment":
      "इसमें आपके चुने हुए प्रदाता का ध्यान रखा गया है।",
    "wizard.hints.reason.safe_standard":
      "जब तक आप प्रदाता नहीं चुनते, kombify अपना मानक प्रबंधित सेटअप इस्तेमाल करता है।",
    "wizard.hints.reason.operator_capability_band":
      "कितना विवरण दिखे, यह इस पर निर्भर है कि आप kombify कैसे इस्तेमाल करते हैं।",
    "wizard.hints.reason.low_compute_profile":
      "आपके एक सर्वर में कम मेमोरी है, इसलिए उसके लिए हल्का सेटअप बेहतर है।",
    "wizard.hints.reason.cloud_owner_asset":
      "आपका kombify खाता आपके लिए मालिक का लॉगिन सेट कर देता है।",
    "wizard.hints.reason.selection_capacity_shortfall":
      "आपके चुने ऐप्स को उससे ज़्यादा चाहिए, जितना आपके सर्वरों पर अभी खाली है।",
    "wizard.hints.reason.module_capacity_shortfall":
      "आपके चुने कम से कम एक ऐप को उससे ज़्यादा चाहिए, जितना आपका कोई भी सर्वर उसे अभी दे सकता है।",
    "wizard.hints.reason.module_capacity_unverified":
      "हर सर्वर ने अपना हार्डवेयर नहीं बताया है, इसलिए हम पक्का नहीं कह सकते कि सब कुछ फ़िट होगा।",
    "wizard.hints.reason.catalog_footprint_unavailable":
      "आपके चुने ऐप्स का आकार अभी पता नहीं है, इसलिए फ़िट होने की जाँच नहीं हुई।",
  },
  ar: {
    "layout.panel.title": "التوزيع الموصى به",
    "layout.panel.intro":
      "يقترح kombify أي خادم يشغّل كل تطبيق. بإنهاء هذا الإعداد تقبل الاقتراح.",
    "layout.panel.loading": "جارٍ تحديد المكان المناسب لتشغيل كل تطبيق…",
    "layout.panel.updating": "جارٍ التحديث…",
    "layout.panel.mainServer": "الخادم الرئيسي",
    "layout.panel.newServer": "خادم جديد",
    "layout.panel.reserved": "{used} من {total} غيغابايت من الذاكرة محجوزة",
    "layout.panel.nothingNew": "لا يعمل هنا أي تطبيق جديد.",
    "layout.panel.moreReasons": "المزيد عن هذا الاختيار",
    "layout.panel.staysTitle": "تبقى حيث هي",
    "layout.panel.staysLine": "{app} على {node}",
    "layout.panel.notesTitle": "من الجيد معرفته",
    "layout.panel.conflictsTitle": "ما لا يتسع بعد",
    "layout.panel.notCurrent": "لم تعد هذه التوصية محدّثة.",
    "layout.availability.always-on": "يعمل دائمًا",
    "layout.availability.part-time": "يعمل جزءًا من الوقت",
    "layout.verification.verified": "تم الفحص مقابل عتادك",
    "layout.verification.infeasible":
      "هذا لا يتسع على عتادك كما هو، لذلك لا يُعرض كتوصية.",
    "layout.verification.unknown":
      "لم نتمكن بعد من التأكد من أن هذا يتسع على عتادك، لذلك لا يُعرض كتوصية.",
    "layout.verification.unavailable":
      "فحص العتاد غير متاح الآن، لذلك هذا للاطلاع فقط.",
    "layout.verification.skipped": "تم تخطي فحص العتاد، لذلك هذا للاطلاع فقط.",
    "layout.verification.details": "التفاصيل",
    "layout.reason.data_budget_unset":
      "لم يحدد بعد مقدار المساحة التي تحتاجها بياناتك على {node}، لذلك لم تفحص مساحة القرص الحرة من أجلها.",
    "layout.outcome.proven-infeasible":
      "لا يتسع كل ما اخترته على خوادمك كما هي.",
    "layout.outcome.unknown":
      "لا يمكننا بعد معرفة ما إذا كان كل ما اخترته يتسع.",
    "layout.outcome.budget-exhausted":
      "لم نتمكن من إنهاء فحص كل طرق توزيع تطبيقاتك.",
    "layout.state.noSetup":
      "يمكن اقتراح توزيع بعد أن ينتهي Homelab لديك من إعداده الأول.",
    "layout.state.invalid": "بعض اختياراتك لا يمكن توزيعها معًا بعد.",
    "layout.state.unavailable":
      "التوزيع الموصى به غير متاح الآن. يمكنك المتابعة رغم ذلك.",
    "layout.summary.title": "أين ستعمل تطبيقاتك",
    "layout.summary.applies": "بإنهاء هذا الإعداد يُطبَّق هذا التوزيع.",
    "layout.summary.line": "{node}: {apps}",

    "layout.axis.cpu": "المعالج",
    "layout.axis.ram": "الذاكرة",
    "layout.axis.storage": "التخزين",
    "layout.axis.cpu_reservation": "احتياجات المعالج",
    "layout.axis.roles": "دور الخادم",
    "layout.axis.cpu_microarchitecture": "جيل المعالج",
    "layout.axis.cpu_architecture": "نوع المعالج",
    "layout.axis.accelerator_vendor": "علامة بطاقة الرسوميات",

    "layout.reason.runs_on_always_on":
      "{title} من أهم تطبيقاتك، لذلك يعمل على {node} الذي ظلّ يعمل على مدار الساعة.",
    "layout.reason.prefers_always_on":
      "يعمل {title} على {node} لأنه ظلّ يعمل على مدار الساعة.",
    "layout.reason.avoids_part_time":
      "يتجنّب {title} الخادم {rival} الذي يُطفأ كثيرًا.",
    "layout.reason.important_tight_fit":
      "{title} مهم وصغير، لذلك يشارك {node} مع تطبيقاتك المهمة الأخرى ويترك {rival} فارغًا للتطبيقات الأثقل.",
    "layout.reason.kept_apart":
      "يعمل {title} على {node} بمعزل عن تطبيقاتك المهمة، حتى لا يبطئها حمله.",
    "layout.reason.tight_fit":
      "يتسع {node} لـ {title} ويُبقي مساحة أكبر فارغة على {rival}.",
    "layout.reason.verified_hardware":
      "أكّد {node} العتاد الذي يعتمد عليه {title}.",
    "layout.reason.equal_choice":
      "يناسب {node} و{rival} التطبيق {title} بالقدر نفسه، لذلك اختار kombify الخادم {node}.",
    "layout.reason.only_fitting_node":
      "{node} هو الخادم الوحيد الذي يوفّر ما يحتاجه {title}.",
    "layout.reason.profile_not_measured":
      "لم يقس kombify التطبيق {title} بعد، لذلك يبقى على خادمك الرئيسي {node}.",
    "layout.reason.pinned": "يبقى {title} على {node}.",
    "layout.reason.with_use_case":
      "يضيف {title} إلى حالة الاستخدام التي تعمل بالفعل على {node}، لذلك يعمل هناك أيضًا.",
    "layout.reason.repacked":
      "يعمل {title} على {node} حتى تتسع جميع التطبيقات التي اخترتها.",
    "layout.reason.uses_accelerator":
      "يحتاج {title} إلى بطاقة رسوميات {vendor}، وهي متوفرة على {node}.",
    "layout.reason.measured_reservation":
      "قاس kombify التطبيق {title}: فهو يحجز {ram} غيغابايت من الذاكرة.",
    "layout.reason.estimated_profile":
      "لم يقس kombify التطبيق {title} بعد، لذلك حجمه تقديري.",
    "layout.reason.evidence_unverified":
      "لم يُبلغ {node} عن كل شيء بعد ({missing})، لذلك لم يتأكد هذا الجزء بالكامل.",
    "layout.reason.node_not_owner_controlled":
      "{node} ليس أحد خوادمك، لذلك لا يوضع عليه أي تطبيق.",
    "layout.reason.node_not_connected": "{node} غير متصل، لذلك تم استبعاده.",
    "layout.reason.node_capacity_unknown":
      "لم يُبلغ {node} عن عتاده بعد، لذلك تم استبعاده.",
    "layout.reason.no_eligible_node": "لم يُبلغ أي خادم متصل عن عتاده بعد.",
    "layout.reason.infeasibility_unproven_unknown_node":
      "قد يتسع خادم لم يُبلغ عن عتاده بعد، لكن kombify لا يستطيع الجزم بذلك الآن.",
    "layout.reason.needs_more_ram":
      "يحتاج {title} إلى {need} غيغابايت من الذاكرة، لكن أكبر ذاكرة متاحة على أي خادم هي {largest} غيغابايت.",
    "layout.reason.needs_more_cpu":
      "يحتاج {title} إلى {need} من أنوية المعالج، بينما يملك أكبر خادم لديك {largest}.",
    "layout.reason.needs_more_storage":
      "يحتاج {title} إلى {need} غيغابايت من التخزين، لكن أكبر مساحة متاحة على أي خادم هي {largest} غيغابايت.",
    "layout.reason.missing_accelerator":
      "يحتاج {title} إلى بطاقة رسوميات {vendor}، ولا يملكها أي من خوادمك.",
    "layout.reason.pinned_node_lacks_room":
      "{title} مرتبط بخادم لا تتسع مساحته له.",
    "layout.reason.no_node_meets_requirements":
      "لا يلبّي أي من خوادمك ما يحتاجه {title}.",
    "layout.reason.selection_exceeds_free_ram":
      "تحجز التطبيقات التي اخترتها {need} غيغابايت من الذاكرة، بينما المتاح على خوادمك {free} غيغابايت.",
    "layout.reason.no_combination_fits":
      "لا توجد طريقة لتوزيع التطبيقات التي اخترتها بحيث تتسع جميعها.",

    "layout.addUseCases.pageTitle": "إضافة حالات استخدام | kombify-Techstack",
    "layout.addUseCases.entry": "إضافة حالات استخدام",
    "layout.addUseCases.title": "إضافة حالات استخدام",
    "layout.addUseCases.subtitle":
      "اختر ما الذي يجب أن يفعله Homelab لديك أيضًا. يوصي kombify بالخادم الذي يشغّل كل تطبيق.",
    "layout.addUseCases.submit": "إضافة حالات الاستخدام",
    "layout.addUseCases.submitting": "جارٍ إضافة حالات الاستخدام…",
    "layout.addUseCases.backToHomelab": "العودة إلى Homelab",
    "layout.addUseCases.accepted.title": "أُضيفت إلى خطة Homelab لديك",
    "layout.addUseCases.accepted.body":
      "تم حفظ حالات الاستخدام. ستُثبَّت على خوادمك عند تطبيق التغيير.",
    "layout.addUseCases.provisioning.title": "جارٍ إعداد حالات الاستخدام",
    "layout.addUseCases.provisioning.body": "يطبّق Homelab لديك التغيير الآن.",
    "layout.addUseCases.provisioning.progress": "عرض التقدّم",
    "layout.addUseCases.superseded.title": "تغيّر Homelab لديك في هذه الأثناء",
    "layout.addUseCases.superseded.body":
      "لم يتغيّر شيء. تظهر التوصية المحدّثة أعلاه. راجعها ثم أضف حالات الاستخدام مرة أخرى للتأكيد.",
    "layout.addUseCases.stale.title": "التوصية لم تعد محدّثة",
    "layout.addUseCases.stale.body":
      "لم يتغيّر شيء. راجع التوزيع مرة أخرى ثم أضف حالات الاستخدام من جديد.",
    "layout.addUseCases.unavailable.title": "تعذّر فحص التوصية الآن",
    "layout.addUseCases.unavailable.body":
      "لم يتغيّر شيء. حاول مرة أخرى بعد قليل.",
    "layout.addUseCases.failed.title": "تعذّرت إضافة حالات الاستخدام",
    "layout.addUseCases.failed.body":
      "تحقق من اختياراتك وحاول مرة أخرى. إجاباتك لا تزال هنا.",
    "wizard.validation.goalsRequired":
      "اختر حالة استخدام واحدة على الأقل للمتابعة.",

    "wizard.hints.why": "لماذا هذا الإعداد",
    "wizard.hints.reason.goal_alignment":
      "يتوافق مع حالات الاستخدام التي اخترتها.",
    "wizard.hints.reason.service_alignment": "يتوافق مع الخدمات التي اخترتها.",
    "wizard.hints.reason.provider_alignment":
      "يأخذ في الحسبان المزوّد الذي اخترته.",
    "wizard.hints.reason.safe_standard":
      "إلى أن تختار مزوّدًا، يستخدم kombify إعداده المُدار القياسي.",
    "wizard.hints.reason.operator_capability_band":
      "يتبع مقدار التفاصيل طريقة استخدامك لـ kombify.",
    "wizard.hints.reason.low_compute_profile":
      "أحد خوادمك ذاكرته قليلة، لذلك يناسبه إعداد أخف.",
    "wizard.hints.reason.cloud_owner_asset":
      "يُعدّ حساب kombify لديك تسجيل دخول المالك نيابةً عنك.",
    "wizard.hints.reason.selection_capacity_shortfall":
      "تحتاج التطبيقات التي اخترتها إلى أكثر مما هو متاح على خوادمك الآن.",
    "wizard.hints.reason.module_capacity_shortfall":
      "يحتاج تطبيق واحد على الأقل مما اخترته إلى أكثر مما يستطيع أي من خوادمك منحه الآن.",
    "wizard.hints.reason.module_capacity_unverified":
      "لم يُبلغ كل خادم عن عتاده، لذلك لا نستطيع الجزم بأن كل شيء سيتسع.",
    "wizard.hints.reason.catalog_footprint_unavailable":
      "حجم التطبيقات التي اخترتها غير معروف بعد، لذلك لم يُفحص ما إذا كانت تتسع.",
  },
} as const;
