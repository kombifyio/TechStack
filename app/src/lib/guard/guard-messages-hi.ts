import type { guardMessages } from "./guard-messages";

// Hindi catalog of the kombify Guard surfaces. Same keys and placeholders as
// the English catalog.
export const guardMessagesHi: Record<keyof typeof guardMessages.en, string> = {
  "guard.backup.title": "दर्ज किए गए पुनर्प्राप्ति बिंदु",
  "guard.backup.load": "पुनर्प्राप्ति बिंदु पढ़ें",
  "guard.backup.loading": "पढ़ा जा रहा है…",
  "guard.backup.recorded":
    "ये बिंदु इस सर्वर ने दर्ज किए हैं। पुनर्स्थापना तैयार करते समय पुराने बैकअप की सामग्री जाँची जाती है।",
  "guard.backup.empty":
    "यह सर्वर चुनने योग्य पुराने पुनर्प्राप्ति बिंदु नहीं बताता है।",
  "guard.backup.choose": "पुनर्प्राप्ति बिंदु",
  "guard.backup.placeholder": "दर्ज किया गया बिंदु चुनें",
  "guard.backup.currentPlan": "वर्तमान योजना",
  "guard.backup.historicalPlan": "पुरानी योजना",
  "guard.backup.clockSkew": "घड़ी का अंतर",
  "guard.backup.stage": "अलग स्थान पर पुनर्स्थापना तैयार करें",
  "guard.backup.confirm":
    "क्या {when} का बिंदु अलग स्थान पर तैयार करना है? चालू अनुप्रयोग का डेटा नहीं बदला जाएगा।",
  "guard.backup.confirmAction": "यह पुनर्स्थापना तैयार करें",
  "guard.backup.running": "सर्वर अलग स्थान पर पुनर्स्थापना तैयार कर रहा है।",
  "guard.backup.staged":
    "पुनर्स्थापना अलग स्थान पर तैयार है। चालू अनुप्रयोग का डेटा नहीं बदला गया है।",
  "guard.backup.unverified":
    "कार्य समाप्त हुआ, लेकिन तैयार पुनर्स्थापना का मेल खाता प्रमाण नहीं मिला। अनुप्रयोग की पुनर्प्राप्ति सत्यापित नहीं है।",
  "guard.backup.failed": "बैकअप की प्रक्रिया पूरी नहीं हुई।",
  "guard.backup.job": "कार्य {id}",
  "guard.backup.scopeMismatch": "यह कार्य इस सर्वर और स्टैक का नहीं है।",
  "guard.name": "kombify Guard",
  "guard.backup.activate": "तैयार पुनर्स्थापना सक्रिय करें",
  "guard.backup.notAdmitted":
    "मूल अनुरोध स्वीकार नहीं हुआ। मौजूदा डेटा की पुनर्स्थापना शुरू नहीं हुई।",
  "guard.backup.historicalOperation":
    "यह कार्य पिछले इंस्टॉलेशन का है। इसका दर्ज परिणाम मौजूदा इंस्टॉलेशन की स्थिति नहीं बताता।",
  "guard.backup.activateConfirm":
    "{name} पर यह पुनर्स्थापना सक्रिय करें? इससे अनुप्रयोग का मौजूदा डेटा बदल जाएगा। सर्वर सुरक्षा स्नैपशॉट रखता है और सेवाओं तथा स्वामी की पहुँच की जाँच करता है।",
  "guard.backup.activateAction": "सक्रिय डेटा की पुनर्स्थापना की पुष्टि करें",
  "guard.backup.activationUnavailable":
    "इस सर्वर के मौजूदा प्रकाशित रनटाइम में सक्रिय करना उपलब्ध नहीं है। तैयार डेटा अलग ही रहता है।",
  "guard.backup.activating":
    "सर्वर स्वीकृत पुनर्स्थापना कार्य का मिलान कर रहा है।",
  "guard.backup.activated":
    "सर्वर ने चुनी हुई पुनर्स्थापना सक्रिय और सत्यापित की।",
  "guard.backup.recovered":
    "अनुप्रयोग की पिछली स्थिति बहाल हुई। चुनी हुई पुनर्स्थापना सक्रिय नहीं हुई।",
  "guard.backup.accessVerified":
    "सर्वर ने {when} पर सेवाओं और स्वामी की पहुँच सत्यापित की।",
  "guard.backup.recoveryRequired":
    "पुनर्स्थापना का परिणाम अपुष्ट है। दूसरी पुनर्स्थापना शुरू करने से पहले मूल कार्य सुलझाएँ।",
  "guard.backup.outcomeUnknown":
    "अभी मेल खाता अंतिम पुनर्स्थापना प्रमाण उपलब्ध नहीं है।",
  "guard.backup.checkOriginal": "मूल कार्य जाँचें",
  "guard.backup.resumeRequest": "मूल अनुरोध जारी रखें",
  "guard.backup.recover": "मूल कार्य की रिकवरी करें",
  "guard.backup.recoverConfirm":
    "मूल कार्य का मिलान करें? अधूरा होने पर सर्वर अनुप्रयोग की पिछली स्थिति बहाल करता है।",
  "guard.backup.recoverAction": "मूल रिकवरी की पुष्टि करें",
  "guard.promise":
    "इस सर्वर को आपसे बंधा हुआ रखने, लगातार जाँचने, कहीं से भी पहुँचने योग्य बनाने और डिज़ाइन से ही पुनर्प्राप्त करने योग्य रखने के लिए बनाया गया। नीचे के तथ्य दिखाते हैं कि अभी किस बात का प्रमाण है।",
  "guard.cancel": "रद्द करें",
  "guard.next": "अगला कदम:",
  "guard.observed": "देखा गया {when}",
  "guard.noObservation": "अवलोकन का समय रिपोर्ट नहीं हुआ",
  "guard.reduced": "घटी हुई सुरक्षा",
  "guard.serverUnavailable":
    "सर्वर का रिकॉर्ड पढ़ा नहीं जा सका, इसलिए अभी इनमें से किसी तथ्य का प्रमाण नहीं है।",
  "guard.state.ok": "प्रमाणित",
  "guard.state.attention": "ध्यान चाहिए",
  "guard.state.unknown": "प्रमाण नहीं",
  "guard.overall.ready": "Guard तैयार",
  "guard.overall.attention": "ध्यान चाहिए",
  "guard.overall.unverified": "अभी प्रमाण नहीं",
  "guard.overall.count": "{total} में से {ok} तथ्य प्रमाणित",
  "guard.badge.ready": "Guard तैयार",
  "guard.badge.attention": "Guard: ध्यान चाहिए",
  "guard.badge.unverified": "Guard: {ok}/{total} प्रमाणित",
  "guard.badge.open": "kombify Guard पैनल खोलें",
  "guard.detail.reasonCode": "कारण कोड: {code}",

  "guard.fact.bound.title": "बंधा हुआ",
  "guard.fact.connected.title": "जुड़ा हुआ",
  "guard.fact.operations.title": "टिकाऊ ऑपरेशन",
  "guard.fact.configuration.title": "कॉन्फ़िगरेशन सुरक्षित",
  "guard.fact.updated.title": "अद्यतन",
  "guard.fact.recoverable.title": "पुनर्प्राप्त करने योग्य",
  "guard.fact.reachable.title": "पहुँच योग्य और नियंत्रित",

  "guard.fact.bound.ok":
    "यह सर्वर हर कनेक्शन पर अपनी ही कुंजी से अपनी पहचान साबित करता है।",
  "guard.fact.bound.legacy":
    "यह सर्वर अब भी केवल बेयरर क्रेडेंशियल से अपनी पहचान देता है, जो यह साबित नहीं करता कि कौन-सी मशीन बात कर रही है।",
  "guard.fact.bound.legacyNext":
    "सर्वर पर Guard अपडेट करें ताकि वह अपनी कुंजी पंजीकृत करे, फिर उस कुंजी को यहाँ स्वीकृत करें।",
  "guard.fact.bound.revoked":
    "इस सर्वर को रद्द कर दिया गया है और अब यह बंधा हुआ नहीं है।",
  "guard.fact.bound.revokedNext":
    "इसे अपने Techstack से बाँधने के लिए सर्वर को फिर से पेयर करें।",
  "guard.fact.bound.stale":
    "पिछली रिपोर्ट इतनी पुरानी है कि यह पुष्टि नहीं हो सकती कि यह सर्वर अपनी पहचान कैसे साबित करता है।",
  "guard.fact.bound.unknown":
    "सर्वर ने रिपोर्ट नहीं किया कि वह अपनी पहचान कैसे साबित करता है।",
  "guard.fact.bound.unknownNext":
    "अगली रिपोर्ट की प्रतीक्षा करें। न आए तो सर्वर पर Guard अपडेट करें।",

  "guard.fact.connected.ok":
    "Guard ने अपने प्रमाणित कनेक्शन पर एक कमांड का उत्तर दिया।",
  "guard.fact.connected.degraded": "कनेक्शन की गुणवत्ता घटी हुई है।",
  "guard.fact.connected.offline":
    "Guard रिपोर्ट नहीं कर रहा। सर्वर ऑफ़लाइन है या Techstack तक नहीं पहुँच पा रहा।",
  "guard.fact.connected.revoked": "कनेक्शन रद्द कर दिया गया।",
  "guard.fact.connected.stale":
    "Guard ने हाल में इतनी ताज़ा रिपोर्ट नहीं दी कि उसे जुड़ा हुआ माना जाए।",
  "guard.fact.connected.pending":
    "Guard ने अभी अपना पहला कनेक्शन पूरा नहीं किया है।",
  "guard.fact.connected.pendingNext":
    "सर्वर पर पेयरिंग कमांड चलाएँ, या थोड़ी देर रुककर फिर जाँचें।",
  "guard.fact.connected.unknown":
    "इस सर्वर के लिए कोई कनेक्शन स्थिति रिपोर्ट नहीं हुई।",
  "guard.fact.connected.reconnectNext":
    "जाँचें कि सर्वर चालू है और Techstack तक पहुँच सकता है।",
  "guard.fact.connected.awaitingRoundTrip":
    "Guard रिपोर्ट कर रहा है, लेकिन उसने अपने वर्तमान क्रेडेंशियल से Techstack की किसी कमांड का अभी तक उत्तर नहीं दिया है।",
  "guard.fact.connected.roundTripStale":
    "Guard रिपोर्ट कर रहा है, लेकिन उसकी पिछली उत्तर दी गई कमांड बहुत पुरानी है या पिछले क्रेडेंशियल से दी गई थी।",
  "guard.fact.connected.roundTripNext":
    "इस सर्वर पर अगला ऑपरेशन कनेक्शन की पुष्टि करता है; केवल रिपोर्ट पर्याप्त नहीं हैं।",

  "guard.fact.operations.ok":
    "ऑपरेशन टिकाऊ ढंग से दर्ज होते हैं और कुछ भी अनसुलझा नहीं बचा।",
  "guard.fact.operations.unacked": "पुष्टि की प्रतीक्षा में परिणाम: {count}।",
  "guard.fact.operations.unresolved":
    "अज्ञात परिणाम वाले ऑपरेशन: {count}। वे इस सर्वर पर आगे के बदलाव रोकते हैं।",
  "guard.fact.operations.unresolvedNext":
    "उन्हें नीचे देखें और दर्ज करें कि आपने क्या जाँचा।",
  "guard.fact.operations.legacy":
    "यह Guard टिकाऊ ऑपरेशन जर्नल नहीं रखता, इसलिए बीच में रुका ऑपरेशन भरोसेमंद ढंग से रिपोर्ट नहीं हो सकता।",
  "guard.fact.operations.legacyNext": "सर्वर पर Guard अपडेट करें।",
  "guard.fact.operations.denied":
    "ऑपरेशन जर्नल में लिखा नहीं जा सकता, इसलिए बदलाव अस्वीकार किए जाते हैं।",
  "guard.fact.operations.deniedNext":
    "सर्वर पर खाली डिस्क स्थान और अनुमतियाँ जाँचें, फिर अगली रिपोर्ट की प्रतीक्षा करें।",
  "guard.fact.operations.stale":
    "पिछली रिपोर्ट इतनी पुरानी है कि जर्नल की स्थिति की पुष्टि नहीं हो सकती।",
  "guard.fact.operations.unknown":
    "सर्वर ने अपना ऑपरेशन जर्नल रिपोर्ट नहीं किया।",
  "guard.fact.operations.unknownNext":
    "अगली रिपोर्ट की प्रतीक्षा करें। न आए तो सर्वर पर Guard अपडेट करें।",

  "guard.fact.configuration.ok": "होस्ट सुरक्षा बेसलाइन से मेल खाता है।",
  "guard.fact.configuration.okExceptions":
    "स्वीकृत अपवादों को छोड़कर होस्ट सुरक्षा बेसलाइन से मेल खाता है।",
  "guard.fact.configuration.baseline": "बेसलाइन संस्करण {version}",
  "guard.fact.configuration.exceptions": "स्वीकृत अपवाद: {count}",
  "guard.fact.configuration.drifted": "बेसलाइन से भटके नियंत्रण: {count}।",
  "guard.fact.configuration.driftedNext":
    "नीचे के निष्कर्ष देखें और समाधान लागू करें।",
  "guard.fact.configuration.stale":
    "पिछली जाँच इतनी पुरानी है कि गिनी नहीं जा सकती। उसके बाद का कोई भटकाव दिखाई नहीं देगा।",
  "guard.fact.configuration.unknown": "पिछली जाँच स्थिति तय नहीं कर सकी।",
  "guard.fact.configuration.notReported":
    "इस सर्वर के लिए होस्ट-सुरक्षा जाँच रिपोर्ट नहीं हुई।",
  "guard.fact.configuration.notReportedNext":
    "सर्वर पर Guard और StackKit अपडेट करें ताकि वे इसे रिपोर्ट कर सकें।",
  "guard.fact.configuration.verifyNext": "सर्वर को फिर से जाँचें।",

  "guard.fact.updated.ok": "Guard और ऑपरेटिंग सिस्टम अद्यतन हैं।",
  "guard.fact.updated.attention": "कुछ अपडेट करना ज़रूरी है।",
  "guard.fact.updated.stale":
    "पिछली रिपोर्ट इतनी पुरानी है कि अपडेट स्थिति की पुष्टि नहीं हो सकती।",
  "guard.fact.updated.unknown": "अपडेट स्थिति का प्रमाण नहीं है।",
  "guard.fact.updated.unknownNext":
    "अगली रिपोर्ट की प्रतीक्षा करें, या सर्वर पर Guard अपडेट करें।",
  "guard.fact.updated.guardOk": "Guard {version} हस्ताक्षरित और अद्यतन है।",
  "guard.fact.updated.guardProblem":
    "Guard अपडेट स्थिति: {state} (संस्करण {version})।",
  "guard.fact.updated.guardProblemNext": "सर्वर पर Guard अपडेट की जाँच करें।",
  "guard.fact.updated.guardInProgress": "Guard अपडेट इंस्टॉल हो रहा है।",
  "guard.fact.updated.guardUnknown":
    "Guard का संस्करण और हस्ताक्षर रिपोर्ट नहीं हुए।",
  "guard.fact.updated.guardUnsigned":
    "इंस्टॉल किया गया Guard आर्टिफ़ैक्ट हस्ताक्षरित नहीं है।",
  "guard.fact.updated.osOk":
    "कोई लंबित सुरक्षा अपडेट नहीं और रीबूट की ज़रूरत नहीं।",
  "guard.fact.updated.osPending":
    "ऑपरेटिंग सिस्टम के लंबित सुरक्षा अपडेट: {count}।",
  "guard.fact.updated.osPendingNext": "लंबित सुरक्षा अपडेट इंस्टॉल करें।",
  "guard.fact.updated.osReboot": "अपडेट पूरे करने के लिए रीबूट ज़रूरी है।",
  "guard.fact.updated.osRebootNext": "सुविधा होने पर सर्वर रीबूट करें।",
  "guard.fact.updated.osUnknown":
    "ऑपरेटिंग सिस्टम की अपडेट स्थिति रिपोर्ट नहीं हुई।",
  "guard.fact.updated.lastRun": "पिछला स्वचालित सुरक्षा अपडेट रन: {at}",

  "guard.fact.recoverable.ok":
    "ताज़ा बैकअप मौजूद है और एक रिस्टोर की पुष्टि हो चुकी है।",
  "guard.fact.recoverable.attention":
    "बैकअप या रिस्टोर का प्रमाण पुराना पड़ चुका है।",
  "guard.fact.recoverable.unknown": "पुनर्प्राप्ति का पूरा प्रमाण नहीं है।",
  "guard.fact.recoverable.notReported":
    "कोई बैकअप या रिस्टोर प्रमाण रिपोर्ट नहीं हुआ।",
  "guard.fact.recoverable.notReportedNext":
    "इस सर्वर के लिए बैकअप कॉन्फ़िगर करें, फिर एक रिस्टोर जाँचें।",
  "guard.fact.recoverable.attentionNext":
    "नया बैकअप लें या रिस्टोर अभ्यास दोहराएँ।",
  "guard.fact.recoverable.unknownNext":
    "अगली रिपोर्ट की प्रतीक्षा करें, या एक रिस्टोर जाँचें।",
  "guard.fact.recoverable.backupFresh": "बैकअप नीति के भीतर है।",
  "guard.fact.recoverable.backupStale":
    "सबसे नया बैकअप नीति की अनुमति से अधिक पुराना है।",
  "guard.fact.recoverable.backupUnknown": "बैकअप की ताज़गी रिपोर्ट नहीं हुई।",
  "guard.fact.recoverable.drillVerified": "एक रिस्टोर की पुष्टि हो चुकी है।",
  "guard.fact.recoverable.drillStale":
    "पिछला जाँचा गया रिस्टोर पुराना हो चुका है।",
  "guard.fact.recoverable.drillUnknown":
    "कोई जाँचा गया रिस्टोर रिपोर्ट नहीं हुआ।",

  "guard.fact.reachable.ok":
    "यह सर्वर अपने kombify.me पते पर पंजीकृत है और kombify Connect में सूचीबद्ध है।",
  "guard.fact.reachable.address": "पता: {address}",
  "guard.fact.reachable.notReported":
    "इस सर्वर के लिए रिमोट पहुँच रिपोर्ट नहीं हुई।",
  "guard.fact.reachable.notReportedNext":
    "इंस्टॉलेशन का पता पंजीकृत करने के लिए उसे kombify Cloud से जोड़ें, या बाद में फिर जाँचें।",
  "guard.fact.reachable.notEnrolled":
    "यह सर्वर अभी रिमोट एक्सेस के लिए नामांकित नहीं है।",
  "guard.fact.reachable.pending": "रिमोट एक्सेस सेट हो रहा है।",
  "guard.fact.reachable.pendingNext": "थोड़ी देर रुककर फिर जाँचें।",
  "guard.fact.reachable.failed": "रिमोट एक्सेस सेट करना विफल रहा।",
  "guard.fact.reachable.failedNext":
    "थोड़ी देर में फिर जाँचें। बना रहे तो कारण कोड के साथ सहायता से संपर्क करें।",
  "guard.fact.reachable.revoked": "इस सर्वर का रिमोट एक्सेस रद्द कर दिया गया।",
  "guard.fact.reachable.revokedNext":
    "रिमोट एक्सेस बहाल करने के लिए सर्वर को फिर से पेयर करें।",
  "guard.fact.reachable.stale":
    "पिछली पहुँच रिपोर्ट इतनी पुरानी है कि यह पुष्टि नहीं हो सकती कि पता अब भी जवाब देता है।",
  "guard.fact.reachable.relayConnecting": "रिले कनेक्शन बन रहा है।",
  "guard.fact.reachable.relayDown":
    "Guard का रिले कनेक्शन बंद है, इसलिए पता जवाब नहीं देता।",
  "guard.fact.reachable.relayDownNext":
    "कारण कोड जाँचें और यह भी कि सर्वर इंटरनेट तक पहुँच सकता है।",

  "guard.fact.reachable.notEnabled":
    "यह सर्वर किसी kombify.me पते पर प्रकाशित नहीं है। जब तक आप इसे चालू नहीं करते, यह आपका चुनाव है।",
  "guard.fact.reachable.notEnabledNext":
    "किसी सेवा को प्रकाशित करने के लिए नीचे उसका नाम और वह पोर्ट बताएँ जिस पर वह सुनती है।",
  "guard.reach.publish.title": "kombify.me पते पर प्रकाशित करें",
  "guard.reach.publish.intro":
    "जब तक आप चालू नहीं करते, कुछ भी प्रकाशित नहीं होता। इंटरनेट पर कोई भी यह पता खोल सकता है, इसलिए केवल वही सेवा प्रकाशित करें जो खुद को सुरक्षित रखती हो।",
  "guard.reach.publish.service": "सेवा का नाम",
  "guard.reach.publish.port": "इस सर्वर पर पोर्ट",
  "guard.reach.publish.enable": "प्रकाशित करें",
  "guard.reach.publish.disable": "प्रकाशन बंद करें",
  "guard.reach.publish.current": "प्रकाशित: {service}, पोर्ट {port} पर।",
  "guard.reach.publish.stepUp":
    "प्रकाशित करने के लिए पिछले पाँच मिनट में दूसरे कारक के साथ नया साइन-इन चाहिए। फिर से साइन इन करें, फिर यह चरण दोहराएँ।",
  "guard.upgrade.title": "इस सर्वर की कुंजी स्वीकृत करें",
  "guard.upgrade.pending":
    "फ़िंगरप्रिंट {fingerprint} वाली एक कुंजी आपकी स्वीकृति की प्रतीक्षा में है। इसे तभी स्वीकृत करें जब Guard इस सर्वर पर वही फ़िंगरप्रिंट लॉग करे।",
  "guard.upgrade.none":
    "इस सर्वर ने अभी तक कोई कुंजी प्रस्तावित नहीं की है। सर्वर पर Guard अपडेट करें; उसकी कुंजी का फ़िंगरप्रिंट स्वीकृति के लिए यहाँ दिखेगा।",
  "guard.upgrade.approve": "कुंजी स्वीकृत करें",
  "guard.upgrade.reject": "कुंजी अस्वीकार करें",
  "guard.upgrade.stepUp":
    "स्वीकृति के लिए पिछले पाँच मिनट में दूसरे कारक के साथ नया साइन-इन चाहिए। फिर से साइन इन करें, फिर यह चरण दोहराएँ।",
  "guard.upgrade.done": "सर्वर की कुंजी स्वीकृत हो गई।",

  "guard.reach.reason.guard_key_not_bound":
    "सर्वर की अभी कोई बँधी हुई कुंजी नहीं है। Guard अपडेट करें ताकि वह अपनी कुंजी बाँधे।",
  "guard.reach.reason.connect_tenant_unmappable":
    "आपके खाते में अभी kombify Connect स्थान नहीं है, इसलिए सर्वर वहाँ सूचीबद्ध नहीं हो सकता।",
  "guard.reach.reason.host_proof_pending":
    "सर्वर के kombify Connect को अपनी पहचान साबित करने की प्रतीक्षा है। यह अक्सर अपने आप सुलझ जाता है।",
  "guard.reach.reason.host_proof_rejected":
    "kombify Connect ने सर्वर का पहचान प्रमाण अस्वीकार कर दिया। सर्वर को फिर से पेयर करें।",
  "guard.reach.reason.kombify_me_unavailable":
    "kombify.me सेवा उपलब्ध नहीं है। थोड़ी देर में फिर जाँचें।",
  "guard.reach.reason.kombify_me_denied":
    "kombify.me ने इस सर्वर का पंजीकरण अस्वीकार कर दिया।",
  "guard.reach.reason.connect_unavailable":
    "kombify Connect उपलब्ध नहीं है। थोड़ी देर में फिर जाँचें।",
  "guard.reach.reason.connect_denied":
    "kombify Connect ने इस सर्वर को सूचीबद्ध करने से इनकार कर दिया।",
  "guard.reach.reason.reach_layout_unsupported":
    "इस इंस्टॉलेशन लेआउट को अभी रिमोट पता नहीं दिया जा सकता।",
  "guard.reach.reason.revocation_incomplete":
    "पहुँच समाप्त करना अभी हर जगह पूरा नहीं हुआ। इसे दोबारा आज़माया जाता है।",
  "guard.reach.reason.identity_unresolved":
    "इस सर्वर के स्वामी की पहचान तय नहीं हो सकी।",

  "guard.notice.legacyRoot":
    "इस सर्वर पर Guard अब भी पूरी तरह root के रूप में चलता है; सख़्त, बिना विशेषाधिकार वाला लेआउट सक्रिय नहीं है। सर्वर पर Guard अपडेट करें।",
  "guard.notice.executorUnavailable":
    "Guard अपने विशेषाधिकार प्राप्त एक्ज़ीक्यूटर के बिना चलता है, इसलिए होस्ट में बदलाव उपलब्ध नहीं हैं।",
  "guard.notice.mutationsDenied":
    "एक विफल अपडेट के बाद कोई उपयोगी Guard नहीं बचा, इसलिए समाधान होने तक बदलाव अस्वीकार किए जाते हैं।",

  "guard.findings.title": "सुरक्षा निष्कर्ष",
  "guard.findings.control": "नियंत्रण",
  "guard.findings.severity": "गंभीरता: {severity}",
  "guard.findings.severity.critical": "गंभीर",
  "guard.findings.severity.high": "उच्च",
  "guard.findings.severity.medium": "मध्यम",
  "guard.findings.severity.low": "निम्न",
  "guard.findings.state.compliant": "अनुपालित",
  "guard.findings.state.drifted": "भटका हुआ",
  "guard.findings.state.unknown": "अज्ञात",
  "guard.findings.state.exception": "स्वीकृत अपवाद",
  "guard.findings.state.approved_exception": "स्वीकृत अपवाद",
  "guard.findings.expected": "अपेक्षित",
  "guard.findings.observed": "देखा गया",
  "guard.findings.remediation": "समाधान:",
  "guard.findings.exception": "स्वीकृत अपवाद।",
  "guard.findings.exceptionWith": "स्वीकृत अपवाद: {detail}",
  "guard.findings.approvedBy": "{name} द्वारा स्वीकृत",
  "guard.findings.until": "{at} तक",

  "guard.verify.action": "अभी जाँचें",
  "guard.verify.running": "जाँच हो रही है…",
  "guard.verify.accepted":
    "{at} पर जाँच का अनुरोध किया गया (जॉब {job})। सर्वर के परिणाम रिपोर्ट करने पर तथ्य अपडेट हो जाते हैं।",
  "guard.verify.failed": "जाँच शुरू नहीं हो सकी: {message}",

  "guard.rotate.action": "क्रेडेंशियल बदलें",
  "guard.rotate.generationUnknown":
    "बदलने के लिए क्रेडेंशियल की वर्तमान पीढ़ी चाहिए, जो अभी रिपोर्ट नहीं हुई।",
  "guard.rotate.confirmTitle": "इस सर्वर का क्रेडेंशियल बदलें?",
  "guard.rotate.confirmBody":
    "एक नया क्रेडेंशियल जारी होगा और केवल एक बार दिखाया जाएगा। उसे सर्वर पर कॉपी करें ताकि Guard उसे इस्तेमाल कर सके। पुराना क्रेडेंशियल स्वीकार होना बंद हो जाएगा।",
  "guard.rotate.confirm": "क्रेडेंशियल बदलें",
  "guard.rotate.running": "बदला जा रहा है…",
  "guard.rotate.done": "नया क्रेडेंशियल जारी हुआ (पीढ़ी {generation})।",
  "guard.rotate.tokenOnce":
    "यह केवल एक बार दिखता है और सहेजा नहीं जाता। अभी कॉपी करें और सर्वर पर Guard को दें।",
  "guard.rotate.copy": "क्रेडेंशियल कॉपी करें",
  "guard.rotate.copied": "कॉपी हो गया",
  "guard.rotate.copyFailed":
    "कॉपी नहीं हो सका। टेक्स्ट चुनकर हाथ से कॉपी करें।",
  "guard.rotate.dismiss": "मैंने इसे सहेज लिया है",

  "guard.detach.action": "सर्वर अलग करें",
  "guard.detach.confirmTitle": "{name} को अलग करें?",
  "guard.detach.confirmBody":
    "इससे Guard का क्रेडेंशियल रद्द होता है और सर्वर की पहुँच समाप्त होती है: उसका kombify.me पता और kombify Connect में उसकी सूची। मशीन और उस पर चल रही चीज़ों को छुआ नहीं जाता।",
  "guard.detach.reverse":
    "यह यहाँ से वापस नहीं किया जा सकता। सर्वर को फिर से प्रबंधित करने के लिए उसे दोबारा पेयर करें।",
  "guard.detach.confirm": "अलग करें और रद्द करें",
  "guard.detach.running": "अलग किया जा रहा है…",

  "guard.connect.link": "kombify Connect में डिवाइस",

  "guard.report.download": "रिपोर्ट डाउनलोड करें (JSON)",
  "guard.report.print": "प्रिंट योग्य रिपोर्ट",
  "guard.report.failed": "रिपोर्ट बनाई नहीं जा सकी।",
  "guard.report.title": "kombify Guard रिपोर्ट",
  "guard.report.generated": "बनाई गई {at}",
  "guard.report.facts": "तथ्य",
  "guard.report.colFact": "तथ्य",
  "guard.report.colState": "स्थिति",
  "guard.report.colEvidence": "प्रमाण",
  "guard.report.colObserved": "देखा गया",
  "guard.report.noObservation": "अवलोकन का समय नहीं",
  "guard.report.notices": "घटी हुई सुरक्षा",
  "guard.report.findings": "सुरक्षा निष्कर्ष",
  "guard.report.noFindings": "कोई निष्कर्ष रिपोर्ट नहीं हुआ।",
  "guard.report.note":
    'यह रिपोर्ट केवल वही सूचीबद्ध करती है जो कंट्रोल प्लेन ने सहेजा है, हर तथ्य के देखे जाने के समय के साथ। ताज़ा प्रमाण के बिना कोई तथ्य "प्रमाण नहीं" दिखता है, उसे कभी ठीक नहीं माना जाता। इसमें कोई क्रेडेंशियल नहीं है।',
  "guard.report.digest": "सामग्री डाइजेस्ट",

  "guard.outcomes.title": "अज्ञात परिणाम वाले ऑपरेशन",
  "guard.outcomes.intro":
    "Guard ने दर्ज किया कि ये ऑपरेशन असर में आए भी हो सकते हैं और नहीं भी। हर एक इस सर्वर पर आगे के बदलाव तब तक रोकता है जब तक वह सुलझ न जाए या आप उसे मुक्त न करें।",
  "guard.outcomes.reconciling": "Guard अब भी इसे सुलझाने की कोशिश कर रहा है",
  "guard.outcomes.needsOperator": "आपका निर्णय चाहिए",
  "guard.outcomes.since": "{at} से अनसुलझा",
  "guard.outcomes.resolve": "सुलझाएँ",
  "guard.outcomes.dispositionLabel": "आपने क्या पाया?",
  "guard.outcomes.disposition.verified_applied": "बदलाव लागू है",
  "guard.outcomes.disposition.verified_applied.hint":
    "मैंने सर्वर जाँचा: ऑपरेशन असर में आया।",
  "guard.outcomes.disposition.verified_not_applied": "कुछ नहीं बदला",
  "guard.outcomes.disposition.verified_not_applied.hint":
    "मैंने सर्वर जाँचा: ऑपरेशन असर में नहीं आया।",
  "guard.outcomes.disposition.accept_unknown": "बिना जाँचे मुक्त करें",
  "guard.outcomes.disposition.accept_unknown.hint":
    "मैं जाँच नहीं सका। परिणाम अज्ञात रहता है और स्वीकृत के रूप में दर्ज होता है।",
  "guard.outcomes.noteLabel": "आपने क्या जाँचा?",
  "guard.outcomes.noteHint":
    "आवश्यक, अधिकतम {max} अक्षर। यह आपके नाम के साथ दर्ज होता है।",
  "guard.outcomes.review": "समीक्षा करें",
  "guard.outcomes.cancel": "रद्द करें",
  "guard.outcomes.confirmTitle": "इस ऑपरेशन को मुक्त करें?",
  "guard.outcomes.confirmBody":
    "इससे इस सर्वर पर लगी रोक हट जाती है। यदि परिणाम वैसा नहीं था जैसा आपने दर्ज किया, तो टकराने वाले बदलाव इस पर चल सकते हैं।",
  "guard.outcomes.confirm": "मुक्त करें",
  "guard.outcomes.releasing": "मुक्त किया जा रहा है…",
  "guard.outcomes.back": "वापस",
  "guard.outcomes.stepUp":
    "परिणाम मुक्त करने के लिए दूसरे फ़ैक्टर के साथ ताज़ा साइन-इन चाहिए, पिछले पाँच मिनट के भीतर का। फिर से साइन इन करें, फिर यह चरण दोहराएँ। आपका नोट सहेजा नहीं जाता।",
  "guard.outcomes.stepUpAction": "फिर से साइन इन करें",
  "guard.outcomes.stale":
    "आपकी समीक्षा के दौरान यह ऑपरेशन बदल गया। सूची अपडेट हो गई है; इसे फिर से देखें।",
  "guard.outcomes.loadFailed": "अनसुलझे ऑपरेशनों की सूची लोड नहीं हो सकी।",
  "guard.outcomes.receipt":
    "{command} को संशोधन {revision} पर “{disposition}” के रूप में मुक्त किया गया, {actor} के नाम दर्ज।",
  "guard.outcomes.fenceReleased": "इस सर्वर पर लगी रोक हट गई है।",

  "guard.repair.title": "इस सर्वर को फिर से पेयर करें",
  "guard.repair.body":
    "जब तक इस सर्वर को फिर से पेयर नहीं किया जाता, बदलने वाले कमांड अस्वीकृत रहते हैं। अवलोकन चलते रहते हैं।",
  "guard.repair.action": "इस सर्वर को फिर से पेयर करें",
  "guard.repair.preparing": "पेयरिंग कमांड तैयार हो रहा है…",
  "guard.repair.command":
    "यह पेयरिंग कमांड सर्वर पर चलाएँ। यह सर्वर को फिर से पेयर करता है और बाइंडिंग कुंजी को पिन करता है।",
  "guard.repair.copy": "कॉपी करें",
  "guard.repair.copied": "कॉपी हो गया",
  "guard.repair.unavailable": "पेयरिंग कमांड तैयार नहीं हो सका।",
  "guard.repair.failed": "पेयरिंग कमांड तैयार नहीं हो सका।",

  "guard.ladder.title": "पेयरिंग की तैयारी",
  "guard.ladder.intro":
    "हर चरण को कंट्रोल प्लेन के दर्ज किए हुए से जाँचा जाता है, मान नहीं लिया जाता।",
  "guard.ladder.bound": "बंधा हुआ",
  "guard.ladder.connected": "जुड़ा हुआ",
  "guard.ladder.configuration": "कॉन्फ़िगरेशन जाँचा गया",
  "guard.ladder.reachable": "पहुँच योग्य",
  "guard.ladder.state.done": "पूरा",
  "guard.ladder.state.failed": "ध्यान चाहिए",
  "guard.ladder.state.waiting": "जारी है",
  "guard.ladder.state.blocked": "पिछले चरण की प्रतीक्षा",
  "guard.ladder.blockedHint": "पिछला चरण पूरा होने पर शुरू होगा।",
  "guard.ladder.complete": "पेयरिंग के सभी चरणों का प्रमाण है।",
  "guard.ladder.bound.awaiting": "सर्वर पर पेयरिंग कमांड चलने की प्रतीक्षा है।",
  "guard.ladder.bound.awaitingNext":
    "सर्वर पर कमांड चलाएँ। समय समाप्त हो गया हो तो नया बनाएँ।",
};
