export const maintenanceMessages = {
  en: {
    "maintenance.inventory.current": "Dependency inventory: current",
    "maintenance.inventory.stale": "Dependency inventory: stale",
    "maintenance.inventory.incomplete": "Dependency inventory: incomplete",
    "maintenance.inventory.unavailable": "Dependency inventory: unavailable",
    "maintenance.noServices":
      "The current inventory reports no affected deployed services.",
    "maintenance.stacksPlacement": "These stacks use this server.",
    "maintenance.servicesPlacement": "These services run on this server.",
    "maintenance.title": "Server maintenance",
    "maintenance.intro":
      "Review the services and stacks on this server before installing OS updates or rebooting.",
    "maintenance.update": "Review OS updates",
    "maintenance.reboot": "Review reboot",
    "maintenance.unavailable": "Maintenance is unavailable for this server.",
    "maintenance.uncertain":
      "The request result is unknown. Retry the same request to retrieve its receipt before starting another operation.",
    "maintenance.changed":
      "The server or its dependencies changed. Review the refreshed impact and confirm again.",
    "maintenance.retry": "Retrieve request receipt",
    "maintenance.loading": "Checking maintenance…",
    "maintenance.target": "{action} · {server}",
    "maintenance.rebootImpact":
      "A reboot interrupts services on this server while it restarts.",
    "maintenance.updateImpact":
      "OS updates may restart services and temporarily affect their stacks.",
    "maintenance.incomplete":
      "The dependency inventory is incomplete, stale or unavailable. Listed impacts are known; other impacts may be missing. Confirmation is disabled.",
    "maintenance.noImpact":
      "The current inventory reports no services or stacks on this server.",
    "maintenance.observed": "Dependency inventory observed {when}",
    "maintenance.stacks": "Affected stacks",
    "maintenance.services": "Affected services",
    "maintenance.locked": "Locked · maintenance blocked",
    "maintenance.plan":
      "{pending} OS updates, including {security} security updates.",
    "maintenance.rebootSeparate":
      "A reboot may be needed. It requires a separate review and confirmation.",
    "maintenance.packages": "Planned package changes",
    "maintenance.packagesTruncated":
      "The package list is shortened; the update count includes all planned packages.",
    "maintenance.blocked":
      "This server cannot admit the operation in its current state.",
    "maintenance.confirmScope":
      "Confirmation applies to this server, this operation and the dependency inventory shown above.",
    "maintenance.confirm": "Confirm maintenance",
    "maintenance.cancel": "Cancel review",
    "maintenance.refresh": "Refresh impact",
    "maintenance.cancelJob": "Cancel queued operation",
    "maintenance.state.queued": "Maintenance queued",
    "maintenance.state.running": "Maintenance running",
    "maintenance.state.waiting": "OS update still running",
    "maintenance.state.awaiting_node_return":
      "Waiting for the server to return",
    "maintenance.state.completed": "Maintenance completed",
    "maintenance.state.failed": "Maintenance failed",
    "maintenance.state.cancelled": "Maintenance cancelled",
  },
  de: {
    "maintenance.inventory.current": "Abhängigkeitsdaten: aktuell",
    "maintenance.inventory.stale": "Abhängigkeitsdaten: veraltet",
    "maintenance.inventory.incomplete": "Abhängigkeitsdaten: unvollständig",
    "maintenance.inventory.unavailable": "Abhängigkeitsdaten: nicht verfügbar",
    "maintenance.noServices":
      "Die aktuellen Bestandsdaten melden keine betroffenen bereitgestellten Dienste.",
    "maintenance.stacksPlacement": "Diese Stacks nutzen diesen Server.",
    "maintenance.servicesPlacement": "Diese Dienste laufen auf diesem Server.",
    "maintenance.title": "Serverwartung",
    "maintenance.intro":
      "Prüfe die Dienste und Stacks auf diesem Server vor Betriebssystemupdates oder einem Neustart.",
    "maintenance.update": "Betriebssystemupdates prüfen",
    "maintenance.reboot": "Neustart prüfen",
    "maintenance.unavailable": "Für diesen Server ist keine Wartung verfügbar.",
    "maintenance.uncertain":
      "Das Ergebnis der Anfrage ist unbekannt. Wiederhole dieselbe Anfrage, um ihren Beleg abzurufen, bevor du eine weitere Aktion startest.",
    "maintenance.changed":
      "Der Server oder seine Abhängigkeiten haben sich geändert. Prüfe die aktualisierten Auswirkungen und bestätige erneut.",
    "maintenance.retry": "Anfragebeleg abrufen",
    "maintenance.loading": "Wartung wird geprüft …",
    "maintenance.target": "{action} · {server}",
    "maintenance.rebootImpact":
      "Ein Neustart unterbricht die Dienste auf diesem Server, während er neu startet.",
    "maintenance.updateImpact":
      "Betriebssystemupdates können Dienste neu starten und ihre Stacks vorübergehend beeinträchtigen.",
    "maintenance.incomplete":
      "Die Abhängigkeitsdaten sind unvollständig, veraltet oder nicht verfügbar. Die gezeigten Auswirkungen sind bekannt; weitere können fehlen. Die Bestätigung ist gesperrt.",
    "maintenance.noImpact":
      "Die aktuellen Bestandsdaten melden keine Dienste oder Stacks auf diesem Server.",
    "maintenance.observed": "Abhängigkeiten erfasst {when}",
    "maintenance.stacks": "Betroffene Stacks",
    "maintenance.services": "Betroffene Dienste",
    "maintenance.locked": "Gesperrt · Wartung blockiert",
    "maintenance.plan":
      "{pending} Betriebssystemupdates, davon {security} Sicherheitsupdates.",
    "maintenance.rebootSeparate":
      "Ein Neustart kann erforderlich sein. Er muss gesondert geprüft und bestätigt werden.",
    "maintenance.packages": "Geplante Paketänderungen",
    "maintenance.packagesTruncated":
      "Die Paketliste ist gekürzt; die Anzahl umfasst alle geplanten Pakete.",
    "maintenance.blocked":
      "Der aktuelle Serverzustand lässt diese Aktion nicht zu.",
    "maintenance.confirmScope":
      "Die Bestätigung gilt für diesen Server, diese Aktion und die oben gezeigten Abhängigkeiten.",
    "maintenance.confirm": "Wartung bestätigen",
    "maintenance.cancel": "Prüfung abbrechen",
    "maintenance.refresh": "Auswirkungen aktualisieren",
    "maintenance.cancelJob": "Eingeplante Aktion abbrechen",
    "maintenance.state.queued": "Wartung eingeplant",
    "maintenance.state.running": "Wartung läuft",
    "maintenance.state.waiting": "Betriebssystemupdate läuft noch",
    "maintenance.state.awaiting_node_return": "Warten auf Rückkehr des Servers",
    "maintenance.state.completed": "Wartung abgeschlossen",
    "maintenance.state.failed": "Wartung fehlgeschlagen",
    "maintenance.state.cancelled": "Wartung abgebrochen",
  },
  es: {
    "maintenance.inventory.current": "Inventario de dependencias: actualizado",
    "maintenance.inventory.stale": "Inventario de dependencias: desactualizado",
    "maintenance.inventory.incomplete":
      "Inventario de dependencias: incompleto",
    "maintenance.inventory.unavailable":
      "Inventario de dependencias: no disponible",
    "maintenance.noServices":
      "El inventario actual no informa de servicios desplegados afectados.",
    "maintenance.stacksPlacement": "Estos stacks utilizan este servidor.",
    "maintenance.servicesPlacement":
      "Estos servicios se ejecutan en este servidor.",
    "maintenance.title": "Mantenimiento del servidor",
    "maintenance.intro":
      "Revisa los servicios y stacks de este servidor antes de actualizar el sistema o reiniciarlo.",
    "maintenance.update": "Revisar actualizaciones del sistema",
    "maintenance.reboot": "Revisar reinicio",
    "maintenance.unavailable":
      "El mantenimiento no está disponible para este servidor.",
    "maintenance.uncertain":
      "Se desconoce el resultado. Repite la misma solicitud para recuperar su comprobante antes de iniciar otra operación.",
    "maintenance.changed":
      "El servidor o sus dependencias han cambiado. Revisa el impacto actualizado y confirma de nuevo.",
    "maintenance.retry": "Recuperar comprobante",
    "maintenance.loading": "Comprobando mantenimiento…",
    "maintenance.target": "{action} · {server}",
    "maintenance.rebootImpact":
      "El reinicio interrumpe los servicios de este servidor mientras se reinicia.",
    "maintenance.updateImpact":
      "Las actualizaciones pueden reiniciar servicios y afectar temporalmente a sus stacks.",
    "maintenance.incomplete":
      "El inventario de dependencias está incompleto, desactualizado o no disponible. Se muestran los impactos conocidos; pueden faltar otros. La confirmación está deshabilitada.",
    "maintenance.noImpact":
      "El inventario actual no informa de servicios ni stacks en este servidor.",
    "maintenance.observed": "Dependencias observadas {when}",
    "maintenance.stacks": "Stacks afectados",
    "maintenance.services": "Servicios afectados",
    "maintenance.locked": "Bloqueado · mantenimiento impedido",
    "maintenance.plan":
      "{pending} actualizaciones del sistema, incluidas {security} de seguridad.",
    "maintenance.rebootSeparate":
      "Puede ser necesario reiniciar. Requiere una revisión y confirmación por separado.",
    "maintenance.packages": "Cambios de paquetes previstos",
    "maintenance.packagesTruncated":
      "La lista está abreviada; el recuento incluye todos los paquetes previstos.",
    "maintenance.blocked":
      "El estado actual del servidor no permite esta operación.",
    "maintenance.confirmScope":
      "La confirmación corresponde a este servidor, esta operación y las dependencias mostradas.",
    "maintenance.confirm": "Confirmar mantenimiento",
    "maintenance.cancel": "Cancelar revisión",
    "maintenance.refresh": "Actualizar impacto",
    "maintenance.cancelJob": "Cancelar operación en cola",
    "maintenance.state.queued": "Mantenimiento en cola",
    "maintenance.state.running": "Mantenimiento en curso",
    "maintenance.state.waiting": "La actualización sigue en curso",
    "maintenance.state.awaiting_node_return":
      "Esperando el regreso del servidor",
    "maintenance.state.completed": "Mantenimiento completado",
    "maintenance.state.failed": "Mantenimiento fallido",
    "maintenance.state.cancelled": "Mantenimiento cancelado",
  },
  "zh-Hans": {
    "maintenance.inventory.current": "依赖清单：最新",
    "maintenance.inventory.stale": "依赖清单：已过期",
    "maintenance.inventory.incomplete": "依赖清单：不完整",
    "maintenance.inventory.unavailable": "依赖清单：不可用",
    "maintenance.noServices": "当前清单未报告有受影响的已部署服务。",
    "maintenance.stacksPlacement": "这些技术栈使用此服务器。",
    "maintenance.servicesPlacement": "这些服务在此服务器上运行。",
    "maintenance.title": "服务器维护",
    "maintenance.intro":
      "安装系统更新或重启之前，请查看此服务器上的服务和技术栈。",
    "maintenance.update": "查看系统更新",
    "maintenance.reboot": "查看重启影响",
    "maintenance.unavailable": "此服务器无法进行维护。",
    "maintenance.uncertain":
      "请求结果未知。请重试同一请求以获取回执，然后再启动其他操作。",
    "maintenance.changed": "服务器或其依赖项已更改。请查看最新影响并重新确认。",
    "maintenance.retry": "获取请求回执",
    "maintenance.loading": "正在检查维护…",
    "maintenance.target": "{action} · {server}",
    "maintenance.rebootImpact": "服务器重启期间，其上的服务将中断。",
    "maintenance.updateImpact": "系统更新可能重启服务，并暂时影响其技术栈。",
    "maintenance.incomplete":
      "依赖清单不完整、已过期或不可用。当前仅显示已知影响，其他影响可能缺失。无法确认。",
    "maintenance.noImpact": "当前清单未报告此服务器上有服务或技术栈。",
    "maintenance.observed": "依赖清单记录时间：{when}",
    "maintenance.stacks": "受影响的技术栈",
    "maintenance.services": "受影响的服务",
    "maintenance.locked": "已锁定 · 阻止维护",
    "maintenance.plan": "{pending} 项系统更新，其中 {security} 项安全更新。",
    "maintenance.rebootSeparate": "可能需要重启。重启需要单独查看并确认。",
    "maintenance.packages": "计划的软件包变更",
    "maintenance.packagesTruncated":
      "列表已缩短；更新数量包含所有计划的软件包。",
    "maintenance.blocked": "服务器当前状态不允许此操作。",
    "maintenance.confirmScope":
      "确认仅适用于此服务器、此操作和以上所示的依赖清单。",
    "maintenance.confirm": "确认维护",
    "maintenance.cancel": "取消查看",
    "maintenance.refresh": "刷新影响",
    "maintenance.cancelJob": "取消排队中的操作",
    "maintenance.state.queued": "维护已排队",
    "maintenance.state.running": "维护进行中",
    "maintenance.state.waiting": "系统更新仍在进行",
    "maintenance.state.awaiting_node_return": "等待服务器恢复连接",
    "maintenance.state.completed": "维护已完成",
    "maintenance.state.failed": "维护失败",
    "maintenance.state.cancelled": "维护已取消",
  },
  hi: {
    "maintenance.inventory.current": "निर्भरता सूची: वर्तमान",
    "maintenance.inventory.stale": "निर्भरता सूची: पुरानी",
    "maintenance.inventory.incomplete": "निर्भरता सूची: अधूरी",
    "maintenance.inventory.unavailable": "निर्भरता सूची: अनुपलब्ध",
    "maintenance.noServices":
      "वर्तमान सूची में कोई प्रभावित तैनात सेवा नहीं है।",
    "maintenance.stacksPlacement": "ये स्टैक इस सर्वर का उपयोग करते हैं।",
    "maintenance.servicesPlacement": "ये सेवाएँ इस सर्वर पर चलती हैं।",
    "maintenance.title": "सर्वर रखरखाव",
    "maintenance.intro":
      "सिस्टम अपडेट या रीबूट से पहले इस सर्वर की सेवाएँ और स्टैक देखें।",
    "maintenance.update": "सिस्टम अपडेट देखें",
    "maintenance.reboot": "रीबूट का प्रभाव देखें",
    "maintenance.unavailable": "इस सर्वर के लिए रखरखाव उपलब्ध नहीं है।",
    "maintenance.uncertain":
      "अनुरोध का परिणाम अज्ञात है। दूसरी कार्रवाई शुरू करने से पहले उसी अनुरोध को दोहराकर उसकी रसीद प्राप्त करें।",
    "maintenance.changed":
      "सर्वर या उसकी निर्भरताएँ बदल गई हैं। नया प्रभाव देखें और फिर पुष्टि करें।",
    "maintenance.retry": "अनुरोध की रसीद प्राप्त करें",
    "maintenance.loading": "रखरखाव की जाँच हो रही है…",
    "maintenance.target": "{action} · {server}",
    "maintenance.rebootImpact":
      "रीबूट के दौरान इस सर्वर की सेवाएँ बाधित होंगी।",
    "maintenance.updateImpact":
      "सिस्टम अपडेट सेवाओं को पुनः शुरू कर सकते हैं और उनके स्टैक को अस्थायी रूप से प्रभावित कर सकते हैं।",
    "maintenance.incomplete":
      "निर्भरता सूची अधूरी, पुरानी या अनुपलब्ध है। ज्ञात प्रभाव दिखाए गए हैं; अन्य प्रभाव छूट सकते हैं। पुष्टि उपलब्ध नहीं है।",
    "maintenance.noImpact":
      "वर्तमान सूची इस सर्वर पर कोई सेवा या स्टैक नहीं बताती।",
    "maintenance.observed": "निर्भरताएँ देखी गईं {when}",
    "maintenance.stacks": "प्रभावित स्टैक",
    "maintenance.services": "प्रभावित सेवाएँ",
    "maintenance.locked": "लॉक किया गया · रखरखाव अवरुद्ध",
    "maintenance.plan":
      "{pending} सिस्टम अपडेट, जिनमें {security} सुरक्षा अपडेट हैं।",
    "maintenance.rebootSeparate":
      "रीबूट की आवश्यकता हो सकती है। इसके लिए अलग समीक्षा और पुष्टि चाहिए।",
    "maintenance.packages": "नियोजित पैकेज बदलाव",
    "maintenance.packagesTruncated":
      "पैकेज सूची संक्षिप्त है; संख्या में सभी नियोजित पैकेज शामिल हैं।",
    "maintenance.blocked":
      "सर्वर की वर्तमान स्थिति यह कार्रवाई स्वीकार नहीं करती।",
    "maintenance.confirmScope":
      "पुष्टि इस सर्वर, इस कार्रवाई और ऊपर दिखाई गई निर्भरता सूची के लिए है।",
    "maintenance.confirm": "रखरखाव की पुष्टि करें",
    "maintenance.cancel": "समीक्षा रद्द करें",
    "maintenance.refresh": "प्रभाव फिर से देखें",
    "maintenance.cancelJob": "कतारबद्ध कार्रवाई रद्द करें",
    "maintenance.state.queued": "रखरखाव कतार में है",
    "maintenance.state.running": "रखरखाव जारी है",
    "maintenance.state.waiting": "सिस्टम अपडेट अभी जारी है",
    "maintenance.state.awaiting_node_return": "सर्वर के लौटने की प्रतीक्षा",
    "maintenance.state.completed": "रखरखाव पूरा हुआ",
    "maintenance.state.failed": "रखरखाव विफल हुआ",
    "maintenance.state.cancelled": "रखरखाव रद्द हुआ",
  },
  ar: {
    "maintenance.inventory.current": "قائمة التبعيات: محدّثة",
    "maintenance.inventory.stale": "قائمة التبعيات: قديمة",
    "maintenance.inventory.incomplete": "قائمة التبعيات: ناقصة",
    "maintenance.inventory.unavailable": "قائمة التبعيات: غير متاحة",
    "maintenance.noServices":
      "لا تسجّل القائمة الحالية أي خدمات منشورة متأثرة.",
    "maintenance.stacksPlacement": "تستخدم هذه الحزم هذا الخادم.",
    "maintenance.servicesPlacement": "تعمل هذه الخدمات على هذا الخادم.",
    "maintenance.title": "صيانة الخادم",
    "maintenance.intro":
      "راجع الخدمات والحزم على هذا الخادم قبل تحديث النظام أو إعادة تشغيله.",
    "maintenance.update": "مراجعة تحديثات النظام",
    "maintenance.reboot": "مراجعة إعادة التشغيل",
    "maintenance.unavailable": "الصيانة غير متاحة لهذا الخادم.",
    "maintenance.uncertain":
      "نتيجة الطلب غير معروفة. أعد الطلب نفسه لاسترجاع إيصال التنفيذ قبل بدء عملية أخرى.",
    "maintenance.changed":
      "تغيّر الخادم أو تبعياته. راجع التأثير المحدّث وأكّد مرة أخرى.",
    "maintenance.retry": "استرجاع إيصال الطلب",
    "maintenance.loading": "جارٍ فحص الصيانة…",
    "maintenance.target": "{action} · {server}",
    "maintenance.rebootImpact":
      "تتوقف خدمات هذا الخادم مؤقتًا أثناء إعادة تشغيله.",
    "maintenance.updateImpact":
      "قد تعيد تحديثات النظام تشغيل الخدمات وتؤثر مؤقتًا في حزمها.",
    "maintenance.incomplete":
      "قائمة التبعيات ناقصة أو قديمة أو غير متاحة. تظهر التأثيرات المعروفة وقد تغيب تأثيرات أخرى. التأكيد معطّل.",
    "maintenance.noImpact":
      "لا تسجّل القائمة الحالية أي خدمات أو حزم على هذا الخادم.",
    "maintenance.observed": "رُصدت التبعيات {when}",
    "maintenance.stacks": "الحزم المتأثرة",
    "maintenance.services": "الخدمات المتأثرة",
    "maintenance.locked": "مقفل · الصيانة محظورة",
    "maintenance.plan":
      "{pending} تحديثات للنظام، منها {security} تحديثات أمنية.",
    "maintenance.rebootSeparate":
      "قد تلزم إعادة التشغيل. تتطلب مراجعة وتأكيدًا منفصلين.",
    "maintenance.packages": "تغييرات حزم البرامج المخططة",
    "maintenance.packagesTruncated":
      "القائمة مختصرة؛ يشمل العدد جميع حزم البرامج المخططة.",
    "maintenance.blocked": "لا تسمح حالة الخادم الحالية بهذه العملية.",
    "maintenance.confirmScope":
      "ينطبق التأكيد على هذا الخادم وهذه العملية وقائمة التبعيات المعروضة أعلاه.",
    "maintenance.confirm": "تأكيد الصيانة",
    "maintenance.cancel": "إلغاء المراجعة",
    "maintenance.refresh": "تحديث التأثير",
    "maintenance.cancelJob": "إلغاء العملية في قائمة الانتظار",
    "maintenance.state.queued": "الصيانة في قائمة الانتظار",
    "maintenance.state.running": "الصيانة قيد التنفيذ",
    "maintenance.state.waiting": "تحديث النظام ما زال قيد التنفيذ",
    "maintenance.state.awaiting_node_return": "بانتظار عودة الخادم",
    "maintenance.state.completed": "اكتملت الصيانة",
    "maintenance.state.failed": "فشلت الصيانة",
    "maintenance.state.cancelled": "أُلغيت الصيانة",
  },
};
