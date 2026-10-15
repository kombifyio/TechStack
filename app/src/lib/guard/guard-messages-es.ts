import type { guardMessages } from "./guard-messages";

// Spanish catalog of the kombify Guard surfaces. Same keys and placeholders as
// the English catalog; the type makes a missing key a compile error.
export const guardMessagesEs: Record<keyof typeof guardMessages.en, string> = {
  "guard.backup.title": "Puntos de recuperación registrados",
  "guard.backup.load": "Consultar puntos de recuperación",
  "guard.backup.loading": "Consultando…",
  "guard.backup.recorded":
    "Este nodo ha registrado estos puntos. El contenido histórico se comprueba al preparar una restauración.",
  "guard.backup.empty":
    "Este nodo no informa de puntos históricos seleccionables.",
  "guard.backup.choose": "Punto de recuperación",
  "guard.backup.placeholder": "Selecciona un punto registrado",
  "guard.backup.currentPlan": "Plan actual",
  "guard.backup.historicalPlan": "Plan anterior",
  "guard.backup.clockSkew": "Diferencia de reloj",
  "guard.backup.stage": "Preparar restauración aislada",
  "guard.backup.confirm":
    "¿Preparar el punto registrado de {when} en un área aislada? No se sustituirán los datos activos.",
  "guard.backup.confirmAction": "Preparar esta restauración",
  "guard.backup.running": "El nodo está preparando una restauración aislada.",
  "guard.backup.staged":
    "Restauración preparada en un área aislada. No se han sustituido los datos activos.",
  "guard.backup.unverified":
    "El trabajo terminó sin evidencia coincidente de restauración preparada. La recuperación de la aplicación no está verificada.",
  "guard.backup.failed": "La operación de copia de seguridad no se completó.",
  "guard.backup.job": "Trabajo {id}",
  "guard.backup.scopeMismatch":
    "El trabajo no pertenece a este servidor y stack.",
  "guard.name": "kombify Guard",
  "guard.backup.activate": "Activar restauración preparada",
  "guard.backup.notAdmitted":
    "La solicitud original no fue admitida. No se inició ninguna restauración activa.",
  "guard.backup.historicalOperation":
    "Esta operación pertenece a una instalación anterior. Su resultado registrado no describe la instalación actual.",
  "guard.backup.activateConfirm":
    "¿Activar esta restauración en {name}? Sustituye los datos activos. El nodo conserva una copia de seguridad y verifica los servicios y el acceso del propietario.",
  "guard.backup.activateAction": "Confirmar restauración activa",
  "guard.backup.activationUnavailable":
    "La activación no está disponible con la versión publicada actual de este nodo. Los datos preparados permanecen aislados.",
  "guard.backup.activating":
    "El nodo está conciliando la restauración aprobada.",
  "guard.backup.activated":
    "El nodo activó y verificó la restauración seleccionada.",
  "guard.backup.recovered":
    "Se recuperó el estado anterior de la aplicación. La restauración seleccionada no se activó.",
  "guard.backup.accessVerified":
    "El nodo verificó los servicios y el acceso del propietario el {when}.",
  "guard.backup.recoveryRequired":
    "El resultado sigue sin confirmar. Resuelve la operación original antes de iniciar otra restauración.",
  "guard.backup.outcomeUnknown":
    "Todavía no hay evidencia final coincidente de restauración.",
  "guard.backup.checkOriginal": "Comprobar operación original",
  "guard.backup.resumeRequest": "Continuar solicitud original",
  "guard.backup.recover": "Recuperar operación original",
  "guard.backup.recoverConfirm":
    "¿Conciliar la operación original? Si está incompleta, el nodo recupera el estado anterior de la aplicación.",
  "guard.backup.recoverAction": "Confirmar recuperación original",
  "guard.promise":
    "Diseñado para mantener este servidor vinculado a ti, comprobado de forma continua, accesible desde cualquier lugar y recuperable por diseño. Los hechos de abajo muestran lo que está acreditado ahora mismo.",
  "guard.cancel": "Cancelar",
  "guard.next": "Siguiente paso:",
  "guard.observed": "Observado {when}",
  "guard.noObservation": "No se informó la hora de observación",
  "guard.reduced": "Protección reducida",
  "guard.serverUnavailable":
    "No se pudo leer el registro del servidor, así que ninguno de estos hechos está acreditado ahora mismo.",
  "guard.state.ok": "Acreditado",
  "guard.state.attention": "Requiere atención",
  "guard.state.unknown": "No acreditado",
  "guard.overall.ready": "Guard listo",
  "guard.overall.attention": "Requiere atención",
  "guard.overall.unverified": "Aún no acreditado",
  "guard.overall.count": "{ok} de {total} hechos acreditados",
  "guard.badge.ready": "Guard listo",
  "guard.badge.attention": "Guard: requiere atención",
  "guard.badge.unverified": "Guard: {ok}/{total} acreditados",
  "guard.badge.open": "Abrir el panel de kombify Guard",
  "guard.detail.reasonCode": "Código de motivo: {code}",

  "guard.fact.bound.title": "Vinculado",
  "guard.fact.connected.title": "Conectado",
  "guard.fact.operations.title": "Operaciones duraderas",
  "guard.fact.configuration.title": "Configuración protegida",
  "guard.fact.updated.title": "Actualizado",
  "guard.fact.recoverable.title": "Recuperable",
  "guard.fact.reachable.title": "Accesible y gobernado",

  "guard.fact.bound.ok":
    "Este servidor demuestra su identidad con su propia clave en cada conexión.",
  "guard.fact.bound.legacy":
    "Este servidor aún se identifica solo con una credencial de portador, que no demuestra qué máquina está hablando.",
  "guard.fact.bound.legacyNext":
    "Actualiza Guard en el servidor para que registre su propia clave y luego aprueba esa clave aquí.",
  "guard.fact.bound.revoked":
    "Este servidor fue revocado y ya no está vinculado.",
  "guard.fact.bound.revokedNext":
    "Vuelve a emparejar el servidor para vincularlo a tu Techstack.",
  "guard.fact.bound.stale":
    "El último informe es demasiado antiguo para confirmar cómo este servidor demuestra su identidad.",
  "guard.fact.bound.unknown":
    "El servidor no ha informado de cómo demuestra su identidad.",
  "guard.fact.bound.unknownNext":
    "Espera al siguiente informe. Si no aparece, actualiza Guard en el servidor.",

  "guard.fact.connected.ok":
    "Guard informa a través de su conexión autenticada.",
  "guard.fact.connected.degraded": "La conexión está degradada.",
  "guard.fact.connected.offline":
    "Guard no informa. El servidor está sin conexión o no puede alcanzar Techstack.",
  "guard.fact.connected.revoked": "La conexión fue revocada.",
  "guard.fact.connected.stale":
    "Guard no ha informado hace lo suficientemente poco como para contar como conectado.",
  "guard.fact.connected.pending":
    "Guard aún no ha completado su primera conexión.",
  "guard.fact.connected.pendingNext":
    "Ejecuta el comando de emparejamiento en el servidor, o espera un momento y vuelve a comprobar.",
  "guard.fact.connected.unknown":
    "No se informa ningún estado de conexión para este servidor.",
  "guard.fact.connected.reconnectNext":
    "Comprueba que el servidor está encendido y puede alcanzar Techstack.",

  "guard.fact.operations.ok":
    "Las operaciones se registran de forma duradera y no queda nada sin resolver.",
  "guard.fact.operations.unacked":
    "Resultados pendientes de confirmación: {count}.",
  "guard.fact.operations.unresolved":
    "Operaciones con resultado desconocido: {count}. Bloquean más cambios en este servidor.",
  "guard.fact.operations.unresolvedNext":
    "Revísalas abajo y anota lo que verificaste.",
  "guard.fact.operations.legacy":
    "Este Guard no mantiene un diario de operaciones duradero, por lo que una operación interrumpida no se puede notificar de forma fiable.",
  "guard.fact.operations.legacyNext": "Actualiza Guard en el servidor.",
  "guard.fact.operations.denied":
    "El diario de operaciones no se puede escribir, por lo que se deniegan los cambios.",
  "guard.fact.operations.deniedNext":
    "Comprueba el espacio libre en disco y los permisos en el servidor, y espera al siguiente informe.",
  "guard.fact.operations.stale":
    "El último informe es demasiado antiguo para confirmar el estado del diario.",
  "guard.fact.operations.unknown":
    "El servidor no ha informado de su diario de operaciones.",
  "guard.fact.operations.unknownNext":
    "Espera al siguiente informe. Si no aparece, actualiza Guard en el servidor.",

  "guard.fact.configuration.ok": "El host cumple la línea base de seguridad.",
  "guard.fact.configuration.okExceptions":
    "El host cumple la línea base de seguridad salvo las excepciones aprobadas.",
  "guard.fact.configuration.baseline": "Versión de la línea base {version}",
  "guard.fact.configuration.exceptions": "Excepciones aprobadas: {count}",
  "guard.fact.configuration.drifted":
    "Controles que se desvían de la línea base: {count}.",
  "guard.fact.configuration.driftedNext":
    "Revisa los hallazgos de abajo y aplica la corrección.",
  "guard.fact.configuration.stale":
    "La última verificación es demasiado antigua para contar. Una desviación posterior no sería visible.",
  "guard.fact.configuration.unknown":
    "La última verificación no pudo establecer el estado.",
  "guard.fact.configuration.notReported":
    "No se informa la verificación de seguridad del host para este servidor.",
  "guard.fact.configuration.notReportedNext":
    "Actualiza Guard y el StackKit en el servidor para que puedan informarla.",
  "guard.fact.configuration.verifyNext": "Vuelve a verificar el servidor.",

  "guard.fact.updated.ok": "Guard y el sistema operativo están actualizados.",
  "guard.fact.updated.attention": "Algo necesita actualizarse.",
  "guard.fact.updated.stale":
    "El último informe es demasiado antiguo para confirmar el estado de las actualizaciones.",
  "guard.fact.updated.unknown":
    "El estado de las actualizaciones no está acreditado.",
  "guard.fact.updated.unknownNext":
    "Espera al siguiente informe o actualiza Guard en el servidor.",
  "guard.fact.updated.guardOk": "Guard {version} está firmado y al día.",
  "guard.fact.updated.guardProblem":
    "Estado de la actualización de Guard: {state} (versión {version}).",
  "guard.fact.updated.guardProblemNext":
    "Revisa la actualización de Guard en el servidor.",
  "guard.fact.updated.guardInProgress":
    "Se está instalando una actualización de Guard.",
  "guard.fact.updated.guardUnknown":
    "No se informan la versión ni la firma de Guard.",
  "guard.fact.updated.guardUnsigned":
    "El artefacto de Guard instalado no está firmado.",
  "guard.fact.updated.osOk":
    "Sin actualizaciones de seguridad pendientes y sin reinicio necesario.",
  "guard.fact.updated.osPending":
    "Actualizaciones de seguridad del sistema operativo pendientes: {count}.",
  "guard.fact.updated.osPendingNext":
    "Instala las actualizaciones de seguridad pendientes.",
  "guard.fact.updated.osReboot":
    "Hace falta un reinicio para terminar las actualizaciones.",
  "guard.fact.updated.osRebootNext": "Reinicia el servidor cuando te convenga.",
  "guard.fact.updated.osUnknown":
    "No se informa el estado de actualización del sistema operativo.",
  "guard.fact.updated.lastRun":
    "Última ejecución desatendida de actualizaciones de seguridad: {at}",

  "guard.fact.recoverable.ok":
    "Hay una copia de seguridad reciente y se verificó una restauración.",
  "guard.fact.recoverable.attention":
    "La evidencia de copia o de restauración está vencida.",
  "guard.fact.recoverable.unknown":
    "La recuperación no está totalmente acreditada.",
  "guard.fact.recoverable.notReported":
    "No se informa ninguna evidencia de copia ni de restauración.",
  "guard.fact.recoverable.notReportedNext":
    "Configura copias de seguridad para este servidor y verifica después una restauración.",
  "guard.fact.recoverable.attentionNext":
    "Haz una copia nueva o repite la prueba de restauración.",
  "guard.fact.recoverable.unknownNext":
    "Espera al siguiente informe o verifica una restauración.",
  "guard.fact.recoverable.backupFresh": "La copia está dentro de la política.",
  "guard.fact.recoverable.backupStale":
    "La última copia es más antigua de lo que permite la política.",
  "guard.fact.recoverable.backupUnknown":
    "No se informa la actualidad de la copia.",
  "guard.fact.recoverable.drillVerified": "Se verificó una restauración.",
  "guard.fact.recoverable.drillStale":
    "La última restauración verificada está desactualizada.",
  "guard.fact.recoverable.drillUnknown":
    "No se informa ninguna restauración verificada.",

  "guard.fact.reachable.ok":
    "Este servidor está registrado en su dirección kombify.me y aparece en kombify Connect.",
  "guard.fact.reachable.address": "Dirección: {address}",
  "guard.fact.reachable.notReported":
    "No se informa el acceso remoto de este servidor.",
  "guard.fact.reachable.notReportedNext":
    "Conecta la instalación a kombify Cloud para registrar su dirección, o vuelve a comprobar más tarde.",
  "guard.fact.reachable.notEnrolled":
    "Este servidor aún no está dado de alta para el acceso remoto.",
  "guard.fact.reachable.pending": "Se está configurando el acceso remoto.",
  "guard.fact.reachable.pendingNext": "Espera un momento y vuelve a comprobar.",
  "guard.fact.reachable.failed": "No se pudo configurar el acceso remoto.",
  "guard.fact.reachable.failedNext":
    "Vuelve a comprobar en breve. Si persiste, contacta con soporte con el código de motivo.",
  "guard.fact.reachable.revoked":
    "Se revocó el acceso remoto de este servidor.",
  "guard.fact.reachable.revokedNext":
    "Vuelve a emparejar el servidor para restablecer el acceso remoto.",
  "guard.fact.reachable.stale":
    "El último informe de acceso es demasiado antiguo para confirmar que la dirección sigue respondiendo.",
  "guard.fact.reachable.relayConnecting":
    "Se está estableciendo la conexión de relé.",
  "guard.fact.reachable.relayDown":
    "La conexión de relé de Guard está caída, así que la dirección no responde.",
  "guard.fact.reachable.relayDownNext":
    "Comprueba el código de motivo y que el servidor pueda llegar a internet.",

  "guard.fact.reachable.notEnabled":
    "Este servidor no está publicado en una dirección de kombify.me. Es tu decisión hasta que lo actives.",
  "guard.fact.reachable.notEnabledNext":
    "Para publicar un servicio, indica abajo su nombre y el puerto en el que escucha.",
  "guard.reach.publish.title": "Publicar en una dirección de kombify.me",
  "guard.reach.publish.intro":
    "No se publica nada hasta que lo actives. Cualquiera en internet puede abrir la dirección, así que publica solo un servicio que se proteja por sí mismo.",
  "guard.reach.publish.service": "Nombre del servicio",
  "guard.reach.publish.port": "Puerto en este servidor",
  "guard.reach.publish.enable": "Publicar",
  "guard.reach.publish.disable": "Dejar de publicar",
  "guard.reach.publish.current": "Publicado: {service} en el puerto {port}.",
  "guard.reach.publish.stepUp":
    "Publicar requiere un inicio de sesión reciente con segundo factor, en los últimos cinco minutos. Inicia sesión de nuevo y repite este paso.",
  "guard.upgrade.title": "Aprobar la clave de este servidor",
  "guard.upgrade.pending":
    "Una clave con la huella {fingerprint} espera tu aprobación. Apruébala solo si Guard registra la misma huella en este servidor.",
  "guard.upgrade.none":
    "Este servidor aún no ha propuesto ninguna clave. Actualiza Guard en el servidor; su huella aparecerá aquí para que la apruebes.",
  "guard.upgrade.approve": "Aprobar clave",
  "guard.upgrade.reject": "Rechazar clave",
  "guard.upgrade.stepUp":
    "Aprobar requiere un inicio de sesión reciente con segundo factor, en los últimos cinco minutos. Inicia sesión de nuevo y repite este paso.",
  "guard.upgrade.done": "La clave del servidor fue aprobada.",

  "guard.reach.reason.guard_key_not_bound":
    "El servidor aún no tiene una clave vinculada. Actualiza Guard para que vincule su propia clave.",
  "guard.reach.reason.connect_tenant_unmappable":
    "Tu cuenta aún no tiene un espacio de kombify Connect, así que el servidor no puede aparecer allí.",
  "guard.reach.reason.host_proof_pending":
    "Esperando a que el servidor demuestre su identidad ante kombify Connect. Suele resolverse solo.",
  "guard.reach.reason.host_proof_rejected":
    "kombify Connect rechazó la prueba de identidad del servidor. Vuelve a emparejarlo.",
  "guard.reach.reason.kombify_me_unavailable":
    "El servicio kombify.me no está disponible. Vuelve a comprobar en breve.",
  "guard.reach.reason.kombify_me_denied":
    "kombify.me rechazó el registro de este servidor.",
  "guard.reach.reason.connect_unavailable":
    "kombify Connect no está disponible. Vuelve a comprobar en breve.",
  "guard.reach.reason.connect_denied":
    "kombify Connect se negó a listar este servidor.",
  "guard.reach.reason.reach_layout_unsupported":
    "A esta disposición de instalación aún no se le puede asignar una dirección remota.",
  "guard.reach.reason.revocation_incomplete":
    "Terminar el acceso aún no ha concluido en todas partes. Se reintenta.",
  "guard.reach.reason.identity_unresolved":
    "No se pudo resolver la identidad del propietario de este servidor.",

  "guard.notice.legacyRoot":
    "Guard en este servidor aún se ejecuta por completo como root; la disposición reforzada y sin privilegios no está activa. Actualiza Guard en el servidor.",
  "guard.notice.executorUnavailable":
    "Guard se ejecuta sin su ejecutor privilegiado, por lo que los cambios en el host no están disponibles.",
  "guard.notice.mutationsDenied":
    "Una actualización fallida no dejó ningún Guard utilizable, por lo que se deniegan los cambios hasta resolverlo.",

  "guard.findings.title": "Hallazgos de seguridad",
  "guard.findings.control": "Control",
  "guard.findings.severity": "Gravedad: {severity}",
  "guard.findings.severity.critical": "crítica",
  "guard.findings.severity.high": "alta",
  "guard.findings.severity.medium": "media",
  "guard.findings.severity.low": "baja",
  "guard.findings.state.compliant": "Conforme",
  "guard.findings.state.drifted": "Desviado",
  "guard.findings.state.unknown": "Desconocido",
  "guard.findings.state.exception": "Excepción aprobada",
  "guard.findings.state.approved_exception": "Excepción aprobada",
  "guard.findings.expected": "Esperado",
  "guard.findings.observed": "Observado",
  "guard.findings.remediation": "Corrección:",
  "guard.findings.exception": "Excepción aprobada.",
  "guard.findings.exceptionWith": "Excepción aprobada: {detail}",
  "guard.findings.approvedBy": "aprobada por {name}",
  "guard.findings.until": "hasta {at}",

  "guard.verify.action": "Verificar ahora",
  "guard.verify.running": "Verificando…",
  "guard.verify.accepted":
    "Verificación solicitada a las {at} (tarea {job}). Los hechos se actualizan cuando el servidor informe del resultado.",
  "guard.verify.failed": "No se pudo iniciar la verificación: {message}",

  "guard.rotate.action": "Rotar credencial",
  "guard.rotate.generationUnknown":
    "La rotación necesita la generación actual de la credencial, que aún no se informa.",
  "guard.rotate.confirmTitle": "¿Reemplazar la credencial de este servidor?",
  "guard.rotate.confirmBody":
    "Se emite una credencial de reemplazo y se muestra una sola vez. Cópiala al servidor para que Guard pueda usarla. La credencial anterior deja de aceptarse.",
  "guard.rotate.confirm": "Rotar credencial",
  "guard.rotate.running": "Rotando…",
  "guard.rotate.done":
    "Credencial de reemplazo emitida (generación {generation}).",
  "guard.rotate.tokenOnce":
    "Se muestra una sola vez y no se guarda. Cópiala ahora y entrégasela a Guard en el servidor.",
  "guard.rotate.copy": "Copiar credencial",
  "guard.rotate.copied": "Copiada",
  "guard.rotate.copyFailed":
    "No se pudo copiar. Selecciona el texto y cópialo manualmente.",
  "guard.rotate.dismiss": "Ya la he guardado",

  "guard.detach.action": "Desvincular servidor",
  "guard.detach.confirmTitle": "¿Desvincular {name}?",
  "guard.detach.confirmBody":
    "Esto revoca la credencial de Guard y termina el acceso del servidor: su dirección kombify.me y su entrada en kombify Connect. La máquina en sí y lo que se ejecuta en ella no se tocan.",
  "guard.detach.reverse":
    "Esto no se puede deshacer aquí. Para volver a gestionar el servidor, empareja de nuevo.",
  "guard.detach.confirm": "Desvincular y revocar",
  "guard.detach.running": "Desvinculando…",

  "guard.connect.link": "Dispositivos en kombify Connect",

  "guard.report.download": "Descargar informe (JSON)",
  "guard.report.print": "Informe imprimible",
  "guard.report.failed": "No se pudo crear el informe.",
  "guard.report.title": "Informe de kombify Guard",
  "guard.report.generated": "Generado {at}",
  "guard.report.facts": "Hechos",
  "guard.report.colFact": "Hecho",
  "guard.report.colState": "Estado",
  "guard.report.colEvidence": "Evidencia",
  "guard.report.colObserved": "Observado",
  "guard.report.noObservation": "Sin hora de observación",
  "guard.report.notices": "Protección reducida",
  "guard.report.findings": "Hallazgos de seguridad",
  "guard.report.noFindings": "No se informa ningún hallazgo.",
  "guard.report.note":
    "Este informe solo lista lo que el plano de control ha guardado, con la hora en que se observó cada hecho. Un hecho sin evidencia reciente aparece como no acreditado y nunca se da por bueno. No contiene credenciales.",
  "guard.report.digest": "Resumen del contenido",

  "guard.outcomes.title": "Operaciones con resultado desconocido",
  "guard.outcomes.intro":
    "Guard registró que estas operaciones pueden haber tenido efecto o no. Cada una bloquea más cambios en este servidor hasta que se resuelva o la liberes.",
  "guard.outcomes.reconciling": "Guard aún intenta resolverla",
  "guard.outcomes.needsOperator": "Requiere tu decisión",
  "guard.outcomes.since": "sin resolver desde {at}",
  "guard.outcomes.resolve": "Resolver",
  "guard.outcomes.dispositionLabel": "¿Qué encontraste?",
  "guard.outcomes.disposition.verified_applied": "El cambio está aplicado",
  "guard.outcomes.disposition.verified_applied.hint":
    "Revisé el servidor: la operación tuvo efecto.",
  "guard.outcomes.disposition.verified_not_applied": "No cambió nada",
  "guard.outcomes.disposition.verified_not_applied.hint":
    "Revisé el servidor: la operación no tuvo efecto.",
  "guard.outcomes.disposition.accept_unknown": "Liberar sin verificar",
  "guard.outcomes.disposition.accept_unknown.hint":
    "No pude verificarlo. El resultado sigue desconocido y se registra como aceptado.",
  "guard.outcomes.noteLabel": "¿Qué verificaste?",
  "guard.outcomes.noteHint":
    "Obligatorio, hasta {max} caracteres. Se registra con tu nombre.",
  "guard.outcomes.review": "Revisar",
  "guard.outcomes.cancel": "Cancelar",
  "guard.outcomes.confirmTitle": "¿Liberar esta operación?",
  "guard.outcomes.confirmBody":
    "Esto levanta el bloqueo de este servidor. Si el resultado no fue el que registraste, pueden ejecutarse cambios en conflicto.",
  "guard.outcomes.confirm": "Liberar",
  "guard.outcomes.releasing": "Liberando…",
  "guard.outcomes.back": "Atrás",
  "guard.outcomes.stepUp":
    "Liberar un resultado requiere un inicio de sesión reciente con segundo factor, de hace menos de cinco minutos. Inicia sesión de nuevo y repite este paso. Tu nota no se conserva.",
  "guard.outcomes.stepUpAction": "Iniciar sesión de nuevo",
  "guard.outcomes.stale":
    "Esta operación cambió mientras la revisabas. La lista está actualizada; revísala de nuevo.",
  "guard.outcomes.loadFailed":
    "No se pudo cargar la lista de operaciones sin resolver.",
  "guard.outcomes.receipt":
    "Se liberó {command} en la revisión {revision} como «{disposition}», registrado para {actor}.",
  "guard.outcomes.fenceReleased": "Se levantó el bloqueo de este servidor.",

  "guard.ladder.title": "Preparación del emparejamiento",
  "guard.ladder.intro":
    "Cada paso se comprueba con lo que el plano de control ha registrado, no se da por supuesto.",
  "guard.ladder.bound": "Vinculado",
  "guard.ladder.connected": "Conectado",
  "guard.ladder.configuration": "Configuración verificada",
  "guard.ladder.reachable": "Accesible",
  "guard.ladder.state.done": "Hecho",
  "guard.ladder.state.failed": "Requiere atención",
  "guard.ladder.state.waiting": "En curso",
  "guard.ladder.state.blocked": "Esperando al paso anterior",
  "guard.ladder.blockedHint": "Empieza cuando el paso anterior esté hecho.",
  "guard.ladder.complete":
    "Todos los pasos del emparejamiento están acreditados.",
  "guard.ladder.bound.awaiting":
    "Esperando a que el comando de emparejamiento se ejecute en el servidor.",
  "guard.ladder.bound.awaitingNext":
    "Ejecuta el comando en el servidor. Si caducó, genera uno nuevo.",
};
