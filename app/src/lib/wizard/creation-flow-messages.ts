// Customer-visible strings of the creation flow: the wizard shell (stepper,
// validation, draft, catalog states, final button), the way a failed creation
// is explained, and the finished-creation page. Merged into every locale by
// i18n.ts.
export const creationFlowMessages = {
  en: {
    "creation.embed.continueFull": "Continue in Techstack",
    "placement.preferred.missing":
      "The server you started from is no longer available. Choose another target.",
    "placement.preferred.incompatible":
      "{name} runs a StackKit that cannot take this selection right now. Another target is preselected.",
    "placement.preferred.notReady":
      "{name} cannot take a use case right now. Check its connection, or choose another target.",
    "wizard.summary.goals.one":
      "{count} use case",
    "wizard.summary.goals.other":
      "{count} use cases",
    "wizard.summary.server.installCommand":
      "Install command",
    "wizard.summary.server.connectRemote":
      "Connect by SSH",
    "wizard.summary.server.managed":
      "Managed server",
    "wizard.summary.server.hypervisor":
      "Proxmox guest",
    "wizard.summary.users.solo":
      "Only you",
    "wizard.summary.users.shared":
      "Shared household",
    "wizard.summary.users.people.one":
      "{count} person",
    "wizard.summary.users.people.other":
      "{count} people",
    "placement.chooseTitle": "Where should your next use case run?",
    "placement.chooseBody":
      "Use one of your StackKits or enrolled servers, or set up a new server. You choose the use cases next.",
    "placement.chooseNext": "Choose its use cases in the next step",
    "placement.leased":
      "This managed server must keep its current provider binding. Open its existing deployment.",
    "placement.restricted":
      "Changes on this server are unavailable. Review its access and readiness.",
    "placement.title": "Where should this use case run?",
    "placement.body":
      "Use an existing StackKit and its servers, or set up a server. Your choices come with you.",
    "placement.loading": "Checking your existing targets…",
    "placement.failed":
      "Your targets could not be checked. Try again, or choose a new server.",
    "placement.change": "Change target",
    "placement.unknownNodes": "Server details unavailable",
    "placement.compatible": "Verified to fit on these servers",
    "placement.unavailable":
      "This target cannot be recommended yet. Review its setup and capacity.",
    "placement.new": "Set up a new server",
    "placement.newBody":
      "Keep your use case and answers and choose where the new server comes from.",
    "placement.continue": "Continue with this target",
    "placement.install": "Install a StackKit on {name}",
    "placement.installBody":
      "This enrolled server has no StackKit yet. Review the setup before installing.",
    "placement.disconnected":
      "This server must reconnect before you can install a StackKit.",

    "common.retry": "Try again",
    "ui.recreate.recreating": "Recreating…",
    "ui.theme.toLight": "Switch to light theme",
    "ui.theme.toDark": "Switch to dark theme",

    "wizard.create.submit": "Create homelab",
    "wizard.create.submitting": "Creating homelab…",

    "wizard.stepper.label": "Setup steps",
    "wizard.stepper.completed": "completed",
    "wizard.stepper.position": "Step {current} of {total}: {step}",
    "wizard.stepper.goTo": "Go to {step}",
    "wizard.stepper.locked": "Complete the steps before this one first",

    "wizard.validation.serverSelection":
      "Choose where your server comes from to continue.",
    "wizard.validation.hypervisorGuest":
      "Complete the details of the new server (storage, network and size) to continue.",
    "wizard.validation.publicUsers":
      "Public access can't be set up here yet. Turn it off to continue.",
    "wizard.validation.stepIncomplete": "Complete this step to continue.",

    "wizard.draft.restored":
      "We restored your earlier answers. Passwords and other secrets are never saved, so enter them again.",
    "wizard.leave.title": "Leave this page?",
    "wizard.leave.confirmAction": "Leave page",
    "wizard.leave.stay": "Stay here",

    "wizard.catalog.loading": "Loading what you can set up…",
    "wizard.catalog.errorTitle":
      "The list of what you can set up could not be loaded",
    "wizard.catalog.errorBody":
      "Your Homelab needs this list to know what it can install, so you can't continue without it. Check your connection and reload.",
    "wizard.catalog.reload": "Reload",
    "wizard.catalog.blockedLoading": "Wait until the list has loaded.",
    "wizard.catalog.blockedError":
      "Reload the list of what you can set up to continue.",

    "wizard.error.technicalDetails": "Technical details",
    "wizard.error.input.title": "Check your choices",
    "wizard.error.network.body":
      "Check your internet connection and try again. Your answers are still here.",
    "wizard.error.blocked.title":
      "Your choices are saved but can't be applied yet",
    "wizard.error.blocked.body":
      "Nothing was set up yet. Go back and change the choices that don't work, or try again later.",
    "wizard.error.validation.title": "Some choices can't be used together yet",
    "wizard.error.validation.body":
      "Go back through the steps, adjust your choices and try again.",

    "creation.header.running": "Your Homelab is being set up",
    "creation.technical.summary": "Technical details",
    "creation.technical.handoffMissing": "The runtime response did not include",
    "creation.complete.server": "Server",
    "creation.next.title": "Next step",
    "creation.next.connectHeadline": "Connect your server",
    "creation.next.connectBody":
      "Run this command on your server. Your Homelab continues as soon as the server checks in.",
    "creation.next.openHeadline": "Your Homelab is ready",
    "creation.next.openBody": "Open it to see your servers and services.",
    "creation.next.reviewHeadline": "Review your Homelab and create it",
    "creation.next.reviewBody":
      "Look over what was prepared, then start the setup on your server.",
    "creation.next.review": "Review and create",
    "creation.next.dashboard": "Go to your Homelab",
    "creation.install.command": "Install command",
    "creation.install.copyFor": "Copy command: {description}",
    "creation.failure.tryAgain": "Try again in a few minutes",
    "creation.failure.contactSupport":
      "If it keeps happening, contact support and share the technical details",
  },
  de: {
    "creation.embed.continueFull": "In Techstack weitermachen",
    "placement.preferred.missing":
      "Der Server, von dem du gestartet bist, ist nicht mehr verfügbar. Wähle ein anderes Ziel.",
    "placement.preferred.incompatible":
      "{name} läuft mit einem StackKit, das diese Auswahl gerade nicht aufnehmen kann. Ein anderes Ziel ist vorausgewählt.",
    "placement.preferred.notReady":
      "{name} kann gerade keinen Use Case aufnehmen. Prüfe die Verbindung oder wähle ein anderes Ziel.",
    "wizard.summary.goals.one":
      "{count} Use Case",
    "wizard.summary.goals.other":
      "{count} Use Cases",
    "wizard.summary.server.installCommand":
      "Installationsbefehl",
    "wizard.summary.server.connectRemote":
      "Verbindung per SSH",
    "wizard.summary.server.managed":
      "Verwalteter Server",
    "wizard.summary.server.hypervisor":
      "Proxmox-Gast",
    "wizard.summary.users.solo":
      "Nur du",
    "wizard.summary.users.shared":
      "Gemeinsamer Haushalt",
    "wizard.summary.users.people.one":
      "{count} Person",
    "wizard.summary.users.people.other":
      "{count} Personen",
    "placement.chooseTitle": "Wo soll dein nächster Anwendungsfall laufen?",
    "placement.chooseBody":
      "Nutze eines deiner StackKits oder einen verbundenen Server, oder richte einen neuen Server ein. Die Anwendungsfälle wählst du im nächsten Schritt.",
    "placement.chooseNext": "Die Anwendungsfälle wählst du im nächsten Schritt",
    "placement.leased":
      "Dieser verwaltete Server muss seine Anbieterbindung behalten. Öffne sein vorhandenes Deployment.",
    "placement.restricted":
      "Änderungen sind auf diesem Server nicht verfügbar. Prüfe Zugriff und Bereitschaft.",
    "placement.title": "Wo soll dieser Anwendungsfall laufen?",
    "placement.body":
      "Nutze ein vorhandenes StackKit mit seinen Servern oder richte einen Server ein. Deine Auswahl bleibt erhalten.",
    "placement.loading": "Vorhandene Ziele werden geprüft…",
    "placement.failed":
      "Deine Ziele konnten nicht geprüft werden. Versuche es erneut oder wähle einen neuen Server.",
    "placement.change": "Ziel ändern",
    "placement.unknownNodes": "Serverdetails nicht verfügbar",
    "placement.compatible": "Passt nachweislich auf diese Server",
    "placement.unavailable":
      "Dieses Ziel kann noch nicht empfohlen werden. Prüfe Einrichtung und Kapazität.",
    "placement.new": "Neuen Server einrichten",
    "placement.newBody":
      "Behalte Anwendungsfall und Antworten und wähle, woher der neue Server kommt.",
    "placement.continue": "Mit diesem Ziel fortfahren",
    "placement.install": "StackKit auf {name} installieren",
    "placement.installBody":
      "Dieser verbundene Server hat noch kein StackKit. Prüfe die Einrichtung vor der Installation.",
    "placement.disconnected":
      "Dieser Server muss sich vor der StackKit-Installation wieder verbinden.",

    "common.retry": "Erneut versuchen",
    "ui.recreate.recreating": "Wird neu erstellt…",
    "ui.theme.toLight": "Zum hellen Design wechseln",
    "ui.theme.toDark": "Zum dunklen Design wechseln",

    "wizard.create.submit": "Homelab erstellen",
    "wizard.create.submitting": "Homelab wird erstellt…",

    "wizard.stepper.label": "Einrichtungsschritte",
    "wizard.stepper.completed": "abgeschlossen",
    "wizard.stepper.position": "Schritt {current} von {total}: {step}",
    "wizard.stepper.goTo": "Zu {step} wechseln",
    "wizard.stepper.locked": "Schließe zuerst die vorherigen Schritte ab",

    "wizard.validation.serverSelection":
      "Wähle, woher dein Server kommt, um weiterzumachen.",
    "wizard.validation.hypervisorGuest":
      "Vervollständige die Angaben zum neuen Server (Speicher, Netzwerk und Größe), um weiterzumachen.",
    "wizard.validation.publicUsers":
      "Öffentlicher Zugriff lässt sich hier noch nicht einrichten. Schalte ihn aus, um weiterzumachen.",
    "wizard.validation.stepIncomplete":
      "Schließe diesen Schritt ab, um weiterzumachen.",

    "wizard.draft.restored":
      "Wir haben deine bisherigen Antworten wiederhergestellt. Passwörter und andere Zugangsdaten werden nie gespeichert – gib sie bitte erneut ein.",
    "wizard.leave.title": "Seite verlassen?",
    "wizard.leave.confirmAction": "Seite verlassen",
    "wizard.leave.stay": "Hier bleiben",

    "wizard.catalog.loading": "Wird geladen, was du einrichten kannst…",
    "wizard.catalog.errorTitle":
      "Die Liste dessen, was du einrichten kannst, konnte nicht geladen werden",
    "wizard.catalog.errorBody":
      "Dein Homelab braucht diese Liste, um zu wissen, was installiert werden kann – ohne sie kannst du nicht weitermachen. Prüfe deine Verbindung und lade erneut.",
    "wizard.catalog.reload": "Erneut laden",
    "wizard.catalog.blockedLoading": "Warte, bis die Liste geladen ist.",
    "wizard.catalog.blockedError":
      "Lade die Liste dessen, was du einrichten kannst, neu, um weiterzumachen.",

    "wizard.error.technicalDetails": "Technische Details",
    "wizard.error.input.title": "Prüfe deine Auswahl",
    "wizard.error.network.body":
      "Prüfe deine Internetverbindung und versuche es erneut. Deine Antworten sind noch da.",
    "wizard.error.blocked.title":
      "Deine Auswahl ist gespeichert, kann aber noch nicht umgesetzt werden",
    "wizard.error.blocked.body":
      "Es wurde noch nichts eingerichtet. Gehe zurück und ändere die Auswahl, die nicht funktioniert, oder versuche es später erneut.",
    "wizard.error.validation.title":
      "Einige Auswahlen lassen sich noch nicht kombinieren",
    "wizard.error.validation.body":
      "Gehe die Schritte noch einmal durch, passe deine Auswahl an und versuche es erneut.",

    "creation.header.running": "Dein Homelab wird eingerichtet",
    "creation.technical.summary": "Technische Details",
    "creation.technical.handoffMissing": "Die Runtime-Antwort enthielt kein",
    "creation.complete.server": "Server",
    "creation.next.title": "Nächster Schritt",
    "creation.next.connectHeadline": "Verbinde deinen Server",
    "creation.next.connectBody":
      "Führe diesen Befehl auf deinem Server aus. Dein Homelab macht weiter, sobald sich der Server meldet.",
    "creation.next.openHeadline": "Dein Homelab ist bereit",
    "creation.next.openBody": "Öffne es, um deine Server und Dienste zu sehen.",
    "creation.next.reviewHeadline": "Prüfe dein Homelab und erstelle es",
    "creation.next.reviewBody":
      "Sieh dir an, was vorbereitet wurde, und starte dann die Einrichtung auf deinem Server.",
    "creation.next.review": "Prüfen und erstellen",
    "creation.next.dashboard": "Zu deinem Homelab",
    "creation.install.command": "Installationsbefehl",
    "creation.install.copyFor": "Befehl kopieren: {description}",
    "creation.failure.tryAgain": "Versuche es in ein paar Minuten erneut",
    "creation.failure.contactSupport":
      "Falls es wieder passiert, wende dich an den Support und nenne die technischen Details",
  },
  es: {
    "creation.embed.continueFull": "Continuar en Techstack",
    "placement.preferred.missing":
      "El servidor desde el que empezaste ya no está disponible. Elige otro destino.",
    "placement.preferred.incompatible":
      "{name} ejecuta un StackKit que ahora no puede aceptar esta selección. Hay otro destino preseleccionado.",
    "placement.preferred.notReady":
      "{name} no puede aceptar un caso de uso ahora. Revisa su conexión o elige otro destino.",
    "wizard.summary.goals.one":
      "{count} caso de uso",
    "wizard.summary.goals.other":
      "{count} casos de uso",
    "wizard.summary.server.installCommand":
      "Comando de instalación",
    "wizard.summary.server.connectRemote":
      "Conexión por SSH",
    "wizard.summary.server.managed":
      "Servidor gestionado",
    "wizard.summary.server.hypervisor":
      "Invitado de Proxmox",
    "wizard.summary.users.solo":
      "Solo tú",
    "wizard.summary.users.shared":
      "Hogar compartido",
    "wizard.summary.users.people.one":
      "{count} persona",
    "wizard.summary.users.people.other":
      "{count} personas",
    "placement.chooseTitle": "¿Dónde debe ejecutarse tu próximo caso de uso?",
    "placement.chooseBody":
      "Usa uno de tus StackKits o servidores registrados, o configura un servidor nuevo. Después eliges los casos de uso.",
    "placement.chooseNext": "Elige sus casos de uso en el siguiente paso",
    "placement.leased":
      "Este servidor gestionado debe conservar su vínculo con el proveedor. Abre su despliegue existente.",
    "placement.restricted":
      "Los cambios en este servidor no están disponibles. Revisa su acceso y disponibilidad.",
    "placement.title": "¿Dónde debe ejecutarse este caso de uso?",
    "placement.body":
      "Usa un StackKit existente y sus servidores o configura un servidor. Tus elecciones se conservan.",
    "placement.loading": "Comprobando tus destinos existentes…",
    "placement.failed":
      "No se pudieron comprobar los destinos. Inténtalo de nuevo o elige un servidor nuevo.",
    "placement.change": "Cambiar destino",
    "placement.unknownNodes": "Detalles del servidor no disponibles",
    "placement.compatible": "Compatibilidad verificada con estos servidores",
    "placement.unavailable":
      "Este destino aún no se puede recomendar. Revisa su configuración y capacidad.",
    "placement.new": "Configurar un servidor nuevo",
    "placement.newBody":
      "Conserva tu caso de uso y tus respuestas y elige de dónde viene el nuevo servidor.",
    "placement.continue": "Continuar con este destino",
    "placement.install": "Instalar un StackKit en {name}",
    "placement.installBody":
      "Este servidor registrado aún no tiene StackKit. Revisa la configuración antes de instalarlo.",
    "placement.disconnected":
      "Este servidor debe volver a conectarse antes de instalar un StackKit.",

    "common.retry": "Reintentar",
    "ui.recreate.recreating": "Recreando…",
    "ui.theme.toLight": "Cambiar al tema claro",
    "ui.theme.toDark": "Cambiar al tema oscuro",

    "wizard.create.submit": "Crear Homelab",
    "wizard.create.submitting": "Creando Homelab…",

    "wizard.stepper.label": "Pasos de la configuración",
    "wizard.stepper.completed": "completado",
    "wizard.stepper.position": "Paso {current} de {total}: {step}",
    "wizard.stepper.goTo": "Ir a {step}",
    "wizard.stepper.locked": "Completa primero los pasos anteriores",

    "wizard.validation.serverSelection":
      "Elige de dónde viene tu servidor para continuar.",
    "wizard.validation.hypervisorGuest":
      "Completa los datos del nuevo servidor (almacenamiento, red y tamaño) para continuar.",
    "wizard.validation.publicUsers":
      "El acceso público aún no se puede configurar aquí. Desactívalo para continuar.",
    "wizard.validation.stepIncomplete": "Completa este paso para continuar.",

    "wizard.draft.restored":
      "Hemos restaurado tus respuestas anteriores. Las contraseñas y otros secretos nunca se guardan, así que vuelve a introducirlos.",
    "wizard.leave.title": "¿Salir de esta página?",
    "wizard.leave.confirmAction": "Salir de la página",
    "wizard.leave.stay": "Quedarme aquí",

    "wizard.catalog.loading": "Cargando lo que puedes configurar…",
    "wizard.catalog.errorTitle":
      "No se pudo cargar la lista de lo que puedes configurar",
    "wizard.catalog.errorBody":
      "Tu Homelab necesita esta lista para saber qué puede instalar, así que no puedes continuar sin ella. Comprueba tu conexión y vuelve a cargar.",
    "wizard.catalog.reload": "Volver a cargar",
    "wizard.catalog.blockedLoading": "Espera a que se cargue la lista.",
    "wizard.catalog.blockedError":
      "Vuelve a cargar la lista de lo que puedes configurar para continuar.",

    "wizard.error.technicalDetails": "Detalles técnicos",
    "wizard.error.input.title": "Revisa tus opciones",
    "wizard.error.network.body":
      "Comprueba tu conexión a internet e inténtalo de nuevo. Tus respuestas siguen aquí.",
    "wizard.error.blocked.title":
      "Tus opciones están guardadas, pero aún no se pueden aplicar",
    "wizard.error.blocked.body":
      "Todavía no se ha configurado nada. Vuelve atrás y cambia las opciones que no funcionan, o inténtalo más tarde.",
    "wizard.error.validation.title":
      "Algunas opciones aún no se pueden combinar",
    "wizard.error.validation.body":
      "Repasa los pasos, ajusta tus opciones e inténtalo de nuevo.",

    "creation.header.running": "Tu Homelab se está configurando",
    "creation.technical.summary": "Detalles técnicos",
    "creation.technical.handoffMissing": "La respuesta del runtime no incluía",
    "creation.complete.server": "Servidor",
    "creation.next.title": "Siguiente paso",
    "creation.next.connectHeadline": "Conecta tu servidor",
    "creation.next.connectBody":
      "Ejecuta este comando en tu servidor. Tu Homelab continúa en cuanto el servidor se comunica.",
    "creation.next.openHeadline": "Tu Homelab está listo",
    "creation.next.openBody": "Ábrelo para ver tus servidores y servicios.",
    "creation.next.reviewHeadline": "Revisa tu Homelab y créalo",
    "creation.next.reviewBody":
      "Revisa lo que se ha preparado y luego inicia la configuración en tu servidor.",
    "creation.next.review": "Revisar y crear",
    "creation.next.dashboard": "Ir a tu Homelab",
    "creation.install.command": "Comando de instalación",
    "creation.install.copyFor": "Copiar comando: {description}",
    "creation.failure.tryAgain": "Inténtalo de nuevo en unos minutos",
    "creation.failure.contactSupport":
      "Si sigue ocurriendo, ponte en contacto con el soporte y comparte los detalles técnicos",
  },
  "zh-Hans": {
    "creation.embed.continueFull": "在 Techstack 中继续",
    "placement.preferred.missing":
      "你开始时使用的服务器已不可用。请选择其他目标。",
    "placement.preferred.incompatible":
      "{name} 运行的 StackKit 目前无法容纳此选择。已预选其他目标。",
    "placement.preferred.notReady":
      "{name} 目前无法接收用例。请检查其连接或选择其他目标。",
    "wizard.summary.goals.one":
      "{count} 个用例",
    "wizard.summary.goals.other":
      "{count} 个用例",
    "wizard.summary.server.installCommand":
      "安装命令",
    "wizard.summary.server.connectRemote":
      "通过 SSH 连接",
    "wizard.summary.server.managed":
      "托管服务器",
    "wizard.summary.server.hypervisor":
      "Proxmox 虚拟机",
    "wizard.summary.users.solo":
      "仅你自己",
    "wizard.summary.users.shared":
      "共享家庭",
    "wizard.summary.users.people.one":
      "{count} 人",
    "wizard.summary.users.people.other":
      "{count} 人",
    "placement.chooseTitle": "你的下一个使用场景应在哪里运行？",
    "placement.chooseBody":
      "使用你现有的 StackKit 或已注册的服务器，或设置一台新服务器。接下来再选择使用场景。",
    "placement.chooseNext": "在下一步中选择它的使用场景",
    "placement.leased":
      "此托管服务器必须保留当前的提供商绑定。请打开其现有部署。",
    "placement.restricted": "此服务器暂不允许更改。请检查访问权限和就绪状态。",
    "placement.title": "这个使用场景应在哪里运行？",
    "placement.body":
      "使用现有 StackKit 及其服务器，或设置服务器。你的选择会保留。",
    "placement.loading": "正在检查现有目标…",
    "placement.failed": "无法检查目标。请重试或选择新服务器。",
    "placement.change": "更改目标",
    "placement.unknownNodes": "服务器详情不可用",
    "placement.compatible": "已验证适合这些服务器",
    "placement.unavailable": "暂时无法推荐此目标。请检查其设置和容量。",
    "placement.new": "设置新服务器",
    "placement.newBody": "保留使用场景和回答，并选择新服务器的来源。",
    "placement.continue": "使用此目标继续",
    "placement.install": "在 {name} 上安装 StackKit",
    "placement.installBody":
      "此已注册服务器尚无 StackKit。请在安装前检查设置。",
    "placement.disconnected": "此服务器必须重新连接后才能安装 StackKit。",

    "common.retry": "重试",
    "ui.recreate.recreating": "正在重新创建…",
    "ui.theme.toLight": "切换到浅色主题",
    "ui.theme.toDark": "切换到深色主题",

    "wizard.create.submit": "创建 Homelab",
    "wizard.create.submitting": "正在创建 Homelab…",

    "wizard.stepper.label": "设置步骤",
    "wizard.stepper.completed": "已完成",
    "wizard.stepper.position": "第 {current} 步，共 {total} 步：{step}",
    "wizard.stepper.goTo": "转到{step}",
    "wizard.stepper.locked": "请先完成前面的步骤",

    "wizard.validation.serverSelection": "请选择你的服务器来源以继续。",
    "wizard.validation.hypervisorGuest":
      "请填写新服务器的详细信息（存储、网络和大小）以继续。",
    "wizard.validation.publicUsers":
      "此处暂时无法设置公开访问。请关闭它以继续。",
    "wizard.validation.stepIncomplete": "请完成此步骤以继续。",

    "wizard.draft.restored":
      "我们已恢复你之前的答案。密码和其他机密信息不会被保存，请重新输入。",
    "wizard.leave.title": "要离开此页面吗？",
    "wizard.leave.confirmAction": "离开页面",
    "wizard.leave.stay": "留在此页",

    "wizard.catalog.loading": "正在加载可设置的内容…",
    "wizard.catalog.errorTitle": "无法加载可设置内容的列表",
    "wizard.catalog.errorBody":
      "你的 Homelab 需要这份列表才能知道可以安装什么，因此没有它就无法继续。请检查网络连接后重新加载。",
    "wizard.catalog.reload": "重新加载",
    "wizard.catalog.blockedLoading": "请等待列表加载完成。",
    "wizard.catalog.blockedError": "请重新加载可设置内容的列表以继续。",

    "wizard.error.technicalDetails": "技术详情",
    "wizard.error.input.title": "请检查你的选择",
    "wizard.error.network.body": "请检查你的网络连接后重试。你的答案仍然保留。",
    "wizard.error.blocked.title": "你的选择已保存，但暂时无法应用",
    "wizard.error.blocked.body":
      "尚未设置任何内容。请返回修改不适用的选择，或稍后再试。",
    "wizard.error.validation.title": "有些选择暂时无法一起使用",
    "wizard.error.validation.body": "请回到各个步骤，调整你的选择后再试。",

    "creation.header.running": "你的 Homelab 正在设置中",
    "creation.technical.summary": "技术详情",
    "creation.technical.handoffMissing": "运行时响应中不包含",
    "creation.complete.server": "服务器",
    "creation.next.title": "下一步",
    "creation.next.connectHeadline": "连接你的服务器",
    "creation.next.connectBody":
      "在你的服务器上运行此命令。服务器报到后，你的 Homelab 会继续进行。",
    "creation.next.openHeadline": "你的 Homelab 已就绪",
    "creation.next.openBody": "打开它即可查看你的服务器和服务。",
    "creation.next.reviewHeadline": "查看你的 Homelab 并创建",
    "creation.next.reviewBody":
      "查看已准备好的内容，然后在你的服务器上开始设置。",
    "creation.next.review": "查看并创建",
    "creation.next.dashboard": "前往你的 Homelab",
    "creation.install.command": "安装命令",
    "creation.install.copyFor": "复制命令：{description}",
    "creation.failure.tryAgain": "几分钟后重试",
    "creation.failure.contactSupport": "如果问题持续，请联系支持并提供技术详情",
  },
  hi: {
    "creation.embed.continueFull": "Techstack में जारी रखें",
    "placement.preferred.missing":
      "जिस सर्वर से आपने शुरू किया था वह अब उपलब्ध नहीं है। कोई दूसरा लक्ष्य चुनें।",
    "placement.preferred.incompatible":
      "{name} पर चल रहा StackKit अभी यह चयन नहीं ले सकता। दूसरा लक्ष्य पहले से चुना गया है।",
    "placement.preferred.notReady":
      "{name} अभी कोई उपयोग मामला नहीं ले सकता। उसका कनेक्शन जाँचें या दूसरा लक्ष्य चुनें।",
    "wizard.summary.goals.one":
      "{count} उपयोग मामला",
    "wizard.summary.goals.other":
      "{count} उपयोग मामले",
    "wizard.summary.server.installCommand":
      "इंस्टॉल कमांड",
    "wizard.summary.server.connectRemote":
      "SSH से कनेक्ट",
    "wizard.summary.server.managed":
      "प्रबंधित सर्वर",
    "wizard.summary.server.hypervisor":
      "Proxmox गेस्ट",
    "wizard.summary.users.solo":
      "केवल आप",
    "wizard.summary.users.shared":
      "साझा घर",
    "wizard.summary.users.people.one":
      "{count} व्यक्ति",
    "wizard.summary.users.people.other":
      "{count} लोग",
    "placement.chooseTitle": "आपका अगला यूज़ केस कहाँ चलना चाहिए?",
    "placement.chooseBody":
      "अपने किसी StackKit या पंजीकृत सर्वर का उपयोग करें, या नया सर्वर सेट करें। यूज़ केस आप अगले चरण में चुनेंगे।",
    "placement.chooseNext": "इसके यूज़ केस अगले चरण में चुनें",
    "placement.leased":
      "इस प्रबंधित सर्वर को अपना मौजूदा प्रदाता संबंध बनाए रखना होगा। इसका मौजूदा डिप्लॉयमेंट खोलें।",
    "placement.restricted":
      "इस सर्वर पर बदलाव उपलब्ध नहीं हैं। इसकी पहुँच और तैयारी जाँचें।",
    "placement.title": "यह उपयोग का मामला कहाँ चले?",
    "placement.body":
      "मौजूदा StackKit और उसके सर्वर इस्तेमाल करें या सर्वर सेट करें। आपके विकल्प सुरक्षित रहेंगे।",
    "placement.loading": "आपके मौजूदा लक्ष्य जाँचे जा रहे हैं…",
    "placement.failed":
      "लक्ष्य जाँचे नहीं जा सके। फिर प्रयास करें या नया सर्वर चुनें।",
    "placement.change": "लक्ष्य बदलें",
    "placement.unknownNodes": "सर्वर की जानकारी उपलब्ध नहीं है",
    "placement.compatible": "इन सर्वर पर चलने की पुष्टि हो गई है",
    "placement.unavailable":
      "अभी इस लक्ष्य की अनुशंसा नहीं की जा सकती। इसका सेटअप और क्षमता जाँचें।",
    "placement.new": "नया सर्वर सेट करें",
    "placement.newBody":
      "अपने उपयोग के मामले और उत्तर सुरक्षित रखें और नए सर्वर का स्रोत चुनें।",
    "placement.continue": "इस लक्ष्य के साथ आगे बढ़ें",
    "placement.install": "{name} पर StackKit इंस्टॉल करें",
    "placement.installBody":
      "इस पंजीकृत सर्वर पर अभी StackKit नहीं है। इंस्टॉल करने से पहले सेटअप की समीक्षा करें।",
    "placement.disconnected":
      "StackKit इंस्टॉल करने से पहले इस सर्वर को दोबारा कनेक्ट होना होगा।",

    "common.retry": "फिर से कोशिश करें",
    "ui.recreate.recreating": "दोबारा बनाया जा रहा है…",
    "ui.theme.toLight": "लाइट थीम पर स्विच करें",
    "ui.theme.toDark": "डार्क थीम पर स्विच करें",

    "wizard.create.submit": "Homelab बनाएँ",
    "wizard.create.submitting": "Homelab बनाया जा रहा है…",

    "wizard.stepper.label": "सेटअप के चरण",
    "wizard.stepper.completed": "पूरा हुआ",
    "wizard.stepper.position": "चरण {current}/{total}: {step}",
    "wizard.stepper.goTo": "{step} पर जाएँ",
    "wizard.stepper.locked": "पहले पिछले चरण पूरे करें",

    "wizard.validation.serverSelection":
      "आगे बढ़ने के लिए चुनें कि आपका सर्वर कहाँ से आएगा।",
    "wizard.validation.hypervisorGuest":
      "आगे बढ़ने के लिए नए सर्वर का विवरण (स्टोरेज, नेटवर्क और आकार) पूरा करें।",
    "wizard.validation.publicUsers":
      "सार्वजनिक पहुँच अभी यहाँ सेट नहीं की जा सकती। आगे बढ़ने के लिए इसे बंद करें।",
    "wizard.validation.stepIncomplete": "आगे बढ़ने के लिए यह चरण पूरा करें।",

    "wizard.draft.restored":
      "हमने आपके पहले के जवाब वापस ला दिए हैं। पासवर्ड और अन्य गोपनीय जानकारी कभी सहेजी नहीं जाती, इसलिए उन्हें फिर से दर्ज करें।",
    "wizard.leave.title": "इस पेज को छोड़ना है?",
    "wizard.leave.confirmAction": "पेज छोड़ें",
    "wizard.leave.stay": "यहीं रहें",

    "wizard.catalog.loading": "यह लोड हो रहा है कि आप क्या सेट कर सकते हैं…",
    "wizard.catalog.errorTitle":
      "आप क्या सेट कर सकते हैं उसकी सूची लोड नहीं हो सकी",
    "wizard.catalog.errorBody":
      "आपके Homelab को यह सूची चाहिए ताकि पता चले कि क्या इंस्टॉल हो सकता है, इसलिए इसके बिना आप आगे नहीं बढ़ सकते। अपना कनेक्शन जाँचें और फिर से लोड करें।",
    "wizard.catalog.reload": "फिर से लोड करें",
    "wizard.catalog.blockedLoading": "सूची लोड होने तक प्रतीक्षा करें।",
    "wizard.catalog.blockedError": "आगे बढ़ने के लिए सूची फिर से लोड करें।",

    "wizard.error.technicalDetails": "तकनीकी विवरण",
    "wizard.error.input.title": "अपने विकल्प जाँचें",
    "wizard.error.network.body":
      "अपना इंटरनेट कनेक्शन जाँचें और फिर कोशिश करें। आपके जवाब अभी भी यहीं हैं।",
    "wizard.error.blocked.title":
      "आपके विकल्प सहेज लिए गए हैं, पर अभी लागू नहीं हो सकते",
    "wizard.error.blocked.body":
      "अभी कुछ भी सेट नहीं हुआ है। वापस जाकर जो विकल्प काम नहीं करते उन्हें बदलें, या बाद में फिर कोशिश करें।",
    "wizard.error.validation.title":
      "कुछ विकल्प अभी साथ में इस्तेमाल नहीं हो सकते",
    "wizard.error.validation.body":
      "चरणों में वापस जाएँ, अपने विकल्प बदलें और फिर कोशिश करें।",

    "creation.header.running": "आपका Homelab सेट हो रहा है",
    "creation.technical.summary": "तकनीकी विवरण",
    "creation.technical.handoffMissing": "रनटाइम प्रतिक्रिया में शामिल नहीं था",
    "creation.complete.server": "सर्वर",
    "creation.next.title": "अगला चरण",
    "creation.next.connectHeadline": "अपना सर्वर कनेक्ट करें",
    "creation.next.connectBody":
      "यह कमांड अपने सर्वर पर चलाएँ। सर्वर के संपर्क करते ही आपका Homelab आगे बढ़ेगा।",
    "creation.next.openHeadline": "आपका Homelab तैयार है",
    "creation.next.openBody": "अपने सर्वर और सेवाएँ देखने के लिए इसे खोलें।",
    "creation.next.reviewHeadline": "अपना Homelab देखें और बनाएँ",
    "creation.next.reviewBody":
      "जो तैयार किया गया है उसे देखें, फिर अपने सर्वर पर सेटअप शुरू करें।",
    "creation.next.review": "समीक्षा करें और बनाएँ",
    "creation.next.dashboard": "अपने Homelab पर जाएँ",
    "creation.install.command": "इंस्टॉल कमांड",
    "creation.install.copyFor": "कमांड कॉपी करें: {description}",
    "creation.failure.tryAgain": "कुछ मिनट बाद फिर कोशिश करें",
    "creation.failure.contactSupport":
      "अगर यह बार-बार हो, तो सहायता से संपर्क करें और तकनीकी विवरण साझा करें",
  },
  ar: {
    "creation.embed.continueFull": "المتابعة في Techstack",
    "placement.preferred.missing":
      "لم يعد الخادم الذي بدأت منه متاحًا. اختر وجهة أخرى.",
    "placement.preferred.incompatible":
      "يشغّل {name} حزمة StackKit لا يمكنها استيعاب هذا الاختيار الآن. تم تحديد وجهة أخرى مسبقًا.",
    "placement.preferred.notReady":
      "لا يمكن لـ {name} استقبال حالة استخدام الآن. تحقق من اتصاله أو اختر وجهة أخرى.",
    "wizard.summary.goals.one":
      "حالة استخدام واحدة ({count})",
    "wizard.summary.goals.other":
      "{count} حالات استخدام",
    "wizard.summary.server.installCommand":
      "أمر التثبيت",
    "wizard.summary.server.connectRemote":
      "الاتصال عبر SSH",
    "wizard.summary.server.managed":
      "خادم مُدار",
    "wizard.summary.server.hypervisor":
      "ضيف Proxmox",
    "wizard.summary.users.solo":
      "أنت فقط",
    "wizard.summary.users.shared":
      "منزل مشترك",
    "wizard.summary.users.people.one":
      "شخص واحد ({count})",
    "wizard.summary.users.people.other":
      "{count} أشخاص",
    "placement.chooseTitle": "أين يجب أن تعمل حالة الاستخدام التالية؟",
    "placement.chooseBody":
      "استخدم إحدى حزم StackKit أو أحد الخوادم المسجلة لديك، أو أعدّ خادمًا جديدًا. تختار حالات الاستخدام في الخطوة التالية.",
    "placement.chooseNext": "اختر حالات الاستخدام الخاصة به في الخطوة التالية",
    "placement.leased":
      "يجب أن يحتفظ هذا الخادم المُدار بارتباطه الحالي بالمزوّد. افتح نشره الموجود.",
    "placement.restricted":
      "التغييرات على هذا الخادم غير متاحة. راجع الوصول وجاهزيته.",
    "placement.title": "أين تريد تشغيل حالة الاستخدام هذه؟",
    "placement.body":
      "استخدم StackKit موجودًا وخوادمه أو أعدّ خادمًا. ستبقى اختياراتك محفوظة.",
    "placement.loading": "جارٍ فحص الوجهات الموجودة…",
    "placement.failed": "تعذّر فحص الوجهات. حاول مجددًا أو اختر خادمًا جديدًا.",
    "placement.change": "تغيير الوجهة",
    "placement.unknownNodes": "تفاصيل الخادم غير متاحة",
    "placement.compatible": "تم التحقق من ملاءمته لهذه الخوادم",
    "placement.unavailable":
      "لا يمكن التوصية بهذه الوجهة بعد. راجع إعدادها وسعتها.",
    "placement.new": "إعداد خادم جديد",
    "placement.newBody":
      "احتفظ بحالة الاستخدام وإجاباتك واختر مصدر الخادم الجديد.",
    "placement.continue": "المتابعة بهذه الوجهة",
    "placement.install": "تثبيت StackKit على {name}",
    "placement.installBody":
      "لا يحتوي هذا الخادم المسجل على StackKit بعد. راجع الإعداد قبل التثبيت.",
    "placement.disconnected": "يجب إعادة اتصال هذا الخادم قبل تثبيت StackKit.",

    "common.retry": "إعادة المحاولة",
    "ui.recreate.recreating": "جارٍ إعادة الإنشاء…",
    "ui.theme.toLight": "التبديل إلى السمة الفاتحة",
    "ui.theme.toDark": "التبديل إلى السمة الداكنة",

    "wizard.create.submit": "إنشاء Homelab",
    "wizard.create.submitting": "جارٍ إنشاء Homelab…",

    "wizard.stepper.label": "خطوات الإعداد",
    "wizard.stepper.completed": "مكتملة",
    "wizard.stepper.position": "الخطوة {current} من {total}: {step}",
    "wizard.stepper.goTo": "الانتقال إلى {step}",
    "wizard.stepper.locked": "أكمل الخطوات السابقة أولاً",

    "wizard.validation.serverSelection": "اختر مصدر خادمك للمتابعة.",
    "wizard.validation.hypervisorGuest":
      "أكمل تفاصيل الخادم الجديد (التخزين والشبكة والحجم) للمتابعة.",
    "wizard.validation.publicUsers":
      "لا يمكن إعداد الوصول العام هنا بعد. أوقفه للمتابعة.",
    "wizard.validation.stepIncomplete": "أكمل هذه الخطوة للمتابعة.",

    "wizard.draft.restored":
      "استعدنا إجاباتك السابقة. لا تُحفظ كلمات المرور والأسرار الأخرى أبدًا، لذا أدخلها مرة أخرى.",
    "wizard.leave.title": "هل تريد مغادرة هذه الصفحة؟",
    "wizard.leave.confirmAction": "مغادرة الصفحة",
    "wizard.leave.stay": "البقاء هنا",

    "wizard.catalog.loading": "جارٍ تحميل ما يمكنك إعداده…",
    "wizard.catalog.errorTitle": "تعذّر تحميل قائمة ما يمكنك إعداده",
    "wizard.catalog.errorBody":
      "يحتاج Homelab الخاص بك إلى هذه القائمة ليعرف ما يمكنه تثبيته، لذا لا يمكنك المتابعة بدونها. تحقق من اتصالك وأعد التحميل.",
    "wizard.catalog.reload": "إعادة التحميل",
    "wizard.catalog.blockedLoading": "انتظر حتى يتم تحميل القائمة.",
    "wizard.catalog.blockedError": "أعد تحميل قائمة ما يمكنك إعداده للمتابعة.",

    "wizard.error.technicalDetails": "التفاصيل التقنية",
    "wizard.error.input.title": "راجع اختياراتك",
    "wizard.error.network.body":
      "تحقق من اتصالك بالإنترنت وحاول مرة أخرى. ما زالت إجاباتك هنا.",
    "wizard.error.blocked.title": "تم حفظ اختياراتك لكن لا يمكن تطبيقها بعد",
    "wizard.error.blocked.body":
      "لم يتم إعداد أي شيء بعد. ارجع وغيّر الاختيارات التي لا تعمل، أو حاول لاحقًا.",
    "wizard.error.validation.title": "لا يمكن استخدام بعض الاختيارات معًا بعد",
    "wizard.error.validation.body":
      "ارجع عبر الخطوات، وعدّل اختياراتك، ثم حاول مرة أخرى.",

    "creation.header.running": "جارٍ إعداد Homelab الخاص بك",
    "creation.technical.summary": "التفاصيل التقنية",
    "creation.technical.handoffMissing": "لم تتضمن استجابة وقت التشغيل",
    "creation.complete.server": "الخادم",
    "creation.next.title": "الخطوة التالية",
    "creation.next.connectHeadline": "اربط خادمك",
    "creation.next.connectBody":
      "شغّل هذا الأمر على خادمك. يتابع Homelab الخاص بك فور اتصال الخادم.",
    "creation.next.openHeadline": "Homelab الخاص بك جاهز",
    "creation.next.openBody": "افتحه لرؤية خوادمك وخدماتك.",
    "creation.next.reviewHeadline": "راجع Homelab الخاص بك وأنشئه",
    "creation.next.reviewBody": "راجع ما تم تجهيزه، ثم ابدأ الإعداد على خادمك.",
    "creation.next.review": "مراجعة وإنشاء",
    "creation.next.dashboard": "انتقل إلى Homelab الخاص بك",
    "creation.install.command": "أمر التثبيت",
    "creation.install.copyFor": "نسخ الأمر: {description}",
    "creation.failure.tryAgain": "حاول مرة أخرى بعد بضع دقائق",
    "creation.failure.contactSupport":
      "إذا استمر ذلك، فتواصل مع الدعم وشارك التفاصيل التقنية",
  },
};
