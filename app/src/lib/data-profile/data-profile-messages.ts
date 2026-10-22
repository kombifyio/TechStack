// Customer-visible strings of the data protection profile line in the account
// menu (workspace platform/standards/DATA-ARCHITECTURE.md §5.4: every product
// shows the active profile). "Zero Retention" and "Full Local" are product
// names and stay untranslated. Merged into the main dictionary by i18n.ts.
export const dataProfileMessages = {
  en: {
    "dataProfile.label": "Data protection",
    "dataProfile.standard": "Standard",
    "dataProfile.eu": "EU residency",
    "dataProfile.zero": "Zero Retention",
    "dataProfile.local": "Full Local",
    "dataProfile.unresolved": "Could not be confirmed",
    "dataProfile.unresolvedHint":
      "kombify could not confirm your data protection settings, so protected requests are paused. Try again in a moment.",
  },
  de: {
    "dataProfile.label": "Datenschutz",
    "dataProfile.standard": "Standard",
    "dataProfile.eu": "EU-Residenz",
    "dataProfile.zero": "Zero Retention",
    "dataProfile.local": "Full Local",
    "dataProfile.unresolved": "Nicht bestätigt",
    "dataProfile.unresolvedHint":
      "kombify konnte deine Datenschutz-Einstellungen nicht bestätigen, deshalb laufen geschützte Anfragen gerade nicht. Versuch es gleich noch einmal.",
  },
  es: {
    "dataProfile.label": "Protección de datos",
    "dataProfile.standard": "Estándar",
    "dataProfile.eu": "Residencia en la UE",
    "dataProfile.zero": "Zero Retention",
    "dataProfile.local": "Full Local",
    "dataProfile.unresolved": "No se pudo confirmar",
    "dataProfile.unresolvedHint":
      "kombify no pudo confirmar tu configuración de protección de datos, así que las solicitudes protegidas están en pausa. Vuelve a intentarlo en un momento.",
  },
  "zh-Hans": {
    "dataProfile.label": "数据保护",
    "dataProfile.standard": "标准",
    "dataProfile.eu": "欧盟数据驻留",
    "dataProfile.zero": "Zero Retention",
    "dataProfile.local": "Full Local",
    "dataProfile.unresolved": "无法确认",
    "dataProfile.unresolvedHint":
      "kombify 无法确认你的数据保护设置，因此受保护的请求已暂停。请稍后再试。",
  },
  hi: {
    "dataProfile.label": "डेटा सुरक्षा",
    "dataProfile.standard": "मानक",
    "dataProfile.eu": "EU में डेटा निवास",
    "dataProfile.zero": "Zero Retention",
    "dataProfile.local": "Full Local",
    "dataProfile.unresolved": "पुष्टि नहीं हो सकी",
    "dataProfile.unresolvedHint":
      "kombify आपकी डेटा सुरक्षा सेटिंग्स की पुष्टि नहीं कर सका, इसलिए सुरक्षित अनुरोध अभी रुके हुए हैं। थोड़ी देर में फिर से कोशिश करें।",
  },
  ar: {
    "dataProfile.label": "حماية البيانات",
    "dataProfile.standard": "قياسي",
    "dataProfile.eu": "الإقامة في الاتحاد الأوروبي",
    "dataProfile.zero": "Zero Retention",
    "dataProfile.local": "Full Local",
    "dataProfile.unresolved": "تعذّر التأكيد",
    "dataProfile.unresolvedHint":
      "تعذّر على kombify تأكيد إعدادات حماية بياناتك، لذلك توقفت الطلبات المحمية مؤقتًا. حاول مرة أخرى بعد قليل.",
  },
} as const;
