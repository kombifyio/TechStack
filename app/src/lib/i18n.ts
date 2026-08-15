/**
 * kombify-TechStack Internationalization (i18n)
 *
 * Simple translation system with English as default language.
 * Language preference is stored in localStorage.
 */

export type Locale = "en" | "de";

export const defaultLocale: Locale = "en";

// Translation dictionary
const translations: Record<Locale, Record<string, string>> = {
  en: {
    // Navigation
    "nav.dashboard": "Dashboard",
    "nav.monitoring": "Monitoring",
    "nav.services": "Services",
    "nav.wallet": "Wallet",
    "nav.help": "Help",
    "nav.settings": "Settings",
    "nav.logout": "Logout",

    // Common
    "common.loading": "Loading...",
    "common.error": "Error",
    "common.save": "Save",
    "common.cancel": "Cancel",
    "common.back": "Back",
    "common.next": "Next",
    "common.create": "Create",
    "common.delete": "Delete",
    "common.edit": "Edit",
    "common.confirm": "Confirm",
    "common.tip": "Tip",
    "common.recommended": "recommended",

    // Wizard
    "wizard.title": "Create kombify-TechStack",
    "wizard.easy": "Easy Setup",
    "wizard.techie": "Techie Mode",
    "wizard.step.goals": "Goals",
    "wizard.step.server": "Server",
    "wizard.step.access": "Access",
    "wizard.step.users": "Users",
    "wizard.step.login": "Login",
    "wizard.creating": "Creating...",
    "wizard.validation.completeFields": "Hint: Complete the highlighted fields",

    // Wizard Step 1 - Goals
    "wizard.goals.title": "Round 1: What do you want to do?",
    "wizard.goals.subtitle":
      "Choose what you want to accomplish with your system. You can always add more features later.",
    "wizard.goals.smartHome.title": "Smart Home",
    "wizard.goals.smartHome.description":
      "Run Home Assistant locally and keep automations working even when the internet is down.",
    "wizard.goals.smartHome.help":
      "Canonical StackKits use case: smart-home with Home Assistant as the default tool.",
    "wizard.goals.smartHome.tip":
      "Best when lights, sensors, heating, and local automations should stay in your trust domain.",
    "wizard.goals.photos.title": "Photo Memories",
    "wizard.goals.photos.description":
      "Your private photo library with AI-assisted organization and family sharing.",
    "wizard.goals.photos.help":
      "Canonical StackKits use case: photos with Immich as the default tool.",
    "wizard.goals.photos.tip":
      "The easiest visible win for most homelabs: private memories without another cloud subscription.",
    "wizard.goals.media.title": "Media Streaming",
    "wizard.goals.media.description":
      "Build a private streaming library for films, series, and household media.",
    "wizard.goals.media.help":
      "Canonical StackKits use case: media with Jellyfin and the curated media stack.",
    "wizard.goals.media.tip":
      "Use a server with enough disk and optional hardware transcoding for the smoothest experience.",
    "wizard.goals.vault.title": "Password Vault",
    "wizard.goals.vault.description":
      "Host your own encrypted password manager behind the StackKit gateway.",
    "wizard.goals.vault.help":
      "Canonical StackKits use case: vault with Vaultwarden as the default tool.",
    "wizard.goals.vault.tip":
      "Good for day-one security because it is small, useful, and easy to back up.",
    "wizard.goals.files.title": "File Sharing",
    "wizard.goals.files.description":
      "Keep documents, shared folders, and sync workflows under your control.",
    "wizard.goals.files.help":
      "Canonical StackKits use case: files with Cloudreve or Nextcloud as curated tools.",
    "wizard.goals.files.tip":
      "Pair it with backups before moving important household documents.",
    "wizard.goals.ai.title": "AI / LLM",
    "wizard.goals.ai.description":
      "Prepare a private local assistant that can work with your own documents and services.",
    "wizard.goals.ai.help":
      "Canonical StackKits use case: ai with Ollama and Open WebUI as the default path.",
    "wizard.goals.ai.tip":
      "Works best on stronger hardware; low-tier nodes can still be planned first.",
    "wizard.goals.dev.title": "Dev Platform",
    "wizard.goals.dev.description":
      "Run code hosting, CI, databases, and development sandboxes on your server.",
    "wizard.goals.dev.help":
      "Canonical StackKits use case: dev with Gitea plus CI.",
    "wizard.goals.dev.tip":
      "Useful when your laptop should stay thin and repeatable environments live on the server.",
    "wizard.goals.mail.title": "Mail Server",
    "wizard.goals.mail.description":
      "Plan a self-managed mail and groupware lane for advanced homelab operators.",
    "wizard.goals.mail.help":
      "Canonical StackKits use case: mail with Stalwart as the default tool.",
    "wizard.goals.mail.tip":
      "Mail has DNS and reputation requirements; the wizard records intent and keeps rollout gated.",
    "wizard.goals.game.title": "Game Server",
    "wizard.goals.game.description":
      "Host persistent multiplayer worlds for friends without exposing your home network directly.",
    "wizard.goals.game.help":
      "Canonical StackKits use case: game for curated game server modules.",
    "wizard.goals.game.tip":
      "Use a low-latency server and keep snapshots before mod updates.",
    "wizard.goals.storage.title":
      "Have my own local storage for photos and documents",
    "wizard.goals.storage.description":
      "Secure and private, accessible only to you",
    "wizard.goals.storage.help":
      "Private cloud storage with automatic sync across devices.",
    "wizard.goals.storage.tip":
      "Recommendation: Start with Nextcloud or similar. Perfect first step into self-hosting.",
    "wizard.goals.website.title": "Host my own website and email address",
    "wizard.goals.website.description":
      "Present yourself professionally with your own online presence",
    "wizard.goals.website.help":
      "Full control over your online presence. No monthly fees.",
    "wizard.goals.website.tip":
      "Recommendation: Use reverse proxy (Traefik) for SSL. Email hosting requires good spam protection.",
    "wizard.goals.everything.title": "To infinity and beyond!",
    "wizard.goals.everything.description":
      "The complete solution for maximum independence. Do all at once and even more.",
    "wizard.goals.everything.help":
      "Enables storage, web hosting, monitoring, and more.",
    "wizard.goals.everything.tip":
      "Recommendation: Best for experienced users or if you have dedicated hardware ready.",
    "wizard.goals.tipText":
      "If you're unsure, pick just one goal. kombify-TechStack stays a single system and you can add more services later. Need more settings? Try advanced settings below.",
    "wizard.goals.advancedTitle": "Advanced use cases",
    "wizard.goals.infoText": "Need to do a more technical configuration?",
    "wizard.goals.tryTechie": "Try our Techie Wizard!",

    // Wizard Step 2 - Server
    "wizard.server.title": "Where should the server come from?",
    "wizard.server.subtitle":
      "Generate the one-liner for your own target server, connect an existing server by SSH, or use a managed kombify server when available.",
    "wizard.server.cloud.title": "kombify Cloud Server",
    "wizard.server.cloud.description":
      "kombify provides and prepares a managed subscription server.",
    "wizard.server.cloud.help":
      "This is the managed server option for the SaaS product. Provider selection stays hidden until the provider switcher is ready.",
    "wizard.server.cloud.info":
      "No server settings are required. kombify provisions the subscription server first, then continues with StackKit provisioning.",
    "wizard.server.mode.managed": "managed",
    "wizard.server.remote.title": "Connect my server",
    "wizard.server.remote.description":
      "Connect an existing remote Linux server directly over SSH.",
    "wizard.server.remote.help":
      "Modeled after remote-server setup flows: kombify needs a reachable host, SSH port, user, and key-based access.",
    "wizard.server.remote.host": "Server host or IP",
    "wizard.server.remote.port": "SSH port",
    "wizard.server.remote.user": "SSH user",
    "wizard.server.remote.auth": "Authentication",
    "wizard.server.remote.auth.sshKey": "SSH key",
    "wizard.server.remote.auth.password": "Password",
    "wizard.server.remote.keyLabel": "SSH key label",
    "wizard.server.remote.sudo": "Use sudo for setup commands",
    "wizard.server.oneliner.title": "Give me a one-liner",
    "wizard.server.oneliner.description":
      "Get the install command and an optional one-hour Simulate demo preview.",
    "wizard.server.oneliner.help":
      "The command registers the server with this TechStack instance. Simulate can show a temporary one-hour preview in parallel.",
    "wizard.server.oneliner.info":
      "The one-liner is ready after stack creation. Run it once on the server or device that should join this stack.",

    // Wizard Step 1 - Advanced Settings
    "wizard.advanced.title": "Advanced Settings",
    "wizard.advanced.isolation": "Service Isolation",
    "wizard.advanced.isolation.isolated": "Isolated (recommended)",
    "wizard.advanced.isolation.shared": "Shared resources",
    "wizard.advanced.autostart": "Autostart",
    "wizard.advanced.autostart.auto": "Auto (start on boot)",
    "wizard.advanced.autostart.manual": "Manual",
    "wizard.advanced.backups": "Enable automatic backups",

    // Wizard Step 2 - Access
    "wizard.access.title": "Round 2: Where do you want to use your system?",
    "wizard.access.subtitle":
      "Consider from where you want to access your data and services.",
    "wizard.access.home.title": "Only at home",
    "wizard.access.home.description":
      "Your system stays in your local network, maximum security",
    "wizard.access.home.help":
      "All data stays on your local network. Perfect for privacy-focused setups.",
    "wizard.access.home.tip":
      "Recommendation: Best for beginners. You can always add remote access later.",
    "wizard.access.anywhere.title": "From anywhere!",
    "wizard.access.anywhere.description":
      "Secure access profile for use outside your home network",
    "wizard.access.anywhere.help":
      "Uses the lane-appropriate profile: private mesh for self-hosted, or provider-direct routing for managed SaaS.",
    "wizard.access.anywhere.tip":
      "Recommendation: Start private. Public entrypoints should be explicit service decisions.",
    "wizard.access.tipText":
      '"Access from anywhere" selects an access profile. SaaS uses the managed edge path; self-hosted can use a private mesh or explicit public entrypoint.',
    "wizard.access.vpn.title": "Access Profile",
    "wizard.access.vpn.type": "Private Mesh Type",
    "wizard.access.vpn.headscale": "Headscale (recommended)",
    "wizard.access.vpn.wireguard": "WireGuard",
    "wizard.access.vpn.cloudflare": "Enable Cloudflare Zero Trust",

    // Wizard Step 3 - Users
    "wizard.users.title": "Round 3: Who should use your system?",
    "wizard.users.subtitle":
      "Decide whether you want to access your system alone or with others.",
    "wizard.users.me.title": "Only me",
    "wizard.users.me.description":
      "A personal space, completely under your control",
    "wizard.users.me.help": "Single-user setup with full admin access.",
    "wizard.users.me.tip": "Simplest setup. You can add users later if needed.",
    "wizard.users.family.title": "Family & Friends",
    "wizard.users.family.description":
      "Share photos, documents, or services with your loved ones",
    "wizard.users.family.help":
      "Multi-user with separate accounts. Each user has their own space.",
    "wizard.users.family.tip":
      "Recommendation: Use group-based permissions (family, friends, admins) for easier management.",
    "wizard.users.public.title": "Public users",
    "wizard.users.public.description":
      "Provide content or services for a broader audience",
    "wizard.users.public.help":
      "This exposes some services publicly. Authentication is still required for admin access.",
    "wizard.users.public.tip":
      "Recommendation: Use a central identity provider (Authentik/Authelia) for SSO across services.",
    "wizard.users.tipText":
      "You can invite more users later. Start with what you need now.",

    // Wizard Step 4 - Login
    "wizard.login.title": "Round 4: How do you want to log in?",
    "wizard.login.subtitle":
      "Choose a login method that fits your security needs and comfort level.",
    "wizard.login.owner.title": "Who should own the first login?",
    "wizard.login.owner.subtitle":
      "Keep the same fourth stage, but decide whether the first bootstrap should stay local or link to your kombify Cloud identity.",
    "wizard.login.owner.local.title": "Create a local owner account",
    "wizard.login.owner.local.description":
      "Bootstrap the first owner for this StackKit rollout.",
    "wizard.login.owner.username": "Owner username",
    "wizard.login.owner.email": "Owner email",
    "wizard.login.owner.displayName": "Display name (optional)",
    "wizard.login.owner.preview": "Owner preview",
    "wizard.login.owner.cloudLink.title": "Use kombify Cloud profile",
    "wizard.login.owner.cloudLink.description":
      "Connect your kombify Cloud account; the owner identity is derived from the verified profile.",
    "wizard.login.owner.cloudLink.connect": "Connect kombify Cloud",
    "wizard.login.owner.cloudLink.connecting": "Waiting for kombify Cloud...",
    "wizard.login.owner.cloudLink.waiting":
      "Complete the login in the opened window. This page updates automatically.",
    "wizard.login.owner.cloudLink.popupBlocked":
      "The popup was blocked by your browser.",
    "wizard.login.owner.cloudLink.openInTab": "Open in a new tab",
    "wizard.login.owner.cloudLink.verified": "verified",
    "wizard.login.owner.cloudLink.unverified": "not verified",
    "wizard.login.owner.cloudLink.verifyHint":
      "Verify the email in your kombify Cloud account, then link again.",
    "wizard.login.owner.cloudLink.use": "Use as owner",
    "wizard.login.owner.cloudLink.unlink": "Use a different account",
    "wizard.login.owner.cloudLink.unavailable":
      "This instance has no kombify Cloud login configured.",
    "wizard.login.recovery.title": "Recovery passphrase",
    "wizard.login.recovery.subtitle":
      "This passphrase protects the break-glass recovery bundle. It is hashed client-side before the wizard payload leaves the browser.",
    "wizard.login.recovery.passphrase": "Recovery passphrase",
    "wizard.login.recovery.confirm": "Confirm recovery passphrase",
    "wizard.login.recovery.hashing": "Hashing recovery passphrase...",
    "wizard.login.recovery.ready": "Recovery passphrase ready.",
    "wizard.login.password.title": "Password",
    "wizard.login.password.description":
      "Classic email + password authentication",
    "wizard.login.password.help":
      "Standard login method. Use a strong, unique password.",
    "wizard.login.password.tip":
      "Recommendation: Combine with 2FA for better security.",
    "wizard.login.mfa.title": "Two-Factor (2FA)",
    "wizard.login.mfa.description":
      "Extra security with authenticator app or hardware key",
    "wizard.login.mfa.help":
      "Adds a second verification step. Supports TOTP apps and hardware keys.",
    "wizard.login.mfa.tip":
      "Highly recommended for remote access. Works with Google Authenticator, Authy, etc.",
    "wizard.login.passwordless.title": "Passwordless",
    "wizard.login.passwordless.description":
      "Magic links via email or biometric login",
    "wizard.login.passwordless.help":
      "No password to remember. Login via email link or fingerprint/face.",
    "wizard.login.passwordless.tip":
      "Modern and secure. Requires reliable email delivery.",
    "wizard.login.adminSection": "Admin Account Setup (optional)",
    "wizard.login.adminNote":
      "A deployer account is created automatically. You can set up an additional admin account below.",
    "wizard.login.username": "Username",
    "wizard.login.email": "Email",
    "wizard.login.password": "Password",
    "wizard.login.confirmPassword": "Confirm Password",
    "wizard.login.passwordMismatch": "Passwords do not match",
    "wizard.login.tipText":
      "The deployer account is created during setup. Admin setup is optional.",

    // Settings
    "settings.title": "Settings",
    "settings.language": "Language",
    "settings.language.en": "English",
    "settings.language.de": "German",
    "settings.profile": "Profile",
    "settings.email": "Email",
    "settings.password": "Password",
    "settings.password.change": "Change Password",

    // Auth
    "auth.login": "Login",
    "auth.register": "Register",
    "auth.email": "Email",
    "auth.password": "Password",
    "auth.username": "Username",

    // Session recovery (inline panel)
    "auth.session.renewal.title": "Session refresh needed",
    "auth.session.renewal.body":
      "We could not renew your secure access to kombify Cloud automatically. Your work is safe — sign in again to pick up right where you left off.",
    "auth.session.renewal.signIn": "Sign in again",
    "auth.session.renewal.retry": "Retry",
    "auth.session.renewal.retrying": "Retrying…",
    "auth.session.gatewayUnavailable":
      "Secure access to kombify Cloud could not be verified. Sign in again to continue.",
    "auth.session.tenantContextRequired":
      "Your session carries no kombify organization, so tenant data cannot be shown. Open TechStack from your kombify Cloud portal or sign in again.",

    // Wizard-run resume banner (dashboard, plan D6)
    "wizard.run.banner.inProgressTitle": "Your homelab is being set up",
    "wizard.run.banner.inProgressBody":
      "Setup is still running. You can watch the progress or come back later.",
    "wizard.run.banner.pairingTitle": "A server is waiting to be connected",
    "wizard.run.banner.pairingBody":
      "Run the install command on your server to finish adding it.",
    "wizard.run.banner.failedTitle": "Your homelab setup needs attention",
    "wizard.run.banner.failedBody":
      "The last setup run did not finish. Continue where you left off.",
    "wizard.run.banner.resume": "Continue setup",

    // Session expiry (modal)
    "auth.session.expired.default":
      "Your session has expired. Please sign in again.",
    "auth.session.expired.title": "Session Expired",
    "auth.session.expired.subtitle": "Please log in again",
    "auth.session.expired.saasBody":
      "Your TechStack session expired. Continue with Auth0 to return to this workflow after sign-in.",
    "auth.session.expired.continueAuth0": "Continue with Auth0",
    "auth.session.expired.reconnecting": "Reconnecting...",
    "auth.session.expired.signOut": "Sign out",
    "auth.session.expired.reopenPortal": "Re-open kombify Cloud",
    "auth.session.expired.embeddedHint":
      "Open kombify Cloud in a new tab, sign in there, then retry the session refresh here.",
    "auth.session.expired.embeddedRefreshFailed":
      "Could not refresh the embedded kombify Cloud session. Re-open kombify Cloud to sign in again, then retry.",
    "auth.session.expired.dataPreserved":
      "Your data is preserved. After logging in, you can continue where you left off.",
  },
  de: {
    // Navigation
    "nav.dashboard": "Dashboard",
    "nav.monitoring": "Monitoring",
    "nav.services": "Dienste",
    "nav.wallet": "Wallet",
    "nav.help": "Hilfe",
    "nav.settings": "Einstellungen",
    "nav.logout": "Abmelden",

    // Common
    "common.loading": "Lädt...",
    "common.error": "Fehler",
    "common.save": "Speichern",
    "common.cancel": "Abbrechen",
    "common.back": "Zurück",
    "common.next": "Weiter",
    "common.create": "Erstellen",
    "common.delete": "Löschen",
    "common.edit": "Bearbeiten",
    "common.confirm": "Bestätigen",
    "common.tip": "Tipp",
    "common.recommended": "empfohlen",

    // Wizard
    "wizard.title": "kombify-TechStack erstellen",
    "wizard.subtitle":
      "Konfiguriere den StackKit-Rollout für deinen eigenen Server oder ein verwaltetes kombify-Ziel.",
    "wizard.easy": "Einfache Einrichtung",
    "wizard.techie": "Techie-Modus",
    "wizard.step.goals": "Ziele",
    "wizard.step.server": "Server",
    "wizard.step.access": "Zugriff",
    "wizard.step.users": "Nutzer",
    "wizard.step.login": "Login",
    "wizard.creating": "Wird erstellt...",
    "wizard.validation.completeFields":
      "Hinweis: Vervollständige die markierten Felder",

    // Wizard Step 1 - Goals
    "wizard.goals.title": "Runde 1: Was möchtest du machen?",
    "wizard.goals.subtitle":
      "Wähle aus, was du mit deinem System erreichen willst. Du kannst später jederzeit weitere Funktionen hinzufügen.",
    "wizard.goals.smartHome.title": "Smart Home",
    "wizard.goals.smartHome.description":
      "Betreibe Home Assistant lokal, damit Automationen auch ohne Internet funktionieren.",
    "wizard.goals.smartHome.help":
      "Kanonischer StackKits-Use-Case: smart-home mit Home Assistant als Default-Tool.",
    "wizard.goals.smartHome.tip":
      "Ideal, wenn Licht, Sensoren, Heizung und lokale Automationen in deiner Vertrauenszone bleiben sollen.",
    "wizard.goals.photos.title": "Photo Memories",
    "wizard.goals.photos.description":
      "Deine private Fotobibliothek mit KI-gestützter Ordnung und Familienfreigaben.",
    "wizard.goals.photos.help":
      "Kanonischer StackKits-Use-Case: photos mit Immich als Default-Tool.",
    "wizard.goals.photos.tip":
      "Der sichtbarste erste Gewinn für viele Homelabs: private Erinnerungen ohne weiteres Cloud-Abo.",
    "wizard.goals.media.title": "Media Streaming",
    "wizard.goals.media.description":
      "Baue deine private Streaming-Bibliothek für Filme, Serien und Haushaltsmedien.",
    "wizard.goals.media.help":
      "Kanonischer StackKits-Use-Case: media mit Jellyfin und kuratiertem Media-Stack.",
    "wizard.goals.media.tip":
      "Am besten mit genug Speicher und optionalem Hardware-Transcoding.",
    "wizard.goals.vault.title": "Password Vault",
    "wizard.goals.vault.description":
      "Hoste deinen eigenen verschlüsselten Passwortmanager hinter dem StackKit-Gateway.",
    "wizard.goals.vault.help":
      "Kanonischer StackKits-Use-Case: vault mit Vaultwarden als Default-Tool.",
    "wizard.goals.vault.tip":
      "Gut für Day One Security: klein, nützlich und gut sicherbar.",
    "wizard.goals.files.title": "File Sharing",
    "wizard.goals.files.description":
      "Behalte Dokumente, geteilte Ordner und Sync-Workflows unter eigener Kontrolle.",
    "wizard.goals.files.help":
      "Kanonischer StackKits-Use-Case: files mit Cloudreve oder Nextcloud als kuratierte Tools.",
    "wizard.goals.files.tip":
      "Vor wichtigen Haushaltsdokumenten zuerst Backup und Restore klären.",
    "wizard.goals.ai.title": "AI / LLM",
    "wizard.goals.ai.description":
      "Bereite einen privaten lokalen Assistenten für eigene Dokumente und Dienste vor.",
    "wizard.goals.ai.help":
      "Kanonischer StackKits-Use-Case: ai mit Ollama und Open WebUI.",
    "wizard.goals.ai.tip":
      "Stärkere Hardware hilft; kleinere Nodes können trotzdem sauber geplant werden.",
    "wizard.goals.dev.title": "Dev Platform",
    "wizard.goals.dev.description":
      "Betreibe Code-Hosting, CI, Datenbanken und Entwicklungs-Sandboxes auf deinem Server.",
    "wizard.goals.dev.help":
      "Kanonischer StackKits-Use-Case: dev mit Gitea plus CI.",
    "wizard.goals.dev.tip":
      "Nützlich, wenn dein Laptop schlank bleiben und reproduzierbare Umgebungen auf dem Server laufen sollen.",
    "wizard.goals.mail.title": "Mail Server",
    "wizard.goals.mail.description":
      "Plane eine selbstverwaltete Mail- und Groupware-Lane für fortgeschrittene Homelab-Operator.",
    "wizard.goals.mail.help":
      "Kanonischer StackKits-Use-Case: mail mit Stalwart als Default-Tool.",
    "wizard.goals.mail.tip":
      "Mail braucht DNS- und Reputationsarbeit; der Wizard hält den Intent fest und lässt Rollout-Gates greifen.",
    "wizard.goals.game.title": "Game Server",
    "wizard.goals.game.description":
      "Hoste persistente Multiplayer-Welten für Freunde, ohne dein Heimnetz direkt freizulegen.",
    "wizard.goals.game.help":
      "Kanonischer StackKits-Use-Case: game für kuratierte Gameserver-Module.",
    "wizard.goals.game.tip":
      "Am besten mit niedriger Latenz und Snapshots vor Mod-Updates.",
    "wizard.goals.storage.title":
      "Eigener lokaler Speicher für Fotos und Dokumente",
    "wizard.goals.storage.description":
      "Sicher und privat, nur für dich zugänglich",
    "wizard.goals.storage.help":
      "Privater Cloud-Speicher mit automatischer Synchronisation über alle Geräte.",
    "wizard.goals.storage.tip":
      "Empfehlung: Starte mit Nextcloud oder ähnlichem. Perfekter erster Schritt ins Self-Hosting.",
    "wizard.goals.website.title": "Eigene Website und E-Mail-Adresse hosten",
    "wizard.goals.website.description":
      "Präsentiere dich professionell mit deiner eigenen Online-Präsenz",
    "wizard.goals.website.help":
      "Volle Kontrolle über deine Online-Präsenz. Keine monatlichen Gebühren.",
    "wizard.goals.website.tip":
      "Empfehlung: Nutze Reverse Proxy (Traefik) für SSL. E-Mail-Hosting erfordert guten Spam-Schutz.",
    "wizard.goals.everything.title": "Bis zur Unendlichkeit und noch weiter!",
    "wizard.goals.everything.description":
      "Die komplette Lösung für maximale Unabhängigkeit. Alles auf einmal und noch mehr.",
    "wizard.goals.everything.help":
      "Aktiviert Speicher, Webhosting, Monitoring und mehr.",
    "wizard.goals.everything.tip":
      "Empfehlung: Am besten für erfahrene Nutzer oder wenn du dedizierte Hardware bereit hast.",
    "wizard.goals.tipText":
      "Wenn du unsicher bist, wähle nur ein Ziel. kombify-TechStack bleibt ein einzelnes System und du kannst später weitere Dienste hinzufügen. Mehr Einstellungen? Probiere die erweiterten Einstellungen unten.",
    "wizard.goals.advancedTitle": "Erweiterte Use Cases",
    "wizard.goals.infoText":
      "Möchtest du eine technischere Konfiguration durchführen?",
    "wizard.goals.tryTechie": "Probiere unseren Techie-Wizard!",

    // Wizard Step 2 - Server
    "wizard.server.title": "Woher soll der Server kommen?",
    "wizard.server.subtitle":
      "Erzeuge den One-Liner für deinen Zielserver, verbinde einen vorhandenen Server per SSH oder nutze ein verwaltetes kombify-Ziel, sobald es verfügbar ist.",
    "wizard.server.cloud.title": "kombify Cloud Server",
    "wizard.server.cloud.description":
      "kombify stellt einen verwalteten Subscription-Server bereit und bereitet ihn vor.",
    "wizard.server.cloud.help":
      "Das ist die verwaltete Server-Option im SaaS-Produkt. Die Provider-Auswahl bleibt verborgen, bis der Provider-Wechsel bereit ist.",
    "wizard.server.cloud.info":
      "Es sind keine Server-Einstellungen nötig. kombify stellt zuerst den Subscription-Server bereit und fährt dann mit dem StackKit-Provisioning fort.",
    "wizard.server.mode.managed": "verwaltet",
    "wizard.server.remote.title": "Eigenen Server verbinden",
    "wizard.server.remote.description":
      "Verbinde einen vorhandenen Linux-Server direkt per SSH.",
    "wizard.server.remote.help":
      "An Remote-Server-Setup-Flows angelehnt: kombify braucht Host, SSH-Port, Benutzer und Zugriff per Schlüssel.",
    "wizard.server.remote.host": "Server-Host oder IP",
    "wizard.server.remote.port": "SSH-Port",
    "wizard.server.remote.user": "SSH-Benutzer",
    "wizard.server.remote.auth": "Authentifizierung",
    "wizard.server.remote.auth.sshKey": "SSH-Schlüssel",
    "wizard.server.remote.auth.password": "Passwort",
    "wizard.server.remote.keyLabel": "SSH-Schlüssel-Label",
    "wizard.server.remote.sudo": "sudo für Setup-Befehle verwenden",
    "wizard.server.oneliner.title": "Gib mir einen One-Liner",
    "wizard.server.oneliner.description":
      "Erhalte den Installationsbefehl und optional eine einstündige Simulate-Demo-Vorschau.",
    "wizard.server.oneliner.help":
      "Das Kommando registriert den Server an dieser TechStack-Instanz. Simulate kann parallel eine temporäre Vorschau für eine Stunde anzeigen.",
    "wizard.server.oneliner.info":
      "Der One-Liner ist nach der Stack-Erstellung bereit. Führe ihn einmal auf dem Server oder Gerät aus, das diesem Stack beitreten soll.",

    // Wizard Step 1 - Advanced Settings
    "wizard.advanced.title": "Erweiterte Einstellungen",
    "wizard.advanced.isolation": "Dienst-Isolation",
    "wizard.advanced.isolation.isolated": "Isoliert (empfohlen)",
    "wizard.advanced.isolation.shared": "Geteilte Ressourcen",
    "wizard.advanced.autostart": "Autostart",
    "wizard.advanced.autostart.auto": "Automatisch (Start beim Booten)",
    "wizard.advanced.autostart.manual": "Manuell",
    "wizard.advanced.backups": "Automatische Backups aktivieren",

    // Wizard Step 2 - Access
    "wizard.access.title": "Runde 2: Wo willst du dein System nutzen?",
    "wizard.access.subtitle":
      "Überlege, von wo aus du auf deine Daten und Dienste zugreifen möchtest.",
    "wizard.access.home.title": "Nur zu Hause",
    "wizard.access.home.description":
      "Dein System bleibt in deinem lokalen Netzwerk, maximale Sicherheit",
    "wizard.access.home.help":
      "Alle Daten bleiben in deinem lokalen Netzwerk. Perfekt für datenschutzorientierte Setups.",
    "wizard.access.home.tip":
      "Empfehlung: Am besten für Anfänger. Du kannst später jederzeit Fernzugriff hinzufügen.",
    "wizard.access.anywhere.title": "Von überall!",
    "wizard.access.anywhere.description":
      "Sicheres Zugriffsprofil für die Nutzung außerhalb deines Heimnetzwerks",
    "wizard.access.anywhere.help":
      "Nutzt das passende Profil je Lane: Private Mesh bei Self-hosted oder Provider-Direct-Routing im SaaS-Betrieb.",
    "wizard.access.anywhere.tip":
      "Empfehlung: Starte privat. Öffentliche Einstiegspunkte sollten bewusste Service-Entscheidungen sein.",
    "wizard.access.tipText":
      '"Von überall zugreifen" wählt ein Zugriffsprofil. SaaS nutzt den verwalteten Edge-Pfad; Self-hosted kann Private Mesh oder explizite öffentliche Einstiegspunkte nutzen.',
    "wizard.access.vpn.title": "Zugriffsprofil",
    "wizard.access.vpn.type": "Private-Mesh-Typ",
    "wizard.access.vpn.headscale": "Headscale (empfohlen)",
    "wizard.access.vpn.wireguard": "WireGuard",
    "wizard.access.vpn.cloudflare": "Cloudflare Zero Trust aktivieren",

    // Wizard Step 3 - Users
    "wizard.users.title": "Runde 3: Wer soll dein System nutzen?",
    "wizard.users.subtitle":
      "Entscheide, ob du allein oder mit anderen auf dein System zugreifen möchtest.",
    "wizard.users.me.title": "Nur ich",
    "wizard.users.me.description":
      "Ein persönlicher Bereich, vollständig unter deiner Kontrolle",
    "wizard.users.me.help": "Einzelbenutzer-Setup mit vollem Admin-Zugriff.",
    "wizard.users.me.tip":
      "Einfachstes Setup. Du kannst später Benutzer hinzufügen.",
    "wizard.users.family.title": "Familie & Freunde",
    "wizard.users.family.description":
      "Teile Fotos, Dokumente oder Dienste mit deinen Liebsten",
    "wizard.users.family.help":
      "Mehrbenutzer mit separaten Konten. Jeder Benutzer hat seinen eigenen Bereich.",
    "wizard.users.family.tip":
      "Empfehlung: Nutze gruppenbasierte Berechtigungen (Familie, Freunde, Admins) für einfachere Verwaltung.",
    "wizard.users.public.title": "Öffentliche Nutzer",
    "wizard.users.public.description":
      "Stelle Inhalte oder Dienste für ein breiteres Publikum bereit",
    "wizard.users.public.help":
      "Einige Dienste werden öffentlich zugänglich. Authentifizierung ist weiterhin für Admin-Zugriff erforderlich.",
    "wizard.users.public.tip":
      "Empfehlung: Nutze einen zentralen Identity Provider (Authentik/Authelia) für SSO über alle Dienste.",
    "wizard.users.tipText":
      "Du kannst später weitere Benutzer einladen. Starte mit dem, was du jetzt brauchst.",

    // Wizard Step 4 - Login
    "wizard.login.title": "Runde 4: Wie möchtest du dich einloggen?",
    "wizard.login.subtitle":
      "Wähle eine Login-Methode, die zu deinen Sicherheitsanforderungen und deinem Komfort passt.",
    "wizard.login.owner.title": "Wer soll den ersten Zugang besitzen?",
    "wizard.login.owner.subtitle":
      "Die vierte Stage bleibt gleich, aber du legst fest, ob der erste Bootstrap lokal bleibt oder an deine kombify Cloud Identität gekoppelt wird.",
    "wizard.login.owner.local.title": "Lokales Owner-Konto anlegen",
    "wizard.login.owner.local.description":
      "Starte mit dem ersten Owner für diesen StackKit-Rollout.",
    "wizard.login.owner.username": "Owner-Benutzername",
    "wizard.login.owner.email": "Owner-E-Mail",
    "wizard.login.owner.displayName": "Anzeigename (optional)",
    "wizard.login.owner.preview": "Owner-Vorschau",
    "wizard.login.owner.cloudLink.title": "kombify Cloud Profil verwenden",
    "wizard.login.owner.cloudLink.description":
      "Verbinde dein kombify Cloud Konto; die Owner-Identität wird aus dem verifizierten Profil abgeleitet.",
    "wizard.login.owner.cloudLink.connect": "kombify Cloud verbinden",
    "wizard.login.owner.cloudLink.connecting": "Warte auf kombify Cloud...",
    "wizard.login.owner.cloudLink.waiting":
      "Schließe den Login im geöffneten Fenster ab. Diese Seite aktualisiert sich automatisch.",
    "wizard.login.owner.cloudLink.popupBlocked":
      "Das Popup wurde vom Browser blockiert.",
    "wizard.login.owner.cloudLink.openInTab": "In neuem Tab öffnen",
    "wizard.login.owner.cloudLink.verified": "verifiziert",
    "wizard.login.owner.cloudLink.unverified": "nicht verifiziert",
    "wizard.login.owner.cloudLink.verifyHint":
      "Verifiziere die E-Mail in deinem kombify Cloud Konto und verknüpfe dann erneut.",
    "wizard.login.owner.cloudLink.use": "Als Owner verwenden",
    "wizard.login.owner.cloudLink.unlink": "Anderes Konto verwenden",
    "wizard.login.owner.cloudLink.unavailable":
      "Auf dieser Instanz ist kein kombify Cloud Login konfiguriert.",
    "wizard.login.recovery.title": "Recovery-Passphrase",
    "wizard.login.recovery.subtitle":
      "Diese Passphrase schützt das Break-Glass-Recovery-Bundle. Sie wird im Browser gehasht, bevor der Wizard-Request abgeschickt wird.",
    "wizard.login.recovery.passphrase": "Recovery-Passphrase",
    "wizard.login.recovery.confirm": "Recovery-Passphrase bestätigen",
    "wizard.login.recovery.hashing": "Recovery-Passphrase wird gehasht...",
    "wizard.login.recovery.ready": "Recovery-Passphrase bereit.",
    "wizard.login.password.title": "Passwort",
    "wizard.login.password.description":
      "Klassische E-Mail + Passwort Authentifizierung",
    "wizard.login.password.help":
      "Standard Login-Methode. Nutze ein starkes, einzigartiges Passwort.",
    "wizard.login.password.tip":
      "Empfehlung: Mit 2FA kombinieren für bessere Sicherheit.",
    "wizard.login.mfa.title": "Zwei-Faktor (2FA)",
    "wizard.login.mfa.description":
      "Extra Sicherheit mit Authenticator-App oder Hardware-Key",
    "wizard.login.mfa.help":
      "Fügt einen zweiten Verifizierungsschritt hinzu. Unterstützt TOTP-Apps und Hardware-Keys.",
    "wizard.login.mfa.tip":
      "Sehr empfohlen für Fernzugriff. Funktioniert mit Google Authenticator, Authy, etc.",
    "wizard.login.passwordless.title": "Passwortlos",
    "wizard.login.passwordless.description":
      "Magic Links per E-Mail oder biometrischer Login",
    "wizard.login.passwordless.help":
      "Kein Passwort zu merken. Login via E-Mail-Link oder Fingerabdruck/Gesicht.",
    "wizard.login.passwordless.tip":
      "Modern und sicher. Erfordert zuverlässige E-Mail-Zustellung.",
    "wizard.login.adminSection": "Admin-Konto einrichten (optional)",
    "wizard.login.adminNote":
      "Ein Deployer-Konto wird automatisch erstellt. Du kannst unten ein zusätzliches Admin-Konto einrichten.",
    "wizard.login.username": "Benutzername",
    "wizard.login.email": "E-Mail",
    "wizard.login.password": "Passwort",
    "wizard.login.confirmPassword": "Passwort bestätigen",
    "wizard.login.passwordMismatch": "Passwörter stimmen nicht überein",
    "wizard.login.tipText":
      "Das Deployer-Konto wird beim Setup erstellt. Admin-Setup ist optional.",

    // Settings
    "settings.title": "Einstellungen",
    "settings.language": "Sprache",
    "settings.language.en": "Englisch",
    "settings.language.de": "Deutsch",
    "settings.profile": "Profil",
    "settings.email": "E-Mail",
    "settings.password": "Passwort",
    "settings.password.change": "Passwort ändern",

    // Auth
    "auth.login": "Anmelden",
    "auth.register": "Registrieren",
    "auth.email": "E-Mail",
    "auth.password": "Passwort",
    "auth.username": "Benutzername",

    // Session recovery (inline panel)
    "auth.session.renewal.title": "Sitzung muss aufgefrischt werden",
    "auth.session.renewal.body":
      "Dein sicherer Zugriff auf kombify Cloud konnte nicht automatisch erneuert werden. Deine Arbeit bleibt erhalten – melde dich erneut an, um genau dort weiterzumachen.",
    "auth.session.renewal.signIn": "Erneut anmelden",
    "auth.session.renewal.retry": "Erneut versuchen",
    "auth.session.renewal.retrying": "Wird erneut versucht…",
    "auth.session.gatewayUnavailable":
      "Der sichere Zugriff auf kombify Cloud konnte nicht bestätigt werden. Melde dich erneut an, um fortzufahren.",
    "auth.session.tenantContextRequired":
      "Deine Sitzung trägt keine kombify-Organisation, daher können keine Organisationsdaten angezeigt werden. Öffne TechStack über dein kombify-Cloud-Portal oder melde dich erneut an.",

    // Wizard-run resume banner (dashboard, plan D6)
    "wizard.run.banner.inProgressTitle": "Dein Homelab wird eingerichtet",
    "wizard.run.banner.inProgressBody":
      "Die Einrichtung läuft noch. Du kannst den Fortschritt ansehen oder später zurückkommen.",
    "wizard.run.banner.pairingTitle": "Ein Server wartet auf die Verbindung",
    "wizard.run.banner.pairingBody":
      "Führe den Installationsbefehl auf deinem Server aus, um ihn fertig hinzuzufügen.",
    "wizard.run.banner.failedTitle":
      "Deine Homelab-Einrichtung braucht Aufmerksamkeit",
    "wizard.run.banner.failedBody":
      "Der letzte Einrichtungslauf wurde nicht abgeschlossen. Mach dort weiter, wo du aufgehört hast.",
    "wizard.run.banner.resume": "Einrichtung fortsetzen",

    // Session expiry (modal)
    "auth.session.expired.default":
      "Deine Sitzung ist abgelaufen. Bitte melde dich erneut an.",
    "auth.session.expired.title": "Sitzung abgelaufen",
    "auth.session.expired.subtitle": "Bitte melde dich erneut an",
    "auth.session.expired.saasBody":
      "Deine TechStack-Sitzung ist abgelaufen. Fahre mit Auth0 fort, um nach der Anmeldung zu diesem Arbeitsschritt zurückzukehren.",
    "auth.session.expired.continueAuth0": "Mit Auth0 fortfahren",
    "auth.session.expired.reconnecting": "Verbindung wird wiederhergestellt...",
    "auth.session.expired.signOut": "Abmelden",
    "auth.session.expired.reopenPortal": "kombify Cloud erneut öffnen",
    "auth.session.expired.embeddedHint":
      "Öffne kombify Cloud in einem neuen Tab, melde dich dort an und versuche die Sitzungsaktualisierung hier erneut.",
    "auth.session.expired.embeddedRefreshFailed":
      "Die eingebettete kombify-Cloud-Sitzung konnte nicht aktualisiert werden. Öffne kombify Cloud erneut, melde dich an und versuche es dann noch einmal.",
    "auth.session.expired.dataPreserved":
      "Deine Daten bleiben erhalten. Nach der Anmeldung kannst du dort weitermachen, wo du aufgehört hast.",
  },
};

// Get stored locale from localStorage
export function getStoredLocale(): Locale {
  if (typeof window === "undefined") return defaultLocale;
  const stored = localStorage.getItem("techstack-locale");
  if (stored === "en" || stored === "de") return stored;
  return defaultLocale;
}

// Store locale in localStorage
export function setStoredLocale(locale: Locale): void {
  if (typeof window === "undefined") return;
  localStorage.setItem("techstack-locale", locale);
}

// Translation function (static, for use in non-reactive contexts)
export function t(key: string, locale?: Locale): string {
  const currentLocale = locale || getStoredLocale();
  const dict = translations[currentLocale] || translations[defaultLocale];
  return dict[key] || translations[defaultLocale][key] || key;
}

// Get all available locales
export function getAvailableLocales(): { code: Locale; name: string }[] {
  return [
    { code: "en", name: "English" },
    { code: "de", name: "Deutsch" },
  ];
}
