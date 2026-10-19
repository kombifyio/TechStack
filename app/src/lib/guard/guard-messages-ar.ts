import type { guardMessages } from "./guard-messages";

// Arabic catalog of the kombify Guard surfaces (right-to-left). Same keys and
// placeholders as the English catalog.
export const guardMessagesAr: Record<keyof typeof guardMessages.en, string> = {
  "guard.backup.title": "نقاط الاستعادة المسجلة",
  "guard.backup.load": "قراءة نقاط الاستعادة",
  "guard.backup.loading": "جارٍ القراءة…",
  "guard.backup.recorded":
    "سجّل هذا الخادم هذه النقاط. يتم التحقق من محتويات النسخ السابقة عند تحضير الاستعادة.",
  "guard.backup.empty":
    "لا يبلغ هذا الخادم عن نقاط استعادة سابقة قابلة للاختيار.",
  "guard.backup.choose": "نقطة الاستعادة",
  "guard.backup.placeholder": "اختر نقطة مسجلة",
  "guard.backup.currentPlan": "الخطة الحالية",
  "guard.backup.historicalPlan": "الخطة السابقة",
  "guard.backup.clockSkew": "اختلاف الساعة",
  "guard.backup.stage": "تحضير استعادة معزولة",
  "guard.backup.confirm":
    "هل تريد تحضير النقطة المسجلة بتاريخ {when} في مساحة معزولة؟ لن يتم استبدال بيانات التطبيق الحالية.",
  "guard.backup.confirmAction": "تحضير هذه الاستعادة",
  "guard.backup.running": "يقوم الخادم بتحضير استعادة معزولة.",
  "guard.backup.staged":
    "تم تحضير الاستعادة في مساحة معزولة. لم يتم استبدال بيانات التطبيق الحالية.",
  "guard.backup.unverified":
    "اكتملت المهمة دون دليل مطابق على استعادة محضرة. لم يتم التحقق من استعادة التطبيق.",
  "guard.backup.failed": "لم تكتمل عملية النسخ الاحتياطي.",
  "guard.backup.job": "المهمة {id}",
  "guard.backup.scopeMismatch": "هذه المهمة لا تخص هذا الخادم والمكدس.",
  "guard.name": "kombify Guard",
  "guard.backup.activate": "تفعيل الاستعادة المُعدّة",
  "guard.backup.notAdmitted":
    "لم يُقبل الطلب الأصلي. لم تبدأ استعادة البيانات الحالية.",
  "guard.backup.historicalOperation":
    "تخص هذه العملية تثبيتًا سابقًا. نتيجتها المسجّلة لا تصف التثبيت الحالي.",
  "guard.backup.activateConfirm":
    "هل تريد تفعيل هذه الاستعادة على {name}؟ ستستبدل بيانات التطبيق الحالية. يحتفظ الخادم بلقطة أمان ويتحقق من الخدمات ووصول المالك.",
  "guard.backup.activateAction": "تأكيد استعادة البيانات الحالية",
  "guard.backup.activationUnavailable":
    "التفعيل غير متاح مع إصدار التشغيل المنشور الحالي لهذا الخادم. تبقى البيانات المُعدّة معزولة.",
  "guard.backup.activating": "يراجع الخادم عملية الاستعادة المعتمدة.",
  "guard.backup.activated": "فعّل الخادم الاستعادة المختارة وتحقق منها.",
  "guard.backup.recovered":
    "استُعيدت حالة التطبيق السابقة. لم تُفعّل الاستعادة المختارة.",
  "guard.backup.accessVerified":
    "تحقق الخادم من الخدمات ووصول المالك في {when}.",
  "guard.backup.recoveryRequired":
    "لم تُؤكّد نتيجة الاستعادة بعد. عالج العملية الأصلية قبل بدء استعادة أخرى.",
  "guard.backup.outcomeUnknown": "لا يوجد حتى الآن دليل نهائي مطابق للاستعادة.",
  "guard.backup.checkOriginal": "فحص العملية الأصلية",
  "guard.backup.resumeRequest": "متابعة الطلب الأصلي",
  "guard.backup.recover": "استرداد العملية الأصلية",
  "guard.backup.recoverConfirm":
    "هل تريد مراجعة العملية الأصلية؟ إذا كانت غير مكتملة، يستعيد الخادم حالة التطبيق السابقة.",
  "guard.backup.recoverAction": "تأكيد الاسترداد الأصلي",
  "guard.promise":
    "مصمَّم ليبقي هذا الخادم مرتبطًا بك، ويُفحص باستمرار، ويمكن الوصول إليه من أي مكان، ويمكن استعادته بحكم التصميم. تعرض الحقائق أدناه ما هو مثبت الآن.",
  "guard.cancel": "إلغاء",
  "guard.next": "الخطوة التالية:",
  "guard.observed": "رُصد {when}",
  "guard.noObservation": "لم يُبلَّغ عن وقت الرصد",
  "guard.reduced": "حماية مخفَّضة",
  "guard.serverUnavailable":
    "تعذّرت قراءة سجل الخادم، لذلك لا تُثبت أي من هذه الحقائق الآن.",
  "guard.state.ok": "مثبت",
  "guard.state.attention": "يحتاج إلى انتباه",
  "guard.state.unknown": "غير مثبت",
  "guard.overall.ready": "Guard جاهز",
  "guard.overall.attention": "يحتاج إلى انتباه",
  "guard.overall.unverified": "لم يُثبت بعد",
  "guard.overall.count": "{ok} من {total} حقائق مثبتة",
  "guard.badge.ready": "Guard جاهز",
  "guard.badge.attention": "Guard: يحتاج إلى انتباه",
  "guard.badge.unverified": "Guard: {ok}/{total} مثبتة",
  "guard.badge.open": "افتح لوحة kombify Guard",
  "guard.detail.reasonCode": "رمز السبب: {code}",

  "guard.fact.bound.title": "مرتبط",
  "guard.fact.connected.title": "متصل",
  "guard.fact.operations.title": "عمليات دائمة",
  "guard.fact.configuration.title": "إعدادات مؤمَّنة",
  "guard.fact.updated.title": "محدَّث",
  "guard.fact.recoverable.title": "قابل للاستعادة",
  "guard.fact.reachable.title": "يمكن الوصول إليه ومحكوم",

  "guard.fact.bound.ok": "يثبت هذا الخادم هويته بمفتاحه الخاص في كل اتصال.",
  "guard.fact.bound.legacy":
    "ما زال هذا الخادم يعرّف نفسه ببيانات اعتماد حامل فقط، وهي لا تثبت أي جهاز يتحدث.",
  "guard.fact.bound.legacyNext":
    "حدّث Guard على الخادم ليسجّل مفتاحه الخاص، ثم وافق على ذلك المفتاح هنا.",
  "guard.fact.bound.revoked": "أُلغي هذا الخادم ولم يعد مرتبطًا.",
  "guard.fact.bound.revokedNext":
    "أعد إقران الخادم لربطه بـ Techstack الخاص بك.",
  "guard.fact.bound.stale":
    "آخر تقرير قديم جدًا ولا يمكن معه تأكيد كيف يثبت هذا الخادم هويته.",
  "guard.fact.bound.unknown": "لم يُبلِّغ الخادم كيف يثبت هويته.",
  "guard.fact.bound.unknownNext":
    "انتظر التقرير التالي. إن لم يظهر فحدّث Guard على الخادم.",

  "guard.fact.connected.ok": "يُبلِّغ Guard عبر اتصاله الموثَّق.",
  "guard.fact.connected.degraded": "الاتصال متدهور.",
  "guard.fact.connected.offline":
    "لا يُبلِّغ Guard. الخادم غير متصل أو لا يستطيع الوصول إلى Techstack.",
  "guard.fact.connected.revoked": "أُلغي الاتصال.",
  "guard.fact.connected.stale":
    "لم يُبلِّغ Guard مؤخرًا بما يكفي لاعتباره متصلًا.",
  "guard.fact.connected.pending": "لم يُكمل Guard اتصاله الأول بعد.",
  "guard.fact.connected.pendingNext":
    "شغّل أمر الإقران على الخادم، أو انتظر قليلًا ثم تحقق مجددًا.",
  "guard.fact.connected.unknown":
    "لا توجد حالة اتصال مُبلَّغ عنها لهذا الخادم.",
  "guard.fact.connected.reconnectNext":
    "تأكد من أن الخادم يعمل ويستطيع الوصول إلى Techstack.",

  "guard.fact.operations.ok":
    "تُسجَّل العمليات بشكل دائم ولا يبقى شيء دون حسم.",
  "guard.fact.operations.unacked": "نتائج تنتظر التأكيد: {count}.",
  "guard.fact.operations.unresolved":
    "عمليات نتيجتها مجهولة: {count}. إنها تمنع المزيد من التغييرات على هذا الخادم.",
  "guard.fact.operations.unresolvedNext": "راجعها أدناه وسجّل ما تحققت منه.",
  "guard.fact.operations.legacy":
    "لا يحتفظ هذا Guard بسجل عمليات دائم، لذا لا يمكن الإبلاغ بموثوقية عن عملية انقطعت.",
  "guard.fact.operations.legacyNext": "حدّث Guard على الخادم.",
  "guard.fact.operations.denied":
    "سجل العمليات غير قابل للكتابة، لذا تُرفض التغييرات.",
  "guard.fact.operations.deniedNext":
    "تحقق من المساحة الحرة على القرص والأذونات على الخادم، ثم انتظر التقرير التالي.",
  "guard.fact.operations.stale":
    "آخر تقرير قديم جدًا ولا يمكن معه تأكيد حالة السجل.",
  "guard.fact.operations.unknown": "لم يُبلِّغ الخادم عن سجل عملياته.",
  "guard.fact.operations.unknownNext":
    "انتظر التقرير التالي. إن لم يظهر فحدّث Guard على الخادم.",

  "guard.fact.configuration.ok": "يطابق المضيف خط الأساس الأمني.",
  "guard.fact.configuration.okExceptions":
    "يطابق المضيف خط الأساس الأمني باستثناء الاستثناءات المعتمدة.",
  "guard.fact.configuration.baseline": "إصدار خط الأساس {version}",
  "guard.fact.configuration.exceptions": "الاستثناءات المعتمدة: {count}",
  "guard.fact.configuration.drifted": "ضوابط تحيد عن خط الأساس: {count}.",
  "guard.fact.configuration.driftedNext": "راجع النتائج أدناه وطبّق المعالجة.",
  "guard.fact.configuration.stale":
    "آخر تحقق قديم جدًا ولا يُحتسب. لن يظهر أي انحراف بعده.",
  "guard.fact.configuration.unknown": "تعذّر على آخر تحقق تحديد الحالة.",
  "guard.fact.configuration.notReported":
    "لا يُبلَّغ عن التحقق من أمان المضيف لهذا الخادم.",
  "guard.fact.configuration.notReportedNext":
    "حدّث Guard وStackKit على الخادم ليتمكنا من الإبلاغ عنه.",
  "guard.fact.configuration.verifyNext": "تحقق من الخادم مرة أخرى.",

  "guard.fact.updated.ok": "Guard ونظام التشغيل محدَّثان.",
  "guard.fact.updated.attention": "هناك ما يحتاج إلى تحديث.",
  "guard.fact.updated.stale":
    "آخر تقرير قديم جدًا ولا يمكن معه تأكيد حالة التحديث.",
  "guard.fact.updated.unknown": "حالة التحديث غير مثبتة.",
  "guard.fact.updated.unknownNext":
    "انتظر التقرير التالي، أو حدّث Guard على الخادم.",
  "guard.fact.updated.guardOk": "Guard {version} موقَّع ومحدَّث.",
  "guard.fact.updated.guardProblem":
    "حالة تحديث Guard: {state} (الإصدار {version}).",
  "guard.fact.updated.guardProblemNext": "افحص تحديث Guard على الخادم.",
  "guard.fact.updated.guardInProgress": "يجري تثبيت تحديث لـ Guard.",
  "guard.fact.updated.guardUnknown": "لا يُبلَّغ عن إصدار Guard وتوقيعه.",
  "guard.fact.updated.guardUnsigned": "مكوّن Guard المثبَّت غير موقَّع.",
  "guard.fact.updated.osOk":
    "لا تحديثات أمنية معلّقة ولا حاجة إلى إعادة تشغيل.",
  "guard.fact.updated.osPending":
    "تحديثات أمان نظام التشغيل المعلّقة: {count}.",
  "guard.fact.updated.osPendingNext": "ثبّت تحديثات الأمان المعلّقة.",
  "guard.fact.updated.osReboot": "يلزم إعادة التشغيل لإكمال التحديثات.",
  "guard.fact.updated.osRebootNext": "أعد تشغيل الخادم عندما يناسبك ذلك.",
  "guard.fact.updated.osUnknown": "لا يُبلَّغ عن حالة تحديث نظام التشغيل.",
  "guard.fact.updated.lastRun": "آخر تشغيل غير مراقَب لتحديثات الأمان: {at}",

  "guard.fact.recoverable.ok":
    "توجد نسخة احتياطية حديثة وتم التحقق من استعادة.",
  "guard.fact.recoverable.attention":
    "دليل النسخ الاحتياطي أو الاستعادة متأخر.",
  "guard.fact.recoverable.unknown": "القدرة على الاستعادة غير مثبتة بالكامل.",
  "guard.fact.recoverable.notReported":
    "لا يُبلَّغ عن أي دليل نسخ احتياطي أو استعادة.",
  "guard.fact.recoverable.notReportedNext":
    "اضبط النسخ الاحتياطي لهذا الخادم ثم تحقق من استعادة.",
  "guard.fact.recoverable.attentionNext":
    "خذ نسخة احتياطية جديدة أو أعد تمرين الاستعادة.",
  "guard.fact.recoverable.unknownNext":
    "انتظر التقرير التالي، أو تحقق من استعادة.",
  "guard.fact.recoverable.backupFresh": "النسخة الاحتياطية ضمن السياسة.",
  "guard.fact.recoverable.backupStale":
    "أحدث نسخة احتياطية أقدم مما تسمح به السياسة.",
  "guard.fact.recoverable.backupUnknown":
    "لا يُبلَّغ عن حداثة النسخة الاحتياطية.",
  "guard.fact.recoverable.drillVerified": "تم التحقق من استعادة.",
  "guard.fact.recoverable.drillStale": "آخر استعادة مُتحقَّق منها قديمة.",
  "guard.fact.recoverable.drillUnknown": "لا يُبلَّغ عن استعادة مُتحقَّق منها.",

  "guard.fact.reachable.ok":
    "هذا الخادم مسجَّل على عنوانه في kombify.me ومدرج في kombify Connect.",
  "guard.fact.reachable.address": "العنوان: {address}",
  "guard.fact.reachable.notReported":
    "لا يُبلَّغ عن إمكانية الوصول عن بُعد لهذا الخادم.",
  "guard.fact.reachable.notReportedNext":
    "اربط التثبيت بـ kombify Cloud لتسجيل عنوانه، أو تحقق لاحقًا.",
  "guard.fact.reachable.notEnrolled":
    "لم يُسجَّل هذا الخادم للوصول عن بُعد بعد.",
  "guard.fact.reachable.pending": "يجري إعداد الوصول عن بُعد.",
  "guard.fact.reachable.pendingNext": "انتظر قليلًا ثم تحقق مجددًا.",
  "guard.fact.reachable.failed": "فشل إعداد الوصول عن بُعد.",
  "guard.fact.reachable.failedNext":
    "تحقق مجددًا بعد قليل. إن استمرت المشكلة فتواصل مع الدعم مع رمز السبب.",
  "guard.fact.reachable.revoked": "أُلغي الوصول عن بُعد لهذا الخادم.",
  "guard.fact.reachable.revokedNext":
    "أعد إقران الخادم لاستعادة الوصول عن بُعد.",
  "guard.fact.reachable.stale":
    "آخر تقرير وصول قديم جدًا ولا يمكن معه تأكيد أن العنوان ما زال يستجيب.",
  "guard.fact.reachable.relayConnecting": "يجري إنشاء اتصال المرحِّل.",
  "guard.fact.reachable.relayDown":
    "اتصال المرحِّل الخاص بـ Guard متوقف، لذا لا يستجيب العنوان.",
  "guard.fact.reachable.relayDownNext":
    "تحقق من رمز السبب ومن قدرة الخادم على الوصول إلى الإنترنت.",

  "guard.fact.reachable.notEnabled":
    "هذا الخادم غير منشور على عنوان kombify.me. هذا قرارك إلى أن تفعّل ذلك.",
  "guard.fact.reachable.notEnabledNext":
    "لنشر خدمة، حدّد أدناه اسمها والمنفذ الذي تستمع عليه.",
  "guard.reach.publish.title": "النشر على عنوان kombify.me",
  "guard.reach.publish.intro":
    "لا يُنشر شيء حتى تفعّل ذلك. يستطيع أي شخص على الإنترنت فتح العنوان، لذا انشر فقط خدمة تحمي نفسها.",
  "guard.reach.publish.service": "اسم الخدمة",
  "guard.reach.publish.port": "المنفذ على هذا الخادم",
  "guard.reach.publish.enable": "نشر",
  "guard.reach.publish.disable": "إيقاف النشر",
  "guard.reach.publish.current": "منشور: {service} على المنفذ {port}.",
  "guard.reach.publish.stepUp":
    "يتطلب النشر تسجيل دخول حديثًا بعامل ثانٍ خلال آخر خمس دقائق. سجّل الدخول مجددًا ثم كرر هذه الخطوة.",
  "guard.upgrade.title": "الموافقة على مفتاح هذا الخادم",
  "guard.upgrade.pending":
    "مفتاح بالبصمة {fingerprint} ينتظر موافقتك. وافق عليه فقط إذا سجّل Guard البصمة نفسها على هذا الخادم.",
  "guard.upgrade.none":
    "لم يقترح هذا الخادم مفتاحًا بعد. حدّث Guard على الخادم؛ ستظهر بصمة مفتاحه هنا لتوافق عليها.",
  "guard.upgrade.approve": "الموافقة على المفتاح",
  "guard.upgrade.reject": "رفض المفتاح",
  "guard.upgrade.stepUp":
    "تتطلب الموافقة تسجيل دخول حديثًا بعامل ثانٍ خلال آخر خمس دقائق. سجّل الدخول مجددًا ثم كرر هذه الخطوة.",
  "guard.upgrade.done": "تمت الموافقة على مفتاح الخادم.",

  "guard.reach.reason.guard_key_not_bound":
    "ليس للخادم مفتاح مرتبط بعد. حدّث Guard ليربط مفتاحه الخاص.",
  "guard.reach.reason.connect_tenant_unmappable":
    "ليس لحسابك مساحة في kombify Connect بعد، لذا لا يمكن إدراج الخادم هناك.",
  "guard.reach.reason.host_proof_pending":
    "في انتظار أن يثبت الخادم هويته لـ kombify Connect. يُحَل هذا عادةً من تلقاء نفسه.",
  "guard.reach.reason.host_proof_rejected":
    "رفض kombify Connect إثبات هوية الخادم. أعد إقران الخادم.",
  "guard.reach.reason.kombify_me_unavailable":
    "خدمة kombify.me غير متاحة. تحقق مجددًا بعد قليل.",
  "guard.reach.reason.kombify_me_denied": "رفض kombify.me تسجيل هذا الخادم.",
  "guard.reach.reason.connect_unavailable":
    "kombify Connect غير متاح. تحقق مجددًا بعد قليل.",
  "guard.reach.reason.connect_denied": "رفض kombify Connect إدراج هذا الخادم.",
  "guard.reach.reason.reach_layout_unsupported":
    "لا يمكن منح عنوان بعيد لهذا التخطيط من التثبيت بعد.",
  "guard.reach.reason.revocation_incomplete":
    "لم ينتهِ إنهاء الوصول في كل مكان بعد. تُعاد المحاولة.",
  "guard.reach.reason.identity_unresolved": "تعذّر تحديد هوية مالك هذا الخادم.",

  "guard.notice.legacyRoot":
    "ما زال Guard على هذا الخادم يعمل بالكامل بصلاحية root؛ والتخطيط المحصَّن بلا صلاحيات غير مفعَّل. حدّث Guard على الخادم.",
  "guard.notice.executorUnavailable":
    "يعمل Guard دون منفِّذه ذي الصلاحيات، لذا لا تتوفر التغييرات على المضيف.",
  "guard.notice.mutationsDenied":
    "لم يترك تحديث فاشل نسخة Guard صالحة للاستخدام، لذا تُرفض التغييرات إلى أن يُحَل ذلك.",

  "guard.findings.title": "النتائج الأمنية",
  "guard.findings.control": "الضابط",
  "guard.findings.severity": "الخطورة: {severity}",
  "guard.findings.severity.critical": "حرجة",
  "guard.findings.severity.high": "عالية",
  "guard.findings.severity.medium": "متوسطة",
  "guard.findings.severity.low": "منخفضة",
  "guard.findings.state.compliant": "مطابق",
  "guard.findings.state.drifted": "منحرف",
  "guard.findings.state.unknown": "غير معروف",
  "guard.findings.state.exception": "استثناء معتمد",
  "guard.findings.state.approved_exception": "استثناء معتمد",
  "guard.findings.expected": "المتوقع",
  "guard.findings.observed": "المرصود",
  "guard.findings.remediation": "المعالجة:",
  "guard.findings.exception": "استثناء معتمد.",
  "guard.findings.exceptionWith": "استثناء معتمد: {detail}",
  "guard.findings.approvedBy": "اعتمده {name}",
  "guard.findings.until": "حتى {at}",

  "guard.verify.action": "تحقق الآن",
  "guard.verify.running": "جارٍ التحقق…",
  "guard.verify.accepted":
    "طُلب التحقق في {at} (المهمة {job}). تتحدث الحقائق عندما يبلّغ الخادم بالنتيجة.",
  "guard.verify.failed": "تعذّر بدء التحقق: {message}",

  "guard.rotate.action": "تدوير بيانات الاعتماد",
  "guard.rotate.generationUnknown":
    "يحتاج التدوير إلى الجيل الحالي لبيانات الاعتماد، وهو غير مُبلَّغ عنه بعد.",
  "guard.rotate.confirmTitle": "استبدال بيانات اعتماد هذا الخادم؟",
  "guard.rotate.confirmBody":
    "تُصدَر بيانات اعتماد بديلة وتُعرض مرة واحدة. انسخها إلى الخادم ليستخدمها Guard. لن تُقبل بيانات الاعتماد القديمة بعد ذلك.",
  "guard.rotate.confirm": "تدوير بيانات الاعتماد",
  "guard.rotate.running": "جارٍ التدوير…",
  "guard.rotate.done": "أُصدرت بيانات اعتماد بديلة (الجيل {generation}).",
  "guard.rotate.tokenOnce":
    "تُعرض مرة واحدة ولا تُحفظ. انسخها الآن وسلّمها إلى Guard على الخادم.",
  "guard.rotate.copy": "نسخ بيانات الاعتماد",
  "guard.rotate.copied": "تم النسخ",
  "guard.rotate.copyFailed": "تعذّر النسخ. حدّد النص وانسخه يدويًا.",
  "guard.rotate.dismiss": "لقد حفظتها",

  "guard.detach.action": "فصل الخادم",
  "guard.detach.confirmTitle": "فصل {name}؟",
  "guard.detach.confirmBody":
    "يؤدي هذا إلى إلغاء بيانات اعتماد Guard وإنهاء وصول الخادم: عنوانه في kombify.me وإدراجه في kombify Connect. لا يُمَس الجهاز نفسه ولا ما يعمل عليه.",
  "guard.detach.reverse":
    "لا يمكن التراجع عن هذا من هنا. لإدارة الخادم مجددًا، أعد إقرانه.",
  "guard.detach.confirm": "فصل وإلغاء",
  "guard.detach.running": "جارٍ الفصل…",

  "guard.connect.link": "الأجهزة في kombify Connect",

  "guard.report.download": "تنزيل التقرير (JSON)",
  "guard.report.print": "تقرير قابل للطباعة",
  "guard.report.failed": "تعذّر إنشاء التقرير.",
  "guard.report.title": "تقرير kombify Guard",
  "guard.report.generated": "أُنشئ {at}",
  "guard.report.facts": "الحقائق",
  "guard.report.colFact": "الحقيقة",
  "guard.report.colState": "الحالة",
  "guard.report.colEvidence": "الدليل",
  "guard.report.colObserved": "وقت الرصد",
  "guard.report.noObservation": "لا يوجد وقت رصد",
  "guard.report.notices": "حماية مخفَّضة",
  "guard.report.findings": "النتائج الأمنية",
  "guard.report.noFindings": "لا توجد نتائج مُبلَّغ عنها.",
  "guard.report.note":
    "يسرد هذا التقرير فقط ما حفظه مستوى التحكم، مع وقت رصد كل حقيقة. تظهر الحقيقة التي بلا دليل حديث على أنها غير مثبتة، ولا يُفترض أبدًا أنها سليمة. لا يحتوي على أي بيانات اعتماد.",
  "guard.report.digest": "بصمة المحتوى",

  "guard.outcomes.title": "عمليات نتيجتها مجهولة",
  "guard.outcomes.intro":
    "سجّل Guard أن هذه العمليات قد تكون نُفِّذت وقد لا تكون. كل واحدة تمنع المزيد من التغييرات على هذا الخادم إلى أن تُحسم أو تحررها أنت.",
  "guard.outcomes.reconciling": "ما زال Guard يحاول حسمها",
  "guard.outcomes.needsOperator": "تحتاج إلى قرارك",
  "guard.outcomes.since": "غير محسومة منذ {at}",
  "guard.outcomes.resolve": "حسم",
  "guard.outcomes.dispositionLabel": "ماذا وجدت؟",
  "guard.outcomes.disposition.verified_applied": "التغيير مطبَّق",
  "guard.outcomes.disposition.verified_applied.hint":
    "فحصت الخادم: نُفِّذت العملية.",
  "guard.outcomes.disposition.verified_not_applied": "لم يتغير شيء",
  "guard.outcomes.disposition.verified_not_applied.hint":
    "فحصت الخادم: لم تُنفَّذ العملية.",
  "guard.outcomes.disposition.accept_unknown": "تحرير دون تحقق",
  "guard.outcomes.disposition.accept_unknown.hint":
    "لم أتمكن من التحقق. تبقى النتيجة مجهولة وتُسجَّل على أنها مقبولة.",
  "guard.outcomes.noteLabel": "ما الذي تحققت منه؟",
  "guard.outcomes.noteHint": "مطلوب، حتى {max} حرفًا. يُسجَّل باسمك.",
  "guard.outcomes.review": "مراجعة",
  "guard.outcomes.cancel": "إلغاء",
  "guard.outcomes.confirmTitle": "تحرير هذه العملية؟",
  "guard.outcomes.confirmBody":
    "يرفع هذا الحظر عن هذا الخادم. إن لم تكن النتيجة كما سجّلت، فقد تُنفَّذ عليه تغييرات متعارضة.",
  "guard.outcomes.confirm": "تحرير",
  "guard.outcomes.releasing": "جارٍ التحرير…",
  "guard.outcomes.back": "رجوع",
  "guard.outcomes.stepUp":
    "يتطلب تحرير نتيجة تسجيل دخول حديثًا بعامل ثانٍ خلال آخر خمس دقائق. سجّل الدخول مجددًا ثم كرر هذه الخطوة. لا تُحفظ ملاحظتك.",
  "guard.outcomes.stepUpAction": "تسجيل الدخول مجددًا",
  "guard.outcomes.stale":
    "تغيّرت هذه العملية أثناء مراجعتك لها. حُدّثت القائمة؛ راجعها مجددًا.",
  "guard.outcomes.loadFailed": "تعذّر تحميل قائمة العمليات غير المحسومة.",
  "guard.outcomes.receipt":
    "حُرِّرت {command} عند المراجعة {revision} بوصفها «{disposition}»، ومسجَّلة باسم {actor}.",
  "guard.outcomes.fenceReleased": "رُفع الحظر عن هذا الخادم.",

  "guard.ladder.title": "جاهزية الإقران",
  "guard.ladder.intro":
    "يُفحص كل خطوة مقابل ما سجّله مستوى التحكم، ولا يُفترض.",
  "guard.ladder.bound": "مرتبط",
  "guard.ladder.connected": "متصل",
  "guard.ladder.configuration": "تم التحقق من الإعدادات",
  "guard.ladder.reachable": "يمكن الوصول إليه",
  "guard.ladder.state.done": "تم",
  "guard.ladder.state.failed": "يحتاج إلى انتباه",
  "guard.ladder.state.waiting": "قيد التنفيذ",
  "guard.ladder.state.blocked": "في انتظار الخطوة السابقة",
  "guard.ladder.blockedHint": "تبدأ بعد اكتمال الخطوة السابقة.",
  "guard.ladder.complete": "جميع خطوات الإقران مثبتة.",
  "guard.ladder.bound.awaiting": "في انتظار تشغيل أمر الإقران على الخادم.",
  "guard.ladder.bound.awaitingNext":
    "شغّل الأمر على الخادم. إن انتهت صلاحيته فأنشئ أمرًا جديدًا.",
};
