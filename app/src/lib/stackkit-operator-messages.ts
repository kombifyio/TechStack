import { getLocale } from "#lib/i18n.svelte.js";
const messages = {
  en: {
    title: "StackKit operations",
    unavailable: "No StackKit actions are currently granted for this server.",
    advanced_change_set: "Apply a change set",
    advanced_change_setDescription:
      "Preview and apply the proposed StackSpec through Advanced Mode.",
    rollback: "Roll back",
    rollbackDescription: "Return to an exact recorded snapshot or change set.",
    restore_drill: "Run a restore drill",
    restore_drillDescription:
      "Verify recovery on this node without activating a restore.",
    backup_run: "Run a backup",
    backup_runDescription: "Ask the node to back up its current applied plan.",
    backup_configure: "Configure node backups",
    backup_configureDescription:
      "Configure the node-owned backup repository for its current plan.",
    candidate: "Proposed StackSpec (JSON)",
    target: "Snapshot or change-set digest",
    anchor: "Snapshot anchor digest (optional)",
    confirm: "Confirm and start",
    cancel: "Cancel",
    approval:
      "This starts the selected operation on this server. Confirm only after checking the proposed changes and recovery target.",
    invalid:
      "Enter a JSON StackSpec object or an exact sha256 digest for the selected operation.",
    accepted: "Request accepted. Execution and verification are pending.",
    running: "Starting…",
    observe: "Refresh job outcome",
    recorded: "The node reported successful execution.",
    unverified:
      "Job finished; matching successful node evidence is unavailable.",
    scopeMismatch: "The job does not belong to the selected stack and node.",
    loadFailed: "Server operations could not be loaded.",
    retry: "Retry loading",
  },
  de: {
    title: "StackKit-Aktionen",
    unavailable:
      "Für diesen Server sind derzeit keine StackKit-Aktionen freigegeben.",
    advanced_change_set: "Änderungssatz anwenden",
    advanced_change_setDescription:
      "Den vorgeschlagenen StackSpec über Advanced Mode planen und anwenden.",
    rollback: "Zurückrollen",
    rollbackDescription:
      "Zu einem genau bezeichneten Snapshot oder Änderungssatz zurückkehren.",
    restore_drill: "Wiederherstellung proben",
    restore_drillDescription:
      "Wiederherstellung auf diesem Knoten prüfen, ohne sie zu aktivieren.",
    backup_run: "Backup starten",
    backup_runDescription:
      "Den Knoten bitten, seinen aktuell angewendeten Plan zu sichern.",
    backup_configure: "Knoten-Backups konfigurieren",
    backup_configureDescription:
      "Das vom Knoten verwaltete Backup-Repository für seinen aktuellen Plan konfigurieren.",
    candidate: "Vorgeschlagener StackSpec (JSON)",
    target: "Snapshot- oder Änderungssatz-Digest",
    anchor: "Snapshot-Anker-Digest (optional)",
    confirm: "Bestätigen und starten",
    cancel: "Abbrechen",
    approval:
      "Dies startet die gewählte Aktion auf diesem Server. Prüfe vor der Bestätigung die Änderungen und das Wiederherstellungsziel.",
    invalid:
      "Gib für die gewählte Aktion ein StackSpec-JSON-Objekt oder einen exakten sha256-Digest ein.",
    accepted: "Anfrage angenommen. Ausführung und Prüfung stehen noch aus.",
    running: "Wird gestartet…",
    observe: "Auftragsergebnis aktualisieren",
    recorded: "Der Knoten hat eine erfolgreiche Ausführung gemeldet.",
    unverified: "Auftrag beendet; passende erfolgreiche Knotenevidenz fehlt.",
    scopeMismatch: "Der Auftrag gehört nicht zum gewählten Stack und Knoten.",
    loadFailed: "Server-Aktionen konnten nicht geladen werden.",
    retry: "Erneut laden",
  },
  es: {
    title: "Operaciones de StackKit",
    unavailable: "No hay acciones de StackKit autorizadas para este servidor.",
    advanced_change_set: "Aplicar cambios",
    advanced_change_setDescription:
      "Planificar y aplicar el StackSpec propuesto con Advanced Mode.",
    rollback: "Revertir",
    rollbackDescription:
      "Volver a una instantánea o conjunto de cambios registrado.",
    restore_drill: "Ensayar restauración",
    restore_drillDescription:
      "Verificar la recuperación en este nodo sin activar una restauración.",
    backup_run: "Crear copia de seguridad",
    backup_runDescription: "Solicitar al nodo una copia de su plan aplicado.",
    backup_configure: "Configurar copias del nodo",
    backup_configureDescription:
      "Configurar el repositorio de copias del nodo para su plan actual.",
    candidate: "StackSpec propuesto (JSON)",
    target: "Digest de instantánea o cambios",
    anchor: "Digest de instantánea (opcional)",
    confirm: "Confirmar e iniciar",
    cancel: "Cancelar",
    approval:
      "Esta operación se ejecutará en este servidor. Revisa los cambios y el destino de recuperación antes de confirmar.",
    invalid: "Introduce un objeto StackSpec JSON o un digest sha256 exacto.",
    accepted:
      "Solicitud aceptada. La ejecución y verificación están pendientes.",
    running: "Iniciando…",
    observe: "Actualizar resultado",
    recorded: "El nodo informó una ejecución correcta.",
    unverified:
      "El trabajo terminó; no hay evidencia coincidente de éxito del nodo.",
    scopeMismatch: "El trabajo no pertenece al stack y nodo seleccionados.",
    loadFailed: "No se pudieron cargar las operaciones del servidor.",
    retry: "Reintentar",
  },
  hi: {
    title: "StackKit कार्रवाइयाँ",
    unavailable: "इस सर्वर के लिए कोई StackKit कार्रवाई स्वीकृत नहीं है।",
    advanced_change_set: "बदलाव लागू करें",
    advanced_change_setDescription:
      "Advanced Mode में प्रस्तावित StackSpec की योजना बनाकर लागू करें।",
    rollback: "वापस लौटाएँ",
    rollbackDescription: "दर्ज स्नैपशॉट या बदलाव सेट पर लौटें।",
    restore_drill: "पुनर्स्थापन अभ्यास चलाएँ",
    restore_drillDescription:
      "पुनर्स्थापन सक्रिय किए बिना इस नोड पर रिकवरी जाँचें।",
    backup_run: "बैकअप चलाएँ",
    backup_runDescription: "नोड से वर्तमान लागू योजना का बैकअप लेने को कहें।",
    backup_configure: "नोड बैकअप कॉन्फ़िगर करें",
    backup_configureDescription:
      "वर्तमान योजना के लिए नोड का बैकअप रिपॉज़िटरी कॉन्फ़िगर करें।",
    candidate: "प्रस्तावित StackSpec (JSON)",
    target: "स्नैपशॉट या बदलाव सेट का डाइजेस्ट",
    anchor: "स्नैपशॉट एंकर डाइजेस्ट (वैकल्पिक)",
    confirm: "पुष्टि करें और शुरू करें",
    cancel: "रद्द करें",
    approval:
      "यह कार्रवाई इस सर्वर पर चलेगी। पुष्टि से पहले बदलाव और रिकवरी लक्ष्य जाँचें।",
    invalid: "StackSpec JSON ऑब्जेक्ट या सटीक sha256 डाइजेस्ट दें।",
    accepted: "अनुरोध स्वीकार हुआ। निष्पादन और सत्यापन लंबित हैं।",
    running: "शुरू हो रहा है…",
    observe: "परिणाम अपडेट करें",
    recorded: "नोड ने सफल निष्पादन की सूचना दी।",
    unverified: "कार्य समाप्त हुआ; नोड का मेल खाता सफल प्रमाण उपलब्ध नहीं है।",
    scopeMismatch: "कार्य चुने हुए स्टैक और नोड का नहीं है।",
    loadFailed: "सर्वर कार्रवाइयाँ लोड नहीं हो सकीं।",
    retry: "फिर लोड करें",
  },
  ar: {
    title: "عمليات StackKit",
    unavailable: "لا توجد عمليات StackKit مسموحة لهذا الخادم حاليًا.",
    advanced_change_set: "تطبيق التغييرات",
    advanced_change_setDescription:
      "تخطيط StackSpec المقترح وتطبيقه عبر Advanced Mode.",
    rollback: "التراجع",
    rollbackDescription: "العودة إلى لقطة أو مجموعة تغييرات مسجلة محددة.",
    restore_drill: "اختبار الاستعادة",
    restore_drillDescription:
      "التحقق من التعافي على هذه العقدة دون تفعيل الاستعادة.",
    backup_run: "تشغيل نسخة احتياطية",
    backup_runDescription: "طلب نسخ الخطة المطبقة حاليًا على العقدة.",
    backup_configure: "إعداد نسخ العقدة الاحتياطية",
    backup_configureDescription:
      "إعداد مستودع النسخ الذي تديره العقدة لخطة العقدة الحالية.",
    candidate: "StackSpec المقترح (JSON)",
    target: "بصمة اللقطة أو مجموعة التغييرات",
    anchor: "بصمة مرساة اللقطة (اختياري)",
    confirm: "التأكيد والبدء",
    cancel: "إلغاء",
    approval:
      "ستبدأ العملية على هذا الخادم. راجع التغييرات وهدف التعافي قبل التأكيد.",
    invalid: "أدخل كائن StackSpec بصيغة JSON أو بصمة sha256 دقيقة.",
    accepted: "تم قبول الطلب. التنفيذ والتحقق قيد الانتظار.",
    running: "جارٍ البدء…",
    observe: "تحديث نتيجة المهمة",
    recorded: "أبلغت العقدة عن تنفيذ ناجح.",
    unverified: "انتهت المهمة؛ دليل نجاح مطابق من العقدة غير متاح.",
    scopeMismatch: "المهمة لا تنتمي إلى الحزمة والعقدة المحددتين.",
    loadFailed: "تعذر تحميل عمليات الخادم.",
    retry: "إعادة التحميل",
  },
  "zh-Hans": {
    title: "StackKit 操作",
    unavailable: "此服务器目前没有获准的 StackKit 操作。",
    advanced_change_set: "应用变更集",
    advanced_change_setDescription:
      "通过 Advanced Mode 规划并应用拟议的 StackSpec。",
    rollback: "回滚",
    rollbackDescription: "返回指定的已记录快照或变更集。",
    restore_drill: "运行恢复演练",
    restore_drillDescription: "在此节点验证恢复，但不激活恢复。",
    backup_run: "运行备份",
    backup_runDescription: "请求节点备份当前已应用的计划。",
    backup_configure: "配置节点备份",
    backup_configureDescription: "为当前计划配置由节点管理的备份仓库。",
    candidate: "拟议的 StackSpec（JSON）",
    target: "快照或变更集摘要",
    anchor: "快照锚点摘要（可选）",
    confirm: "确认并启动",
    cancel: "取消",
    approval: "此操作将在此服务器上启动。确认前请检查拟议变更和恢复目标。",
    invalid: "请输入 StackSpec JSON 对象或精确的 sha256 摘要。",
    accepted: "请求已接受。执行和验证尚待完成。",
    running: "正在启动…",
    observe: "刷新任务结果",
    recorded: "节点已报告执行成功。",
    unverified: "任务已结束，但缺少匹配的节点成功证据。",
    scopeMismatch: "此任务不属于所选栈和节点。",
    loadFailed: "无法加载服务器操作。",
    retry: "重新加载",
  },
} as const;
export type OperatorMessageId = keyof typeof messages.en;
export function operatorMessage(id: OperatorMessageId): string {
  return (messages[getLocale() as keyof typeof messages] ?? messages.en)[id];
}
