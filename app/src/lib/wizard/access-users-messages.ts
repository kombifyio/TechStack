type AccessUsersLocale = "en" | "de";

export const accessUsersMessages = {
  en: {
    "wizard.access.address.managedBody":
      "Your kombify VPS uses its managed address. Own domains are not available in this setup yet.",
    "wizard.navigation.back": "Back",
    "wizard.navigation.next": "Next",
    "wizard.access.forYou": "For what you have chosen",
    "wizard.access.context.photos":
      "Want your holiday photos to arrive in your own library while you travel? Choose access from home and away.",
    "wizard.access.context.files":
      "Your documents can travel with you: open a file at work or find a booking while on holiday. Access from home and away fits that use.",
    "wizard.access.context.vault":
      "You may need your passwords away from home too. Access from home and away keeps that need in your setup plan.",
    "wizard.access.context.media":
      "Watch on your sofa, or take your library on a trip. Choose the places where you want to enjoy it.",
    "wizard.access.context.smart-home":
      "Control your home while you are there, or check on it while travelling. Home-only is enough if you use it only on your own Wi-Fi.",
    "wizard.access.context.default":
      "Your own device can stay at home while you use your digital home on the go. This choice is about your access; who else can use it comes next.",
    "wizard.users.profile.shared": "With family & friends",
    "wizard.users.profile.sharedHint":
      "Personal logins for people you invite. Share selected things together.",
    "wizard.users.people.prepare": "Prepare personal invitations · optional",
    "wizard.users.public.title": "Something for everyone?",
    "wizard.users.public.body":
      "A public website, a community or an open game server needs a different setup from sharing privately with people you know.",
    "wizard.users.public.choice": "I also want to offer something publicly",
    "wizard.users.public.consequence":
      "Public access needs a separate, supported setup",
    "wizard.users.public.pending":
      "This release cannot yet configure public access for individual applications in the wizard. To continue today, keep your setup private. Public addresses, certificates and application access must be configured together before anything is published.",
    "wizard.users.public.private":
      "Access while travelling does not make your home public. Your tools keep their sign-in protection.",
    "wizard.access.eyebrow": "Access",
    "wizard.access.heading": "At home. Away. Or both?",
    "wizard.access.intro":
      "Where do you want to use your photos, files and the rest of your digital home?",
    "wizard.access.home.label": "At home, on my own Wi-Fi",
    "wizard.access.home.body":
      "Use your digital home on your home network. You do not need access while travelling.",
    "wizard.access.home.managedReason":
      "Your kombify VPS is in a data centre. Choose access from home and away for this server.",
    "wizard.access.anywhere.label": "At home and on the go",
    "wizard.access.anywhere.body":
      "Reach your digital home on holiday, at work or while visiting friends.",
    "wizard.access.anywhere.managedBody":
      "Reach your kombify VPS from home or while travelling, through its protected sign-in.",
    "wizard.access.recommended": "Recommended",
    "wizard.access.route.planned": "Planned route",
    "wizard.access.route.home": "Your devices → local access → your tools",
    "wizard.access.route.anywhere":
      "Your devices → protected access → your tools",
    "wizard.access.route.homeNote":
      "The exact local address is confirmed during setup.",
    "wizard.access.route.anywhereNote":
      "The available private or managed route is confirmed before rollout.",
    "wizard.access.route.tool": "Access route",
    "wizard.access.route.homeTool": "Supported local route",
    "wizard.access.route.anywhereTool": "Supported protected route",
    "wizard.access.customize": "Customize access",
    "wizard.access.details.label": "Access details",
    "wizard.access.details.address": "Address",
    "wizard.access.details.connection": "Connection",
    "wizard.access.details.publication": "Publishing",
    "wizard.access.connection.homeTitle": "Available in your home setup",
    "wizard.access.connection.homeBody":
      "This choice keeps the route within the access supported by the selected node. It does not promise a local network for a cloud node.",
    "wizard.access.connection.anywhereTitle": "Reachability intent recorded",
    "wizard.access.connection.anywhereBody":
      "The rollout may use only a route admitted by the selected runtime. A private client or managed route is shown once that capability is known.",
    "wizard.access.address.title": "Choose how the address is prepared",
    "wizard.access.address.automatic": "Set automatically",
    "wizard.access.address.automaticBody":
      "Setup proposes an address only after the selected route is known.",
    "wizard.access.address.custom": "Use my own domain",
    "wizard.access.address.customBody":
      "Save a domain request for this Techstack. It does not change DNS by itself.",
    "wizard.access.address.domainLabel": "Base domain",
    "wizard.access.address.domainPlaceholder": "home.example.com",
    "wizard.access.address.prerequisites": "Before the address can work",
    "wizard.access.address.prerequisitesBody":
      "Your DNS must point to the effective route, and TLS issuance must succeed there. Setup will show the concrete records and remaining action once a supported route owns them.",
    "wizard.access.publication.privateTitle": "No public service publishing",
    "wizard.access.publication.privateBody":
      "Remote reachability stays separate from publishing a service on the public internet.",
    "wizard.access.publication.pendingTitle":
      "Existing public preference needs capability confirmation",
    "wizard.access.publication.pendingBody":
      "The saved preference is retained, but this wizard will not present it as active until a supported per-service route is available.",
    "wizard.users.eyebrow": "People",
    "wizard.users.heading": "Yours to keep. Yours to share.",
    "wizard.users.intro":
      "Decide who your digital home is for. Personal invitations and public offers are two different things.",
    "wizard.users.profile.label": "Household",
    "wizard.users.profile.solo": "Only me",
    "wizard.users.profile.soloHint":
      "Your photos, files and tools stay in your own account.",
    "wizard.users.people.title": "Prepare people",
    "wizard.users.people.body":
      "These are invitation drafts, not active accounts. No invitation is sent during setup.",
    "wizard.users.people.owner": "Owner",
    "wizard.users.people.ownerFixed": "Owner · fixed",
    "wizard.users.people.member": "Household member",
    "wizard.users.people.name": "Name",
    "wizard.users.people.email": "Email address",
    "wizard.users.people.namePlaceholder": "Name (optional)",
    "wizard.users.people.emailPlaceholder": "Email (optional)",
    "wizard.users.people.remove": "Remove person",
    "wizard.users.people.add": "Add person",
    "wizard.users.people.later": "Invite later",
    "wizard.users.people.laterBody":
      "You can continue without drafts and create individual invitations after the owner is active.",
    "wizard.users.access.title": "Shape access together",
    "wizard.users.access.body":
      "Household members receive the supported member role. More detailed areas appear only when their policies can be enforced.",
  },
  de: {
    "wizard.access.address.managedBody":
      "Dein kombify VPS verwendet seine verwaltete Adresse. Eigene Domains sind in dieser Einrichtung noch nicht verfügbar.",
    "wizard.navigation.back": "Zurück",
    "wizard.navigation.next": "Weiter",
    "wizard.access.forYou": "Passend zu deiner Auswahl",
    "wizard.access.context.photos":
      "Sollen deine Urlaubsfotos schon auf Reisen in deiner eigenen Mediathek landen? Dafür passt der Zugang zu Hause und unterwegs.",
    "wizard.access.context.files":
      "Deine Dokumente können mitreisen: eine Datei im Büro öffnen oder die Buchung im Urlaub finden. Dafür passt der Zugang zu Hause und unterwegs.",
    "wizard.access.context.vault":
      "Deine Passwörter brauchst du möglicherweise auch außer Haus. Mit Zugang zu Hause und unterwegs planst du das direkt mit ein.",
    "wizard.access.context.media":
      "Auf dem Sofa schauen oder die eigene Mediathek mit auf Reisen nehmen? Wähle, wo du sie nutzen möchtest.",
    "wizard.access.context.smart-home":
      "Dein Zuhause vor Ort steuern oder auf Reisen nach dem Rechten sehen? Nur zu Hause reicht, wenn du alles ausschließlich im eigenen WLAN nutzt.",
    "wizard.access.context.default":
      "Dein eigenes Gerät kann zu Hause stehen, während du dein digitales Zuhause unterwegs nutzt. Hier geht es um deinen Zugang; wer sonst noch Zugriff bekommt, entscheidest du anschließend.",
    "wizard.users.profile.shared": "Mit Familie & Freunden",
    "wizard.users.profile.sharedHint":
      "Eigene Anmeldungen für eingeladene Menschen. Ausgewählte Dinge gemeinsam nutzen.",
    "wizard.users.people.prepare":
      "Persönliche Einladungen vorbereiten · optional",
    "wizard.users.public.title": "Etwas für alle?",
    "wizard.users.public.body":
      "Eine öffentliche Website, eine Community oder ein offener Spieleserver brauchen eine andere Einrichtung als das private Teilen mit vertrauten Menschen.",
    "wizard.users.public.choice": "Ich möchte auch etwas öffentlich anbieten",
    "wizard.users.public.consequence":
      "Öffentlicher Zugang braucht eine eigene, unterstützte Einrichtung",
    "wizard.users.public.pending":
      "Diese Version kann öffentliche Freigaben einzelner Anwendungen im Wizard noch nicht einrichten. Für die Einrichtung heute bleibt dein Zuhause privat. Öffentliche Adressen, Zertifikate und Anwendungszugriff müssen zusammen konfiguriert werden, bevor etwas veröffentlicht wird.",
    "wizard.users.public.private":
      "Unterwegs zugreifen macht dein Zuhause nicht öffentlich. Deine Tools behalten ihren Anmeldeschutz.",
    "wizard.access.eyebrow": "Zugang",
    "wizard.access.heading": "Zu Hause. Unterwegs. Oder beides?",
    "wizard.access.intro":
      "Wo möchtest du deine Fotos, Dokumente und den Rest deines digitalen Zuhauses nutzen?",
    "wizard.access.home.label": "Zu Hause im eigenen WLAN",
    "wizard.access.home.body":
      "Nutze dein digitales Zuhause in deinem Heimnetz. Auf Reisen brauchst du keinen Zugriff.",
    "wizard.access.home.managedReason":
      "Dein kombify VPS steht im Rechenzentrum. Wähle für diesen Server den Zugang zu Hause und unterwegs.",
    "wizard.access.anywhere.label": "Zu Hause und unterwegs",
    "wizard.access.anywhere.body":
      "Erreiche dein digitales Zuhause im Urlaub, im Büro oder beim Besuch bei Freunden.",
    "wizard.access.anywhere.managedBody":
      "Erreiche deinen kombify VPS von zu Hause und auf Reisen über seine geschützte Anmeldung.",
    "wizard.access.recommended": "Empfohlen",
    "wizard.access.route.planned": "Geplanter Weg",
    "wizard.access.route.home": "Deine Geräte → lokaler Zugang → deine Tools",
    "wizard.access.route.anywhere":
      "Deine Geräte → geschützter Zugang → deine Tools",
    "wizard.access.route.homeNote":
      "Die genaue lokale Adresse wird bei der Einrichtung bestätigt.",
    "wizard.access.route.anywhereNote":
      "Der verfügbare private oder verwaltete Weg wird vor dem Rollout bestätigt.",
    "wizard.access.route.tool": "Zugangsweg",
    "wizard.access.route.homeTool": "Unterstützter lokaler Weg",
    "wizard.access.route.anywhereTool": "Unterstützter geschützter Weg",
    "wizard.access.customize": "Zugang anpassen",
    "wizard.access.details.label": "Zugangsdetails",
    "wizard.access.details.address": "Adresse",
    "wizard.access.details.connection": "Verbindung",
    "wizard.access.details.publication": "Freigaben",
    "wizard.access.connection.homeTitle": "In deinem Zuhause erreichbar",
    "wizard.access.connection.homeBody":
      "Diese Wahl nutzt den vom gewählten Node unterstützten Zugang. Bei einem Cloud-Node verspricht sie kein lokales Heimnetz.",
    "wizard.access.connection.anywhereTitle": "Zugriffswunsch vorgemerkt",
    "wizard.access.connection.anywhereBody":
      "Der Rollout darf nur einen vom gewählten Runtime-Weg unterstützten Zugang nutzen. Ein privater Client oder verwalteter Weg wird angezeigt, sobald die Fähigkeit feststeht.",
    "wizard.access.address.title":
      "Lege fest, wie die Adresse vorbereitet wird",
    "wizard.access.address.automatic": "Automatisch festlegen",
    "wizard.access.address.automaticBody":
      "Das Setup schlägt erst eine Adresse vor, wenn der gewählte Zugangsweg feststeht.",
    "wizard.access.address.custom": "Eigene Domain verwenden",
    "wizard.access.address.customBody":
      "Speichere einen Domain-Wunsch für diesen Techstack. DNS wird dadurch nicht automatisch geändert.",
    "wizard.access.address.domainLabel": "Basis-Domain",
    "wizard.access.address.domainPlaceholder": "home.example.com",
    "wizard.access.address.prerequisites": "Damit die Adresse funktioniert",
    "wizard.access.address.prerequisitesBody":
      "Dein DNS muss auf den wirksamen Zugangsweg zeigen und die TLS-Ausstellung muss dort gelingen. Das Setup zeigt die konkreten Einträge und den verbleibenden Schritt, sobald ein unterstützter Weg sie übernimmt.",
    "wizard.access.publication.privateTitle": "Keine öffentlichen Dienste",
    "wizard.access.publication.privateBody":
      "Fernzugriff bleibt getrennt davon, einen Dienst im öffentlichen Internet freizugeben.",
    "wizard.access.publication.pendingTitle":
      "Bestehende Freigabe braucht eine Fähigkeitsprüfung",
    "wizard.access.publication.pendingBody":
      "Der gespeicherte Wunsch bleibt erhalten. Der Wizard zeigt ihn erst als aktiv, wenn ein unterstützter Weg pro Dienst verfügbar ist.",
    "wizard.users.eyebrow": "Personen",
    "wizard.users.heading": "Deins. Gemeinsam. Oder für alle?",
    "wizard.users.intro":
      "Lege fest, für wen dein digitales Zuhause gedacht ist. Persönliche Einladungen und öffentliche Angebote sind zwei verschiedene Dinge.",
    "wizard.users.profile.label": "Haushalt",
    "wizard.users.profile.solo": "Nur ich",
    "wizard.users.profile.soloHint":
      "Deine Fotos, Dateien und Tools bleiben in deinem eigenen Konto.",
    "wizard.users.people.title": "Personen vorbereiten",
    "wizard.users.people.body":
      "Das sind Einladungsentwürfe, keine aktiven Konten. Beim Setup wird keine Einladung versendet.",
    "wizard.users.people.owner": "Owner",
    "wizard.users.people.ownerFixed": "Owner · fest",
    "wizard.users.people.member": "Haushaltsmitglied",
    "wizard.users.people.name": "Name",
    "wizard.users.people.email": "E-Mail-Adresse",
    "wizard.users.people.namePlaceholder": "Name (optional)",
    "wizard.users.people.emailPlaceholder": "E-Mail (optional)",
    "wizard.users.people.remove": "Person entfernen",
    "wizard.users.people.add": "Person hinzufügen",
    "wizard.users.people.later": "Später einladen",
    "wizard.users.people.laterBody":
      "Du kannst ohne Entwürfe fortfahren und nach der Aktivierung des Owners einzelne Einladungen erstellen.",
    "wizard.users.access.title": "Zugriff gemeinsam gestalten",
    "wizard.users.access.body":
      "Haushaltsmitglieder erhalten die unterstützte Mitgliedsrolle. Feinere Bereiche erscheinen erst, wenn die zugehörigen Regeln durchgesetzt werden können.",
  },
} as const satisfies Record<AccessUsersLocale, Record<string, string>>;
