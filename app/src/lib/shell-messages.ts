// Customer-visible strings of the app shell and of the pages the usability
// review reworked: navigation entries, the shared error callout, keyboard
// shortcut settings, the server list, monitoring alerts, the dashboard load
// error and the server and homelab detail pages. Merged into the main
// dictionary by i18n.ts for every locale.
//
// Voice: German addresses the person with "du", sentence case, the brand is
// written "kombify". Glossary (see the glossary pass): "Server" for machines,
// "Homelab" for the person's setup, "Personen & Agents" for the actors page,
// "Entfernen / Remove" for taking something out of the homelab and
// "Löschen / Delete" only for permanent data deletion.
export const shellMessages = {
  en: {
    "shell.copied": "Copied",
    "shell.copyFailed": "Could not copy",
    "serverPage.primaryServer": "primary server",
    "serverPage.runtimeOnly": "runtime only",
    "settings.remove": "Remove",
    "errorCallout.details": "Technical details",
    "errorCallout.retry": "Try again",
    "errorCallout.retrying": "Trying again…",
    "errorCallout.nextAttempt": "Trying again in {seconds} s",

    "switch.on": "On",
    "switch.off": "Off",

    "shell.skipToContent": "Skip to content",
    "shell.homeLink": "Go to the dashboard",
    "shell.footerLinks": "Legal and company links",

    "nav.servicesPlacement": "Placement",
    "nav.monitoring.now": "Right now",
    "nav.createHomelab": "Create homelab",
    "nav.group.homelab": "Homelab",

    "shortcuts.turnedOff": "Keyboard shortcuts turned off",
    "shortcuts.turnedOffHint": "You can turn them back on in Settings.",
    "shortcuts.turnOff": "Turn off shortcuts",
    "shortcuts.settingsTitle": "Keyboard shortcuts",
    "shortcuts.settingsLead":
      "Single-key shortcuts such as G, then H, jump between pages. Turn them off if they get in the way of typing or a screen reader.",
    "shortcuts.switchLabel": "Single-key shortcuts",
    "shortcuts.switchHint": "Saved on this device.",
    "ui.shortcuts.closeDialog": "Close dialog",
    "ui.shortcutsHelp.then": "then",

    "servers.add": "Add server",

    "monitoring.serverRemoved": "{name} was removed",
    "monitoring.allServers": "All servers",
    "monitoring.alertsHint": "Rules that are firing right now.",
    "monitoring.alertsUnavailable": "The alerts could not be read right now.",
    "monitoring.noAlerts": "No active alerts. Everything is quiet.",
    "monitoring.alertSince": "since {time}",
    "monitoring.openServer": "Manage {name}",

    "dashboard.error.title": "Your homelab could not be loaded",
    "dashboard.error.body":
      "kombify could not reach your homelab just now. This often clears up on its own.",
    "dashboard.apps.rollingOut": "1 being installed",
    "dashboard.apps.running": "{count} running",
    "dashboard.apps.noHealth": "no status reported",
    "dashboard.referenceId": "ID {id}",
    "dashboard.rollout.label": "Setup · {phase}",

    "settings.saved": "Saved",
    "settings.homelabRemoved": "{name} was removed",

    "stackDetail.toast.starting": "Server is starting",
    "stackDetail.toast.stopping": "Server is stopping",
    "stackDetail.toast.sshOn": "SSH access turned on",
    "stackDetail.toast.sshOff": "SSH access turned off",
    "stackDetail.toast.removing": "Server is being removed",
    "stackDetail.ssh.label": "SSH access",
    "stackDetail.ssh.details": "Connection details",
    "stackDetail.remove.title": "Delete this server",
    "stackDetail.remove.body":
      "The managed server is shut down and deleted at the provider, including its data. This cannot be undone.",
    "stackDetail.managedServer": "managed server",
    "stackDetail.loadError": "This setup could not be loaded.",

    "serverPage.tab.overview": "Overview",
    "serverPage.tab.services": "Services",
    "serverPage.tab.ports": "Ports",
    "serverPage.tab.checks": "Checks",
    "serverPage.tab.logs": "Logs",
    "serverPage.tab.settings": "Settings",
    "serverPage.noIp": "no address",
    "serverPage.loadError": "The server details could not be loaded.",
    "serverPage.refreshError":
      "The latest refresh failed. Showing the last known state.",
    "serverPage.historyLink": "History & metrics",
  },
  de: {
    "shell.copied": "Kopiert",
    "shell.copyFailed": "Kopieren nicht möglich",
    "serverPage.primaryServer": "Hauptserver",
    "serverPage.runtimeOnly": "nur zur Laufzeit",
    "settings.remove": "Entfernen",
    "errorCallout.details": "Technische Details",
    "errorCallout.retry": "Erneut versuchen",
    "errorCallout.retrying": "Neuer Versuch läuft …",
    "errorCallout.nextAttempt": "Neuer Versuch in {seconds} s",

    "switch.on": "An",
    "switch.off": "Aus",

    "shell.skipToContent": "Zum Inhalt springen",
    "shell.homeLink": "Zum Dashboard",
    "shell.footerLinks": "Rechtliches und Unternehmen",

    "nav.servicesPlacement": "Platzierung",
    "nav.monitoring.now": "Aktuell",
    "nav.createHomelab": "Homelab erstellen",
    "nav.group.homelab": "Homelab",

    "shortcuts.turnedOff": "Tastenkürzel ausgeschaltet",
    "shortcuts.turnedOffHint":
      "Du kannst sie in den Einstellungen wieder einschalten.",
    "shortcuts.turnOff": "Tastenkürzel ausschalten",
    "shortcuts.settingsTitle": "Tastenkürzel",
    "shortcuts.settingsLead":
      "Einzeltasten wie G, dann H, springen zwischen Seiten. Schalte sie aus, wenn sie beim Tippen oder mit einem Screenreader stören.",
    "shortcuts.switchLabel": "Einzeltasten-Kürzel",
    "shortcuts.switchHint": "Auf diesem Gerät gespeichert.",
    "ui.shortcuts.closeDialog": "Dialog schließen",
    "ui.shortcutsHelp.then": "dann",

    "servers.add": "Server hinzufügen",

    "monitoring.serverRemoved": "„{name}“ wurde entfernt",
    "monitoring.allServers": "Alle Server",
    "monitoring.alertsHint": "Regeln, die gerade ausgelöst sind.",
    "monitoring.alertsUnavailable":
      "Die Warnungen konnten gerade nicht gelesen werden.",
    "monitoring.noAlerts": "Keine aktiven Warnungen. Alles ruhig.",
    "monitoring.alertSince": "seit {time}",
    "monitoring.openServer": "{name} verwalten",

    "dashboard.error.title": "Dein Homelab konnte nicht geladen werden",
    "dashboard.error.body":
      "kombify konnte dein Homelab gerade nicht erreichen. Das erledigt sich oft von selbst.",
    "dashboard.apps.rollingOut": "1 wird installiert",
    "dashboard.apps.running": "{count} laufen",
    "dashboard.apps.noHealth": "kein Status gemeldet",
    "dashboard.referenceId": "ID {id}",
    "dashboard.rollout.label": "Einrichtung · {phase}",

    "settings.saved": "Gespeichert",
    "settings.homelabRemoved": "„{name}“ wurde entfernt",

    "stackDetail.toast.starting": "Server wird gestartet",
    "stackDetail.toast.stopping": "Server wird gestoppt",
    "stackDetail.toast.sshOn": "SSH-Zugang eingeschaltet",
    "stackDetail.toast.sshOff": "SSH-Zugang ausgeschaltet",
    "stackDetail.toast.removing": "Server wird entfernt",
    "stackDetail.ssh.label": "SSH-Zugang",
    "stackDetail.ssh.details": "Verbindungsdetails",
    "stackDetail.remove.title": "Diesen Server löschen",
    "stackDetail.remove.body":
      "Der verwaltete Server wird heruntergefahren und beim Anbieter samt seinen Daten gelöscht. Das lässt sich nicht rückgängig machen.",
    "stackDetail.managedServer": "verwalteter Server",
    "stackDetail.loadError": "Diese Einrichtung konnte nicht geladen werden.",

    "serverPage.tab.overview": "Übersicht",
    "serverPage.tab.services": "Dienste",
    "serverPage.tab.ports": "Ports",
    "serverPage.tab.checks": "Prüfungen",
    "serverPage.tab.logs": "Logs",
    "serverPage.tab.settings": "Einstellungen",
    "serverPage.noIp": "keine Adresse",
    "serverPage.loadError": "Die Serverdetails konnten nicht geladen werden.",
    "serverPage.refreshError":
      "Die letzte Aktualisierung ist fehlgeschlagen. Der zuletzt bekannte Stand wird angezeigt.",
    "serverPage.historyLink": "Verlauf & Metriken",
  },
  es: {
    "shell.copied": "Copiado",
    "shell.copyFailed": "No se pudo copiar",
    "serverPage.primaryServer": "servidor principal",
    "serverPage.runtimeOnly": "solo en ejecución",
    "settings.remove": "Quitar",
    "errorCallout.details": "Detalles técnicos",
    "errorCallout.retry": "Reintentar",
    "errorCallout.retrying": "Reintentando…",
    "errorCallout.nextAttempt": "Nuevo intento en {seconds} s",

    "switch.on": "Activado",
    "switch.off": "Desactivado",

    "shell.skipToContent": "Saltar al contenido",
    "shell.homeLink": "Ir al panel",
    "shell.footerLinks": "Enlaces legales y de la empresa",

    "nav.servicesPlacement": "Ubicación",
    "nav.monitoring.now": "Ahora",
    "nav.createHomelab": "Crear homelab",
    "nav.group.homelab": "Homelab",

    "shortcuts.turnedOff": "Atajos de teclado desactivados",
    "shortcuts.turnedOffHint": "Puedes volver a activarlos en Ajustes.",
    "shortcuts.turnOff": "Desactivar atajos",
    "shortcuts.settingsTitle": "Atajos de teclado",
    "shortcuts.settingsLead":
      "Los atajos de una sola tecla, como G y luego H, saltan entre páginas. Desactívalos si estorban al escribir o con un lector de pantalla.",
    "shortcuts.switchLabel": "Atajos de una sola tecla",
    "shortcuts.switchHint": "Se guarda en este dispositivo.",
    "ui.shortcuts.closeDialog": "Cerrar diálogo",
    "ui.shortcutsHelp.then": "luego",

    "servers.add": "Añadir servidor",

    "monitoring.serverRemoved": "Se quitó {name}",
    "monitoring.allServers": "Todos los servidores",
    "monitoring.alertsHint": "Reglas que se están activando ahora mismo.",
    "monitoring.alertsUnavailable":
      "No se pudieron leer las alertas en este momento.",
    "monitoring.noAlerts": "No hay alertas activas. Todo está tranquilo.",
    "monitoring.alertSince": "desde {time}",
    "monitoring.openServer": "Administrar {name}",

    "dashboard.error.title": "No se pudo cargar tu homelab",
    "dashboard.error.body":
      "kombify no pudo conectar con tu homelab en este momento. A menudo se resuelve solo.",
    "dashboard.apps.rollingOut": "1 en instalación",
    "dashboard.apps.running": "{count} en ejecución",
    "dashboard.apps.noHealth": "sin estado informado",
    "dashboard.referenceId": "ID {id}",
    "dashboard.rollout.label": "Configuración · {phase}",

    "settings.saved": "Guardado",
    "settings.homelabRemoved": "Se quitó {name}",

    "stackDetail.toast.starting": "El servidor se está iniciando",
    "stackDetail.toast.stopping": "El servidor se está deteniendo",
    "stackDetail.toast.sshOn": "Acceso SSH activado",
    "stackDetail.toast.sshOff": "Acceso SSH desactivado",
    "stackDetail.toast.removing": "Se está quitando el servidor",
    "stackDetail.ssh.label": "Acceso SSH",
    "stackDetail.ssh.details": "Detalles de conexión",
    "stackDetail.remove.title": "Eliminar este servidor",
    "stackDetail.remove.body":
      "El servidor administrado se apaga y se elimina en el proveedor, junto con sus datos. No se puede deshacer.",
    "stackDetail.managedServer": "servidor administrado",
    "stackDetail.loadError": "No se pudo cargar esta configuración.",

    "serverPage.tab.overview": "Resumen",
    "serverPage.tab.services": "Servicios",
    "serverPage.tab.ports": "Puertos",
    "serverPage.tab.checks": "Comprobaciones",
    "serverPage.tab.logs": "Registros",
    "serverPage.tab.settings": "Ajustes",
    "serverPage.noIp": "sin dirección",
    "serverPage.loadError": "No se pudieron cargar los detalles del servidor.",
    "serverPage.refreshError":
      "La última actualización falló. Se muestra el último estado conocido.",
    "serverPage.historyLink": "Historial y métricas",
  },
  "zh-Hans": {
    "shell.copied": "已复制",
    "shell.copyFailed": "无法复制",
    "serverPage.primaryServer": "主服务器",
    "serverPage.runtimeOnly": "仅运行时",
    "settings.remove": "移除",
    "errorCallout.details": "技术详情",
    "errorCallout.retry": "重试",
    "errorCallout.retrying": "正在重试…",
    "errorCallout.nextAttempt": "{seconds} 秒后自动重试",

    "switch.on": "开",
    "switch.off": "关",

    "shell.skipToContent": "跳到内容",
    "shell.homeLink": "前往仪表板",
    "shell.footerLinks": "法律与公司链接",

    "nav.servicesPlacement": "应用分布",
    "nav.monitoring.now": "当前状态",
    "nav.createHomelab": "创建 Homelab",
    "nav.group.homelab": "Homelab",

    "shortcuts.turnedOff": "键盘快捷键已关闭",
    "shortcuts.turnedOffHint": "你可以在设置中重新开启。",
    "shortcuts.turnOff": "关闭快捷键",
    "shortcuts.settingsTitle": "键盘快捷键",
    "shortcuts.settingsLead":
      "单键快捷键(例如先按 G 再按 H)用于在页面之间跳转。如果它们影响输入或读屏软件,可以将其关闭。",
    "shortcuts.switchLabel": "单键快捷键",
    "shortcuts.switchHint": "保存在此设备上。",
    "ui.shortcuts.closeDialog": "关闭对话框",
    "ui.shortcutsHelp.then": "然后",

    "servers.add": "添加服务器",

    "monitoring.serverRemoved": "已移除 {name}",
    "monitoring.allServers": "全部服务器",
    "monitoring.alertsHint": "当前正在触发的规则。",
    "monitoring.alertsUnavailable": "暂时无法读取警报。",
    "monitoring.noAlerts": "没有活动警报,一切正常。",
    "monitoring.alertSince": "自 {time} 起",
    "monitoring.openServer": "管理 {name}",

    "dashboard.error.title": "无法加载你的 Homelab",
    "dashboard.error.body":
      "kombify 暂时无法连接你的 Homelab,这通常会自行恢复。",
    "dashboard.apps.rollingOut": "1 个正在安装",
    "dashboard.apps.running": "{count} 个运行中",
    "dashboard.apps.noHealth": "未报告状态",
    "dashboard.referenceId": "ID {id}",
    "dashboard.rollout.label": "设置 · {phase}",

    "settings.saved": "已保存",
    "settings.homelabRemoved": "已移除 {name}",

    "stackDetail.toast.starting": "服务器正在启动",
    "stackDetail.toast.stopping": "服务器正在停止",
    "stackDetail.toast.sshOn": "已开启 SSH 访问",
    "stackDetail.toast.sshOff": "已关闭 SSH 访问",
    "stackDetail.toast.removing": "正在移除服务器",
    "stackDetail.ssh.label": "SSH 访问",
    "stackDetail.ssh.details": "连接详情",
    "stackDetail.remove.title": "删除此服务器",
    "stackDetail.remove.body":
      "托管服务器将被关闭,并连同其数据一起在提供商处删除。此操作无法撤销。",
    "stackDetail.managedServer": "托管服务器",
    "stackDetail.loadError": "无法加载此配置。",

    "serverPage.tab.overview": "概览",
    "serverPage.tab.services": "服务",
    "serverPage.tab.ports": "端口",
    "serverPage.tab.checks": "检查",
    "serverPage.tab.logs": "日志",
    "serverPage.tab.settings": "设置",
    "serverPage.noIp": "无地址",
    "serverPage.loadError": "无法加载服务器详情。",
    "serverPage.refreshError": "最近一次刷新失败,显示的是最后已知的状态。",
    "serverPage.historyLink": "历史与指标",
  },
  hi: {
    "shell.copied": "कॉपी किया गया",
    "shell.copyFailed": "कॉपी नहीं हो सका",
    "serverPage.primaryServer": "मुख्य सर्वर",
    "serverPage.runtimeOnly": "केवल रनटाइम",
    "settings.remove": "हटाएँ",
    "errorCallout.details": "तकनीकी विवरण",
    "errorCallout.retry": "फिर से कोशिश करें",
    "errorCallout.retrying": "फिर से कोशिश की जा रही है…",
    "errorCallout.nextAttempt": "{seconds} सेकंड में फिर से कोशिश होगी",

    "switch.on": "चालू",
    "switch.off": "बंद",

    "shell.skipToContent": "सामग्री पर जाएँ",
    "shell.homeLink": "डैशबोर्ड पर जाएँ",
    "shell.footerLinks": "कानूनी और कंपनी लिंक",

    "nav.servicesPlacement": "प्लेसमेंट",
    "nav.monitoring.now": "अभी",
    "nav.createHomelab": "Homelab बनाएँ",
    "nav.group.homelab": "Homelab",

    "shortcuts.turnedOff": "कीबोर्ड शॉर्टकट बंद कर दिए गए",
    "shortcuts.turnedOffHint":
      "आप इन्हें सेटिंग्स में फिर से चालू कर सकते हैं।",
    "shortcuts.turnOff": "शॉर्टकट बंद करें",
    "shortcuts.settingsTitle": "कीबोर्ड शॉर्टकट",
    "shortcuts.settingsLead":
      "G फिर H जैसे एकल-कुंजी शॉर्टकट पेजों के बीच जाने देते हैं। अगर वे टाइप करने या स्क्रीन रीडर में बाधा डालें, तो इन्हें बंद कर दें।",
    "shortcuts.switchLabel": "एकल-कुंजी शॉर्टकट",
    "shortcuts.switchHint": "इस डिवाइस पर सहेजा गया।",
    "ui.shortcuts.closeDialog": "डायलॉग बंद करें",
    "ui.shortcutsHelp.then": "फिर",

    "servers.add": "सर्वर जोड़ें",

    "monitoring.serverRemoved": "{name} हटा दिया गया",
    "monitoring.allServers": "सभी सर्वर",
    "monitoring.alertsHint": "वे नियम जो अभी सक्रिय हैं।",
    "monitoring.alertsUnavailable": "अलर्ट अभी पढ़े नहीं जा सके।",
    "monitoring.noAlerts": "कोई सक्रिय अलर्ट नहीं। सब कुछ शांत है।",
    "monitoring.alertSince": "{time} से",
    "monitoring.openServer": "{name} प्रबंधित करें",

    "dashboard.error.title": "आपका Homelab लोड नहीं हो सका",
    "dashboard.error.body":
      "kombify अभी आपके Homelab तक नहीं पहुँच सका। यह अक्सर अपने आप ठीक हो जाता है।",
    "dashboard.apps.rollingOut": "1 इंस्टॉल हो रहा है",
    "dashboard.apps.running": "{count} चल रहे हैं",
    "dashboard.apps.noHealth": "कोई स्थिति रिपोर्ट नहीं",
    "dashboard.referenceId": "ID {id}",
    "dashboard.rollout.label": "सेटअप · {phase}",

    "settings.saved": "सहेजा गया",
    "settings.homelabRemoved": "{name} हटा दिया गया",

    "stackDetail.toast.starting": "सर्वर शुरू हो रहा है",
    "stackDetail.toast.stopping": "सर्वर रुक रहा है",
    "stackDetail.toast.sshOn": "SSH एक्सेस चालू किया गया",
    "stackDetail.toast.sshOff": "SSH एक्सेस बंद किया गया",
    "stackDetail.toast.removing": "सर्वर हटाया जा रहा है",
    "stackDetail.ssh.label": "SSH एक्सेस",
    "stackDetail.ssh.details": "कनेक्शन विवरण",
    "stackDetail.remove.title": "इस सर्वर को हटाएँ",
    "stackDetail.remove.body":
      "प्रबंधित सर्वर बंद कर दिया जाएगा और उसके डेटा सहित प्रदाता पर हटा दिया जाएगा। इसे पूर्ववत नहीं किया जा सकता।",
    "stackDetail.managedServer": "प्रबंधित सर्वर",
    "stackDetail.loadError": "यह सेटअप लोड नहीं हो सका।",

    "serverPage.tab.overview": "अवलोकन",
    "serverPage.tab.services": "सेवाएँ",
    "serverPage.tab.ports": "पोर्ट",
    "serverPage.tab.checks": "जाँचें",
    "serverPage.tab.logs": "लॉग",
    "serverPage.tab.settings": "सेटिंग्स",
    "serverPage.noIp": "कोई पता नहीं",
    "serverPage.loadError": "सर्वर का विवरण लोड नहीं हो सका।",
    "serverPage.refreshError":
      "ताज़ा करना विफल रहा। आख़िरी ज्ञात स्थिति दिखाई जा रही है।",
    "serverPage.historyLink": "इतिहास और मेट्रिक्स",
  },
  ar: {
    "shell.copied": "تم النسخ",
    "shell.copyFailed": "تعذّر النسخ",
    "serverPage.primaryServer": "الخادم الرئيسي",
    "serverPage.runtimeOnly": "وقت التشغيل فقط",
    "settings.remove": "إزالة",
    "errorCallout.details": "التفاصيل التقنية",
    "errorCallout.retry": "إعادة المحاولة",
    "errorCallout.retrying": "جارٍ إعادة المحاولة…",
    "errorCallout.nextAttempt": "إعادة المحاولة خلال {seconds} ث",

    "switch.on": "تشغيل",
    "switch.off": "إيقاف",

    "shell.skipToContent": "تخطَّ إلى المحتوى",
    "shell.homeLink": "الانتقال إلى لوحة التحكم",
    "shell.footerLinks": "الروابط القانونية وروابط الشركة",

    "nav.servicesPlacement": "التوزيع",
    "nav.monitoring.now": "الآن",
    "nav.createHomelab": "إنشاء Homelab",
    "nav.group.homelab": "Homelab",

    "shortcuts.turnedOff": "تم إيقاف اختصارات لوحة المفاتيح",
    "shortcuts.turnedOffHint": "يمكنك إعادة تشغيلها من الإعدادات.",
    "shortcuts.turnOff": "إيقاف الاختصارات",
    "shortcuts.settingsTitle": "اختصارات لوحة المفاتيح",
    "shortcuts.settingsLead":
      "اختصارات المفتاح الواحد، مثل G ثم H، تنقلك بين الصفحات. أوقفها إذا تعارضت مع الكتابة أو مع قارئ الشاشة.",
    "shortcuts.switchLabel": "اختصارات المفتاح الواحد",
    "shortcuts.switchHint": "تُحفظ على هذا الجهاز.",
    "ui.shortcuts.closeDialog": "إغلاق مربع الحوار",
    "ui.shortcutsHelp.then": "ثم",

    "servers.add": "إضافة خادم",

    "monitoring.serverRemoved": "تمت إزالة {name}",
    "monitoring.allServers": "كل الخوادم",
    "monitoring.alertsHint": "القواعد التي يجري تفعيلها الآن.",
    "monitoring.alertsUnavailable": "تعذّرت قراءة التنبيهات الآن.",
    "monitoring.noAlerts": "لا توجد تنبيهات نشطة. كل شيء هادئ.",
    "monitoring.alertSince": "منذ {time}",
    "monitoring.openServer": "إدارة {name}",

    "dashboard.error.title": "تعذّر تحميل Homelab الخاص بك",
    "dashboard.error.body":
      "تعذّر على kombify الوصول إلى Homelab الخاص بك الآن. غالبًا ما تُحَلّ المشكلة من تلقاء نفسها.",
    "dashboard.apps.rollingOut": "1 قيد التثبيت",
    "dashboard.apps.running": "{count} قيد التشغيل",
    "dashboard.apps.noHealth": "لا توجد حالة مُبلَّغ عنها",
    "dashboard.referenceId": "ID {id}",
    "dashboard.rollout.label": "الإعداد · {phase}",

    "settings.saved": "تم الحفظ",
    "settings.homelabRemoved": "تمت إزالة {name}",

    "stackDetail.toast.starting": "جارٍ تشغيل الخادم",
    "stackDetail.toast.stopping": "جارٍ إيقاف الخادم",
    "stackDetail.toast.sshOn": "تم تفعيل وصول SSH",
    "stackDetail.toast.sshOff": "تم إيقاف وصول SSH",
    "stackDetail.toast.removing": "جارٍ إزالة الخادم",
    "stackDetail.ssh.label": "وصول SSH",
    "stackDetail.ssh.details": "تفاصيل الاتصال",
    "stackDetail.remove.title": "حذف هذا الخادم",
    "stackDetail.remove.body":
      "سيتم إيقاف الخادم المُدار وحذفه لدى المزوّد مع بياناته. لا يمكن التراجع عن ذلك.",
    "stackDetail.managedServer": "خادم مُدار",
    "stackDetail.loadError": "تعذّر تحميل هذا الإعداد.",

    "serverPage.tab.overview": "نظرة عامة",
    "serverPage.tab.services": "الخدمات",
    "serverPage.tab.ports": "المنافذ",
    "serverPage.tab.checks": "الفحوصات",
    "serverPage.tab.logs": "السجلات",
    "serverPage.tab.settings": "الإعدادات",
    "serverPage.noIp": "لا يوجد عنوان",
    "serverPage.loadError": "تعذّر تحميل تفاصيل الخادم.",
    "serverPage.refreshError": "فشل آخر تحديث. تُعرض آخر حالة معروفة.",
    "serverPage.historyLink": "السجل والمقاييس",
  },
} satisfies Record<string, Record<string, string>>;
