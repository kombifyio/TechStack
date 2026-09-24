export const loginMessages = {
  en: {
    "wizard.identity.eyebrow": "One last key",
    "wizard.identity.title": "Make this home yours.",
    "wizard.identity.ready": "Your sign-in is ready.",
    "wizard.identity.loading": "Checking your Homelab sign-in…",
    "wizard.identity.active": "Your owner account has an active passkey.",
    "wizard.identity.expired":
      "Your setup link has expired. Create a fresh one to finish your sign-in.",
    "wizard.identity.unavailable":
      "Your installation has not reported a ready identity service yet. Check again once it is reachable.",
    "wizard.identity.pending":
      "Open your Homelab’s secure sign-in page and create a passkey with your device. Keep this window open; we’ll check when you’re ready.",
    "wizard.identity.statusError":
      "We couldn’t check your sign-in. Your installation is still saved. Try checking again.",
    "wizard.identity.activationError":
      "We couldn’t open a verified setup link. Check the connection and try again.",
    "wizard.identity.invitationError":
      "We couldn’t prepare this person’s setup link. Try again once your identity service is reachable.",
    "wizard.identity.copyError":
      "The link couldn’t be copied. Allow clipboard access and try again.",
    "wizard.identity.reissue": "Create a fresh setup link",
    "wizard.identity.activate": "Set up my passkey",
    "wizard.identity.openActivation": "Open sign-in setup",
    "wizard.identity.check": "Check status",
    "wizard.identity.destination": "Your secure sign-in:",
    "wizard.identity.people": "Bring your people in",
    "wizard.identity.peopleReady":
      "Prepare a personal setup link, then share it yourself. Each person creates their own passkey.",
    "wizard.identity.peopleWait":
      "Finish your own sign-in first. Then you can prepare a personal link for each person.",
    "wizard.identity.emailNeeded":
      "Add an email to prepare this person’s access.",
    "wizard.identity.email": "Email for {name}",
    "wizard.identity.memberReady": "Account prepared",
    "wizard.identity.copy": "Copy personal link",
    "wizard.identity.copied": "Copied",
    "wizard.identity.invite": "Prepare access",
    "wizard.identity.installationComplete": "Your installation is complete",
    "wizard.identity.installationDescription":
      "Your services are installed. Finish your sign-in above, then open your Homelab.",
    "wizard.login.setup.eyebrow": "Your digital home · Your identity",
    "wizard.login.setup.title": "One familiar face. Your own front door.",
    "wizard.login.setup.subtitle":
      "Choose who will own your digital home. We will guide you through a secure first sign-in once it is ready.",
    "wizard.login.profile.title": "Start with my kombify profile",
    "wizard.login.profile.description":
      "Use your name and email for the first owner.",
    "wizard.login.profile.local": "Create a new Homelab user",
    "wizard.login.profile.localDescription":
      "Give this home its own owner profile.",
    "wizard.login.profile.current": "Your profile",
    "wizard.login.profile.selected": "Selected",
    "wizard.login.profile.verified": "Verified email",
    "wizard.login.profile.unverified":
      "Use a kombify profile with a verified email, or create a new Homelab user.",
    "wizard.login.profile.emailRequired":
      "Enter a valid email address for your Homelab owner.",
    "wizard.login.profile.dataOnly":
      "Your name and email are reused. Your Homelab gets its own secure sign-in.",
    "wizard.login.profile.email": "Email address",
    "wizard.login.profile.name": "Name · optional",
    "wizard.login.profile.emailHint":
      "This identifies the owner. It does not send an invitation or create a kombify account.",
    "wizard.login.profile.username": "Your username",
    "wizard.login.profile.usernameHint":
      "Suggested from your email. Change it if your tools need a particular username.",
    "wizard.login.profile.linkHint":
      "Connect a kombify profile through the secure sign-in window. You can also continue with a local user.",
    "wizard.login.profile.existing": "Your existing login comes with you.",
    "wizard.login.profile.existingHint":
      "This Node joins your Homelab. Your owner and sign-in settings stay in place.",
    "wizard.login.passkey.title": "Your key, already in your pocket.",
    "wizard.login.passkey.label": "Passkey",
    "wizard.login.passkey.recommended": "Recommended",
    "wizard.login.passkey.description":
      "Sign in with your fingerprint, face or device unlock. There is no everyday password to remember.",
    "wizard.login.passkey.afterSetup": "Set up after installation",
    "wizard.login.passkey.next":
      "Once your home is ready, we will take you to its secure sign-in page to create your first passkey.",
    "wizard.login.passkey.device": "Your device",
    "wizard.login.passkey.home": "Your digital home",
    "wizard.login.passkey.steps":
      "Choose your profile → Create your home → Set up your passkey",
    "wizard.login.details.title": "Make it yours",
    "wizard.login.details.subtitle":
      "Sign-in, recovery and the tools behind them",
    "wizard.login.details.tabs": "Login settings",
    "wizard.login.details.signin": "Sign-in",
    "wizard.login.details.signinDescription": "Your everyday access",
    "wizard.login.details.recovery": "Recovery",
    "wizard.login.details.recoveryDescription": "Keep a way back in",
    "wizard.login.details.tools": "Tools & identity",
    "wizard.login.details.toolsDescription": "Understand what runs your login",
    "wizard.login.signin.title": "A passkey for your own home",
    "wizard.login.signin.description":
      "Pocket ID manages your Homelab login. After activation, manage additional passkeys on its account page.",
    "wizard.login.signin.noTransfer":
      "A passkey used for kombify is separate from the one you create for your Homelab.",
    "wizard.login.recovery.auto": "Keep your recovery material somewhere safe",
    "wizard.login.recovery.autoDescription":
      "The installation prepares your owner recovery material. The completion screen shows where to retrieve it. Save it before you need it.",
    "wizard.login.recovery.extra": "An extra way back in",
    "wizard.login.recovery.extraDescription":
      "An optional recovery phrase is separate from your everyday sign-in. Only its hash is sent during setup.",
    "wizard.login.tools.identity": "Your Homelab identity",
    "wizard.login.tools.identityDescription":
      "Pocket ID holds your local owner and passkeys.",
    "wizard.login.tools.gateway": "Access gateway",
    "wizard.login.tools.gatewayDescription":
      "TinyAuth protects the front door to supported tools. It does not replace your identity provider.",
    "wizard.login.tools.cloud": "kombify account",
    "wizard.login.tools.cloudDescription":
      "Account creation and kombify sign-in stay on login.kombify.io. Your local Homelab can work independently.",
    "wizard.login.recovery.strength": "Strength",
    "wizard.login.recovery.mismatch": "Recovery phrases do not match.",
  },
  de: {
    "wizard.identity.eyebrow": "Dein letzter Schlüssel",
    "wizard.identity.title": "Mach dieses Zuhause zu deinem.",
    "wizard.identity.ready": "Deine Anmeldung ist bereit.",
    "wizard.identity.loading": "Deine Homelab-Anmeldung wird geprüft…",
    "wizard.identity.active":
      "Für deinen Eigentümer ist ein Passkey eingerichtet.",
    "wizard.identity.expired":
      "Dein Einrichtungslink ist abgelaufen. Erstelle einen neuen, um deine Anmeldung abzuschließen.",
    "wizard.identity.unavailable":
      "Deine Installation meldet noch keinen bereiten Identitätsdienst. Prüfe erneut, sobald er erreichbar ist.",
    "wizard.identity.pending":
      "Öffne die sichere Anmeldeseite deines Homelabs und erstelle einen Passkey mit deinem Gerät. Lass dieses Fenster geöffnet; wir prüfen anschließend den Status.",
    "wizard.identity.statusError":
      "Deine Anmeldung konnte nicht geprüft werden. Deine Installation bleibt gespeichert. Prüfe den Status erneut.",
    "wizard.identity.activationError":
      "Es konnte kein geprüfter Einrichtungslink geöffnet werden. Prüfe die Verbindung und versuche es erneut.",
    "wizard.identity.invitationError":
      "Der persönliche Einrichtungslink konnte nicht vorbereitet werden. Versuche es erneut, sobald dein Identitätsdienst erreichbar ist.",
    "wizard.identity.copyError":
      "Der Link konnte nicht kopiert werden. Erlaube den Zugriff auf die Zwischenablage und versuche es erneut.",
    "wizard.identity.reissue": "Neuen Einrichtungslink erstellen",
    "wizard.identity.activate": "Meinen Passkey einrichten",
    "wizard.identity.openActivation": "Anmeldung einrichten",
    "wizard.identity.check": "Status prüfen",
    "wizard.identity.destination": "Deine sichere Anmeldung:",
    "wizard.identity.people": "Hol deine Menschen dazu",
    "wizard.identity.peopleReady":
      "Bereite einen persönlichen Link vor und teile ihn selbst. Jede Person richtet ihren eigenen Passkey ein.",
    "wizard.identity.peopleWait":
      "Richte zuerst deine eigene Anmeldung ein. Danach kannst du für jede Person einen persönlichen Link vorbereiten.",
    "wizard.identity.emailNeeded":
      "Ergänze eine E-Mail-Adresse, um den Zugang vorzubereiten.",
    "wizard.identity.email": "E-Mail für {name}",
    "wizard.identity.memberReady": "Konto vorbereitet",
    "wizard.identity.copy": "Persönlichen Link kopieren",
    "wizard.identity.copied": "Kopiert",
    "wizard.identity.invite": "Zugang vorbereiten",
    "wizard.identity.installationComplete":
      "Deine Installation ist abgeschlossen",
    "wizard.identity.installationDescription":
      "Deine Dienste sind installiert. Schließe oben deine Anmeldung ab und öffne anschließend dein Homelab.",
    "wizard.login.setup.eyebrow": "Dein digitales Zuhause · Deine Identität",
    "wizard.login.setup.title": "Dein Profil. Deine eigene Haustür.",
    "wizard.login.setup.subtitle":
      "Wähle, wem dein digitales Zuhause gehört. Sobald es bereit ist, begleiten wir dich zur ersten sicheren Anmeldung.",
    "wizard.login.profile.title": "Mit meinem kombify-Profil starten",
    "wizard.login.profile.description":
      "Name und E-Mail für den ersten Eigentümer übernehmen.",
    "wizard.login.profile.local": "Neuen Homelab-Benutzer anlegen",
    "wizard.login.profile.localDescription":
      "Ein eigenes Profil für dieses Zuhause anlegen.",
    "wizard.login.profile.current": "Dein Profil",
    "wizard.login.profile.selected": "Ausgewählt",
    "wizard.login.profile.verified": "E-Mail bestätigt",
    "wizard.login.profile.unverified":
      "Nutze ein kombify-Profil mit bestätigter E-Mail oder lege einen neuen Homelab-Benutzer an.",
    "wizard.login.profile.emailRequired":
      "Gib eine gültige E-Mail-Adresse für den Eigentümer deines Homelabs ein.",
    "wizard.login.profile.dataOnly":
      "Name und E-Mail werden übernommen. Dein Homelab erhält eine eigene sichere Anmeldung.",
    "wizard.login.profile.email": "E-Mail-Adresse",
    "wizard.login.profile.name": "Name · optional",
    "wizard.login.profile.emailHint":
      "Damit wird der Eigentümer festgelegt. Es wird keine Einladung verschickt und kein kombify-Konto angelegt.",
    "wizard.login.profile.username": "Dein Benutzername",
    "wizard.login.profile.usernameHint":
      "Aus deiner E-Mail vorgeschlagen. Bei Bedarf kannst du ihn für deine Tools anpassen.",
    "wizard.login.profile.linkHint":
      "Verbinde dein kombify-Profil über das sichere Anmeldefenster. Du kannst auch mit einem lokalen Benutzer fortfahren.",
    "wizard.login.profile.existing": "Dein bestehender Zugang zieht mit.",
    "wizard.login.profile.existingHint":
      "Dieser Node ergänzt dein Homelab. Eigentümer und Anmeldung bleiben erhalten.",
    "wizard.login.passkey.title": "Dein Schlüssel ist schon bei dir.",
    "wizard.login.passkey.label": "Passkey",
    "wizard.login.passkey.recommended": "Empfohlen",
    "wizard.login.passkey.description":
      "Melde dich mit Fingerabdruck, Gesicht oder Gerätesperre an. Ein Passwort für den Alltag musst du dir nicht merken.",
    "wizard.login.passkey.afterSetup": "Nach der Installation einrichten",
    "wizard.login.passkey.next":
      "Sobald dein Zuhause bereit ist, führen wir dich zu seiner sicheren Anmeldeseite. Dort legst du deinen ersten Passkey an.",
    "wizard.login.passkey.device": "Dein Gerät",
    "wizard.login.passkey.home": "Dein digitales Zuhause",
    "wizard.login.passkey.steps":
      "Profil wählen → Zuhause erstellen → Passkey einrichten",
    "wizard.login.details.title": "So, wie es zu dir passt",
    "wizard.login.details.subtitle":
      "Anmeldung, Wiederherstellung und die Tools dahinter",
    "wizard.login.details.tabs": "Einstellungen zur Anmeldung",
    "wizard.login.details.signin": "Anmeldung",
    "wizard.login.details.signinDescription": "Dein Zugang im Alltag",
    "wizard.login.details.recovery": "Wiederherstellung",
    "wizard.login.details.recoveryDescription": "Behalte einen Weg zurück",
    "wizard.login.details.tools": "Tools & Identität",
    "wizard.login.details.toolsDescription": "Was deine Anmeldung ermöglicht",
    "wizard.login.signin.title": "Ein Passkey für dein eigenes Zuhause",
    "wizard.login.signin.description":
      "Pocket ID verwaltet deine Homelab-Anmeldung. Nach der Aktivierung kannst du dort weitere Passkeys hinzufügen.",
    "wizard.login.signin.noTransfer":
      "Dein Passkey für kombify und der neue Passkey für dein Homelab sind unabhängig voneinander.",
    "wizard.login.recovery.auto": "Bewahre deine Wiederherstellung sicher auf",
    "wizard.login.recovery.autoDescription":
      "Die Installation bereitet die Wiederherstellungsdaten vor. Zum Abschluss erfährst du, wo du sie abrufen kannst. Sichere sie, bevor du sie brauchst.",
    "wizard.login.recovery.extra": "Ein zusätzlicher Weg zurück",
    "wizard.login.recovery.extraDescription":
      "Eine optionale Wiederherstellungsphrase ist von deiner täglichen Anmeldung getrennt. Beim Setup wird nur ihr Hash übertragen.",
    "wizard.login.tools.identity": "Deine Homelab-Identität",
    "wizard.login.tools.identityDescription":
      "Pocket ID verwaltet den lokalen Eigentümer und seine Passkeys.",
    "wizard.login.tools.gateway": "Zugang zu deinen Tools",
    "wizard.login.tools.gatewayDescription":
      "TinyAuth schützt den Zugang zu unterstützten Tools. Es ersetzt nicht deinen Identitätsanbieter.",
    "wizard.login.tools.cloud": "kombify-Konto",
    "wizard.login.tools.cloudDescription":
      "Kontoerstellung und kombify-Anmeldung bleiben auf login.kombify.io. Dein lokales Homelab kann unabhängig davon arbeiten.",
    "wizard.login.recovery.strength": "Stärke",
    "wizard.login.recovery.mismatch":
      "Die Wiederherstellungsphrasen stimmen nicht überein.",
  },
};
