import type { guardMessages } from "./guard-messages";

// Simplified Chinese catalog of the kombify Guard surfaces. Same keys and
// placeholders as the English catalog.
export const guardMessagesZhHans: Record<
  keyof typeof guardMessages.en,
  string
> = {
  "guard.backup.title": "已记录的恢复点",
  "guard.backup.load": "读取恢复点",
  "guard.backup.loading": "正在读取…",
  "guard.backup.recorded":
    "这些恢复点由此节点记录。准备恢复时会检查历史备份内容。",
  "guard.backup.empty": "此节点未报告可选择的历史恢复点。",
  "guard.backup.choose": "恢复点",
  "guard.backup.placeholder": "选择一个已记录的恢复点",
  "guard.backup.currentPlan": "当前方案",
  "guard.backup.historicalPlan": "较早方案",
  "guard.backup.clockSkew": "时钟偏差",
  "guard.backup.stage": "准备隔离恢复",
  "guard.backup.confirm":
    "将 {when} 的恢复点准备到隔离暂存区？不会替换正在使用的应用数据。",
  "guard.backup.confirmAction": "准备此恢复",
  "guard.backup.running": "节点正在准备隔离恢复。",
  "guard.backup.staged": "已在隔离暂存区准备恢复。正在使用的应用数据未被替换。",
  "guard.backup.unverified":
    "任务已完成，但没有匹配的暂存恢复证据。应用恢复尚未验证。",
  "guard.backup.failed": "备份操作未完成。",
  "guard.backup.job": "任务 {id}",
  "guard.backup.scopeMismatch": "此任务不属于当前服务器和环境。",
  "guard.name": "kombify Guard",
  "guard.backup.activate": "启用已准备的恢复",
  "guard.backup.notAdmitted": "原始请求未获准，未开始恢复现有数据。",
  "guard.backup.historicalOperation":
    "此操作属于先前的安装。其记录的结果不代表当前安装的状态。",
  "guard.backup.activateConfirm":
    "在 {name} 上启用此恢复？这会替换应用的现有数据。节点将保留安全快照，并验证服务和所有者访问权限。",
  "guard.backup.activateAction": "确认恢复现有数据",
  "guard.backup.activationUnavailable":
    "此节点当前已发布的运行时不支持启用恢复。已准备的数据仍保持隔离。",
  "guard.backup.activating": "节点正在核对已批准的恢复操作。",
  "guard.backup.activated": "节点已启用并验证所选恢复。",
  "guard.backup.recovered": "已恢复应用先前的状态。所选恢复未启用。",
  "guard.backup.accessVerified": "节点于 {when} 验证了服务和所有者访问权限。",
  "guard.backup.recoveryRequired":
    "恢复结果尚未确认。请先处理原始操作，再开始其他恢复。",
  "guard.backup.outcomeUnknown": "尚无匹配的最终恢复证据。",
  "guard.backup.checkOriginal": "检查原始操作",
  "guard.backup.resumeRequest": "继续原始请求",
  "guard.backup.recover": "恢复原始操作",
  "guard.backup.recoverConfirm":
    "核对原始操作？若操作尚未完成，节点将恢复应用先前的状态。",
  "guard.backup.recoverAction": "确认原始恢复",
  "guard.promise":
    "旨在让这台服务器与你绑定、持续受到检查、可从任何地方访问,并在设计上可恢复。下方的事实显示当前有证据支持的内容。",
  "guard.cancel": "取消",
  "guard.next": "下一步:",
  "guard.observed": "观测于 {when}",
  "guard.noObservation": "未报告观测时间",
  "guard.reduced": "保护降级",
  "guard.serverUnavailable":
    "无法读取服务器记录,因此目前这些事实都没有证据支持。",
  "guard.state.ok": "有证据",
  "guard.state.attention": "需要关注",
  "guard.state.unknown": "无证据",
  "guard.overall.ready": "Guard 就绪",
  "guard.overall.attention": "需要关注",
  "guard.overall.unverified": "尚无证据",
  "guard.overall.count": "{total} 项事实中有 {ok} 项有证据",
  "guard.badge.ready": "Guard 就绪",
  "guard.badge.attention": "Guard:需要关注",
  "guard.badge.unverified": "Guard:{ok}/{total} 项有证据",
  "guard.badge.open": "打开 kombify Guard 面板",
  "guard.detail.reasonCode": "原因代码:{code}",

  "guard.fact.bound.title": "已绑定",
  "guard.fact.connected.title": "已连接",
  "guard.fact.operations.title": "操作持久化",
  "guard.fact.configuration.title": "配置已加固",
  "guard.fact.updated.title": "已是最新",
  "guard.fact.recoverable.title": "可恢复",
  "guard.fact.reachable.title": "可访问且受管",

  "guard.fact.bound.ok": "这台服务器在每次连接时都用自己的密钥证明身份。",
  "guard.fact.bound.legacy":
    "这台服务器仍只用持有者凭据标识自己,这无法证明是哪台机器在通信。",
  "guard.fact.bound.legacyNext":
    "请更新服务器上的 Guard,使其注册自己的密钥,然后在此处批准该密钥。",
  "guard.fact.bound.revoked": "这台服务器已被撤销,不再绑定。",
  "guard.fact.bound.revokedNext": "请重新配对服务器,将其绑定到你的 Techstack。",
  "guard.fact.bound.stale": "最近一次报告太旧,无法确认这台服务器如何证明身份。",
  "guard.fact.bound.unknown": "服务器尚未报告它如何证明身份。",
  "guard.fact.bound.unknownNext":
    "请等待下一次报告。如果没有出现,请更新服务器上的 Guard。",

  "guard.fact.connected.ok": "Guard 正通过其已认证的连接上报。",
  "guard.fact.connected.degraded": "连接已降级。",
  "guard.fact.connected.offline":
    "Guard 没有上报。服务器已离线或无法连接到 Techstack。",
  "guard.fact.connected.revoked": "连接已被撤销。",
  "guard.fact.connected.stale": "Guard 最近没有上报,不能算作已连接。",
  "guard.fact.connected.pending": "Guard 尚未完成首次连接。",
  "guard.fact.connected.pendingNext":
    "请在服务器上运行配对命令,或稍等片刻后再检查。",
  "guard.fact.connected.unknown": "未报告这台服务器的连接状态。",
  "guard.fact.connected.reconnectNext":
    "请检查服务器是否已开机并能连接到 Techstack。",

  "guard.fact.operations.ok": "操作已被持久记录,没有遗留未解决的项目。",
  "guard.fact.operations.unacked": "等待确认的结果:{count}。",
  "guard.fact.operations.unresolved":
    "结果未知的操作:{count}。它们会阻止对这台服务器的后续更改。",
  "guard.fact.operations.unresolvedNext": "请在下方查看,并记录你核实的内容。",
  "guard.fact.operations.legacy":
    "这个 Guard 不保留持久的操作日志,因此无法可靠地报告被中断的操作。",
  "guard.fact.operations.legacyNext": "请更新服务器上的 Guard。",
  "guard.fact.operations.denied": "操作日志无法写入,因此拒绝更改。",
  "guard.fact.operations.deniedNext":
    "请检查服务器上的可用磁盘空间和权限,然后等待下一次报告。",
  "guard.fact.operations.stale": "最近一次报告太旧,无法确认日志状态。",
  "guard.fact.operations.unknown": "服务器尚未报告其操作日志。",
  "guard.fact.operations.unknownNext":
    "请等待下一次报告。如果没有出现,请更新服务器上的 Guard。",

  "guard.fact.configuration.ok": "主机符合安全基线。",
  "guard.fact.configuration.okExceptions":
    "除已批准的例外外,主机符合安全基线。",
  "guard.fact.configuration.baseline": "基线版本 {version}",
  "guard.fact.configuration.exceptions": "已批准的例外:{count}",
  "guard.fact.configuration.drifted": "偏离基线的控制项:{count}。",
  "guard.fact.configuration.driftedNext": "请查看下方的发现并执行修复。",
  "guard.fact.configuration.stale":
    "最近一次验证太旧,不予计入。此后出现的偏离将无法看到。",
  "guard.fact.configuration.unknown": "最近一次验证无法确定状态。",
  "guard.fact.configuration.notReported": "未报告这台服务器的主机安全验证。",
  "guard.fact.configuration.notReportedNext":
    "请更新服务器上的 Guard 和 StackKit,使其能够上报。",
  "guard.fact.configuration.verifyNext": "请再次验证服务器。",

  "guard.fact.updated.ok": "Guard 和操作系统均已是最新。",
  "guard.fact.updated.attention": "有内容需要更新。",
  "guard.fact.updated.stale": "最近一次报告太旧,无法确认更新状态。",
  "guard.fact.updated.unknown": "更新状态无证据。",
  "guard.fact.updated.unknownNext": "请等待下一次报告,或更新服务器上的 Guard。",
  "guard.fact.updated.guardOk": "Guard {version} 已签名且为最新。",
  "guard.fact.updated.guardProblem": "Guard 更新状态:{state}(版本 {version})。",
  "guard.fact.updated.guardProblemNext": "请检查服务器上的 Guard 更新。",
  "guard.fact.updated.guardInProgress": "正在安装 Guard 更新。",
  "guard.fact.updated.guardUnknown": "未报告 Guard 的版本和签名。",
  "guard.fact.updated.guardUnsigned": "已安装的 Guard 构件未签名。",
  "guard.fact.updated.osOk": "没有待安装的安全更新,也无需重启。",
  "guard.fact.updated.osPending": "待安装的操作系统安全更新:{count}。",
  "guard.fact.updated.osPendingNext": "请安装待处理的安全更新。",
  "guard.fact.updated.osReboot": "需要重启才能完成更新。",
  "guard.fact.updated.osRebootNext": "请在方便时重启服务器。",
  "guard.fact.updated.osUnknown": "未报告操作系统的更新状态。",
  "guard.fact.updated.lastRun": "上次无人值守安全更新运行:{at}",

  "guard.fact.recoverable.ok": "存在新的备份,并且已验证过一次恢复。",
  "guard.fact.recoverable.attention": "备份或恢复的证据已过期。",
  "guard.fact.recoverable.unknown": "可恢复性尚无完整证据。",
  "guard.fact.recoverable.notReported": "未报告任何备份或恢复证据。",
  "guard.fact.recoverable.notReportedNext":
    "请为这台服务器配置备份,然后验证一次恢复。",
  "guard.fact.recoverable.attentionNext": "请创建新的备份,或重新进行恢复演练。",
  "guard.fact.recoverable.unknownNext": "请等待下一次报告,或验证一次恢复。",
  "guard.fact.recoverable.backupFresh": "备份在策略范围内。",
  "guard.fact.recoverable.backupStale": "最新备份比策略允许的更旧。",
  "guard.fact.recoverable.backupUnknown": "未报告备份是否足够新。",
  "guard.fact.recoverable.drillVerified": "已验证过一次恢复。",
  "guard.fact.recoverable.drillStale": "最近一次已验证的恢复已过时。",
  "guard.fact.recoverable.drillUnknown": "未报告已验证的恢复。",

  "guard.fact.reachable.ok":
    "这台服务器已在其 kombify.me 地址注册,并列入 kombify Connect。",
  "guard.fact.reachable.address": "地址:{address}",
  "guard.fact.reachable.notReported": "未报告这台服务器的远程访问。",
  "guard.fact.reachable.notReportedNext":
    "请将该安装连接到 kombify Cloud 以注册其地址,或稍后再检查。",
  "guard.fact.reachable.notEnrolled": "这台服务器尚未登记远程访问。",
  "guard.fact.reachable.pending": "正在设置远程访问。",
  "guard.fact.reachable.pendingNext": "请稍等片刻后再检查。",
  "guard.fact.reachable.failed": "设置远程访问失败。",
  "guard.fact.reachable.failedNext":
    "请稍后再检查。如果问题持续,请携带原因代码联系支持。",
  "guard.fact.reachable.revoked": "这台服务器的远程访问已被撤销。",
  "guard.fact.reachable.revokedNext": "请重新配对服务器以恢复远程访问。",
  "guard.fact.reachable.stale":
    "最近一次可达性报告太旧,无法确认该地址仍有响应。",
  "guard.fact.reachable.relayConnecting": "正在建立中继连接。",
  "guard.fact.reachable.relayDown":
    "Guard 的中继连接已断开,因此该地址没有响应。",
  "guard.fact.reachable.relayDownNext":
    "请检查原因代码,并确认服务器能访问互联网。",

  "guard.fact.reachable.notEnabled":
    "这台服务器未发布在 kombify.me 地址上。在你启用之前,这是你的选择。",
  "guard.fact.reachable.notEnabledNext":
    "要发布服务,请在下方填写服务名称及其监听的端口。",
  "guard.reach.publish.title": "发布到 kombify.me 地址",
  "guard.reach.publish.intro":
    "在你开启之前不会发布任何内容。互联网上的任何人都能打开该地址,因此只发布能自我保护的服务。",
  "guard.reach.publish.service": "服务名称",
  "guard.reach.publish.port": "此服务器上的端口",
  "guard.reach.publish.enable": "发布",
  "guard.reach.publish.disable": "停止发布",
  "guard.reach.publish.current": "已发布:{service},端口 {port}。",
  "guard.reach.publish.stepUp":
    "发布需要在最近五分钟内使用第二因素重新登录。请重新登录后再重复此步骤。",
  "guard.upgrade.title": "批准此服务器的密钥",
  "guard.upgrade.pending":
    "指纹为 {fingerprint} 的密钥正在等待你的批准。仅当 Guard 在此服务器上记录了相同指纹时才批准。",
  "guard.upgrade.none":
    "此服务器尚未提出密钥。请更新服务器上的 Guard,其密钥指纹随后会显示在此处等待批准。",
  "guard.upgrade.approve": "批准密钥",
  "guard.upgrade.reject": "拒绝密钥",
  "guard.upgrade.stepUp":
    "批准需要在最近五分钟内使用第二因素重新登录。请重新登录后再重复此步骤。",
  "guard.upgrade.done": "服务器密钥已批准。",

  "guard.reach.reason.guard_key_not_bound":
    "服务器尚无已绑定的密钥。请更新 Guard,使其绑定自己的密钥。",
  "guard.reach.reason.connect_tenant_unmappable":
    "你的账户还没有 kombify Connect 空间,因此无法在其中列出该服务器。",
  "guard.reach.reason.host_proof_pending":
    "正在等待服务器向 kombify Connect 证明身份。这通常会自行解决。",
  "guard.reach.reason.host_proof_rejected":
    "kombify Connect 拒绝了服务器的身份证明。请重新配对服务器。",
  "guard.reach.reason.kombify_me_unavailable":
    "kombify.me 服务不可用。请稍后再检查。",
  "guard.reach.reason.kombify_me_denied": "kombify.me 拒绝了这台服务器的注册。",
  "guard.reach.reason.connect_unavailable":
    "kombify Connect 不可用。请稍后再检查。",
  "guard.reach.reason.connect_denied": "kombify Connect 拒绝列出这台服务器。",
  "guard.reach.reason.reach_layout_unsupported":
    "这种安装布局暂时无法分配远程地址。",
  "guard.reach.reason.revocation_incomplete":
    "终止访问尚未在所有位置完成。系统会重试。",
  "guard.reach.reason.identity_unresolved": "无法解析这台服务器的所有者身份。",

  "guard.notice.legacyRoot":
    "这台服务器上的 Guard 仍完全以 root 运行;加固的非特权布局尚未启用。请更新服务器上的 Guard。",
  "guard.notice.executorUnavailable":
    "Guard 在没有特权执行器的情况下运行,因此无法对主机进行更改。",
  "guard.notice.mutationsDenied":
    "一次失败的更新没有留下可用的 Guard,因此在解决之前拒绝更改。",

  "guard.findings.title": "安全发现",
  "guard.findings.control": "控制项",
  "guard.findings.severity": "严重程度:{severity}",
  "guard.findings.severity.critical": "严重",
  "guard.findings.severity.high": "高",
  "guard.findings.severity.medium": "中",
  "guard.findings.severity.low": "低",
  "guard.findings.state.compliant": "合规",
  "guard.findings.state.drifted": "已偏离",
  "guard.findings.state.unknown": "未知",
  "guard.findings.state.exception": "已批准的例外",
  "guard.findings.state.approved_exception": "已批准的例外",
  "guard.findings.expected": "预期",
  "guard.findings.observed": "观测到",
  "guard.findings.remediation": "修复:",
  "guard.findings.exception": "已批准的例外。",
  "guard.findings.exceptionWith": "已批准的例外:{detail}",
  "guard.findings.approvedBy": "由 {name} 批准",
  "guard.findings.until": "至 {at}",

  "guard.verify.action": "立即验证",
  "guard.verify.running": "正在验证…",
  "guard.verify.accepted":
    "已于 {at} 请求验证(任务 {job})。服务器上报结果后,这些事实会随之更新。",
  "guard.verify.failed": "无法启动验证:{message}",

  "guard.rotate.action": "轮换凭据",
  "guard.rotate.generationUnknown": "轮换需要当前的凭据代数,但目前尚未上报。",
  "guard.rotate.confirmTitle": "替换这台服务器的凭据?",
  "guard.rotate.confirmBody":
    "将签发一个替换凭据并仅显示一次。请把它复制到服务器上,供 Guard 使用。旧凭据将不再被接受。",
  "guard.rotate.confirm": "轮换凭据",
  "guard.rotate.running": "正在轮换…",
  "guard.rotate.done": "已签发替换凭据(第 {generation} 代)。",
  "guard.rotate.tokenOnce":
    "它只显示一次且不会被保存。请立即复制,并交给服务器上的 Guard。",
  "guard.rotate.copy": "复制凭据",
  "guard.rotate.copied": "已复制",
  "guard.rotate.copyFailed": "无法复制。请选中文本后手动复制。",
  "guard.rotate.dismiss": "我已保存",

  "guard.detach.action": "分离服务器",
  "guard.detach.confirmTitle": "分离 {name}?",
  "guard.detach.confirmBody":
    "这会撤销 Guard 的凭据并终止该服务器的远程访问:它的 kombify.me 地址和它在 kombify Connect 中的列表。机器本身及其上运行的内容不会受影响。",
  "guard.detach.reverse": "这里无法撤销。若要再次管理该服务器,请重新配对。",
  "guard.detach.confirm": "分离并撤销",
  "guard.detach.running": "正在分离…",

  "guard.connect.link": "kombify Connect 中的设备",

  "guard.report.download": "下载报告(JSON)",
  "guard.report.print": "可打印的报告",
  "guard.report.failed": "无法创建报告。",
  "guard.report.title": "kombify Guard 报告",
  "guard.report.generated": "生成于 {at}",
  "guard.report.facts": "事实",
  "guard.report.colFact": "事实",
  "guard.report.colState": "状态",
  "guard.report.colEvidence": "证据",
  "guard.report.colObserved": "观测时间",
  "guard.report.noObservation": "无观测时间",
  "guard.report.notices": "保护降级",
  "guard.report.findings": "安全发现",
  "guard.report.noFindings": "未报告任何发现。",
  "guard.report.note":
    "本报告只列出控制平面已持久保存的内容,以及每项事实的观测时间。没有新证据的事实显示为无证据,绝不会被默认视为正常。报告不含任何凭据。",
  "guard.report.digest": "内容摘要",

  "guard.outcomes.title": "结果未知的操作",
  "guard.outcomes.intro":
    "Guard 记录到这些操作可能生效,也可能没有生效。每一项都会阻止对这台服务器的后续更改,直到它被解决或由你释放。",
  "guard.outcomes.reconciling": "Guard 仍在尝试解决",
  "guard.outcomes.needsOperator": "需要你来决定",
  "guard.outcomes.since": "自 {at} 起未解决",
  "guard.outcomes.resolve": "处理",
  "guard.outcomes.dispositionLabel": "你发现了什么?",
  "guard.outcomes.disposition.verified_applied": "更改已生效",
  "guard.outcomes.disposition.verified_applied.hint":
    "我检查了服务器:该操作已生效。",
  "guard.outcomes.disposition.verified_not_applied": "没有任何更改",
  "guard.outcomes.disposition.verified_not_applied.hint":
    "我检查了服务器:该操作没有生效。",
  "guard.outcomes.disposition.accept_unknown": "未经核实直接释放",
  "guard.outcomes.disposition.accept_unknown.hint":
    "我无法核实。结果保持未知,并记录为已接受。",
  "guard.outcomes.noteLabel": "你核实了什么?",
  "guard.outcomes.noteHint": "必填,最多 {max} 个字符。将与你的姓名一起记录。",
  "guard.outcomes.review": "检查",
  "guard.outcomes.cancel": "取消",
  "guard.outcomes.confirmTitle": "释放此操作?",
  "guard.outcomes.confirmBody":
    "这会解除对这台服务器的阻止。如果结果与你记录的不符,可能会有冲突的更改在其上运行。",
  "guard.outcomes.confirm": "释放",
  "guard.outcomes.releasing": "正在释放…",
  "guard.outcomes.back": "返回",
  "guard.outcomes.stepUp":
    "释放结果需要使用第二重验证因素重新登录(五分钟内)。请重新登录,然后重复此步骤。你的备注不会被保留。",
  "guard.outcomes.stepUpAction": "重新登录",
  "guard.outcomes.stale": "你检查期间该操作发生了变化。列表已更新,请重新检查。",
  "guard.outcomes.loadFailed": "无法加载未解决操作的列表。",
  "guard.outcomes.receipt":
    "已在修订 {revision} 将 {command} 释放为“{disposition}”,记录在 {actor} 名下。",
  "guard.outcomes.fenceReleased": "对这台服务器的阻止已解除。",

  "guard.ladder.title": "配对就绪情况",
  "guard.ladder.intro": "每一步都对照控制平面的记录进行检查,而不是想当然。",
  "guard.ladder.bound": "已绑定",
  "guard.ladder.connected": "已连接",
  "guard.ladder.configuration": "配置已验证",
  "guard.ladder.reachable": "可访问",
  "guard.ladder.state.done": "已完成",
  "guard.ladder.state.failed": "需要关注",
  "guard.ladder.state.waiting": "进行中",
  "guard.ladder.state.blocked": "等待前一步",
  "guard.ladder.blockedHint": "前一步完成后开始。",
  "guard.ladder.complete": "所有配对步骤均有证据。",
  "guard.ladder.bound.awaiting": "正在等待配对命令在服务器上运行。",
  "guard.ladder.bound.awaitingNext":
    "请在服务器上运行该命令。如果已过期,请生成新的命令。",
};
