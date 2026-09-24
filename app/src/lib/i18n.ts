/**
 * kombify-TechStack Internationalization (i18n)
 *
 * Simple translation system with English as default language.
 * Language preference is stored in localStorage.
 */

import { loginMessages } from "./wizard/login-messages";
import { accessUsersMessages } from "./wizard/access-users-messages";

export type Locale = "en" | "de";

export const defaultLocale: Locale = "en";

// Translation dictionary
const translations: Record<Locale, Record<string, string>> = {
  en: {
    ...loginMessages.en,
    ...accessUsersMessages.en,
    "wizard.creation.preferenceOnly":
      "Preference saved. This release installs the suggested service; the alternative is not applied yet.",
    "wizard.preview.mail.title": "Mail",
    "wizard.preview.game.title": "Games",
    "wizard.preview.ai.description":
      "Explore local models and private AI workflows.",
    "wizard.preview.label": "Creation design preview",
    "wizard.preview.navigation": "Preview navigation",
    "wizard.preview.back": "Current wizard",
    "wizard.preview.reset": "Reset",
    "wizard.preview.language": "Preview language",
    "wizard.preview.sandboxNotice":
      "Interactive design preview. Compare A and B with the same selection. Choices stay in this view; nothing is installed. Mail and Games show the planned Beta experience.",
    "wizard.preview.files.title": "Documents & Files",
    "wizard.preview.files.description":
      "Your everyday files, in a place of your own.",
    "wizard.preview.files.help":
      "Keep your documents together, access them across devices and share selected files with other people. Cloudreve is the suggested drive; Nextcloud is the collaboration alternative. Add Paperless-ngx when you also want a searchable archive for scanned paperwork.",
    "wizard.preview.paperless.title": "Add a document archive",
    "wizard.preview.paperless.help":
      "Paperless-ngx organizes scanned paperwork and makes its text searchable. Optional alongside your drive.",
    "wizard.preview.mail.description":
      "Bring your mailboxes together. Choose your client.",
    "wizard.preview.mail.help":
      "Start with your existing mailboxes and a client you enjoy using. Paperwork is the first-party direction; Roundcube is an independent webmail option. Running your own mail server is optional, with Stalwart or the integrated mailcow suite.",
    "wizard.preview.mail.hosting": "Mailbox hosting",
    "wizard.preview.mail.existing": "Keep my existing provider",
    "wizard.preview.game.description":
      "A Minecraft world for you and your friends.",
    "wizard.preview.game.help":
      "The planned Games experience uses Pterodactyl to manage a persistent Minecraft world. Choose Java or Bedrock to match your players. Local hosting and a managed VPS are separate placement options; invitations and world recovery belong to the setup.",
    "wizard.preview.game.edition": "Minecraft edition",
    "wizard.preview.dev.description":
      "Your projects and remote workspace, together.",
    "wizard.preview.dev.help":
      "Keep private repositories with Gitea and explore the planned development and remote-work experience. Repository access, a development workspace and remote desktop remain individual choices inside one Dev use case.",
    "wizard.preview.service.immich":
      "A home for photos and videos with a timeline, albums and optional machine-learning features. Use the mobile app for your photo-backup workflow.",
    "wizard.preview.service.cloudreve":
      "A personal file drive for storing, accessing and sharing files. Cloudreve is the established default for Documents & Files.",
    "wizard.preview.service.nextcloud":
      "A file and collaboration platform with its own app ecosystem. Choose it when collaboration is more important than a focused personal drive.",
    "wizard.preview.service.paperless-ngx":
      "A document archive with OCR, search and organization for scanned paperwork. It complements your file drive and does not replace it.",
    "wizard.preview.service.vaultwarden":
      "A password-vault server used with compatible Bitwarden clients. Device access and recovery remain part of setting up your vault.",
    "wizard.preview.service.jellyfin":
      "A media server for your own films, shows and music. Playback and transcoding needs depend on the devices and media you use.",
    "wizard.preview.service.home-assistant":
      "Connect home devices and control them from one place. Keep your existing Home Assistant configuration or plan a fresh setup around your actual devices.",
    "wizard.preview.service.gitea":
      "Private Git repositories with code review and project collaboration. A development workspace or remote desktop is a separate choice within Dev.",
    "wizard.preview.service.paperwork":
      "kombify's first-party Mail direction. The planned integration distinguishes hosted accounts, an eligible self-hosted edition and native device clients.",
    "wizard.preview.service.roundcube":
      "A browser-based mail client for existing mailboxes. A client-only setup does not require you to run a mail server.",
    "wizard.preview.service.stalwart":
      "An optional mail-server choice when you deliberately want to operate your own mailboxes. Domain, delivery and recovery settings need their own setup.",
    "wizard.preview.service.mailcow":
      "An optional integrated mail suite with SOGo webmail and groupware. Choose a suite or a separate server; they are not both required for the same mailboxes.",
    "wizard.preview.service.pterodactyl":
      "The selected platform for Games. The planned integration manages separate Minecraft Java and Bedrock profiles, their resources and persistent worlds.",
    "wizard.preview.compare": "Compare the two designs",
    "wizard.preview.discover": "Discover",
    "wizard.preview.focus": "Focus",
    "wizard.preview.eyebrow": "Your digital home",
    "wizard.preview.title": "Make room for what matters.",
    "wizard.preview.subtitle":
      "Choose what you want to make your own. Start with the suggested service, or pick an alternative. The details are there when you need them.",
    "wizard.preview.available": "Choose your use cases",
    "wizard.preview.availableHint": "Pick a use case. Make it yours.",
    "wizard.preview.defaultService": "Suggested",
    "wizard.preview.alternative": "Alternative",
    "wizard.preview.aboutService": "About",
    "wizard.preview.serviceFor": "Service for",
    "wizard.preview.preferenceOnly":
      "Selected for this design preview. Actual availability depends on the released integration.",
    "wizard.preview.aboutUseCase": "A closer look",
    "wizard.preview.inside": "Tools in this use case",
    "wizard.preview.explore": "Explore & customize",
    "wizard.preview.collapse": "Less detail",
    "wizard.preview.selected": "Selected",
    "wizard.preview.optional": "Optional",
    "wizard.preview.selectionCount": "selected",
    "wizard.preview.comingSoon": "Coming soon",
    "wizard.preview.comingSoonHint":
      "Part of the plan. Not available to add yet.",
    "wizard.preview.setupPreferences": "Setup preferences",
    "wizard.preview.setupPreferencesHint":
      "Backups, isolation and startup behavior",
    "wizard.preview.loading": "Loading use cases from your StackKits release…",
    "wizard.preview.unverified":
      "Availability could not be confirmed for some use cases. They stay disabled until the catalog and feature access are available.",
    "wizard.preview.unverifiedShort": "Currently unavailable",
    "wizard.preview.retry": "Check again",
    "wizard.preview.partOf": "Use case",
    "wizard.preview.source": "Catalog release",
    "wizard.preview.website": "Official website",
    "wizard.preview.useService": "Choose",
    "wizard.preview.documents.title": "Documents",
    "wizard.preview.documents.description":
      "Give incoming paperwork a searchable home.",
    "wizard.preview.network.title": "Network",
    "wizard.preview.network.description":
      "Connect your devices and understand your home network.",
    "wizard.preview.automation.title": "Automation",
    "wizard.preview.automation.description":
      "Let reviewed routines take care of repetitive work.",
    "wizard.preview.role.primary": "Suggested service",
    "wizard.preview.role.alternative": "Alternative service",
    "wizard.preview.role.supporting": "Supporting service",
    "wizard.preview.role.connector": "Connector",
    "wizard.preview.role.bridge": "Bridge",
    "wizard.preview.roleHelp.primary":
      "The catalog's suggested service for this use case. It is included when you select the use case.",
    "wizard.preview.roleHelp.alternative":
      "A catalogued alternative to the suggested service. Its selection is recorded as a preference; release support is shown separately.",
    "wizard.preview.roleHelp.supporting":
      "A supporting component that helps the main application run. Its inclusion is managed by the StackKit.",
    "wizard.preview.roleHelp.connector":
      "A connector declared by this use case to connect it to another service.",
    "wizard.preview.roleHelp.bridge":
      "A bridge declared by this use case to connect compatible systems.",
    "onboarding.ui.explore": "Show me around first",
    "onboarding.ui.start_failed":
      "We could not open your setup. Your saved choices are kept. Try again, or explore first.",
    "onboarding.ui.continue": "Your next step",
    "hostBaseline.title": "Existing services and ports",
    "hostBaseline.preserve":
      "This inventory check leaves existing services and data unchanged. Review this Node before adding a StackKit.",
    "hostBaseline.loading": "Reading the Node inventory…",
    "hostBaseline.current": "Current listener inventory received",
    "hostBaseline.incomplete":
      "Listener inventory is incomplete or out of date. Port availability is not yet verified.",
    "hostBaseline.observed": "Observed",
    "hostBaseline.claimed": "StackKit claim present",
    "hostBaseline.existing": "Existing listener",
    "hostBaseline.noListeners":
      "No listeners were reported in the observed scope. Stopped applications and other namespaces may still reserve bindings.",
    "hostBaseline.recheck":
      "Before execution, Techstack and StackKits check the required bindings again. A conflict needs a supported configuration change, another Node, or a reviewed migration; an open port does not prove public reachability.",
    "hostBaseline.refresh": "Refresh inventory",
    "hostBaseline.review": "Review all ports and services",

    "wizard.smartHome.applianceRetained":
      "The separate Home Assistant OS request is saved. Resuming this rollout keeps the same appliance. Its native API connection and readiness are verified separately.",
    "wizard.smartHome.advice": "Home Assistant installation",
    "wizard.smartHome.preserve":
      "Connect your existing installation in observation mode. Your accounts, automations and configuration stay intact.",
    "wizard.smartHome.haos":
      "Home Assistant OS runs in a separate VM with Supervisor, Apps and appliance maintenance.",
    "wizard.smartHome.container":
      "Container remains available for Home Assistant Core. You manage host services separately; Supervisor and Apps require Home Assistant OS.",
    "wizard.smartHome.apps-require-haos":
      "Your requested Apps require Home Assistant OS.",
    "wizard.smartHome.verified-proxmox-lan-and-guest-capacity":
      "Verify a connected Proxmox host, enough capacity for a separate VM, and access to the home network.",
    "wizard.smartHome.verify-local-device-reachability":
      "Access to your home devices still needs verification. An available network bridge alone does not prove it.",
    "wizard.smartHome.identify-and-exclusively-authorize-radio":
      "Select the exact radio adapter and authorize its exclusive assignment before use.",
    "wizard.smartHome.detect-installed-form-and-api-capabilities":
      "Detect the installed edition, versions and actual API capabilities before management.",
    "wizard.smartHome.separate-management-grant":
      "Management requires a separate explicit grant.",
    "wizard.server.hypervisor.title": "Hypervisor",
    "wizard.server.hypervisor.connected": "Guest connected",
    "wizard.server.hypervisor.connectedDetail":
      "The Ubuntu guest is connected to this deployment.",
    "wizard.server.hypervisor.description":
      "Create a VM on your Proxmox server.",
    "wizard.server.hypervisor.help":
      "Uses your connected Proxmox and its existing storage and network.",
    "wizard.server.hypervisor.standard":
      "Ubuntu 24.04 LTS runs your StackKits core in a separate VM. Techstack prepares and connects the guest automatically.",
    "wizard.server.hypervisor.appliance":
      "Home Assistant OS uses a separate appliance VM. The Ubuntu guest continues to run the core and other services.",
    "wizard.server.hypervisor.host": "Connected Proxmox server",
    "wizard.server.hypervisor.storage": "Storage",
    "wizard.server.hypervisor.bridge": "LAN bridge",
    "wizard.server.hypervisor.disk": "Disk",
    "wizard.server.hypervisor.select": "Select…",
    "wizard.server.hypervisor.loading": "Loading…",
    "wizard.server.hypervisor.offline": "Unavailable",
    "wizard.server.hypervisor.unavailable":
      "The connected hypervisor or its resource inventory is unavailable. Refresh after the connection is restored.",
    "wizard.server.hypervisor.refresh": "Refresh",
    "wizard.server.hypervisor.resourcesMissing":
      "An active storage supporting images and imports, and an active LAN bridge are required.",
    "wizard.server.hypervisor.scan": "Find Proxmox on my network",
    "wizard.server.hypervisor.scanning": "Searching…",
    "wizard.server.hypervisor.scanFailed":
      "Network discovery could not complete. You can still connect Proxmox manually.",
    "wizard.server.hypervisor.noLan":
      "Network discovery requires a connected local executor. Manual connection remains available.",
    "wizard.server.hypervisor.detected": "Proxmox verified",
    "wizard.server.hypervisor.discoveryHint":
      "Detected servers become selectable after you connect and authorize them.",
    "wizard.server.hypervisor.connect": "Connect another Proxmox server",
    "wizard.server.hypervisor.authorize": "Enable guest management",
    "wizard.server.hypervisor.authorizeFailed":
      "Guest management could not be enabled. Check the Guard's local Proxmox configuration and refresh.",
    "wizard.server.hypervisor.progress":
      "Techstack is preparing the Ubuntu guest on your connected Proxmox, then enrolling it and running the selected StackKit. Progress resumes with the same operation.",
    // Navigation
    "nav.dashboard": "Dashboard",
    "nav.monitoring": "Monitoring",
    "nav.services": "Services",
    "nav.wallet": "Wallet",
    "nav.help": "Help",
    "nav.settings": "Settings",
    "nav.logout": "Logout",
    "nav.allServices": "All services",
    "nav.servers": "Servers",
    "nav.monitoring.servers": "Servers",
    "nav.monitoring.alerts": "Alerts",
    "nav.monitoring.history": "History",
    "nav.preview.services": "Services",
    "nav.preview.noServices": "No services registered yet.",
    "nav.preview.openServices": "Open services",
    "nav.preview.monitoring": "Monitoring",
    "nav.preview.activeAlerts": "active alerts",
    "nav.preview.noAlerts": "No active alerts.",
    "nav.preview.openMonitoring": "Open monitoring",

    // Onboarding — chrome the UI package resolves, plus this product's steps.
    // The descriptor carries only ids; the prose lives here (§2).
    "onboarding.ui.getting_started.title": "Get your homelab running",
    "onboarding.ui.getting_started.subtitle":
      "Five steps from empty to a service you can open — and the fastest way to learn your way around.",
    "onboarding.ui.getting_started.completed": "You're set up",
    "onboarding.ui.getting_started.completed_body":
      "Everything on the list is done. The checklist stays here if you want to revisit a step.",
    "onboarding.ui.getting_started.dismiss": "Hide this",
    "onboarding.ui.getting_started.resume": "Show the checklist again",
    "onboarding.ui.getting_started.minimize": "Compact checklist",
    "onboarding.ui.getting_started.expand": "Detailed checklist",
    "onboarding.ui.progress.label": "Progress",
    "onboarding.ui.step.optional": "Optional",
    "onboarding.ui.step.done": "Done",
    "onboarding.ui.step.blocked": "Not available",
    "onboarding.ui.step.mark_done": "Skip this",
    "onboarding.ui.coach.dismiss": "Got it",
    "onboarding.ui.introduction.replay": "Replay the introduction",
    "onboarding.ui.introduction.back": "Back",
    "onboarding.ui.introduction.next": "Next",
    "onboarding.ui.introduction.done": "Done",
    "onboarding.ui.introduction.skip": "Skip the tour",
    "onboarding.ui.introduction.close": "Close",
    "introduction.restart": "Replay the introduction",
    "introduction.welcome.title": "Welcome to your Techstack",
    "introduction.welcome.body":
      "Choose what you want to run, connect your own server and follow the setup to a verified service. Your configuration stays editable. Health, logs and next actions help you look after it once it is running.",
    "introduction.welcome.name_label":
      "First things first: what should we call this place?",
    "introduction.welcome.name_hint":
      'Your homelab deserves better than "server 1". Anything goes, and you can rename it later in Settings.',
    "introduction.welcome.name_placeholder": "MyHomeLab",
    "introduction.welcome.cta": "Create my homelab",
    "introduction.welcome.cta_plain": "Create my homelab",
    "introduction.navigation.title": "This is your map",
    "introduction.navigation.body":
      "Every surface hangs off the sidebar. Hover a section — go on, try it now, the tour will wait — and its contents fan out beside the rail without taking you off the page.",
    "introduction.getting_started.title": "Getting started, and getting good",
    "introduction.getting_started.body":
      "Not sure where to begin with all of this? Open Getting started. It hands you the first real tasks in order, ticks each one off by itself as you do it, and disappears once you are finished. Short route from new here to knowing the place.",
    "introduction.account.title": "Settings sit behind your name",
    "introduction.account.body":
      "Theme, help and your account live here — and this is where you replay this introduction. You can personalize the appearance here whenever you like.",
    "onboarding.techstack_platform.techstack_find_stackkit.title":
      "Pick a StackKit",
    "onboarding.techstack_platform.techstack_find_stackkit.body":
      "A StackKit is a ready-made set of services. Browse them and pick the one closest to what you want to run.",
    "onboarding.techstack_platform.techstack_find_stackkit.cta":
      "Browse StackKits",
    "onboarding.techstack_platform.techstack_configure_intent.title":
      "Say what it is for",
    "onboarding.techstack_platform.techstack_configure_intent.body":
      "Answer a few questions about what you want to host. kombify turns the answers into a configuration you can review.",
    "onboarding.techstack_platform.techstack_configure_intent.cta":
      "Open the wizard",
    "onboarding.techstack_platform.techstack_deploy_stackkit.title":
      "Deploy your StackKit",
    "onboarding.techstack_platform.techstack_deploy_stackkit.body":
      "Confirm the plan and let kombify deploy the StackKit to your Homelab. This step ticks itself off once the deployment exists.",
    "onboarding.techstack_platform.techstack_deploy_stackkit.cta":
      "Continue deployment",
    "onboarding.techstack_platform.techstack_add_server.title": "Add a Node",
    "onboarding.techstack_platform.techstack_add_server.body":
      "Connect a Node you own, or let kombify run one for you. Your services need somewhere to live.",
    "onboarding.techstack_platform.techstack_add_server.cta": "Add a Node",
    "onboarding.techstack_platform.techstack_deploy_service.title":
      "Open your first service",
    "onboarding.techstack_platform.techstack_deploy_service.body":
      "Once a service is running you can reach it from the services list. That is the point of all of this.",
    "onboarding.techstack_platform.techstack_deploy_service.cta":
      "Go to services",
    "onboarding.denied.policy_selfhost_byos.title":
      "kombify-managed Nodes are a Cloud feature",
    "onboarding.denied.policy_selfhost_byos.body":
      "This installation runs self-hosted, so kombify does not provision Nodes for it. Connect a Node you own and the rest works the same.",
    "onboarding.denied.policy_selfhost_byos.next_step":
      "Connect a Node you already run.",
    "onboarding.denied.required_feature_disabled.title":
      "Managed Nodes are not active for this account",
    "onboarding.denied.required_feature_disabled.body":
      "This account is not entitled to kombify-managed Nodes yet. You can still connect a Node you own.",
    "onboarding.denied.required_feature_disabled.next_step":
      "Connect your own Node, or ask kombify support to enable managed runtime.",

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
    "common.moreInfo": "More about this option",
    "common.recommended": "recommended",

    // Wizard
    "wizard.title": "Create kombify-Techstack",
    "wizard.step.goals": "Goals",
    "wizard.step.server": "Node",
    "wizard.step.access": "Access",
    "wizard.step.users": "Users",
    "wizard.step.login": "Login",
    "wizard.creating": "Creating...",
    "wizard.validation.completeFields": "Hint: Complete the highlighted fields",

    // Wizard Step 1 - Goals
    "wizard.goals.title": "What do you want to do?",
    "wizard.goals.subtitle":
      "Choose what you want to accomplish with your system. You can always add more features later.",
    "wizard.hints.title": "Your Homelab journey",
    "wizard.hints.updating": "Updating…",
    "wizard.hints.stale": "Inventory may be stale",
    "wizard.hints.degraded": "Limited evidence",
    "wizard.hints.bestFit": " is the best path for your adventure.",
    "wizard.hints.review": "Review recommendation",
    "wizard.hints.incomplete":
      "Choose what you want to experience first. We will shape the safest supported standard around it.",
    "wizard.hints.unavailable":
      "We will keep your journey on the best supported standard while live guidance reconnects.",
    "wizard.hints.evaluating": "Shaping your best supported path…",
    "wizard.goals.smartHome.title": "Smart Home",
    "wizard.goals.smartHome.description":
      "Home Assistant on your own Node: automations that keep working when the internet is down.",
    "wizard.goals.smartHome.help":
      "Turn this on and Home Assistant runs at home, controlling lights, heating and sensors locally instead of through a vendor cloud. You decide whether a Zigbee or Z-Wave stick plugged into the Node is used and whether devices on your network are discovered automatically.",
    "wizard.goals.smartHome.tip":
      "Automations keep running when your internet connection does not.",
    "wizard.goals.photos.title": "Photo Memories",
    "wizard.goals.photos.description":
      "Your own photo and video library, with backup from every phone in the household.",
    "wizard.goals.photos.help":
      "Turn this on and your Node becomes the place your photos live: phones back up automatically, you can search by faces and places, and albums can be shared with the family without a cloud subscription. You decide whether smart search runs on the Node and where the library is stored.",
    "wizard.goals.photos.tip":
      "The easiest visible win for most homelabs: private memories without another cloud subscription.",
    "wizard.goals.media.title": "Media Streaming",
    "wizard.goals.media.description":
      "Stream your films, series and music to any TV, phone or laptop at home.",
    "wizard.goals.media.help":
      "Turn this on and your Node serves your own media collection to every screen in the house and, if you allow remote access, on the road. You decide whether the Node's graphics card helps convert video for smaller screens and where the library is stored.",
    "wizard.goals.media.tip":
      "Use a server with enough disk and optional hardware transcoding for the smoothest experience.",
    "wizard.goals.vault.title": "Password Vault",
    "wizard.goals.vault.description":
      "A password manager the household runs itself, compatible with the Bitwarden apps.",
    "wizard.goals.vault.help":
      "Turn this on and everyone gets a vault for passwords, cards and secure notes, synced across their devices through the apps they already know. Only the owner creates accounts unless you open sign-ups.",
    "wizard.goals.vault.tip":
      "Good for day-one security because it is small, useful, and easy to back up.",
    "wizard.goals.files.title": "File Sharing",
    "wizard.goals.files.description":
      "Documents and folders in one place, shared and synced across your devices.",
    "wizard.goals.files.help":
      "Turn this on and you get a private drive: upload from any device, share links with family, and keep working files in sync. Cloudreve is the default; Nextcloud is available as an alternative when you want a full collaboration suite. You decide where the files are stored.",
    "wizard.goals.files.tip":
      "Pair it with backups before moving important household documents.",
    "wizard.goals.ai.title": "AI / LLM",
    "wizard.goals.ai.description":
      "A private assistant that runs on your own hardware and never sends your data away.",
    "wizard.goals.ai.help":
      "Turn this on and you get a chat assistant that runs entirely on your Node. It can work with your own documents. You decide which accelerator it uses and how large a model it loads; a GPU makes a big difference.",
    "wizard.goals.ai.tip":
      "Works best on stronger hardware; low-tier nodes can still be planned first.",
    "wizard.goals.dev.title": "Dev Platform",
    "wizard.goals.dev.description":
      "Your own Git hosting, with builds and test pipelines when you want them.",
    "wizard.goals.dev.help":
      "Turn this on and your code lives on your Node with a Gitea server the whole household or team can use. You decide whether CI runners build and test on the Node too.",
    "wizard.goals.dev.tip":
      "Useful when your laptop should stay thin and repeatable environments live on the server.",
    "wizard.goals.mail.title": "Mail Server",
    "wizard.goals.mail.description":
      "Receive and send email on your own domain, from your own Node.",
    "wizard.goals.mail.help":
      "Turn this on and your Node runs a complete mail server for a domain you own. Email needs correct DNS records and a good sending reputation, so this lane records your intent first and rolls out with guidance. You decide the mail domain.",
    "wizard.goals.mail.tip":
      "Mail has DNS and reputation requirements; the wizard records intent and keeps rollout gated.",
    "wizard.goals.game.title": "Game Server",
    "wizard.goals.game.description":
      "Persistent game worlds for friends, hosted at home without opening your network.",
    "wizard.goals.game.help":
      "Turn this on and your Node hosts game servers your friends join through the kit's secure access, without exposing your home network directly. Which games are available depends on the release.",
    "wizard.goals.game.tip":
      "Session-based load: the Node works hard while friends play and idles in between.",
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
      "If you're unsure, pick just one goal. kombify-Techstack stays a single system and you can add more services later. Need more settings? Try advanced settings below.",
    "wizard.goals.advancedTitle": "Advanced use cases",
    "wizard.goals.showMoreDetails": "Show more details",
    "wizard.goals.showLessDetails": "Show less",
    "wizard.goals.notInThisRelease": "Not installable yet",
    "wizard.goals.notInThisReleaseLong":
      "Not installable yet — your choice is saved for a later release",
    "wizard.goals.spec.backend": "Backend",
    "wizard.goals.spec.alternative": "Alternative",
    "wizard.goals.spec.alternativeNote": "The default backend is installed",
    "wizard.goals.spec.tiers": "Compute tiers",
    "wizard.goals.spec.tiersNotIncluded": "not included",
    "wizard.goals.spec.delivery": "Delivery",
    "wizard.goals.spec.deliveryNow": "This release",
    "wizard.goals.spec.deliveryLater": "A later release",
    "wizard.goals.advanced.label": "Advanced",
    "wizard.goals.advanced.tabsLabel": "Use case settings",
    "wizard.goals.setting.later": "Later release",
    "wizard.goals.group.profile.description":
      "Installation and resource preferences for this use case.",
    "wizard.goals.group.storage.description":
      "Choose how this use case stores and protects your data.",
    "wizard.goals.group.hardware.description":
      "Configure the hardware this use case can use.",
    "wizard.goals.group.access.description":
      "Decide how you connect to this use case.",
    "wizard.goals.group.features.description":
      "Make this use case fit the way you use it.",
    "wizard.goals.group.backend.description":
      "Choose the service behind this use case.",
    "wizard.goals.advanced.tiers": "Compute tiers",
    "wizard.goals.advanced.components": "Components",
    "wizard.goals.advanced.tierIncluded": "included",
    "wizard.goals.advanced.tierExcluded": "not included in this release",
    "wizard.goals.role.primary": "Primary",
    "wizard.goals.role.alternative": "Alternative",
    "wizard.goals.role.supporting": "Supporting",
    "wizard.goals.role.connector": "Connector",
    "wizard.goals.role.bridge": "Bridge",
    "wizard.goals.decisions": "Your choices",
    "wizard.goals.learnMore": "Read the guide",
    "wizard.goals.group.backend": "Backend",
    "wizard.goals.group.profile": "Profile",
    "wizard.goals.group.storage": "Storage",
    "wizard.goals.group.hardware": "Hardware",
    "wizard.goals.group.access": "Access",
    "wizard.goals.group.features": "Features",
    "wizard.goals.tier.low": "Low",
    "wizard.goals.tier.standard": "Standard",
    "wizard.goals.tier.high": "High",
    "wizard.goals.backend.help":
      "Which product does the job. The default is what StackKits tests most.",
    "wizard.goals.profile.help":
      "How much of the Node this use case may use. Standard fits most homes.",
    "wizard.goals.setting.on": "on",
    "wizard.goals.setting.off": "off",
    "wizard.goals.setting.recorded": "Saved now, applied by a later release.",
    "wizard.goals.add": "Add to your kit",
    "wizard.goals.remove": "Remove from your kit",

    // Wizard Step 2 - Node
    "wizard.server.eyebrow": "Make it yours",
    "wizard.server.branch.question": "Where are you starting?",
    "wizard.server.owned.subtitle":
      "A computer, home server, or VPS you already own.",
    "wizard.server.new.subtitle":
      "Let kombify provide one, or rent from a partner.",
    "wizard.server.choice.choose": "Use this path",
    "wizard.server.choice.selected": "Your chosen path",
    "wizard.server.choice.change": "Change system",
    "wizard.server.partner.title": "Find a server with a partner",
    "wizard.server.partner.description":
      "Rent your VPS directly. Bring it back here and make it part of your Homelab.",
    "wizard.server.partner.explore": "Explore our partners",
    "wizard.server.partner.next": "Choose your provider. Then come back here.",
    "wizard.server.partner.hint":
      "Visit a partner to rent a server. Once it is ready, return to connect it with a command or SSH. Your wizard choices stay here.",
    "wizard.server.partner.visit": "Visit provider",
    "wizard.server.partner.ready": "My device is ready — connect it",
    "wizard.server.owned.title": "I already have a device",
    "wizard.server.new.title": "I need a server",
    "wizard.server.path.command": "Copy · run · connect",
    "wizard.server.path.remote": "SSH key or password",
    "wizard.server.path.managed": "Managed by kombify",
    "wizard.server.path.owned": "Your own hardware",
    "wizard.server.partners.title": "Prefer to rent directly?",
    "wizard.server.partners.body":
      "Explore our infrastructure partners, then connect your VPS using either of the device options.",
    "wizard.server.next.label": "Your next step",
    "wizard.server.next.command": "One command to get connected.",
    "wizard.server.next.remote": "Let’s connect your device.",
    "wizard.server.next.managed": "Your VPS, ready to make your own.",
    "wizard.server.command.review": "Finish your choices",
    "wizard.server.command.reviewBody":
      "Choose access and people, then confirm your setup.",
    "wizard.server.command.run": "Run your command",
    "wizard.server.command.runBody":
      "Copy your personal connection command into your device’s terminal.",
    "wizard.server.command.connected": "Follow the setup",
    "wizard.server.command.connectedBody":
      "kombify connects the device and shows the progress of your installation.",
    "wizard.server.system.title": "Device & system details",
    "wizard.server.system.hint":
      "Start with the standard Linux setup. If this device is a Proxmox host, choose its dedicated connection below.",
    "wizard.server.system.ubuntu": "Ubuntu / Linux",
    "wizard.server.system.ubuntuBody":
      "Ubuntu is the standard. The installer checks the operating system on your device.",
    "wizard.server.system.proxmox": "Proxmox hypervisor",
    "wizard.server.system.proxmoxBody":
      "Connect the host and prepare a separate Ubuntu VM for your tools.",
    "wizard.server.system.proxmoxSequence":
      "Connect the Proxmox host, authorize guest management, then choose where its Ubuntu VM should run.",
    "wizard.server.managed.checking":
      "Checking VPS availability for your account…",
    "wizard.server.managed.verificationFailed":
      "VPS access could not be checked right now. Try again, or connect your own device while verification is unavailable.",
    "wizard.server.managed.verificationFailedCaption":
      "VPS access could not be verified.",
    "wizard.server.managed.authFailed":
      "Your sign-in session could not be verified yet. Once sign-in completes, try again. You can also connect your own device.",
    "wizard.server.managed.baseMissing":
      "This account is missing: {features}. Connect your own device, or ask your account administrator or support to enable VPS access with Cloud Kit.",
    "wizard.server.managed.providersMissing":
      "No VPS provider is enabled for this account. Missing provider access: {providers}. Connect your own device, or ask your account administrator or support to enable a provider.",
    "wizard.server.managed.unavailable":
      "VPS access is not enabled for this account.",
    "wizard.server.managed.retry": "Try again",
    "wizard.server.managed.standard": "Your standard VPS",
    "wizard.server.managed.premium": "Your premium VPS",
    "wizard.server.managed.poweredBy": "Powered by",
    "wizard.server.managed.available": "Available for your account",
    "wizard.server.managed.loadingSpecs": "Loading your server configuration…",
    "wizard.server.managed.location": "Location",
    "wizard.server.managed.specsUnavailable":
      "Server details are temporarily unavailable.",
    "wizard.server.managed.details": "Provider & server details",
    "wizard.server.managed.detailsHint":
      "See your configuration and available providers.",
    "wizard.server.managed.provider": "Infrastructure provider",
    "wizard.server.managed.providerChoice":
      "Choose from the providers enabled for your account.",
    "wizard.server.managed.providerAssigned":
      "This provider is included in your current VPS access.",
    "wizard.server.managed.moreOptionsPending":
      "Additional server sizes and locations are not available to choose here yet.",
    "wizard.server.remote.connectionDetails": "Connection details",
    "wizard.server.remote.connectionHint":
      "Enter your device’s address and choose how to sign in.",
    "wizard.server.remote.advanced": "SSH details",
    "wizard.server.remote.keyHint":
      "Use the label of an SSH key saved in your Wallet.",
    "wizard.server.preview.goals": "1 · Use cases",
    "wizard.server.preview.node": "2 · Your server",
    "wizard.server.title": "Give your tools a home.",
    "wizard.server.subtitle":
      "Start with what you have. We’ll guide you to the right connection.",
    "wizard.server.join.title": "Your Homelab, one more Node.",
    "wizard.server.join.description":
      "This Node joins your existing setup. Your access, people and sign-in stay in place, so you can focus on the device.",
    "wizard.server.command.join.review": "Confirm this Node",
    "wizard.server.command.join.reviewBody":
      "Choose how this device connects. Your Homelab access, people and sign-in stay as they are.",
    "wizard.step.inherited": "Already set for your Homelab",
    "wizard.step.existingOwner": "Your Homelab owner stays the same",
    "wizard.server.cloud.title": "kombify VPS",
    "wizard.server.cloud.description":
      "Your own virtual server, provided and connected by kombify.",
    "wizard.server.cloud.help":
      "Available with a qualifying subscription. Your account determines the providers you can choose.",
    "wizard.server.cloud.info":
      "kombify prepares your server and connects it to your Homelab. Your selected tools follow the reviewed setup.",
    "wizard.server.mode.managed": "managed",
    "wizard.server.remote.title": "Connect my device",
    "wizard.server.remote.description":
      "Have a server already? Connect it with an SSH key or password.",
    "wizard.server.remote.help":
      "Enter the address of a reachable server. Use a saved SSH key or your SSH password to connect.",
    "wizard.server.remote.host": "Node host or IP",
    "wizard.server.remote.port": "SSH port",
    "wizard.server.remote.user": "SSH user",
    "wizard.server.remote.auth": "Authentication",
    "wizard.server.remote.auth.sshKey": "SSH key",
    "wizard.server.remote.auth.password": "Password",
    "wizard.server.remote.keyLabel": "SSH key label",
    "wizard.server.remote.password": "SSH password",
    "wizard.server.remote.test": "Test connection",
    "wizard.server.remote.testing": "Testing connection…",
    "wizard.server.remote.test.success": "SSH connection successful",
    "wizard.server.remote.test.failed": "SSH connection failed",
    "wizard.server.remote.sudo": "Use sudo for setup commands",
    "wizard.server.oneliner.title": "Give me a 1-liner",
    "wizard.server.oneliner.description":
      "Run one command in your terminal. We guide the rest of the setup.",
    "wizard.server.oneliner.help":
      "Finish the configuration to get a connection command for your device.",
    "wizard.server.oneliner.info":
      "Your personal command is ready after you finish this wizard. Run it in the terminal of the device you want to connect.",

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
    "wizard.access.title": "Where do you want to use your system?",
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
    "wizard.users.title": "Who should use your system?",
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
    "wizard.login.title": "How do you want to log in?",
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
    "wizard.login.owner.email.sessionHint":
      "Leave empty to use your signed-in kombify Cloud account:",
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
    "wizard.login.passkey.title": "Pocket ID passkey",
    "wizard.login.passkey.description":
      "Password-free owner login with a device passkey",
    "wizard.login.passkey.help":
      "Pocket ID exposes the first-passkey handoff after rollout; no passkey secret enters Techstack.",
    "wizard.login.password.title": "TinyAuth gateway credential",
    "wizard.login.password.description":
      "Optional break-glass protection for the application gateway",
    "wizard.login.password.help":
      "Enable the intent here, then create the credential in TinyAuth after rollout. The Wizard never accepts or transports it.",
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
    "wizard.login.email": "Email",
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
      "Your session carries no kombify organization, so tenant data cannot be shown. Open Techstack from your kombify Cloud portal or sign in again.",

    // Wizard-run resume banner (dashboard, plan D6)
    "wizard.run.banner.inProgressTitle": "Your homelab is being set up",
    "wizard.run.banner.inProgressBody":
      "Setup is still running. You can watch the progress or come back later.",
    "wizard.run.banner.pairingTitle": "A Node is waiting to be connected",
    "wizard.run.banner.pairingBody":
      "Run the install command on your Node to finish adding it.",
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
      "Your Techstack session expired. Continue with Auth0 to return to this workflow after sign-in.",
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

    // Companion. The panel itself is served centrally and localizes its own
    // interior; these are the host's launcher strings, which the SDK never
    // ships (packages carry no customer-facing prose).
    "nav.notifications": "Notifications",
    "companion.frameTitle": "kombify Companion",
    "companion.open": "Open the Companion",
    "companion.close": "Close the Companion",
    "companion.startVoiceAgent": "Start voice agent",
    "companion.launcherLabel": "Ask kombify",
  },
  de: {
    ...loginMessages.de,
    ...accessUsersMessages.de,
    "wizard.creation.preferenceOnly":
      "Wunsch gespeichert. Dieses Release installiert den vorgeschlagenen Dienst; die Alternative wird noch nicht angewendet.",
    "wizard.preview.mail.title": "Mail",
    "wizard.preview.game.title": "Games",
    "wizard.preview.ai.description":
      "Lokale Modelle und private KI-Workflows entdecken.",
    "wizard.preview.label": "Designvorschau · Creation",
    "wizard.preview.navigation": "Navigation der Vorschau",
    "wizard.preview.back": "Aktueller Wizard",
    "wizard.preview.reset": "Zurücksetzen",
    "wizard.preview.language": "Sprache der Vorschau",
    "wizard.preview.sandboxNotice":
      "Interaktive Designvorschau. Vergleiche A und B mit derselben Auswahl. Deine Auswahl bleibt in dieser Ansicht; es wird nichts installiert. Mail und Games zeigen das geplante Beta-Erlebnis.",
    "wizard.preview.files.title": "Documents & Files",
    "wizard.preview.files.description":
      "Deine Dateien des Alltags, an deinem eigenen Ort.",
    "wizard.preview.files.help":
      "Bewahre deine Dokumente gemeinsam auf, greife von verschiedenen Geräten darauf zu und teile ausgewählte Dateien. Cloudreve ist der vorgeschlagene Dateispeicher; Nextcloud die Alternative für Zusammenarbeit. Paperless-ngx ergänzt bei Bedarf ein durchsuchbares Archiv für eingescannte Unterlagen.",
    "wizard.preview.paperless.title": "Dokumentenarchiv ergänzen",
    "wizard.preview.paperless.help":
      "Paperless-ngx organisiert eingescannte Unterlagen und macht ihren Text durchsuchbar. Optional neben deinem Dateispeicher.",
    "wizard.preview.mail.description":
      "Deine Postfächer zusammen. Mit deinem Mailclient.",
    "wizard.preview.mail.help":
      "Starte mit deinen vorhandenen Postfächern und einem passenden Client. Paperwork ist die Richtung für unseren eigenen Client; Roundcube eine unabhängige Webmail-Option. Ein eigener Mailserver bleibt optional: Stalwart oder die integrierte mailcow-Suite.",
    "wizard.preview.mail.hosting": "Hosting der Postfächer",
    "wizard.preview.mail.existing": "Vorhandenen Anbieter behalten",
    "wizard.preview.game.description":
      "Eine Minecraft-Welt für dich und deine Freunde.",
    "wizard.preview.game.help":
      "Der geplante Games-Use-Case nutzt Pterodactyl für eine dauerhafte Minecraft-Welt. Wähle Java oder Bedrock passend zu deinen Spielern. Lokales Hosting und ein Managed-VPS sind getrennte Optionen; Einladungen und die Wiederherstellung der Welt gehören zur Einrichtung.",
    "wizard.preview.game.edition": "Minecraft-Edition",
    "wizard.preview.dev.description":
      "Deine Projekte und dein Remote-Arbeitsplatz.",
    "wizard.preview.dev.help":
      "Verwalte private Repositories mit Gitea und entdecke den geplanten Entwicklungs- und Remote-Arbeitsplatz. Repository-Zugriff, Entwicklungsumgebung und Remote Desktop bleiben einzelne Optionen innerhalb des Dev-Use-Cases.",
    "wizard.preview.service.immich":
      "Ein Zuhause für Fotos und Videos mit Zeitleiste, Alben und optionalen KI-Funktionen. Die mobile App unterstützt deinen Foto-Backup-Ablauf.",
    "wizard.preview.service.cloudreve":
      "Ein eigener Dateispeicher zum Aufbewahren, Öffnen und Teilen deiner Dateien. Cloudreve ist der etablierte Standard für Documents & Files.",
    "wizard.preview.service.nextcloud":
      "Eine Plattform für Dateien und Zusammenarbeit mit einem eigenen App-Ökosystem. Eine Alternative, wenn Zusammenarbeit über einen persönlichen Dateispeicher hinausgeht.",
    "wizard.preview.service.paperless-ngx":
      "Ein Dokumentenarchiv mit OCR, Suche und Organisation für eingescannte Unterlagen. Es ergänzt deinen Dateispeicher.",
    "wizard.preview.service.vaultwarden":
      "Ein Passwort-Tresor für kompatible Bitwarden-Clients. Gerätezugriff und Wiederherstellung gehören zur Einrichtung deines Tresors.",
    "wizard.preview.service.jellyfin":
      "Ein Medienserver für deine Filme, Serien und Musik. Wiedergabe und Transcoding hängen von deinen Geräten und Medien ab.",
    "wizard.preview.service.home-assistant":
      "Verbinde deine Geräte im Zuhause und steuere sie zentral. Behalte deine vorhandene Home-Assistant-Konfiguration oder plane eine neue Einrichtung für deine tatsächlichen Geräte.",
    "wizard.preview.service.gitea":
      "Private Git-Repositories mit Code-Review und Projektzusammenarbeit. Entwicklungsumgebung und Remote Desktop sind eigene Optionen innerhalb von Dev.",
    "wizard.preview.service.paperwork":
      "kombifys eigener Client für den Mail-Use-Case. Die geplante Integration unterscheidet gehostete Accounts, eine geeignete Self-hosted-Edition und native Geräte-Clients.",
    "wizard.preview.service.roundcube":
      "Ein Mailclient im Browser für vorhandene Postfächer. Für einen Client allein brauchst du keinen eigenen Mailserver.",
    "wizard.preview.service.stalwart":
      "Eine optionale Mailserver-Wahl, wenn du eigene Postfächer betreiben möchtest. Domain, Zustellung und Wiederherstellung brauchen eine eigene Einrichtung.",
    "wizard.preview.service.mailcow":
      "Eine optionale integrierte Mail-Suite mit SOGo-Webmail und Groupware. Suite und separater Server sind alternative Wege für dieselben Postfächer.",
    "wizard.preview.service.pterodactyl":
      "Die gewählte Plattform für Games. Die geplante Integration verwaltet getrennte Minecraft-Java- und Bedrock-Profile, ihre Ressourcen und dauerhaften Welten.",
    "wizard.preview.compare": "Die beiden Entwürfe vergleichen",
    "wizard.preview.discover": "Entdecken",
    "wizard.preview.focus": "Fokus",
    "wizard.preview.eyebrow": "Dein digitales Zuhause",
    "wizard.preview.title": "Platz für das, was dir wichtig ist.",
    "wizard.preview.subtitle":
      "Wähle, was zu deinem digitalen Zuhause gehört. Starte mit dem vorgeschlagenen Dienst oder wähle eine Alternative. Die Details öffnest du, wenn du sie brauchst.",
    "wizard.preview.available": "Wähle deine Use Cases",
    "wizard.preview.availableHint":
      "Use Case auswählen. Nach deinen Wünschen anpassen.",
    "wizard.preview.defaultService": "Vorgeschlagen",
    "wizard.preview.alternative": "Alternative",
    "wizard.preview.aboutService": "Über",
    "wizard.preview.serviceFor": "Dienst für",
    "wizard.preview.preferenceOnly":
      "Für diese Designvorschau ausgewählt. Die tatsächliche Verfügbarkeit hängt von der veröffentlichten Integration ab.",
    "wizard.preview.aboutUseCase": "Genauer ansehen",
    "wizard.preview.inside": "Die Tools dahinter",
    "wizard.preview.explore": "Entdecken & anpassen",
    "wizard.preview.collapse": "Weniger Details",
    "wizard.preview.selected": "Ausgewählt",
    "wizard.preview.optional": "Optional",
    "wizard.preview.selectionCount": "ausgewählt",
    "wizard.preview.comingSoon": "Coming soon",
    "wizard.preview.comingSoonHint":
      "Schon geplant. Noch nicht zum Hinzufügen verfügbar.",
    "wizard.preview.setupPreferences": "Einstellungen für dein Setup",
    "wizard.preview.setupPreferencesHint":
      "Backups, Isolation und Startverhalten",
    "wizard.preview.loading":
      "Use Cases aus deinem StackKits-Release werden geladen…",
    "wizard.preview.unverified":
      "Die Verfügbarkeit einiger Use Cases ist noch nicht bestätigt. Bis Katalog und Freischaltung vorliegen, bleiben sie deaktiviert.",
    "wizard.preview.unverifiedShort": "Derzeit nicht verfügbar",
    "wizard.preview.retry": "Erneut prüfen",
    "wizard.preview.partOf": "Use Case",
    "wizard.preview.source": "Katalog-Release",
    "wizard.preview.website": "Offizielle Website",
    "wizard.preview.useService": "Auswählen:",
    "wizard.preview.documents.title": "Dokumente",
    "wizard.preview.documents.description":
      "Gib deiner eingehenden Post ein durchsuchbares Zuhause.",
    "wizard.preview.network.title": "Netzwerk",
    "wizard.preview.network.description":
      "Verbinde deine Geräte und behalte dein Heimnetz im Blick.",
    "wizard.preview.automation.title": "Automatisierung",
    "wizard.preview.automation.description":
      "Überlass wiederkehrende Aufgaben deinen geprüften Abläufen.",
    "wizard.preview.role.primary": "Vorgeschlagener Dienst",
    "wizard.preview.role.alternative": "Alternativer Dienst",
    "wizard.preview.role.supporting": "Unterstützender Dienst",
    "wizard.preview.role.connector": "Verbindung",
    "wizard.preview.role.bridge": "Brücke",
    "wizard.preview.roleHelp.primary":
      "Der vom Katalog vorgeschlagene Dienst für diesen Use Case. Er gehört dazu, wenn du den Use Case auswählst.",
    "wizard.preview.roleHelp.alternative":
      "Eine im Katalog hinterlegte Alternative. Die Auswahl wird als Wunsch gespeichert; die Unterstützung im Release wird gesondert angezeigt.",
    "wizard.preview.roleHelp.supporting":
      "Eine unterstützende Komponente für die Hauptanwendung. Das StackKit verwaltet, wann sie dazugehört.",
    "wizard.preview.roleHelp.connector":
      "Eine für diesen Use Case vorgesehene Verbindung zu einem weiteren Dienst.",
    "wizard.preview.roleHelp.bridge":
      "Eine für diesen Use Case vorgesehene Brücke zwischen kompatiblen Systemen.",
    "onboarding.ui.explore": "Erst kurz umsehen",
    "onboarding.ui.start_failed":
      "Die Einrichtung konnte nicht geöffnet werden. Gespeicherte Angaben bleiben erhalten. Versuche es erneut oder sieh dich zunächst um.",
    "onboarding.ui.continue": "Dein nächster Schritt",
    "hostBaseline.title": "Bestehende Dienste und Ports",
    "hostBaseline.preserve":
      "Diese Bestandsprüfung verändert bestehende Dienste und Daten nicht. Prüfe diesen Node, bevor du ein StackKit hinzufügst.",
    "hostBaseline.loading": "Bestand des Nodes wird geladen…",
    "hostBaseline.current": "Aktueller Listener-Bestand empfangen",
    "hostBaseline.incomplete":
      "Der Listener-Bestand ist unvollständig oder veraltet. Die Portverfügbarkeit ist noch nicht bestätigt.",
    "hostBaseline.observed": "Erfasst",
    "hostBaseline.claimed": "StackKit-Zuordnung vorhanden",
    "hostBaseline.existing": "Bestehender Listener",
    "hostBaseline.noListeners":
      "Im erfassten Bereich wurden keine Listener gemeldet. Gestoppte Anwendungen und andere Namespaces können weiterhin Bindungen beanspruchen.",
    "hostBaseline.recheck":
      "Vor der Ausführung prüfen Techstack und StackKits die benötigten Bindungen erneut. Bei Konflikten braucht es eine unterstützte Konfigurationsänderung, einen anderen Node oder eine geprüfte Migration. Ein offener Port bestätigt keine öffentliche Erreichbarkeit.",
    "hostBaseline.refresh": "Bestand aktualisieren",
    "hostBaseline.review": "Alle Ports und Dienste prüfen",

    "wizard.smartHome.applianceRetained":
      "Der Auftrag für das separate Home Assistant OS ist gespeichert. Beim Fortsetzen bleibt dieselbe Appliance zugeordnet. Ihre native API-Verbindung und Betriebsbereitschaft werden separat geprüft.",
    "wizard.smartHome.advice": "Home-Assistant-Installation",
    "wizard.smartHome.preserve":
      "Binde deine vorhandene Installation beobachtend ein. Accounts, Automationen und Konfiguration bleiben erhalten.",
    "wizard.smartHome.haos":
      "Home Assistant OS läuft in einer separaten VM mit Supervisor, Apps und Systemwartung.",
    "wizard.smartHome.container":
      "Container bleibt für Home Assistant Core verfügbar. Host-Dienste verwaltest du separat; Supervisor und Apps benötigen Home Assistant OS.",
    "wizard.smartHome.apps-require-haos":
      "Die gewünschten Apps benötigen Home Assistant OS.",
    "wizard.smartHome.verified-proxmox-lan-and-guest-capacity":
      "Prüfe einen verbundenen Proxmox-Host, freie Ressourcen für eine separate VM und Zugang zum Heimnetz.",
    "wizard.smartHome.verify-local-device-reachability":
      "Die Erreichbarkeit deiner Geräte muss noch geprüft werden. Eine vorhandene Netzwerkbrücke allein belegt sie nicht.",
    "wizard.smartHome.identify-and-exclusively-authorize-radio":
      "Wähle den konkreten Funkadapter und autorisiere seine exklusive Zuordnung vor der Nutzung.",
    "wizard.smartHome.detect-installed-form-and-api-capabilities":
      "Vor Verwaltungsaktionen werden Installationsform, Versionen und verfügbare APIs erfasst.",
    "wizard.smartHome.separate-management-grant":
      "Verwaltungsaktionen benötigen eine separate ausdrückliche Freigabe.",
    "wizard.server.hypervisor.title": "Hypervisor",
    "wizard.server.hypervisor.connected": "Gast verbunden",
    "wizard.server.hypervisor.connectedDetail":
      "Der Ubuntu-Gast ist mit diesem Deployment verbunden.",
    "wizard.server.hypervisor.description":
      "Erstelle eine VM auf deinem Proxmox-Server.",
    "wizard.server.hypervisor.help":
      "Nutzt dein verbundenes Proxmox mit vorhandenem Speicher und Netzwerk.",
    "wizard.server.hypervisor.standard":
      "Ubuntu 24.04 LTS betreibt den StackKits-Core in einer eigenen VM. Techstack bereitet den Gast vor und verbindet ihn automatisch.",
    "wizard.server.hypervisor.appliance":
      "Home Assistant OS bekommt eine separate Appliance-VM. Der Ubuntu-Gast betreibt weiterhin den Core und weitere Dienste.",
    "wizard.server.hypervisor.host": "Verbundener Proxmox-Server",
    "wizard.server.hypervisor.storage": "Speicher",
    "wizard.server.hypervisor.bridge": "LAN-Bridge",
    "wizard.server.hypervisor.disk": "Datenträger",
    "wizard.server.hypervisor.select": "Auswählen…",
    "wizard.server.hypervisor.loading": "Wird geladen…",
    "wizard.server.hypervisor.offline": "Nicht verfügbar",
    "wizard.server.hypervisor.unavailable":
      "Der verbundene Hypervisor oder sein Ressourceninventar ist nicht verfügbar. Aktualisiere nach Wiederherstellung der Verbindung.",
    "wizard.server.hypervisor.refresh": "Aktualisieren",
    "wizard.server.hypervisor.resourcesMissing":
      "Benötigt werden ein aktiver Speicher für Images und Imports sowie eine aktive LAN-Bridge.",
    "wizard.server.hypervisor.scan": "Proxmox im Heimnetz finden",
    "wizard.server.hypervisor.scanning": "Suche läuft…",
    "wizard.server.hypervisor.scanFailed":
      "Die Netzwerksuche konnte nicht abgeschlossen werden. Du kannst Proxmox weiterhin manuell verbinden.",
    "wizard.server.hypervisor.noLan":
      "Die Netzwerksuche benötigt einen verbundenen lokalen Executor. Die manuelle Verbindung bleibt verfügbar.",
    "wizard.server.hypervisor.detected": "Proxmox verifiziert",
    "wizard.server.hypervisor.discoveryHint":
      "Gefundene Server werden auswählbar, nachdem du sie verbunden und autorisiert hast.",
    "wizard.server.hypervisor.connect": "Weiteren Proxmox-Server verbinden",
    "wizard.server.hypervisor.authorize": "Gastverwaltung aktivieren",
    "wizard.server.hypervisor.authorizeFailed":
      "Die Gastverwaltung konnte nicht aktiviert werden. Prüfe die lokale Proxmox-Konfiguration des Guards und aktualisiere die Liste.",
    "wizard.server.hypervisor.progress":
      "Techstack bereitet den Ubuntu-Gast auf deinem verbundenen Proxmox vor, verbindet ihn und führt das gewählte StackKit aus. Die Fortsetzung verwendet denselben Vorgang.",
    // Navigation
    "nav.dashboard": "Dashboard",
    "nav.monitoring": "Monitoring",
    "nav.services": "Dienste",
    "nav.wallet": "Wallet",
    "nav.help": "Hilfe",
    "nav.settings": "Einstellungen",
    "nav.logout": "Abmelden",
    "nav.allServices": "Alle Dienste",
    "nav.servers": "Server",
    "nav.monitoring.servers": "Server",
    "nav.monitoring.alerts": "Alarme",
    "nav.monitoring.history": "Verlauf",
    "nav.preview.services": "Dienste",
    "nav.preview.noServices": "Noch keine Dienste registriert.",
    "nav.preview.openServices": "Dienste öffnen",
    "nav.preview.monitoring": "Monitoring",
    "nav.preview.activeAlerts": "aktive Alarme",
    "nav.preview.noAlerts": "Keine aktiven Alarme.",
    "nav.preview.openMonitoring": "Monitoring öffnen",

    // Onboarding
    "onboarding.ui.getting_started.title": "Bring dein Homelab ans Laufen",
    "onboarding.ui.getting_started.subtitle":
      "Fünf Schritte von leer bis zum ersten Dienst — und der schnellste Weg, dich hier auszukennen.",
    "onboarding.ui.getting_started.completed": "Alles eingerichtet",
    "onboarding.ui.getting_started.completed_body":
      "Die Liste ist abgearbeitet. Sie bleibt hier, falls du einen Schritt noch einmal ansehen willst.",
    "onboarding.ui.getting_started.dismiss": "Ausblenden",
    "onboarding.ui.getting_started.resume": "Checkliste wieder anzeigen",
    "onboarding.ui.getting_started.minimize": "Kompakte Checkliste",
    "onboarding.ui.getting_started.expand": "Ausführliche Checkliste",
    "onboarding.ui.progress.label": "Fortschritt",
    "onboarding.ui.step.optional": "Optional",
    "onboarding.ui.step.done": "Erledigt",
    "onboarding.ui.step.blocked": "Nicht verfügbar",
    "onboarding.ui.step.mark_done": "Überspringen",
    "onboarding.ui.coach.dismiss": "Verstanden",
    "onboarding.ui.introduction.replay": "Einführung wiederholen",
    "onboarding.ui.introduction.back": "Zurück",
    "onboarding.ui.introduction.next": "Weiter",
    "onboarding.ui.introduction.done": "Fertig",
    "onboarding.ui.introduction.skip": "Tour überspringen",
    "onboarding.ui.introduction.close": "Schließen",
    "introduction.restart": "Einführung wiederholen",
    "introduction.welcome.title": "Willkommen in deinem Techstack",
    "introduction.welcome.body":
      "Techstack ist die Leitstelle für dein Homelab. Es orchestriert deine eigenen Server, rollt fertige StackKits darauf aus und hält jeden laufenden Dienst — mit Zustand, Logs und Kosten — auf einem Bildschirm. Hardware und Daten bleiben bei dir; Techstack macht die Verkabelung.",
    "introduction.welcome.name_label":
      "Das Wichtigste zuerst: Wie sollen wir diesen Ort nennen?",
    "introduction.welcome.name_hint":
      "Dein Homelab hat mehr verdient als „Server 1“. Alles erlaubt, und umbenennen kannst du es später in den Einstellungen.",
    "introduction.welcome.name_placeholder": "MyHomeLab",
    "introduction.welcome.cta": "Mein Homelab einrichten",
    "introduction.welcome.cta_plain": "Mein Homelab einrichten",
    "introduction.navigation.title": "Das ist deine Karte",
    "introduction.navigation.body":
      "Alle Bereiche hängen an der Seitenleiste. Fahr über einen Abschnitt — probier es ruhig gleich aus, die Tour wartet — und sein Inhalt klappt neben der Leiste auf, ohne dass du die Seite verlässt.",
    "introduction.getting_started.title":
      "Erste Schritte, und dann richtig gut",
    "introduction.getting_started.body":
      "Keine Ahnung, wo du bei alldem anfangen sollst? Öffne „Erste Schritte“. Die Liste gibt dir die ersten echten Aufgaben der Reihe nach, hakt jede von selbst ab, sobald du sie erledigt hast, und verschwindet am Ende. Der kurze Weg von neu hier zu kennt sich aus.",
    "introduction.account.title": "Einstellungen liegen hinter deinem Namen",
    "introduction.account.body":
      "Design, Hilfe und dein Konto liegen hier — und hier startest du diese Einführung erneut. Das Aussehen kannst du hier jederzeit anpassen.",
    "onboarding.techstack_platform.techstack_find_stackkit.title":
      "StackKit auswählen",
    "onboarding.techstack_platform.techstack_find_stackkit.body":
      "Ein StackKit ist ein fertiges Bündel von Diensten. Sieh dir die Auswahl an und nimm das, was deinem Vorhaben am nächsten kommt.",
    "onboarding.techstack_platform.techstack_find_stackkit.cta":
      "StackKits ansehen",
    "onboarding.techstack_platform.techstack_configure_intent.title":
      "Sag, wofür es sein soll",
    "onboarding.techstack_platform.techstack_configure_intent.body":
      "Beantworte ein paar Fragen zu deinem Vorhaben. kombify macht daraus eine Konfiguration, die du prüfen kannst.",
    "onboarding.techstack_platform.techstack_configure_intent.cta":
      "Assistent öffnen",
    "onboarding.techstack_platform.techstack_deploy_stackkit.title":
      "Bereitstellung fortsetzen",
    "onboarding.techstack_platform.techstack_deploy_stackkit.body":
      "Bestätige den Plan und lass kombify das StackKit in dein Homelab ausrollen. Dieser Schritt hakt sich selbst ab, sobald die Bereitstellung existiert.",
    "onboarding.techstack_platform.techstack_deploy_stackkit.cta":
      "Bereitstellung fortsetzen",
    "onboarding.techstack_platform.techstack_add_server.title":
      "Node hinzufügen",
    "onboarding.techstack_platform.techstack_add_server.body":
      "Verbinde einen eigenen Node oder lass kombify einen betreiben. Deine Dienste brauchen einen Ort.",
    "onboarding.techstack_platform.techstack_add_server.cta": "Node hinzufügen",
    "onboarding.techstack_platform.techstack_deploy_service.title":
      "Ersten Dienst öffnen",
    "onboarding.techstack_platform.techstack_deploy_service.body":
      "Sobald ein Dienst läuft, erreichst du ihn über die Dienstliste. Darum geht es am Ende.",
    "onboarding.techstack_platform.techstack_deploy_service.cta":
      "Zu den Diensten",
    "onboarding.denied.policy_selfhost_byos.title":
      "Von kombify betriebene Nodes gibt es nur in der Cloud",
    "onboarding.denied.policy_selfhost_byos.body":
      "Diese Installation läuft selbst gehostet, kombify stellt dafür keine Nodes bereit. Verbinde einen eigenen Node — der Rest funktioniert genauso.",
    "onboarding.denied.policy_selfhost_byos.next_step":
      "Verbinde einen Node, den du bereits betreibst.",
    "onboarding.denied.required_feature_disabled.title":
      "Verwaltete Nodes sind für dieses Konto nicht aktiv",
    "onboarding.denied.required_feature_disabled.body":
      "Dieses Konto ist noch nicht für von kombify betriebene Nodes freigeschaltet. Ein eigener Node lässt sich trotzdem verbinden.",
    "onboarding.denied.required_feature_disabled.next_step":
      "Verbinde einen eigenen Node oder lass die verwaltete Laufzeit freischalten.",

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
    "common.moreInfo": "Mehr zu dieser Option",
    "common.recommended": "empfohlen",

    // Wizard
    "wizard.title": "kombify-Techstack erstellen",
    "wizard.subtitle":
      "Konfiguriere das StackKit Deployment für deinen eigenen Node oder ein verwaltetes kombify-Ziel.",
    "wizard.step.goals": "Ziele",
    "wizard.step.server": "Node",
    "wizard.step.access": "Zugriff",
    "wizard.step.users": "Nutzer",
    "wizard.step.login": "Login",
    "wizard.creating": "Wird erstellt...",
    "wizard.validation.completeFields":
      "Hinweis: Vervollständige die markierten Felder",

    // Wizard Step 1 - Goals
    "wizard.goals.title": "Was möchtest du machen?",
    "wizard.goals.subtitle":
      "Wähle aus, was du mit deinem System erreichen willst. Du kannst später jederzeit weitere Funktionen hinzufügen.",
    "wizard.hints.title": "Dein Homelab-Abenteuer",
    "wizard.hints.updating": "Wird aktualisiert…",
    "wizard.hints.stale": "Inventar möglicherweise veraltet",
    "wizard.hints.degraded": "Begrenzte Datengrundlage",
    "wizard.hints.bestFit": " ist der beste Weg für dein Abenteuer.",
    "wizard.hints.review": "Empfehlung prüfen",
    "wizard.hints.incomplete":
      "Wähle, was du zuerst erleben möchtest. Wir formen daraus den sichersten unterstützten Standard.",
    "wizard.hints.unavailable":
      "Deine Reise bleibt auf dem besten unterstützten Standard, während die Live-Guidance neu verbindet.",
    "wizard.hints.evaluating":
      "Dein bester unterstützter Weg wird zusammengestellt…",
    "wizard.goals.smartHome.title": "Smart Home",
    "wizard.goals.smartHome.description":
      "Home Assistant auf deinem eigenen Node: Automationen, die auch ohne Internet weiterlaufen.",
    "wizard.goals.smartHome.help":
      "Ist das an, läuft Home Assistant zu Hause und steuert Licht, Heizung und Sensoren lokal statt über eine Hersteller-Cloud. Du entscheidest, ob ein am Node eingesteckter Zigbee- oder Z-Wave-Stick genutzt wird und ob Geräte im Netzwerk automatisch gefunden werden.",
    "wizard.goals.smartHome.tip":
      "Automationen laufen weiter, wenn die Internetverbindung ausfällt.",
    "wizard.goals.photos.title": "Fotoerinnerungen",
    "wizard.goals.photos.description":
      "Deine eigene Foto- und Videosammlung, mit Backup von jedem Handy im Haushalt.",
    "wizard.goals.photos.help":
      "Ist das an, wird dein Node der Ort, an dem deine Fotos wohnen: Handys sichern automatisch, du suchst nach Gesichtern und Orten, und Alben lassen sich ohne Cloud-Abo mit der Familie teilen. Du entscheidest, ob die smarte Suche auf dem Node läuft und wo die Bibliothek liegt.",
    "wizard.goals.photos.tip":
      "Der sichtbarste Gewinn für die meisten Homelabs: private Erinnerungen ohne weiteres Cloud-Abo.",
    "wizard.goals.media.title": "Medien-Streaming",
    "wizard.goals.media.description":
      "Filme, Serien und Musik auf jeden Fernseher, jedes Handy und jeden Laptop zu Hause streamen.",
    "wizard.goals.media.help":
      "Ist das an, liefert dein Node deine eigene Mediensammlung an jeden Bildschirm im Haus und – wenn du Fernzugriff erlaubst – auch unterwegs. Du entscheidest, ob die Grafikkarte des Nodes beim Umwandeln für kleine Bildschirme hilft und wo die Bibliothek liegt.",
    "wizard.goals.media.tip":
      "Ein Server mit genug Speicher und optionalem Hardware-Transcoding sorgt für das flüssigste Erlebnis.",
    "wizard.goals.vault.title": "Passwort-Tresor",
    "wizard.goals.vault.description":
      "Ein Passwort-Manager, den der Haushalt selbst betreibt – kompatibel mit den Bitwarden-Apps.",
    "wizard.goals.vault.help":
      "Ist das an, bekommt jede Person einen Tresor für Passwörter, Karten und sichere Notizen, synchronisiert über die Apps, die sie schon kennt. Nur der Besitzer legt Konten an, solange du die Registrierung nicht öffnest.",
    "wizard.goals.vault.tip":
      "Gut für Sicherheit ab Tag eins: klein, nützlich und leicht zu sichern.",
    "wizard.goals.files.title": "Dateien teilen",
    "wizard.goals.files.description":
      "Dokumente und Ordner an einem Ort, geteilt und synchron auf allen Geräten.",
    "wizard.goals.files.help":
      "Ist das an, hast du ein privates Laufwerk: von jedem Gerät hochladen, Links mit der Familie teilen, Arbeitsdateien synchron halten. Cloudreve ist der Standard; Nextcloud steht als Alternative bereit, wenn du eine volle Kollaborations-Suite willst. Du entscheidest, wo die Dateien liegen.",
    "wizard.goals.files.tip":
      "Vor wichtigen Haushaltsdokumenten zuerst Backups einrichten.",
    "wizard.goals.ai.title": "KI / LLM",
    "wizard.goals.ai.description":
      "Ein privater Assistent auf deiner eigenen Hardware, der deine Daten nie nach draußen schickt.",
    "wizard.goals.ai.help":
      "Ist das an, bekommst du einen Chat-Assistenten, der komplett auf deinem Node läuft und mit deinen eigenen Dokumenten arbeiten kann. Du entscheidest, welchen Beschleuniger er nutzt und wie groß das Modell ist; eine GPU macht einen großen Unterschied.",
    "wizard.goals.ai.tip":
      "Läuft am besten auf stärkerer Hardware; kleine Nodes lassen sich trotzdem schon planen.",
    "wizard.goals.dev.title": "Entwicklungsplattform",
    "wizard.goals.dev.description":
      "Dein eigenes Git-Hosting, mit Builds und Test-Pipelines, wenn du sie willst.",
    "wizard.goals.dev.help":
      "Ist das an, liegt dein Code auf deinem Node, mit einem Gitea-Server für den ganzen Haushalt oder das Team. Du entscheidest, ob CI-Runner auf dem Node auch bauen und testen.",
    "wizard.goals.dev.tip":
      "Nützlich, wenn der Laptop schlank bleiben soll und reproduzierbare Umgebungen auf dem Server leben.",
    "wizard.goals.mail.title": "Mailserver",
    "wizard.goals.mail.description":
      "E-Mail auf deiner eigenen Domain empfangen und senden – von deinem eigenen Node.",
    "wizard.goals.mail.help":
      "Ist das an, betreibt dein Node einen kompletten Mailserver für eine Domain, die dir gehört. E-Mail braucht korrekte DNS-Einträge und einen guten Versand-Ruf, deshalb hält diese Lane zuerst deine Absicht fest und rollt mit Anleitung aus. Du entscheidest die Mail-Domain.",
    "wizard.goals.mail.tip":
      "E-Mail hat DNS- und Reputationsanforderungen; der Wizard hält die Absicht fest und schaltet den Rollout bewusst frei.",
    "wizard.goals.game.title": "Gameserver",
    "wizard.goals.game.description":
      "Dauerhafte Spielwelten für Freunde, zu Hause gehostet, ohne dein Netzwerk zu öffnen.",
    "wizard.goals.game.help":
      "Ist das an, hostet dein Node Spielserver, denen Freunde über den sicheren Zugang des Kits beitreten – ohne dein Heimnetz direkt freizugeben. Welche Spiele verfügbar sind, hängt vom Release ab.",
    "wizard.goals.game.tip":
      "Sitzungsbasierte Last: der Node arbeitet, während Freunde spielen, und ruht dazwischen.",
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
      "Wenn du unsicher bist, wähle nur ein Ziel. kombify-Techstack bleibt ein einzelnes System und du kannst später weitere Dienste hinzufügen. Mehr Einstellungen? Probiere die erweiterten Einstellungen unten.",
    "wizard.goals.advancedTitle": "Erweiterte Use Cases",
    "wizard.goals.showMoreDetails": "Mehr Details anzeigen",
    "wizard.goals.showLessDetails": "Weniger anzeigen",
    "wizard.goals.notInThisRelease": "Noch nicht installierbar",
    "wizard.goals.notInThisReleaseLong":
      "Noch nicht installierbar — die Auswahl wird für ein späteres Release vorgemerkt",
    "wizard.goals.spec.backend": "Backend",
    "wizard.goals.spec.alternative": "Alternative",
    "wizard.goals.spec.alternativeNote":
      "Das Standard-Backend wird installiert",
    "wizard.goals.spec.tiers": "Compute-Tiers",
    "wizard.goals.spec.tiersNotIncluded": "nicht enthalten",
    "wizard.goals.spec.delivery": "Auslieferung",
    "wizard.goals.spec.deliveryNow": "Dieses Release",
    "wizard.goals.spec.deliveryLater": "Ein späteres Release",
    "wizard.goals.advanced.label": "Erweitert",
    "wizard.goals.advanced.tabsLabel": "Use-Case-Einstellungen",
    "wizard.goals.setting.later": "Späteres Release",
    "wizard.goals.group.profile.description":
      "Installation und Ressourcen für diesen Use Case.",
    "wizard.goals.group.storage.description":
      "Wähle, wie dieser Use Case deine Daten speichert und schützt.",
    "wizard.goals.group.hardware.description":
      "Lege fest, welche Hardware dieser Use Case verwenden kann.",
    "wizard.goals.group.access.description":
      "Bestimme, wie du dich mit diesem Use Case verbindest.",
    "wizard.goals.group.features.description":
      "Passe den Use Case an deine Nutzung an.",
    "wizard.goals.group.backend.description":
      "Wähle den Dienst hinter diesem Use Case.",
    "wizard.goals.advanced.tiers": "Compute-Tiers",
    "wizard.goals.advanced.components": "Komponenten",
    "wizard.goals.advanced.tierIncluded": "enthalten",
    "wizard.goals.advanced.tierExcluded": "in diesem Release nicht enthalten",
    "wizard.goals.role.primary": "Primär",
    "wizard.goals.role.alternative": "Alternative",
    "wizard.goals.role.supporting": "Unterstützend",
    "wizard.goals.role.connector": "Connector",
    "wizard.goals.role.bridge": "Bridge",
    "wizard.goals.decisions": "Deine Entscheidungen",
    "wizard.goals.learnMore": "Anleitung lesen",
    "wizard.goals.group.backend": "Backend",
    "wizard.goals.group.profile": "Profil",
    "wizard.goals.group.storage": "Speicher",
    "wizard.goals.group.hardware": "Hardware",
    "wizard.goals.group.access": "Zugang",
    "wizard.goals.group.features": "Funktionen",
    "wizard.goals.tier.low": "Klein",
    "wizard.goals.tier.standard": "Standard",
    "wizard.goals.tier.high": "Groß",
    "wizard.goals.backend.help":
      "Welches Produkt die Arbeit macht. Der Standard ist das, was StackKits am meisten testet.",
    "wizard.goals.profile.help":
      "Wie viel vom Node dieser Use Case nutzen darf. Standard passt für die meisten Haushalte.",
    "wizard.goals.setting.on": "an",
    "wizard.goals.setting.off": "aus",
    "wizard.goals.setting.recorded":
      "Jetzt gespeichert, ein späteres Release wendet es an.",
    "wizard.goals.add": "Zum Kit hinzufügen",
    "wizard.goals.remove": "Aus dem Kit entfernen",

    // Wizard Step 2 - Node
    "wizard.server.eyebrow": "Mach es zu deinem",
    "wizard.server.branch.question": "Womit startest du?",
    "wizard.server.owned.subtitle":
      "Ein Computer, Heimserver oder dein eigener VPS.",
    "wizard.server.new.subtitle":
      "Von kombify bereitgestellt oder beim Partner gemietet.",
    "wizard.server.choice.choose": "Diesen Weg wählen",
    "wizard.server.choice.selected": "Dein gewählter Weg",
    "wizard.server.choice.change": "System ändern",
    "wizard.server.partner.title": "Server beim Partner finden",
    "wizard.server.partner.description":
      "Miete deinen VPS direkt. Verbinde ihn anschließend hier mit deinem Homelab.",
    "wizard.server.partner.explore": "Unsere Partner entdecken",
    "wizard.server.partner.next":
      "Wähle deinen Anbieter. Komm danach hierher zurück.",
    "wizard.server.partner.hint":
      "Miete einen Server bei einem unserer Partner. Sobald er bereit ist, verbindest du ihn hier per Befehl oder SSH. Deine Wizard-Auswahl bleibt erhalten.",
    "wizard.server.partner.visit": "Zum Anbieter",
    "wizard.server.partner.ready": "Mein Gerät ist bereit — jetzt verbinden",
    "wizard.server.owned.title": "Ich habe schon ein Gerät",
    "wizard.server.new.title": "Ich brauche einen Server",
    "wizard.server.path.command": "Kopieren · ausführen · verbinden",
    "wizard.server.path.remote": "SSH-Schlüssel oder Passwort",
    "wizard.server.path.managed": "Von kombify verwaltet",
    "wizard.server.path.owned": "Deine eigene Hardware",
    "wizard.server.partners.title": "Lieber direkt einen Server mieten?",
    "wizard.server.partners.body":
      "Schau bei unseren Infrastrukturpartnern vorbei und verbinde deinen VPS danach über einen der beiden Gerätewege.",
    "wizard.server.next.label": "So geht es weiter",
    "wizard.server.next.command": "Ein Befehl bis zur Verbindung.",
    "wizard.server.next.remote": "Verbinden wir dein Gerät.",
    "wizard.server.next.managed": "Dein VPS für dein digitales Zuhause.",
    "wizard.server.command.review": "Auswahl abschließen",
    "wizard.server.command.reviewBody":
      "Lege Zugriff und Nutzer fest und bestätige deine Einrichtung.",
    "wizard.server.command.run": "Befehl ausführen",
    "wizard.server.command.runBody":
      "Kopiere deinen persönlichen Verbindungsbefehl in das Terminal deines Geräts.",
    "wizard.server.command.connected": "Einrichtung verfolgen",
    "wizard.server.command.connectedBody":
      "kombify verbindet das Gerät und zeigt dir den Fortschritt der Installation.",
    "wizard.server.system.title": "Geräte- und Systemdetails",
    "wizard.server.system.hint":
      "Starte mit der Standard-Einrichtung für Linux. Ist dein Gerät ein Proxmox-Host, wähle unten den dafür vorgesehenen Verbindungsweg.",
    "wizard.server.system.ubuntu": "Ubuntu / Linux",
    "wizard.server.system.ubuntuBody":
      "Ubuntu ist der Standard. Der Installer prüft das Betriebssystem auf deinem Gerät.",
    "wizard.server.system.proxmox": "Proxmox-Hypervisor",
    "wizard.server.system.proxmoxBody":
      "Verbinde den Host und bereite eine eigene Ubuntu-VM für deine Tools vor.",
    "wizard.server.system.proxmoxSequence":
      "Verbinde den Proxmox-Host, aktiviere die Gastverwaltung und wähle anschließend den Platz für die Ubuntu-VM.",
    "wizard.server.managed.checking":
      "VPS-Verfügbarkeit für dein Konto wird geprüft …",
    "wizard.server.managed.verificationFailed":
      "Der VPS-Zugriff lässt sich gerade nicht prüfen. Versuche es erneut oder verbinde in der Zwischenzeit dein eigenes Gerät.",
    "wizard.server.managed.verificationFailedCaption":
      "VPS-Zugriff konnte nicht geprüft werden.",
    "wizard.server.managed.authFailed":
      "Deine Anmeldung konnte noch nicht geprüft werden. Versuche es nach abgeschlossener Anmeldung erneut. Du kannst auch dein eigenes Gerät verbinden.",
    "wizard.server.managed.baseMissing":
      "Für dieses Konto fehlt: {features}. Verbinde dein eigenes Gerät oder bitte deine Kontoverwaltung beziehungsweise den Support, den VPS-Zugang mit Cloud Kit freizuschalten.",
    "wizard.server.managed.providersMissing":
      "Für dieses Konto ist kein VPS-Anbieter freigeschaltet. Es fehlt der Anbieterzugang für: {providers}. Verbinde dein eigenes Gerät oder bitte deine Kontoverwaltung beziehungsweise den Support um Freischaltung eines Anbieters.",
    "wizard.server.managed.unavailable":
      "VPS-Zugang ist für dieses Konto nicht freigeschaltet.",
    "wizard.server.managed.retry": "Erneut prüfen",
    "wizard.server.managed.standard": "Dein Standard-VPS",
    "wizard.server.managed.premium": "Dein Premium-VPS",
    "wizard.server.managed.poweredBy": "Powered by",
    "wizard.server.managed.available": "Für dein Konto verfügbar",
    "wizard.server.managed.loadingSpecs": "Serverkonfiguration wird geladen …",
    "wizard.server.managed.location": "Standort",
    "wizard.server.managed.specsUnavailable":
      "Die Serverdetails sind gerade nicht verfügbar.",
    "wizard.server.managed.details": "Anbieter- und Serverdetails",
    "wizard.server.managed.detailsHint":
      "Deine Konfiguration und verfügbaren Anbieter ansehen.",
    "wizard.server.managed.provider": "Infrastrukturanbieter",
    "wizard.server.managed.providerChoice":
      "Wähle aus den für dein Konto freigeschalteten Anbietern.",
    "wizard.server.managed.providerAssigned":
      "Dieser Anbieter ist in deinem aktuellen VPS-Zugang enthalten.",
    "wizard.server.managed.moreOptionsPending":
      "Weitere Servergrößen und Standorte lassen sich hier noch nicht auswählen.",
    "wizard.server.remote.connectionDetails": "Verbindungsdetails",
    "wizard.server.remote.connectionHint":
      "Gib die Adresse deines Geräts ein und wähle die Anmeldung.",
    "wizard.server.remote.advanced": "SSH-Details",
    "wizard.server.remote.keyHint":
      "Verwende den Namen eines in deinem Wallet gespeicherten SSH-Schlüssels.",
    "wizard.server.preview.goals": "1 · Use Cases",
    "wizard.server.preview.node": "2 · Dein Server",
    "wizard.server.title": "Gib deinen Tools ein Zuhause.",
    "wizard.server.subtitle":
      "Starte mit dem, was du hast. Wir führen dich zur passenden Verbindung.",
    "wizard.server.join.title": "Dein Homelab bekommt einen weiteren Node.",
    "wizard.server.join.description":
      "Dieser Node ergänzt dein bestehendes Setup. Zugriff, Personen und Anmeldung bleiben erhalten – du kümmerst dich nur um das Gerät.",
    "wizard.server.command.join.review": "Diesen Node bestätigen",
    "wizard.server.command.join.reviewBody":
      "Wähle, wie sich das Gerät verbindet. Zugriff, Personen und Anmeldung deines Homelabs bleiben bestehen.",
    "wizard.step.inherited": "Für dein Homelab bereits festgelegt",
    "wizard.step.existingOwner": "Der Owner deines Homelabs bleibt gleich",
    "wizard.server.cloud.title": "kombify VPS",
    "wizard.server.cloud.description":
      "Dein eigener virtueller Server, von kombify bereitgestellt und verbunden.",
    "wizard.server.cloud.help":
      "Mit einem passenden Abo verfügbar. Dein Konto bestimmt, welche Anbieter du wählen kannst.",
    "wizard.server.cloud.info":
      "kombify bereitet deinen Server vor und verbindet ihn mit deinem Homelab. Deine ausgewählten Tools folgen der geprüften Konfiguration.",
    "wizard.server.mode.managed": "verwaltet",
    "wizard.server.remote.title": "Mein Gerät verbinden",
    "wizard.server.remote.description":
      "Du hast schon einen Server? Verbinde ihn mit SSH-Schlüssel oder Passwort.",
    "wizard.server.remote.help":
      "Gib die Adresse deines erreichbaren Servers ein. Verbinde ihn mit einem gespeicherten SSH-Schlüssel oder deinem SSH-Passwort.",
    "wizard.server.remote.host": "Node-Host oder IP",
    "wizard.server.remote.port": "SSH-Port",
    "wizard.server.remote.user": "SSH-Benutzer",
    "wizard.server.remote.auth": "Authentifizierung",
    "wizard.server.remote.auth.sshKey": "SSH-Schlüssel",
    "wizard.server.remote.auth.password": "Passwort",
    "wizard.server.remote.keyLabel": "SSH-Schlüssel-Label",
    "wizard.server.remote.password": "SSH-Passwort",
    "wizard.server.remote.test": "Verbindung testen",
    "wizard.server.remote.testing": "Verbindung wird getestet…",
    "wizard.server.remote.test.success": "SSH-Verbindung erfolgreich",
    "wizard.server.remote.test.failed": "SSH-Verbindung fehlgeschlagen",
    "wizard.server.remote.sudo": "sudo für Setup-Befehle verwenden",
    "wizard.server.oneliner.title": "Gib mir einen 1-Liner",
    "wizard.server.oneliner.description":
      "Ein Befehl in deinem Terminal. Wir führen dich durch den Rest der Einrichtung.",
    "wizard.server.oneliner.help":
      "Schließe die Konfiguration ab und erhalte einen Verbindungsbefehl für dein Gerät.",
    "wizard.server.oneliner.info":
      "Dein persönlicher Befehl steht nach Abschluss des Wizards bereit. Führe ihn im Terminal des Geräts aus, das du verbinden möchtest.",

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
    "wizard.access.title": "Wo willst du dein System nutzen?",
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
    "wizard.users.title": "Wer soll dein System nutzen?",
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
    "wizard.login.title": "Wie möchtest du dich einloggen?",
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
    "wizard.login.owner.email.sessionHint":
      "Leer lassen, um das angemeldete kombify Cloud Konto zu verwenden:",
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
    "wizard.login.passkey.title": "Pocket ID Passkey",
    "wizard.login.passkey.description":
      "Passwortloser Owner-Login mit einem Geräte-Passkey",
    "wizard.login.passkey.help":
      "Pocket ID zeigt nach dem Rollout die Einrichtung des ersten Passkeys; kein Passkey-Geheimnis gelangt in Techstack.",
    "wizard.login.password.title": "TinyAuth-Gateway-Zugang",
    "wizard.login.password.description":
      "Optionaler Break-Glass-Schutz für das Anwendungs-Gateway",
    "wizard.login.password.help":
      "Aktiviere hier nur die Absicht und lege den Zugang nach dem Rollout in TinyAuth an. Der Wizard nimmt ihn nie entgegen.",
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
    "wizard.login.email": "E-Mail",
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
      "Deine Sitzung trägt keine kombify-Organisation, daher können keine Organisationsdaten angezeigt werden. Öffne Techstack über dein kombify-Cloud-Portal oder melde dich erneut an.",

    // Wizard-run resume banner (dashboard, plan D6)
    "wizard.run.banner.inProgressTitle": "Dein Homelab wird eingerichtet",
    "wizard.run.banner.inProgressBody":
      "Die Einrichtung läuft noch. Du kannst den Fortschritt ansehen oder später zurückkommen.",
    "wizard.run.banner.pairingTitle": "Ein Node wartet auf die Verbindung",
    "wizard.run.banner.pairingBody":
      "Führe den Installationsbefehl auf deinem Node aus, um ihn fertig hinzuzufügen.",
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
      "Deine Techstack-Sitzung ist abgelaufen. Fahre mit Auth0 fort, um nach der Anmeldung zu diesem Arbeitsschritt zurückzukehren.",
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

    // Companion
    "nav.notifications": "Benachrichtigungen",
    "companion.frameTitle": "kombify Companion",
    "companion.open": "Companion öffnen",
    "companion.close": "Companion schließen",
    "companion.startVoiceAgent": "Sprachassistent starten",
    "companion.launcherLabel": "kombify fragen",
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
