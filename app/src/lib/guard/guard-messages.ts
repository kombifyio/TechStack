// Customer-visible strings of the kombify Guard surfaces: the Guard panel, the
// badge, the pairing readiness ladder, the outcome resolution form and the
// evidence report. Merged into the main dictionary by i18n.ts for every locale.
//
// Wording rules (Brand brief gtm/components/guard.md): name "kombify Guard",
// describe what the owner can see and control, and state a capability only as
// far as the evidence shown on screen supports it. No SLA, zero-compromise or
// EDR claim.
export const guardMessages = {
  en: {
    "guard.backup.title": "Recorded recovery points",
    "guard.backup.load": "Read recovery points",
    "guard.backup.loading": "Reading…",
    "guard.backup.recorded":
      "These points are recorded by this server. Historical backup contents are checked when you prepare a restore.",
    "guard.backup.empty":
      "This server reports no selectable historical recovery points.",
    "guard.backup.choose": "Recovery point",
    "guard.backup.placeholder": "Choose a recorded point",
    "guard.backup.currentPlan": "Current plan",
    "guard.backup.historicalPlan": "Earlier plan",
    "guard.backup.clockSkew": "Clock discrepancy",
    "guard.backup.stage": "Prepare isolated restore",
    "guard.backup.confirm":
      "Prepare the recorded point from {when} in isolated staging? Live application data will not be replaced.",
    "guard.backup.confirmAction": "Prepare this restore",
    "guard.backup.running": "The server is preparing an isolated restore.",
    "guard.backup.staged":
      "Restore prepared in isolated staging. Live application data has not been replaced.",
    "guard.backup.unverified":
      "The job completed without matching staged-restore evidence. Application recovery is unverified.",
    "guard.backup.failed": "The backup operation did not complete.",
    "guard.backup.job": "Job {id}",
    "guard.backup.scopeMismatch":
      "The job does not belong to this server and stack.",
    "guard.backup.activate": "Activate prepared restore",
    "guard.backup.notAdmitted":
      "The original request was not admitted. No live restore was started.",
    "guard.backup.historicalOperation":
      "This operation belongs to a previous installation. Its recorded outcome does not describe the current installation.",
    "guard.backup.activateConfirm":
      "Activate this prepared restore on {name}? This replaces live application data. The server keeps a safety snapshot and verifies services and Owner access.",
    "guard.backup.activateAction": "Confirm live restore",
    "guard.backup.activationUnavailable":
      "Live activation is unavailable with this server's current released runtime. The prepared data remains isolated.",
    "guard.backup.activating":
      "The server is reconciling the approved restore operation.",
    "guard.backup.activated":
      "The selected restore was activated and verified by the server.",
    "guard.backup.recovered":
      "Previous application state recovered. The selected restore was not activated.",
    "guard.backup.accessVerified":
      "The server verified services and Owner access at {when}.",
    "guard.backup.recoveryRequired":
      "The restore outcome remains unconfirmed. Resolve the original operation before starting another restore.",
    "guard.backup.outcomeUnknown":
      "No matching terminal restore evidence is available yet.",
    "guard.backup.checkOriginal": "Check original operation",
    "guard.backup.resumeRequest": "Resume original request",
    "guard.backup.recover": "Recover original operation",
    "guard.backup.recoverConfirm":
      "Reconcile the original operation? If it is incomplete, the server restores the previous application state.",
    "guard.backup.recoverAction": "Confirm original recovery",
    "guard.name": "kombify Guard",
    "guard.promise":
      "Built to keep this server bound to you, checked continuously, reachable from anywhere and recoverable by design. The facts below show what is evidenced right now.",
    "guard.cancel": "Cancel",
    "guard.next": "Next:",
    "guard.observed": "Observed {when}",
    "guard.noObservation": "No observation time reported",
    "guard.reduced": "Reduced protection",
    "guard.serverUnavailable":
      "The server record could not be read, so none of these facts is evidenced right now.",
    "guard.state.ok": "Evidenced",
    "guard.state.attention": "Needs attention",
    "guard.state.unknown": "Not evidenced",
    "guard.overall.ready": "Guard-ready",
    "guard.overall.attention": "Needs attention",
    "guard.overall.unverified": "Not yet evidenced",
    "guard.overall.count": "{ok} of {total} facts evidenced",
    "guard.badge.ready": "Guard-ready",
    "guard.badge.attention": "Guard: attention needed",
    "guard.badge.unverified": "Guard: {ok}/{total} evidenced",
    "guard.badge.open": "Open the kombify Guard panel",
    "guard.detail.reasonCode": "Reason code: {code}",

    "guard.fact.bound.title": "Bound",
    "guard.fact.connected.title": "Connected",
    "guard.fact.operations.title": "Operations durable",
    "guard.fact.configuration.title": "Configuration secured",
    "guard.fact.updated.title": "Up to date",
    "guard.fact.recoverable.title": "Recoverable",
    "guard.fact.reachable.title": "Reachable & governed",

    "guard.fact.bound.ok":
      "This server proves its identity with its own key on every connection.",
    "guard.fact.bound.legacy":
      "This server still identifies itself with a bearer credential only, which does not prove which machine is talking.",
    "guard.fact.bound.legacyNext":
      "Update Guard on the server so it registers its own key, then approve that key here.",
    "guard.fact.bound.revoked":
      "This server has been revoked and is no longer bound.",
    "guard.fact.bound.revokedNext":
      "Pair the server again to bind it to your Techstack.",
    "guard.fact.bound.stale":
      "The last report is too old to confirm how this server proves its identity.",
    "guard.fact.bound.unknown":
      "The server has not reported how it proves its identity.",
    "guard.fact.bound.unknownNext":
      "Wait for the next report. If it does not appear, update Guard on the server.",

    "guard.fact.connected.ok":
      "Guard answered a command over its authenticated connection.",
    "guard.fact.connected.degraded": "The connection is degraded.",
    "guard.fact.connected.offline":
      "Guard is not reporting. The server is offline or cannot reach Techstack.",
    "guard.fact.connected.revoked": "The connection was revoked.",
    "guard.fact.connected.stale":
      "Guard has not reported recently enough to count as connected.",
    "guard.fact.connected.pending":
      "Guard has not completed its first connection yet.",
    "guard.fact.connected.pendingNext":
      "Run the pairing command on the server, or wait a moment and check again.",
    "guard.fact.connected.unknown":
      "No connection state is reported for this server.",
    "guard.fact.connected.reconnectNext":
      "Check that the server is powered on and can reach Techstack.",
    "guard.fact.connected.awaitingRoundTrip":
      "Guard is reporting, but has not yet answered a command from Techstack under its current credential.",
    "guard.fact.connected.roundTripStale":
      "Guard is reporting, but the last command it answered is too old, or was answered under an earlier credential.",
    "guard.fact.connected.roundTripNext":
      "The next operation on this server confirms the connection; reports alone do not.",

    "guard.fact.operations.ok":
      "Operations are journaled durably and nothing is left unresolved.",
    "guard.fact.operations.unacked":
      "Results waiting to be acknowledged: {count}.",
    "guard.fact.operations.unresolved":
      "Operations with an unknown outcome: {count}. They block further changes on this server.",
    "guard.fact.operations.unresolvedNext":
      "Review them below and record what you verified.",
    "guard.fact.operations.legacy":
      "This Guard does not keep a durable operation journal, so an interrupted operation cannot be reported reliably.",
    "guard.fact.operations.legacyNext": "Update Guard on the server.",
    "guard.fact.operations.denied":
      "The operation journal is not writable, so changes are denied.",
    "guard.fact.operations.deniedNext":
      "Check free disk space and permissions on the server, then wait for the next report.",
    "guard.fact.operations.stale":
      "The last report is too old to confirm the journal state.",
    "guard.fact.operations.unknown":
      "The server has not reported its operation journal.",
    "guard.fact.operations.unknownNext":
      "Wait for the next report. If it does not appear, update Guard on the server.",

    "guard.fact.configuration.ok": "The host matches the security baseline.",
    "guard.fact.configuration.okExceptions":
      "The host matches the security baseline apart from approved exceptions.",
    "guard.fact.configuration.baseline": "Baseline version {version}",
    "guard.fact.configuration.exceptions": "Approved exceptions: {count}",
    "guard.fact.configuration.drifted":
      "Controls that deviate from the baseline: {count}.",
    "guard.fact.configuration.driftedNext":
      "Review the findings below and apply the remediation.",
    "guard.fact.configuration.stale":
      "The last verification is too old to count. A deviation since then would not be visible.",
    "guard.fact.configuration.unknown":
      "The last verification could not establish the state.",
    "guard.fact.configuration.notReported":
      "Host-security verification is not reported for this server.",
    "guard.fact.configuration.notReportedNext":
      "Update Guard and the StackKit on the server so they can report it.",
    "guard.fact.configuration.verifyNext": "Verify the server again.",

    "guard.fact.updated.ok": "Guard and the operating system are up to date.",
    "guard.fact.updated.attention": "Something needs updating.",
    "guard.fact.updated.stale":
      "The last report is too old to confirm the update status.",
    "guard.fact.updated.unknown": "The update status is not evidenced.",
    "guard.fact.updated.unknownNext":
      "Wait for the next report, or update Guard on the server.",
    "guard.fact.updated.guardOk": "Guard {version} is signed and current.",
    "guard.fact.updated.guardProblem":
      "Guard update state: {state} (version {version}).",
    "guard.fact.updated.guardProblemNext":
      "Check the Guard update on the server.",
    "guard.fact.updated.guardInProgress": "A Guard update is being installed.",
    "guard.fact.updated.guardUnknown":
      "The Guard version and signature are not reported.",
    "guard.fact.updated.guardUnsigned":
      "The installed Guard artifact is not signed.",
    "guard.fact.updated.osOk":
      "No pending security updates and no reboot required.",
    "guard.fact.updated.osPending":
      "Pending operating-system security updates: {count}.",
    "guard.fact.updated.osPendingNext": "Install the pending security updates.",
    "guard.fact.updated.osReboot": "A reboot is required to finish updates.",
    "guard.fact.updated.osRebootNext":
      "Reboot the server when it is convenient.",
    "guard.fact.updated.osUnknown":
      "The operating-system update status is not reported.",
    "guard.fact.updated.lastRun": "Last unattended security update run: {at}",

    "guard.fact.recoverable.ok":
      "A fresh backup exists and a restore was verified.",
    "guard.fact.recoverable.attention":
      "Backup or restore evidence is overdue.",
    "guard.fact.recoverable.unknown": "Recovery is not fully evidenced.",
    "guard.fact.recoverable.notReported":
      "No backup or restore evidence is reported.",
    "guard.fact.recoverable.notReportedNext":
      "Configure backups for this server, then verify a restore.",
    "guard.fact.recoverable.attentionNext":
      "Take a fresh backup or repeat the restore drill.",
    "guard.fact.recoverable.unknownNext":
      "Wait for the next report, or verify a restore.",
    "guard.fact.recoverable.backupFresh": "The backup is within policy.",
    "guard.fact.recoverable.backupStale":
      "The latest backup is older than the policy allows.",
    "guard.fact.recoverable.backupUnknown": "Backup freshness is not reported.",
    "guard.fact.recoverable.drillVerified": "A restore was verified.",
    "guard.fact.recoverable.drillStale":
      "The last verified restore is out of date.",
    "guard.fact.recoverable.drillUnknown": "No verified restore is reported.",

    "guard.fact.reachable.ok":
      "This server is registered at its kombify.me address and listed in kombify Connect.",
    "guard.fact.reachable.address": "Address: {address}",
    "guard.fact.reachable.notReported":
      "Remote reach is not reported for this server.",
    "guard.fact.reachable.notReportedNext":
      "Connect the installation to kombify Cloud to register its address, or check again later.",
    "guard.fact.reachable.notEnrolled":
      "This server is not enrolled for remote access yet.",
    "guard.fact.reachable.pending": "Remote access is being set up.",
    "guard.fact.reachable.pendingNext": "Wait a moment and check again.",
    "guard.fact.reachable.failed": "Setting up remote access failed.",
    "guard.fact.reachable.failedNext":
      "Check again shortly. If it persists, contact support with the reason code.",
    "guard.fact.reachable.revoked":
      "Remote access for this server was revoked.",
    "guard.fact.reachable.revokedNext":
      "Pair the server again to restore remote access.",
    "guard.fact.reachable.stale":
      "The last reach report is too old to confirm that the address still answers.",
    "guard.fact.reachable.relayConnecting":
      "The relay connection is being established.",
    "guard.fact.reachable.relayDown":
      "Guard's relay connection is down, so the address does not answer.",
    "guard.fact.reachable.relayDownNext":
      "Check the reason code and that the server can reach the internet.",

    "guard.fact.reachable.notEnabled":
      "This server is not published at a kombify.me address. That is your choice until you enable it.",
    "guard.fact.reachable.notEnabledNext":
      "To publish a service, name it and the port it listens on below.",
    "guard.reach.publish.title": "Publish at a kombify.me address",
    "guard.reach.publish.intro":
      "Nothing is published until you turn it on. Anyone on the internet can open the address, so publish only a service that protects itself.",
    "guard.reach.publish.service": "Service name",
    "guard.reach.publish.port": "Port on this server",
    "guard.reach.publish.enable": "Publish",
    "guard.reach.publish.disable": "Stop publishing",
    "guard.reach.publish.current": "Published: {service} on port {port}.",
    "guard.reach.publish.stepUp":
      "Publishing needs a fresh sign-in with a second factor, within the last five minutes. Sign in again, then repeat this step.",
    "guard.upgrade.title": "Approve this server's key",
    "guard.upgrade.pending":
      "A key with fingerprint {fingerprint} is waiting for your approval. Approve it only if Guard logs the same fingerprint on this server.",
    "guard.upgrade.none":
      "This server has not proposed a key yet. Update Guard on the server; its key fingerprint then appears here for your approval.",
    "guard.upgrade.approve": "Approve key",
    "guard.upgrade.reject": "Reject key",
    "guard.upgrade.stepUp":
      "Approving needs a fresh sign-in with a second factor, within the last five minutes. Sign in again, then repeat this step.",
    "guard.upgrade.done": "The server key was approved.",

    "guard.reach.reason.guard_key_not_bound":
      "The server has no bound key yet. Update Guard so it binds its own key.",
    "guard.reach.reason.connect_tenant_unmappable":
      "Your account has no kombify Connect space yet, so the server cannot be listed there.",
    "guard.reach.reason.host_proof_pending":
      "Waiting for the server to prove its identity to kombify Connect. This usually resolves on its own.",
    "guard.reach.reason.host_proof_rejected":
      "kombify Connect rejected the server's identity proof. Pair the server again.",
    "guard.reach.reason.kombify_me_unavailable":
      "The kombify.me service is unavailable. Check again shortly.",
    "guard.reach.reason.kombify_me_denied":
      "kombify.me refused the registration for this server.",
    "guard.reach.reason.connect_unavailable":
      "kombify Connect is unavailable. Check again shortly.",
    "guard.reach.reason.connect_denied":
      "kombify Connect refused to list this server.",
    "guard.reach.reason.reach_layout_unsupported":
      "This installation layout cannot be given a remote address yet.",
    "guard.reach.reason.revocation_incomplete":
      "Ending access has not finished everywhere yet. It is retried.",
    "guard.reach.reason.identity_unresolved":
      "The owner identity for this server could not be resolved.",

    "guard.notice.legacyRoot":
      "Guard on this server still runs entirely as root; the hardened, unprivileged layout is not active. Update Guard on the server.",
    "guard.notice.executorUnavailable":
      "Guard runs without its privileged executor, so changes to the host are unavailable.",
    "guard.notice.mutationsDenied":
      "A failed update left no usable Guard, so changes are denied until it is resolved.",

    "guard.findings.title": "Security findings",
    "guard.findings.control": "Control",
    "guard.findings.severity": "Severity: {severity}",
    "guard.findings.severity.critical": "critical",
    "guard.findings.severity.high": "high",
    "guard.findings.severity.medium": "medium",
    "guard.findings.severity.low": "low",
    "guard.findings.state.compliant": "Compliant",
    "guard.findings.state.drifted": "Drifted",
    "guard.findings.state.unknown": "Unknown",
    "guard.findings.state.exception": "Approved exception",
    "guard.findings.state.approved_exception": "Approved exception",
    "guard.findings.expected": "Expected",
    "guard.findings.observed": "Observed",
    "guard.findings.remediation": "Remediation:",
    "guard.findings.exception": "Approved exception.",
    "guard.findings.exceptionWith": "Approved exception: {detail}",
    "guard.findings.approvedBy": "approved by {name}",
    "guard.findings.until": "until {at}",

    "guard.verify.action": "Verify now",
    "guard.verify.running": "Verifying…",
    "guard.verify.accepted":
      "Verification requested at {at} (job {job}). The facts update when the server reports the result.",
    "guard.verify.failed": "Verification could not be started: {message}",

    "guard.rotate.action": "Rotate credential",
    "guard.rotate.generationUnknown":
      "Rotation needs the current credential generation, which is not reported yet.",
    "guard.rotate.confirmTitle": "Replace this server's credential?",
    "guard.rotate.confirmBody":
      "A replacement credential is issued and shown once. Copy it to the server so Guard can use it. The old credential stops being accepted.",
    "guard.rotate.confirm": "Rotate credential",
    "guard.rotate.running": "Rotating…",
    "guard.rotate.done":
      "Replacement credential issued (generation {generation}).",
    "guard.rotate.tokenOnce":
      "It is shown once and not stored. Copy it now and give it to Guard on the server.",
    "guard.rotate.copy": "Copy credential",
    "guard.rotate.copied": "Copied",
    "guard.rotate.copyFailed":
      "Could not copy. Select the text and copy it manually.",
    "guard.rotate.dismiss": "I have saved it",

    "guard.detach.action": "Detach server",
    "guard.detach.confirmTitle": "Detach {name}?",
    "guard.detach.confirmBody":
      "This revokes Guard's credential and ends the server's reach: its kombify.me address and its kombify Connect listing. The machine itself and what runs on it are not touched.",
    "guard.detach.reverse":
      "This cannot be undone here. To manage the server again, pair it again.",
    "guard.detach.confirm": "Detach and revoke",
    "guard.detach.running": "Detaching…",

    "guard.connect.link": "Devices in kombify Connect",

    "guard.report.download": "Download report (JSON)",
    "guard.report.print": "Printable report",
    "guard.report.failed": "The report could not be created.",
    "guard.report.title": "kombify Guard report",
    "guard.report.generated": "Generated {at}",
    "guard.report.facts": "Facts",
    "guard.report.colFact": "Fact",
    "guard.report.colState": "State",
    "guard.report.colEvidence": "Evidence",
    "guard.report.colObserved": "Observed",
    "guard.report.noObservation": "No observation time",
    "guard.report.notices": "Reduced protection",
    "guard.report.findings": "Security findings",
    "guard.report.noFindings": "No findings are reported.",
    "guard.report.note":
      "This report lists only what the control plane has persisted, with the time each fact was observed. A fact without fresh evidence is shown as not evidenced, never assumed to be fine. It contains no credentials.",
    "guard.report.digest": "Content digest",

    "guard.outcomes.title": "Operations with an unknown outcome",
    "guard.outcomes.intro":
      "Guard recorded that these operations may or may not have taken effect. Each one blocks further changes on this server until it is settled or you release it.",
    "guard.outcomes.reconciling": "Guard is still trying to settle it",
    "guard.outcomes.needsOperator": "Needs your decision",
    "guard.outcomes.since": "unresolved since {at}",
    "guard.outcomes.resolve": "Resolve",
    "guard.outcomes.dispositionLabel": "What did you find?",
    "guard.outcomes.disposition.verified_applied": "The change is in place",
    "guard.outcomes.disposition.verified_applied.hint":
      "I checked the server: the operation took effect.",
    "guard.outcomes.disposition.verified_not_applied": "Nothing changed",
    "guard.outcomes.disposition.verified_not_applied.hint":
      "I checked the server: the operation did not take effect.",
    "guard.outcomes.disposition.accept_unknown": "Release without verifying",
    "guard.outcomes.disposition.accept_unknown.hint":
      "I could not verify. The outcome stays unknown and is recorded as accepted.",
    "guard.outcomes.noteLabel": "What did you verify?",
    "guard.outcomes.noteHint":
      "Required, up to {max} characters. It is recorded with your name.",
    "guard.outcomes.review": "Review",
    "guard.outcomes.cancel": "Cancel",
    "guard.outcomes.confirmTitle": "Release this operation?",
    "guard.outcomes.confirmBody":
      "This releases the block on this server. If the outcome was not what you recorded, conflicting changes may run against it.",
    "guard.outcomes.confirm": "Release",
    "guard.outcomes.releasing": "Releasing…",
    "guard.outcomes.back": "Back",
    "guard.outcomes.stepUp":
      "Releasing an outcome needs a fresh sign-in with a second factor, within the last five minutes. Sign in again, then repeat this step. Your note is not kept.",
    "guard.outcomes.stepUpAction": "Sign in again",
    "guard.outcomes.stale":
      "This operation changed while you were reviewing it. The list is updated; review it again.",
    "guard.outcomes.loadFailed":
      "The list of unresolved operations could not be loaded.",
    "guard.outcomes.receipt":
      "Released {command} at revision {revision} as “{disposition}”, recorded for {actor}.",
    "guard.outcomes.fenceReleased": "The block on this server is released.",

    "guard.ladder.title": "Pairing readiness",
    "guard.ladder.intro":
      "Each step is checked against what the control plane has recorded, not assumed.",
    "guard.ladder.bound": "Bound",
    "guard.ladder.connected": "Connected",
    "guard.ladder.configuration": "Configuration verified",
    "guard.ladder.reachable": "Reachable",
    "guard.ladder.state.done": "Done",
    "guard.ladder.state.failed": "Needs attention",
    "guard.ladder.state.waiting": "In progress",
    "guard.ladder.state.blocked": "Waiting for the earlier step",
    "guard.ladder.blockedHint": "Starts once the earlier step is done.",
    "guard.ladder.complete": "All pairing steps are evidenced.",
    "guard.ladder.bound.awaiting":
      "Waiting for the pairing command to run on the server.",
    "guard.ladder.bound.awaitingNext":
      "Run the command on the server. If it has expired, generate a new one.",
  },

  de: {
    "guard.backup.title": "Aufgezeichnete Wiederherstellungspunkte",
    "guard.backup.load": "Wiederherstellungspunkte lesen",
    "guard.backup.loading": "Wird gelesen…",
    "guard.backup.recorded":
      "Diese Punkte wurden von diesem Server aufgezeichnet. Historische Backup-Inhalte werden beim Vorbereiten einer Wiederherstellung geprüft.",
    "guard.backup.empty":
      "Dieser Server meldet keine auswählbaren historischen Wiederherstellungspunkte.",
    "guard.backup.choose": "Wiederherstellungspunkt",
    "guard.backup.placeholder": "Aufgezeichneten Punkt auswählen",
    "guard.backup.currentPlan": "Aktueller Plan",
    "guard.backup.historicalPlan": "Früherer Plan",
    "guard.backup.clockSkew": "Uhrzeitabweichung",
    "guard.backup.stage": "Isolierte Wiederherstellung vorbereiten",
    "guard.backup.confirm":
      "Den aufgezeichneten Punkt vom {when} isoliert vorbereiten? Laufende Anwendungsdaten werden nicht ersetzt.",
    "guard.backup.confirmAction": "Diese Wiederherstellung vorbereiten",
    "guard.backup.running":
      "Der Server bereitet eine isolierte Wiederherstellung vor.",
    "guard.backup.staged":
      "Wiederherstellung isoliert vorbereitet. Laufende Anwendungsdaten wurden nicht ersetzt.",
    "guard.backup.unverified":
      "Der Auftrag wurde ohne passenden Nachweis einer vorbereiteten Wiederherstellung beendet. Die Anwendungswiederherstellung ist nicht verifiziert.",
    "guard.backup.failed": "Der Backup-Vorgang wurde nicht abgeschlossen.",
    "guard.backup.job": "Auftrag {id}",
    "guard.backup.scopeMismatch":
      "Der Auftrag gehört nicht zu diesem Server und Stack.",
    "guard.backup.activate": "Vorbereitete Wiederherstellung aktivieren",
    "guard.backup.notAdmitted":
      "Die ursprüngliche Anfrage wurde nicht zugelassen. Es wurde keine aktive Wiederherstellung gestartet.",
    "guard.backup.historicalOperation":
      "Dieser Vorgang gehört zu einer früheren Installation. Sein aufgezeichnetes Ergebnis beschreibt nicht die aktuelle Installation.",
    "guard.backup.activateConfirm":
      "Diese Wiederherstellung auf {name} aktivieren? Laufende Anwendungsdaten werden ersetzt. Der Server behält einen Sicherheitssnapshot und prüft Dienste und Eigentümerzugriff.",
    "guard.backup.activateAction": "Aktive Wiederherstellung bestätigen",
    "guard.backup.activationUnavailable":
      "Mit der aktuell veröffentlichten Laufzeit dieses Servers ist die Aktivierung nicht verfügbar. Die vorbereiteten Daten bleiben isoliert.",
    "guard.backup.activating":
      "Der Server gleicht den bestätigten Wiederherstellungsvorgang ab.",
    "guard.backup.activated":
      "Die gewählte Wiederherstellung wurde vom Server aktiviert und verifiziert.",
    "guard.backup.recovered":
      "Vorheriger Anwendungszustand wiederhergestellt. Die gewählte Wiederherstellung wurde nicht aktiviert.",
    "guard.backup.accessVerified":
      "Dienste und Eigentümerzugriff wurden vom Server am {when} geprüft.",
    "guard.backup.recoveryRequired":
      "Das Ergebnis ist noch unbestätigt. Kläre den ursprünglichen Vorgang, bevor du eine weitere Wiederherstellung startest.",
    "guard.backup.outcomeUnknown":
      "Es liegt noch kein passender endgültiger Wiederherstellungsnachweis vor.",
    "guard.backup.checkOriginal": "Ursprünglichen Vorgang prüfen",
    "guard.backup.resumeRequest": "Ursprüngliche Anfrage fortsetzen",
    "guard.backup.recover": "Ursprünglichen Vorgang wiederherstellen",
    "guard.backup.recoverConfirm":
      "Den ursprünglichen Vorgang abgleichen? Ist er unvollständig, stellt der Server den vorherigen Anwendungszustand wieder her.",
    "guard.backup.recoverAction": "Ursprüngliche Wiederherstellung bestätigen",
    "guard.name": "kombify Guard",
    "guard.promise":
      "Gebaut, damit dieser Server an dich gebunden ist, laufend geprüft wird, von überall erreichbar und von Grund auf wiederherstellbar ist. Die Fakten unten zeigen, was gerade belegt ist.",
    "guard.cancel": "Abbrechen",
    "guard.next": "Als Nächstes:",
    "guard.observed": "Beobachtet {when}",
    "guard.noObservation": "Keine Beobachtungszeit gemeldet",
    "guard.reduced": "Reduzierter Schutz",
    "guard.serverUnavailable":
      "Der Servereintrag konnte nicht gelesen werden, daher ist aktuell keiner dieser Fakten belegt.",
    "guard.state.ok": "Belegt",
    "guard.state.attention": "Braucht Aufmerksamkeit",
    "guard.state.unknown": "Nicht belegt",
    "guard.overall.ready": "Guard-bereit",
    "guard.overall.attention": "Braucht Aufmerksamkeit",
    "guard.overall.unverified": "Noch nicht belegt",
    "guard.overall.count": "{ok} von {total} Fakten belegt",
    "guard.badge.ready": "Guard-bereit",
    "guard.badge.attention": "Guard: Aufmerksamkeit nötig",
    "guard.badge.unverified": "Guard: {ok}/{total} belegt",
    "guard.badge.open": "kombify Guard öffnen",
    "guard.detail.reasonCode": "Grundcode: {code}",

    "guard.fact.bound.title": "Gebunden",
    "guard.fact.connected.title": "Verbunden",
    "guard.fact.operations.title": "Vorgänge dauerhaft",
    "guard.fact.configuration.title": "Konfiguration abgesichert",
    "guard.fact.updated.title": "Aktuell",
    "guard.fact.recoverable.title": "Wiederherstellbar",
    "guard.fact.reachable.title": "Erreichbar & gesteuert",

    "guard.fact.bound.ok":
      "Dieser Server weist bei jeder Verbindung seine Identität mit seinem eigenen Schlüssel nach.",
    "guard.fact.bound.legacy":
      "Dieser Server identifiziert sich noch allein mit einem Bearer-Zugangsdatum, das nicht beweist, welche Maschine spricht.",
    "guard.fact.bound.legacyNext":
      "Aktualisiere Guard auf dem Server, damit es seinen eigenen Schlüssel anmeldet, und gib diesen Schlüssel dann hier frei.",
    "guard.fact.bound.revoked":
      "Dieser Server wurde widerrufen und ist nicht mehr gebunden.",
    "guard.fact.bound.revokedNext":
      "Koppele den Server erneut, um ihn an deinen Techstack zu binden.",
    "guard.fact.bound.stale":
      "Die letzte Meldung ist zu alt, um zu bestätigen, wie dieser Server seine Identität nachweist.",
    "guard.fact.bound.unknown":
      "Der Server hat nicht gemeldet, wie er seine Identität nachweist.",
    "guard.fact.bound.unknownNext":
      "Warte auf die nächste Meldung. Erscheint sie nicht, aktualisiere Guard auf dem Server.",

    "guard.fact.connected.ok":
      "Guard hat über seine authentifizierte Verbindung einen Befehl beantwortet.",
    "guard.fact.connected.degraded": "Die Verbindung ist beeinträchtigt.",
    "guard.fact.connected.offline":
      "Guard meldet sich nicht. Der Server ist offline oder erreicht Techstack nicht.",
    "guard.fact.connected.revoked": "Die Verbindung wurde widerrufen.",
    "guard.fact.connected.stale":
      "Guard hat sich nicht aktuell genug gemeldet, um als verbunden zu gelten.",
    "guard.fact.connected.pending":
      "Guard hat seine erste Verbindung noch nicht abgeschlossen.",
    "guard.fact.connected.pendingNext":
      "Führe den Kopplungsbefehl auf dem Server aus oder warte kurz und prüfe erneut.",
    "guard.fact.connected.unknown":
      "Für diesen Server wird kein Verbindungsstatus gemeldet.",
    "guard.fact.connected.reconnectNext":
      "Prüfe, ob der Server eingeschaltet ist und Techstack erreichen kann.",
    "guard.fact.connected.awaitingRoundTrip":
      "Guard meldet sich, hat aber noch keinen Befehl von Techstack mit seinem aktuellen Zugang beantwortet.",
    "guard.fact.connected.roundTripStale":
      "Guard meldet sich, aber der zuletzt beantwortete Befehl ist zu alt oder wurde mit einem früheren Zugang beantwortet.",
    "guard.fact.connected.roundTripNext":
      "Der nächste Vorgang auf diesem Server bestätigt die Verbindung; Meldungen allein genügen nicht.",

    "guard.fact.operations.ok":
      "Vorgänge werden dauerhaft protokolliert und nichts bleibt ungeklärt.",
    "guard.fact.operations.unacked":
      "Ergebnisse, die auf Bestätigung warten: {count}.",
    "guard.fact.operations.unresolved":
      "Vorgänge mit unbekanntem Ausgang: {count}. Sie blockieren weitere Änderungen an diesem Server.",
    "guard.fact.operations.unresolvedNext":
      "Sieh sie unten durch und halte fest, was du geprüft hast.",
    "guard.fact.operations.legacy":
      "Dieser Guard führt kein dauerhaftes Vorgangsjournal, daher kann ein unterbrochener Vorgang nicht verlässlich gemeldet werden.",
    "guard.fact.operations.legacyNext": "Aktualisiere Guard auf dem Server.",
    "guard.fact.operations.denied":
      "Das Vorgangsjournal ist nicht beschreibbar, daher werden Änderungen abgelehnt.",
    "guard.fact.operations.deniedNext":
      "Prüfe freien Speicherplatz und Berechtigungen auf dem Server und warte dann auf die nächste Meldung.",
    "guard.fact.operations.stale":
      "Die letzte Meldung ist zu alt, um den Journalstatus zu bestätigen.",
    "guard.fact.operations.unknown":
      "Der Server hat sein Vorgangsjournal nicht gemeldet.",
    "guard.fact.operations.unknownNext":
      "Warte auf die nächste Meldung. Erscheint sie nicht, aktualisiere Guard auf dem Server.",

    "guard.fact.configuration.ok":
      "Der Host entspricht der Sicherheits-Baseline.",
    "guard.fact.configuration.okExceptions":
      "Der Host entspricht der Sicherheits-Baseline bis auf genehmigte Ausnahmen.",
    "guard.fact.configuration.baseline": "Baseline-Version {version}",
    "guard.fact.configuration.exceptions": "Genehmigte Ausnahmen: {count}",
    "guard.fact.configuration.drifted":
      "Kontrollen, die von der Baseline abweichen: {count}.",
    "guard.fact.configuration.driftedNext":
      "Sieh dir die Befunde unten an und wende die Behebung an.",
    "guard.fact.configuration.stale":
      "Die letzte Prüfung ist zu alt, um zu zählen. Eine Abweichung seitdem wäre nicht sichtbar.",
    "guard.fact.configuration.unknown":
      "Die letzte Prüfung konnte den Zustand nicht feststellen.",
    "guard.fact.configuration.notReported":
      "Für diesen Server wird keine Host-Sicherheitsprüfung gemeldet.",
    "guard.fact.configuration.notReportedNext":
      "Aktualisiere Guard und das StackKit auf dem Server, damit sie sie melden können.",
    "guard.fact.configuration.verifyNext": "Prüfe den Server erneut.",

    "guard.fact.updated.ok": "Guard und das Betriebssystem sind aktuell.",
    "guard.fact.updated.attention": "Etwas muss aktualisiert werden.",
    "guard.fact.updated.stale":
      "Die letzte Meldung ist zu alt, um den Update-Stand zu bestätigen.",
    "guard.fact.updated.unknown": "Der Update-Stand ist nicht belegt.",
    "guard.fact.updated.unknownNext":
      "Warte auf die nächste Meldung oder aktualisiere Guard auf dem Server.",
    "guard.fact.updated.guardOk": "Guard {version} ist signiert und aktuell.",
    "guard.fact.updated.guardProblem":
      "Guard-Update-Status: {state} (Version {version}).",
    "guard.fact.updated.guardProblemNext":
      "Prüfe das Guard-Update auf dem Server.",
    "guard.fact.updated.guardInProgress": "Ein Guard-Update wird installiert.",
    "guard.fact.updated.guardUnknown":
      "Guard-Version und Signatur werden nicht gemeldet.",
    "guard.fact.updated.guardUnsigned":
      "Das installierte Guard-Artefakt ist nicht signiert.",
    "guard.fact.updated.osOk":
      "Keine ausstehenden Sicherheitsupdates und kein Neustart nötig.",
    "guard.fact.updated.osPending":
      "Ausstehende Sicherheitsupdates des Betriebssystems: {count}.",
    "guard.fact.updated.osPendingNext":
      "Installiere die ausstehenden Sicherheitsupdates.",
    "guard.fact.updated.osReboot":
      "Ein Neustart ist nötig, um Updates abzuschließen.",
    "guard.fact.updated.osRebootNext":
      "Starte den Server neu, wenn es dir passt.",
    "guard.fact.updated.osUnknown":
      "Der Update-Stand des Betriebssystems wird nicht gemeldet.",
    "guard.fact.updated.lastRun":
      "Letzter unbeaufsichtigter Sicherheitsupdate-Lauf: {at}",

    "guard.fact.recoverable.ok":
      "Ein aktuelles Backup liegt vor und eine Wiederherstellung wurde geprüft.",
    "guard.fact.recoverable.attention":
      "Backup- oder Wiederherstellungsnachweis ist überfällig.",
    "guard.fact.recoverable.unknown":
      "Die Wiederherstellbarkeit ist nicht vollständig belegt.",
    "guard.fact.recoverable.notReported":
      "Es wird kein Backup- oder Wiederherstellungsnachweis gemeldet.",
    "guard.fact.recoverable.notReportedNext":
      "Richte Backups für diesen Server ein und prüfe dann eine Wiederherstellung.",
    "guard.fact.recoverable.attentionNext":
      "Erstelle ein frisches Backup oder wiederhole die Wiederherstellungsprobe.",
    "guard.fact.recoverable.unknownNext":
      "Warte auf die nächste Meldung oder prüfe eine Wiederherstellung.",
    "guard.fact.recoverable.backupFresh":
      "Das Backup liegt innerhalb der Vorgabe.",
    "guard.fact.recoverable.backupStale":
      "Das letzte Backup ist älter, als die Vorgabe erlaubt.",
    "guard.fact.recoverable.backupUnknown":
      "Die Aktualität des Backups wird nicht gemeldet.",
    "guard.fact.recoverable.drillVerified":
      "Eine Wiederherstellung wurde geprüft.",
    "guard.fact.recoverable.drillStale":
      "Die zuletzt geprüfte Wiederherstellung ist veraltet.",
    "guard.fact.recoverable.drillUnknown":
      "Es wird keine geprüfte Wiederherstellung gemeldet.",

    "guard.fact.reachable.ok":
      "Dieser Server ist unter seiner kombify.me-Adresse registriert und in kombify Connect gelistet.",
    "guard.fact.reachable.address": "Adresse: {address}",
    "guard.fact.reachable.notReported":
      "Für diesen Server wird keine Fernerreichbarkeit gemeldet.",
    "guard.fact.reachable.notReportedNext":
      "Verbinde die Installation mit kombify Cloud, um ihre Adresse zu registrieren, oder prüfe später erneut.",
    "guard.fact.reachable.notEnrolled":
      "Dieser Server ist noch nicht für den Fernzugriff eingerichtet.",
    "guard.fact.reachable.pending": "Der Fernzugriff wird eingerichtet.",
    "guard.fact.reachable.pendingNext": "Warte kurz und prüfe erneut.",
    "guard.fact.reachable.failed":
      "Die Einrichtung des Fernzugriffs ist fehlgeschlagen.",
    "guard.fact.reachable.failedNext":
      "Prüfe in Kürze erneut. Besteht das Problem, wende dich mit dem Grundcode an den Support.",
    "guard.fact.reachable.revoked":
      "Der Fernzugriff für diesen Server wurde widerrufen.",
    "guard.fact.reachable.revokedNext":
      "Koppele den Server erneut, um den Fernzugriff wiederherzustellen.",
    "guard.fact.reachable.stale":
      "Die letzte Erreichbarkeitsmeldung ist zu alt, um zu bestätigen, dass die Adresse noch antwortet.",
    "guard.fact.reachable.relayConnecting":
      "Die Relay-Verbindung wird aufgebaut.",
    "guard.fact.reachable.relayDown":
      "Die Relay-Verbindung von Guard ist unterbrochen, daher antwortet die Adresse nicht.",
    "guard.fact.reachable.relayDownNext":
      "Prüfe den Grundcode und ob der Server das Internet erreichen kann.",

    "guard.fact.reachable.notEnabled":
      "Dieser Server ist nicht unter einer kombify.me-Adresse veröffentlicht. Das ist deine Entscheidung, bis du es einschaltest.",
    "guard.fact.reachable.notEnabledNext":
      "Um einen Dienst zu veröffentlichen, gib unten seinen Namen und den Port an, auf dem er lauscht.",
    "guard.reach.publish.title":
      "Unter einer kombify.me-Adresse veröffentlichen",
    "guard.reach.publish.intro":
      "Nichts wird veröffentlicht, bevor du es einschaltest. Jeder im Internet kann die Adresse öffnen – veröffentliche daher nur einen Dienst, der sich selbst schützt.",
    "guard.reach.publish.service": "Dienstname",
    "guard.reach.publish.port": "Port auf diesem Server",
    "guard.reach.publish.enable": "Veröffentlichen",
    "guard.reach.publish.disable": "Veröffentlichung beenden",
    "guard.reach.publish.current": "Veröffentlicht: {service} auf Port {port}.",
    "guard.reach.publish.stepUp":
      "Das Veröffentlichen braucht eine frische Anmeldung mit zweitem Faktor innerhalb der letzten fünf Minuten. Melde dich erneut an und wiederhole diesen Schritt.",
    "guard.upgrade.title": "Schlüssel dieses Servers freigeben",
    "guard.upgrade.pending":
      "Ein Schlüssel mit dem Fingerabdruck {fingerprint} wartet auf deine Freigabe. Gib ihn nur frei, wenn Guard auf diesem Server denselben Fingerabdruck protokolliert.",
    "guard.upgrade.none":
      "Dieser Server hat noch keinen Schlüssel vorgeschlagen. Aktualisiere Guard auf dem Server; sein Schlüssel-Fingerabdruck erscheint dann hier zur Freigabe.",
    "guard.upgrade.approve": "Schlüssel freigeben",
    "guard.upgrade.reject": "Schlüssel ablehnen",
    "guard.upgrade.stepUp":
      "Die Freigabe braucht eine frische Anmeldung mit zweitem Faktor innerhalb der letzten fünf Minuten. Melde dich erneut an und wiederhole diesen Schritt.",
    "guard.upgrade.done": "Der Serverschlüssel wurde freigegeben.",

    "guard.reach.reason.guard_key_not_bound":
      "Der Server hat noch keinen gebundenen Schlüssel. Aktualisiere Guard, damit er seinen eigenen Schlüssel bindet.",
    "guard.reach.reason.connect_tenant_unmappable":
      "Dein Konto hat noch keinen kombify-Connect-Bereich, daher kann der Server dort nicht gelistet werden.",
    "guard.reach.reason.host_proof_pending":
      "Warte darauf, dass der Server sich gegenüber kombify Connect ausweist. Das löst sich meist von selbst.",
    "guard.reach.reason.host_proof_rejected":
      "kombify Connect hat den Identitätsnachweis des Servers abgelehnt. Koppele den Server erneut.",
    "guard.reach.reason.kombify_me_unavailable":
      "Der Dienst kombify.me ist nicht verfügbar. Prüfe in Kürze erneut.",
    "guard.reach.reason.kombify_me_denied":
      "kombify.me hat die Registrierung dieses Servers abgelehnt.",
    "guard.reach.reason.connect_unavailable":
      "kombify Connect ist nicht verfügbar. Prüfe in Kürze erneut.",
    "guard.reach.reason.connect_denied":
      "kombify Connect hat es abgelehnt, diesen Server zu listen.",
    "guard.reach.reason.reach_layout_unsupported":
      "Für dieses Installationslayout kann noch keine Fernadresse vergeben werden.",
    "guard.reach.reason.revocation_incomplete":
      "Das Beenden des Zugriffs ist noch nicht überall abgeschlossen. Es wird erneut versucht.",
    "guard.reach.reason.identity_unresolved":
      "Die Eigentümeridentität dieses Servers konnte nicht aufgelöst werden.",

    "guard.notice.legacyRoot":
      "Guard läuft auf diesem Server noch vollständig als root; das gehärtete, unprivilegierte Layout ist nicht aktiv. Aktualisiere Guard auf dem Server.",
    "guard.notice.executorUnavailable":
      "Guard läuft ohne seinen privilegierten Ausführer, daher sind Änderungen am Host nicht verfügbar.",
    "guard.notice.mutationsDenied":
      "Ein fehlgeschlagenes Update hat keinen nutzbaren Guard hinterlassen, daher werden Änderungen abgelehnt, bis es geklärt ist.",

    "guard.findings.title": "Sicherheitsbefunde",
    "guard.findings.control": "Kontrolle",
    "guard.findings.severity": "Schweregrad: {severity}",
    "guard.findings.severity.critical": "kritisch",
    "guard.findings.severity.high": "hoch",
    "guard.findings.severity.medium": "mittel",
    "guard.findings.severity.low": "niedrig",
    "guard.findings.state.compliant": "Konform",
    "guard.findings.state.drifted": "Abgewichen",
    "guard.findings.state.unknown": "Unbekannt",
    "guard.findings.state.exception": "Genehmigte Ausnahme",
    "guard.findings.state.approved_exception": "Genehmigte Ausnahme",
    "guard.findings.expected": "Erwartet",
    "guard.findings.observed": "Beobachtet",
    "guard.findings.remediation": "Behebung:",
    "guard.findings.exception": "Genehmigte Ausnahme.",
    "guard.findings.exceptionWith": "Genehmigte Ausnahme: {detail}",
    "guard.findings.approvedBy": "genehmigt von {name}",
    "guard.findings.until": "bis {at}",

    "guard.verify.action": "Jetzt prüfen",
    "guard.verify.running": "Prüfung läuft…",
    "guard.verify.accepted":
      "Prüfung um {at} angefordert (Auftrag {job}). Die Fakten aktualisieren sich, sobald der Server das Ergebnis meldet.",
    "guard.verify.failed":
      "Die Prüfung konnte nicht gestartet werden: {message}",

    "guard.rotate.action": "Zugangsdaten rotieren",
    "guard.rotate.generationUnknown":
      "Die Rotation braucht die aktuelle Generation der Zugangsdaten, die noch nicht gemeldet wird.",
    "guard.rotate.confirmTitle": "Zugangsdaten dieses Servers ersetzen?",
    "guard.rotate.confirmBody":
      "Es werden neue Zugangsdaten ausgestellt und einmalig angezeigt. Kopiere sie auf den Server, damit Guard sie nutzen kann. Die alten Zugangsdaten werden nicht mehr akzeptiert.",
    "guard.rotate.confirm": "Zugangsdaten rotieren",
    "guard.rotate.running": "Rotation läuft…",
    "guard.rotate.done":
      "Neue Zugangsdaten ausgestellt (Generation {generation}).",
    "guard.rotate.tokenOnce":
      "Sie werden einmalig angezeigt und nicht gespeichert. Kopiere sie jetzt und gib sie Guard auf dem Server.",
    "guard.rotate.copy": "Zugangsdaten kopieren",
    "guard.rotate.copied": "Kopiert",
    "guard.rotate.copyFailed":
      "Kopieren nicht möglich. Markiere den Text und kopiere ihn manuell.",
    "guard.rotate.dismiss": "Ich habe sie gesichert",

    "guard.detach.action": "Server trennen",
    "guard.detach.confirmTitle": "{name} trennen?",
    "guard.detach.confirmBody":
      "Das widerruft die Zugangsdaten von Guard und beendet die Erreichbarkeit des Servers: seine kombify.me-Adresse und seinen Eintrag in kombify Connect. Die Maschine selbst und was darauf läuft, bleiben unberührt.",
    "guard.detach.reverse":
      "Das lässt sich hier nicht rückgängig machen. Um den Server wieder zu verwalten, koppele ihn erneut.",
    "guard.detach.confirm": "Trennen und widerrufen",
    "guard.detach.running": "Trennung läuft…",

    "guard.connect.link": "Geräte in kombify Connect",

    "guard.report.download": "Bericht herunterladen (JSON)",
    "guard.report.print": "Druckbarer Bericht",
    "guard.report.failed": "Der Bericht konnte nicht erstellt werden.",
    "guard.report.title": "kombify-Guard-Bericht",
    "guard.report.generated": "Erstellt {at}",
    "guard.report.facts": "Fakten",
    "guard.report.colFact": "Fakt",
    "guard.report.colState": "Status",
    "guard.report.colEvidence": "Nachweis",
    "guard.report.colObserved": "Beobachtet",
    "guard.report.noObservation": "Keine Beobachtungszeit",
    "guard.report.notices": "Reduzierter Schutz",
    "guard.report.findings": "Sicherheitsbefunde",
    "guard.report.noFindings": "Es werden keine Befunde gemeldet.",
    "guard.report.note":
      "Dieser Bericht listet nur, was die Steuerungsebene gespeichert hat, mit der Zeit, zu der jeder Fakt beobachtet wurde. Ein Fakt ohne frischen Nachweis erscheint als nicht belegt und wird nie als in Ordnung angenommen. Er enthält keine Zugangsdaten.",
    "guard.report.digest": "Inhalts-Digest",

    "guard.outcomes.title": "Vorgänge mit unbekanntem Ausgang",
    "guard.outcomes.intro":
      "Guard hat festgehalten, dass diese Vorgänge gewirkt haben können oder nicht. Jeder blockiert weitere Änderungen an diesem Server, bis er geklärt ist oder du ihn freigibst.",
    "guard.outcomes.reconciling": "Guard versucht noch, ihn zu klären",
    "guard.outcomes.needsOperator": "Braucht deine Entscheidung",
    "guard.outcomes.since": "ungeklärt seit {at}",
    "guard.outcomes.resolve": "Klären",
    "guard.outcomes.dispositionLabel": "Was hast du festgestellt?",
    "guard.outcomes.disposition.verified_applied": "Die Änderung ist umgesetzt",
    "guard.outcomes.disposition.verified_applied.hint":
      "Ich habe den Server geprüft: Der Vorgang hat gewirkt.",
    "guard.outcomes.disposition.verified_not_applied":
      "Nichts hat sich geändert",
    "guard.outcomes.disposition.verified_not_applied.hint":
      "Ich habe den Server geprüft: Der Vorgang hat nicht gewirkt.",
    "guard.outcomes.disposition.accept_unknown": "Ohne Prüfung freigeben",
    "guard.outcomes.disposition.accept_unknown.hint":
      "Ich konnte nichts prüfen. Der Ausgang bleibt unbekannt und wird als akzeptiert festgehalten.",
    "guard.outcomes.noteLabel": "Was hast du geprüft?",
    "guard.outcomes.noteHint":
      "Pflichtfeld, bis zu {max} Zeichen. Es wird mit deinem Namen festgehalten.",
    "guard.outcomes.review": "Prüfen",
    "guard.outcomes.cancel": "Abbrechen",
    "guard.outcomes.confirmTitle": "Diesen Vorgang freigeben?",
    "guard.outcomes.confirmBody":
      "Das hebt die Sperre auf diesem Server auf. War der Ausgang nicht wie festgehalten, können widersprüchliche Änderungen darauf laufen.",
    "guard.outcomes.confirm": "Freigeben",
    "guard.outcomes.releasing": "Freigabe läuft…",
    "guard.outcomes.back": "Zurück",
    "guard.outcomes.stepUp":
      "Zum Freigeben eines Ausgangs ist eine frische Anmeldung mit zweitem Faktor nötig, nicht älter als fünf Minuten. Melde dich erneut an und wiederhole dann diesen Schritt. Deine Notiz bleibt nicht erhalten.",
    "guard.outcomes.stepUpAction": "Erneut anmelden",
    "guard.outcomes.stale":
      "Dieser Vorgang hat sich geändert, während du ihn geprüft hast. Die Liste ist aktualisiert; prüfe ihn erneut.",
    "guard.outcomes.loadFailed":
      "Die Liste ungeklärter Vorgänge konnte nicht geladen werden.",
    "guard.outcomes.receipt":
      "{command} in Revision {revision} als „{disposition}“ freigegeben, festgehalten für {actor}.",
    "guard.outcomes.fenceReleased":
      "Die Sperre auf diesem Server ist aufgehoben.",

    "guard.ladder.title": "Kopplungsbereitschaft",
    "guard.ladder.intro":
      "Jeder Schritt wird gegen das geprüft, was die Steuerungsebene gespeichert hat, nicht angenommen.",
    "guard.ladder.bound": "Gebunden",
    "guard.ladder.connected": "Verbunden",
    "guard.ladder.configuration": "Konfiguration geprüft",
    "guard.ladder.reachable": "Erreichbar",
    "guard.ladder.state.done": "Erledigt",
    "guard.ladder.state.failed": "Braucht Aufmerksamkeit",
    "guard.ladder.state.waiting": "In Arbeit",
    "guard.ladder.state.blocked": "Wartet auf den früheren Schritt",
    "guard.ladder.blockedHint":
      "Beginnt, sobald der frühere Schritt erledigt ist.",
    "guard.ladder.complete": "Alle Kopplungsschritte sind belegt.",
    "guard.ladder.bound.awaiting":
      "Warte darauf, dass der Kopplungsbefehl auf dem Server ausgeführt wird.",
    "guard.ladder.bound.awaitingNext":
      "Führe den Befehl auf dem Server aus. Ist er abgelaufen, erzeuge einen neuen.",
  },
} as const;
