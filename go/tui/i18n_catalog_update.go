package tui

const (
	MsgUpdateAttached              MessageID = "update.attached"
	MsgUpdateReadOnly              MessageID = "update.read_only"
	MsgUpdateActiveRestored        MessageID = "update.active_restored"
	MsgUpdateReconnected           MessageID = "update.reconnected"
	MsgUpdateCancelFailed          MessageID = "update.cancel_failed"
	MsgUpdateCancelRecorded        MessageID = "update.cancel_recorded"
	MsgUpdateRPCFailed             MessageID = "update.rpc_failed"
	MsgUpdateSubmissionAck         MessageID = "update.submission_ack"
	MsgUpdateExitHint              MessageID = "update.exit_hint"
	MsgUpdateUnknownSubmission     MessageID = "update.unknown_submission"
	MsgUpdateDraftCleared          MessageID = "update.draft_cleared"
	MsgUpdateDraftClearedRecover   MessageID = "update.draft_cleared_recover"
	MsgUpdateCommandParseFailed    MessageID = "update.command_parse_failed"
	MsgUpdateDisconnectedDraft     MessageID = "update.disconnected_draft"
	MsgUpdateSubmissionUnavailable MessageID = "update.submission_unavailable"
	MsgUpdatePendingSubmission     MessageID = "update.pending_submission"
	MsgUpdateRecoverySaveFailed    MessageID = "update.recovery_save_failed"
	MsgUpdateUsageShell            MessageID = "update.usage_shell"
	MsgUpdateTaskNotAcknowledged   MessageID = "update.task_not_acknowledged"
	MsgUpdateSubmissionFailed      MessageID = "update.submission_failed"
	MsgUpdateQueueChanged          MessageID = "update.queue_changed"
	MsgUpdateRecoveryClearFailed   MessageID = "update.recovery_clear_failed"
	MsgUpdateYou                   MessageID = "update.you"
	MsgUpdateYouSteer              MessageID = "update.you_steer"
	MsgUpdateYouShell              MessageID = "update.you_shell"
	MsgUpdateShell                 MessageID = "update.shell"
	MsgUpdateTaskSubmitted         MessageID = "update.task_submitted"
	MsgUpdateSteeringQueued        MessageID = "update.steering_queued"
	MsgUpdateUsageSearch           MessageID = "update.usage_search"
	MsgUpdateSearchMatches         MessageID = "update.search_matches"
	MsgUpdateRecap                 MessageID = "update.recap"
	MsgUpdateUsageMode             MessageID = "update.usage_mode"
	MsgUpdateMode                  MessageID = "update.mode"
	MsgUpdateAgents                MessageID = "update.agents"
	MsgUpdateUsageResume           MessageID = "update.usage_resume"
	MsgUpdateUnknownCommand        MessageID = "update.unknown_command"
	MsgUpdateRewindAgain           MessageID = "update.rewind_again"
	MsgWorkspaceExternalEditor     MessageID = "workspace.external_editor"
	MsgWorkspaceDraftRestored      MessageID = "workspace.draft_restored"
	MsgWorkspaceEditorApplied      MessageID = "workspace.editor_applied"
	MsgWorkspaceNothingToCopy      MessageID = "workspace.nothing_to_copy"
	MsgWorkspaceCopyFailed         MessageID = "workspace.copy_failed"
	MsgWorkspaceCopied             MessageID = "workspace.copied"
	MsgWorkspaceTranscriptEmpty    MessageID = "workspace.transcript_empty"
	MsgWorkspaceTranscriptTiny     MessageID = "workspace.transcript_tiny"
	MsgWorkspaceTranscriptHeader   MessageID = "workspace.transcript_header"
	MsgWorkspaceTranscriptFooter   MessageID = "workspace.transcript_footer"
	MsgTasksHeader                 MessageID = "tasks.header"
	MsgTasksMore                   MessageID = "tasks.more"
	MsgTaskLine                    MessageID = "tasks.line"
	MsgTaskStatusRunning           MessageID = "tasks.status.running"
	MsgTaskStatusCompleted         MessageID = "tasks.status.completed"
	MsgTaskStatusFailed            MessageID = "tasks.status.failed"
	MsgTaskStatusCancelled         MessageID = "tasks.status.cancelled"
	MsgTaskStatusDegraded          MessageID = "tasks.status.degraded"
	MsgTaskStatusWaiting           MessageID = "tasks.status.waiting"
	MsgTaskStatusQueued            MessageID = "tasks.status.queued"
	MsgTaskStatusPaused            MessageID = "tasks.status.paused"
	MsgFollowupRestored            MessageID = "followup.restored"
	MsgFollowupShellEmpty          MessageID = "followup.shell_empty"
	MsgFollowupDisconnected        MessageID = "followup.disconnected"
	MsgFollowupSlashRecalled       MessageID = "followup.slash_recalled"
	MsgFollowupQueued              MessageID = "followup.queued"
	MsgFollowupRecalled            MessageID = "followup.recalled"
	MsgFollowupRetryRecalled       MessageID = "followup.retry_recalled"
	MsgSubmissionRecoveryFailed    MessageID = "submission.recovery_failed"
	MsgSubmissionRestored          MessageID = "submission.restored"
	MsgSubmissionReconciling       MessageID = "submission.reconciling"
	MsgTranscriptArtifact          MessageID = "transcript.artifact"
	MsgTranscriptOpenArtifact      MessageID = "transcript.open_artifact"
)

var updateCatalogRows = []catalogRow{
	catalog(MsgUpdateAttached, "- attached to {session}", "- 已连接到 {session}", "- {session} に接続", "- {session}에 연결됨", "- conectado a {session}", "- connecté à {session}"),
	catalog(MsgUpdateReadOnly, "{glyph} task submission is read-only in this TUI: {error}", "{glyph} 此 TUI 中的任务提交为只读：{error}", "{glyph} この TUI ではタスク送信は読み取り専用です: {error}", "{glyph} 이 TUI에서 작업 제출은 읽기 전용입니다: {error}", "{glyph} el envío de tareas es de solo lectura: {error}", "{glyph} l’envoi de tâche est en lecture seule : {error}"),
	catalog(MsgUpdateActiveRestored, "- active task {task} restored", "- 已恢复活动任务 {task}", "- 実行中タスク {task} を復元", "- 활성 작업 {task} 복원됨", "- tarea activa {task} restaurada", "- tâche active {task} restaurée"),
	catalog(MsgUpdateReconnected, "- reconnected: live event stream resumed", "- 已重新连接：实时事件流已恢复", "- 再接続: ライブイベントを再開", "- 재연결됨: 실시간 이벤트 스트림 재개", "- reconectado: flujo de eventos reanudado", "- reconnecté : flux d’événements repris"),
	catalog(MsgUpdateCancelFailed, "{glyph} cancel failed for task {task}: {error}", "{glyph} 取消任务 {task} 失败：{error}", "{glyph} タスク {task} の取消失敗: {error}", "{glyph} 작업 {task} 취소 실패: {error}", "{glyph} no se pudo cancelar {task}: {error}", "{glyph} échec de l’annulation de {task} : {error}"),
	catalog(MsgUpdateCancelRecorded, "- cancel recorded for task {task}", "- 已记录任务 {task} 的取消", "- タスク {task} の取消を記録", "- 작업 {task} 취소 기록됨", "- cancelación registrada para {task}", "- annulation enregistrée pour {task}"),
	catalog(MsgUpdateRPCFailed, "{glyph} RPC: {error}", "{glyph} RPC：{error}", "{glyph} RPC: {error}", "{glyph} RPC: {error}", "{glyph} RPC: {error}", "{glyph} RPC : {error}"),
	catalog(MsgUpdateSubmissionAck, "- submission is being acknowledged; press {interrupt} again within 2s to exit", "- 正在确认提交；2 秒内再次按 {interrupt} 退出", "- 送信確認中。2 秒以内に {interrupt} を再度押すと終了", "- 제출 확인 중. 2초 안에 {interrupt}를 다시 누르면 종료", "- confirmando el envío; pulsa {interrupt} otra vez en 2 s para salir", "- confirmation de l’envoi ; rappuyez sur {interrupt} sous 2 s pour quitter"),
	catalog(MsgUpdateExitHint, "- press {interrupt} again within 2s to exit", "- 2 秒内再次按 {interrupt} 退出", "- 2 秒以内に {interrupt} を再度押すと終了", "- 2초 안에 {interrupt}를 다시 누르면 종료", "- pulsa {interrupt} otra vez en 2 s para salir", "- rappuyez sur {interrupt} sous 2 s pour quitter"),
	catalog(MsgUpdateUnknownSubmission, "- submission outcome is unknown; Enter reconciles it, {new} starts a distinct task, and a second {interrupt} exits with recovery preserved", "- 提交结果未知；Enter 进行核对，{new} 新建任务，再按 {interrupt} 退出并保留恢复记录", "- 送信結果は不明です。Enter で照合、{new} で別タスク開始、{interrupt} 再押下で復旧情報を保持して終了", "- 제출 결과를 알 수 없습니다. Enter로 확인, {new}로 새 작업 시작, {interrupt}를 다시 눌러 복구 기록을 유지하고 종료", "- resultado desconocido; Enter lo concilia, {new} inicia otra tarea y {interrupt} sale conservando la recuperación", "- résultat inconnu ; Entrée vérifie, {new} crée une tâche et {interrupt} quitte en conservant la reprise"),
	catalog(MsgUpdateDraftCleared, "- empty draft cleared", "- 已清空草稿", "- 下書きを消去", "- 초안 지움", "- borrador vaciado", "- brouillon effacé"),
	catalog(MsgUpdateDraftClearedRecover, "- draft cleared; use prompt history to restore it", "- 草稿已清空；可从提示历史恢复", "- 下書きを消去しました。履歴から復元できます", "- 초안 지움. 프롬프트 기록에서 복원할 수 있습니다", "- borrador vaciado; restáuralo desde el historial", "- brouillon effacé ; restaurez-le depuis l’historique"),
	catalog(MsgUpdateCommandParseFailed, "{glyph} command parse failed: {error}; draft kept for retry", "{glyph} 命令解析失败：{error}；草稿已保留以便重试", "{glyph} コマンド解析失敗: {error}。下書きを保持", "{glyph} 명령 구문 분석 실패: {error}. 재시도용 초안 유지", "{glyph} no se pudo analizar el comando: {error}; se conserva el borrador", "{glyph} analyse de la commande impossible : {error} ; brouillon conservé"),
	catalog(MsgUpdateDisconnectedDraft, "{glyph} not connected: draft kept for retry", "{glyph} 未连接：草稿已保留以便重试", "{glyph} 未接続: 下書きを保持", "{glyph} 연결되지 않음: 재시도용 초안 유지", "{glyph} sin conexión: borrador conservado", "{glyph} non connecté : brouillon conservé"),
	catalog(MsgUpdateSubmissionUnavailable, "{glyph} task submission is unavailable: {error}", "{glyph} 无法提交任务：{error}", "{glyph} タスクを送信できません: {error}", "{glyph} 작업 제출 불가: {error}", "{glyph} el envío no está disponible: {error}", "{glyph} envoi de tâche indisponible : {error}"),
	catalog(MsgUpdatePendingSubmission, "{glyph} an unacknowledged submission is pending; restore its exact prompt or use {new} to create a new task", "{glyph} 有一项未确认的提交；请恢复其完整提示，或使用 {new} 新建任务", "{glyph} 未確認の送信があります。同じプロンプトを復元するか {new} で新規タスクを作成", "{glyph} 확인되지 않은 제출이 대기 중입니다. 동일한 프롬프트를 복원하거나 {new}로 새 작업을 만드세요", "{glyph} hay un envío sin confirmar; restaura el texto exacto o usa {new} para crear otra tarea", "{glyph} un envoi non confirmé est en attente ; restaurez le texte exact ou utilisez {new}"),
	catalog(MsgUpdateRecoverySaveFailed, "{glyph} submission was not sent because its recovery record could not be saved: {error}", "{glyph} 恢复记录无法保存，因此未发送提交：{error}", "{glyph} 復旧記録を保存できず送信しませんでした: {error}", "{glyph} 복구 기록을 저장할 수 없어 제출하지 않았습니다: {error}", "{glyph} no se envió porque no pudo guardarse la recuperación: {error}", "{glyph} envoi annulé car la reprise n’a pas pu être enregistrée : {error}"),
	catalog(MsgUpdateUsageShell, "usage: !<command>", "用法：!<command>", "使用法: !<command>", "사용법: !<command>", "uso: !<command>", "utilisation : !<command>"),
	catalog(MsgUpdateTaskNotAcknowledged, "{glyph} task submission was not acknowledged: {error}; draft kept for retry with idempotency key {key}", "{glyph} 任务提交未获确认：{error}；草稿已保留，幂等键 {key}", "{glyph} タスク送信未確認: {error}。下書きと冪等キー {key} を保持", "{glyph} 작업 제출이 확인되지 않음: {error}. 초안과 멱등성 키 {key} 유지", "{glyph} envío no confirmado: {error}; borrador conservado con clave {key}", "{glyph} envoi non confirmé : {error} ; brouillon conservé avec la clé {key}"),
	catalog(MsgUpdateSubmissionFailed, "{glyph} {kind} failed: {error}; draft kept for retry", "{glyph} {kind} 失败：{error}；草稿已保留以便重试", "{glyph} {kind} 失敗: {error}。下書きを保持", "{glyph} {kind} 실패: {error}. 재시도용 초안 유지", "{glyph} falló {kind}: {error}; borrador conservado", "{glyph} échec de {kind} : {error} ; brouillon conservé"),
	catalog(MsgUpdateQueueChanged, "{glyph} queued follow-up ordering changed; drafts restored", "{glyph} 后续队列顺序发生变化；草稿已恢复", "{glyph} 待機順が変わりました。下書きを復元", "{glyph} 후속 대기열 순서 변경됨. 초안 복원", "{glyph} cambió el orden de la cola; borradores restaurados", "{glyph} ordre de la file modifié ; brouillons restaurés"),
	catalog(MsgUpdateRecoveryClearFailed, "{glyph} task acknowledged, but its local recovery record could not be cleared: {error}", "{glyph} 任务已确认，但无法清除本地恢复记录：{error}", "{glyph} タスク確認済みですが復旧記録を削除できません: {error}", "{glyph} 작업은 확인되었으나 로컬 복구 기록을 지울 수 없습니다: {error}", "{glyph} tarea confirmada, pero no se pudo borrar la recuperación: {error}", "{glyph} tâche confirmée, mais reprise locale impossible à effacer : {error}"),
	catalog(MsgUpdateYou, "you ", "你 ", "あなた ", "나 ", "tú ", "vous "),
	catalog(MsgUpdateYouSteer, "you (steer) ", "你（引导）", "あなた（追加指示）", "나 (방향 전환) ", "tú (orientación) ", "vous (orientation) "),
	catalog(MsgUpdateYouShell, "you (shell) ", "你（Shell）", "あなた（Shell）", "나 (Shell) ", "tú (shell) ", "vous (shell) "),
	catalog(MsgUpdateShell, "shell", "Shell", "Shell", "Shell", "shell", "shell"),
	catalog(MsgUpdateTaskSubmitted, "- task {task} submitted", "- 任务 {task} 已提交", "- タスク {task} を送信", "- 작업 {task} 제출됨", "- tarea {task} enviada", "- tâche {task} envoyée"),
	catalog(MsgUpdateSteeringQueued, "- steering queued for task {task}", "- 任务 {task} 的引导已排队", "- タスク {task} への追加指示を待機", "- 작업 {task} 방향 전환 대기열 추가", "- orientación en cola para {task}", "- orientation en file pour {task}"),
	catalog(MsgUpdateUsageSearch, "usage: /search <text>", "用法：/search <text>", "使用法: /search <text>", "사용법: /search <text>", "uso: /search <text>", "utilisation : /search <text>"),
	{ID: MsgUpdateSearchMatches, EN: "- transcript search: {count} matches", ZH: "- 记录搜索：{count} 个匹配", JA: "- 履歴検索: {count} 件", KO: "- 기록 검색: {count}개 일치", ES: "- búsqueda en historial: {count} coincidencias", FR: "- recherche dans l’historique : {count} résultats", ENOne: "- transcript search: {count} match", ZHOne: "- 记录搜索：{count} 个匹配", JAOne: "- 履歴検索: {count} 件", KOOne: "- 기록 검색: {count}개 일치", ESOne: "- búsqueda en historial: {count} coincidencia", FROne: "- recherche dans l’historique : {count} résultat"},
	catalog(MsgUpdateRecap, "recap", "回顾", "要約", "요약", "resumen", "récapitulatif"),
	catalog(MsgUpdateUsageMode, "usage: /mode <build|plan>", "用法：/mode <build|plan>", "使用法: /mode <build|plan>", "사용법: /mode <build|plan>", "uso: /mode <build|plan>", "utilisation : /mode <build|plan>"),
	catalog(MsgUpdateMode, "mode {mode}", "模式 {mode}", "モード {mode}", "모드 {mode}", "modo {mode}", "mode {mode}"),
	catalog(MsgUpdateAgents, "agents", "Agent", "Agent", "Agent", "Agents", "Agents"),
	catalog(MsgUpdateUsageResume, "usage: /resume [task_id]", "用法：/resume [task_id]", "使用法: /resume [task_id]", "사용법: /resume [task_id]", "uso: /resume [task_id]", "utilisation : /resume [task_id]"),
	catalog(MsgUpdateUnknownCommand, "unknown command /{command}; use /help", "未知命令 /{command}；请使用 /help", "不明なコマンド /{command}。/help を参照", "알 수 없는 명령 /{command}. /help를 사용하세요", "comando desconocido /{command}; usa /help", "commande inconnue /{command} ; utilisez /help"),
	catalog(MsgUpdateRewindAgain, "- press {rewind} again to choose a rewind point", "- 再按一次 {rewind} 选择回退点", "- {rewind} をもう一度押して巻き戻し点を選択", "- {rewind}를 다시 눌러 되돌리기 지점 선택", "- pulsa {rewind} otra vez para elegir un punto", "- rappuyez sur {rewind} pour choisir un point de retour"),
	catalog(MsgWorkspaceExternalEditor, "{glyph} external editor: {error}", "{glyph} 外部编辑器：{error}", "{glyph} 外部エディタ: {error}", "{glyph} 외부 편집기: {error}", "{glyph} editor externo: {error}", "{glyph} éditeur externe : {error}"),
	catalog(MsgWorkspaceDraftRestored, "{glyph} {error}; draft restored", "{glyph} {error}；草稿已恢复", "{glyph} {error}。下書きを復元", "{glyph} {error}. 초안 복원됨", "{glyph} {error}; borrador restaurado", "{glyph} {error} ; brouillon restauré"),
	catalog(MsgWorkspaceEditorApplied, "- external editor draft applied", "- 已应用外部编辑器草稿", "- 外部エディタの下書きを適用", "- 외부 편집기 초안 적용됨", "- borrador del editor aplicado", "- brouillon de l’éditeur appliqué"),
	catalog(MsgWorkspaceNothingToCopy, "{glyph} nothing to copy: no rendered Agent response", "{glyph} 无内容可复制：没有已渲染的 Agent 回复", "{glyph} コピー対象なし: 表示済みの Agent 応答がありません", "{glyph} 복사할 내용 없음: 렌더링된 Agent 응답이 없습니다", "{glyph} nada que copiar: no hay respuesta del Agent", "{glyph} rien à copier : aucune réponse de l’Agent"),
	catalog(MsgWorkspaceCopyFailed, "{glyph} copy failed: {error}", "{glyph} 复制失败：{error}", "{glyph} コピー失敗: {error}", "{glyph} 복사 실패: {error}", "{glyph} error al copiar: {error}", "{glyph} échec de la copie : {error}"),
	catalog(MsgWorkspaceCopied, "- copied last Agent response", "- 已复制最近的 Agent 回复", "- 最新の Agent 応答をコピー", "- 최근 Agent 응답 복사됨", "- última respuesta del Agent copiada", "- dernière réponse de l’Agent copiée"),
	catalog(MsgWorkspaceTranscriptEmpty, "(transcript is empty)", "（记录为空）", "（履歴は空です）", "(기록이 비어 있음)", "(el historial está vacío)", "(l’historique est vide)"),
	catalog(MsgWorkspaceTranscriptTiny, "transcript - {close} closes", "记录 - {close} 关闭", "履歴 - {close} で閉じる", "기록 - {close} 닫기", "historial - {close} cierra", "historique - {close} ferme"),
	{ID: MsgWorkspaceTranscriptHeader, EN: "transcript · {count} lines", ZH: "记录 · {count} 行", JA: "履歴 · {count} 行", KO: "기록 · {count}줄", ES: "historial · {count} líneas", FR: "historique · {count} lignes", ENOne: "transcript · {count} line", ZHOne: "记录 · {count} 行", JAOne: "履歴 · {count} 行", KOOne: "기록 · {count}줄", ESOne: "historial · {count} línea", FROne: "historique · {count} ligne"},
	catalog(MsgWorkspaceTranscriptFooter, "{up}/{down} scroll · {page_up}/{page_down} page · {close} close", "{up}/{down} 滚动 · {page_up}/{page_down} 翻页 · {close} 关闭", "{up}/{down} スクロール · {page_up}/{page_down} ページ · {close} 閉じる", "{up}/{down} 스크롤 · {page_up}/{page_down} 페이지 · {close} 닫기", "{up}/{down} desplazar · {page_up}/{page_down} página · {close} cerrar", "{up}/{down} défiler · {page_up}/{page_down} page · {close} fermer"),
	catalog(MsgTasksHeader, "tasks · {active} active · {done} done", "任务 · {active} 活动 · {done} 完成", "タスク · 実行中 {active} · 完了 {done}", "작업 · 활성 {active} · 완료 {done}", "tareas · {active} activas · {done} completadas", "tâches · {active} actives · {done} terminées"),
	{ID: MsgTasksMore, EN: "  +{count} more", ZH: "  另有 {count} 项", JA: "  他 {count} 件", KO: "  +{count}개 더", ES: "  +{count} más", FR: "  +{count} autres", ENOne: "  +{count} more", ZHOne: "  另有 {count} 项", JAOne: "  他 {count} 件", KOOne: "  +{count}개 더", ESOne: "  +{count} más", FROne: "  +{count} autre"},
	catalog(MsgTaskLine, "{prefix} {glyph} {kind} · {label} · {status}", "{prefix} {glyph} {kind} · {label} · {status}", "{prefix} {glyph} {kind} · {label} · {status}", "{prefix} {glyph} {kind} · {label} · {status}", "{prefix} {glyph} {kind} · {label} · {status}", "{prefix} {glyph} {kind} · {label} · {status}"),
	catalog(MsgTaskStatusRunning, "running", "运行中", "実行中", "실행 중", "en ejecución", "en cours"),
	catalog(MsgTaskStatusCompleted, "completed", "已完成", "完了", "완료됨", "completada", "terminée"),
	catalog(MsgTaskStatusFailed, "failed", "失败", "失敗", "실패", "fallida", "échouée"),
	catalog(MsgTaskStatusCancelled, "cancelled", "已取消", "取消済み", "취소됨", "cancelada", "annulée"),
	catalog(MsgTaskStatusDegraded, "degraded", "已降级", "縮退", "성능 저하", "degradada", "dégradée"),
	catalog(MsgTaskStatusWaiting, "waiting", "等待中", "待機中", "대기 중", "en espera", "en attente"),
	catalog(MsgTaskStatusQueued, "queued", "排队中", "待機列", "대기열", "en cola", "en file"),
	catalog(MsgTaskStatusPaused, "paused", "已暂停", "一時停止", "일시 중지", "pausada", "en pause"),
	{ID: MsgFollowupRestored, EN: "- restored {count} queued follow-ups", ZH: "- 已恢复 {count} 条排队指令", JA: "- 待機中の {count} 件を復元", KO: "- 대기 중인 후속 요청 {count}개 복원", ES: "- {count} seguimientos restaurados", FR: "- {count} suites restaurées", ENOne: "- restored {count} queued follow-up", ZHOne: "- 已恢复 {count} 条排队指令", JAOne: "- 待機中の {count} 件を復元", KOOne: "- 대기 중인 후속 요청 {count}개 복원", ESOne: "- {count} seguimiento restaurado", FROne: "- {count} suite restaurée"},
	catalog(MsgFollowupShellEmpty, "{glyph} queued shell command is empty; drafts restored", "{glyph} 排队的 Shell 命令为空；草稿已恢复", "{glyph} 待機中の Shell コマンドが空です。下書きを復元", "{glyph} 대기 중인 Shell 명령이 비어 있습니다. 초안 복원", "{glyph} el comando shell en cola está vacío; borradores restaurados", "{glyph} la commande shell en file est vide ; brouillons restaurés"),
	catalog(MsgFollowupDisconnected, "{glyph} automatic follow-up submission failed: daemon not connected", "{glyph} 自动提交后续指令失败：守护进程未连接", "{glyph} 自動フォローアップ送信失敗: daemon 未接続", "{glyph} 자동 후속 요청 제출 실패: 데몬 연결 안 됨", "{glyph} falló el seguimiento automático: daemon sin conexión", "{glyph} échec de l’envoi automatique : daemon non connecté"),
	catalog(MsgFollowupSlashRecalled, "- queued slash command recalled; review and run it from the composer", "- 已取回排队的斜杠命令；请在输入区审阅并运行", "- 待機中のスラッシュコマンドを戻しました。入力欄で確認して実行", "- 대기 중인 슬래시 명령을 불러왔습니다. 작성기에서 검토 후 실행하세요", "- comando en cola recuperado; revísalo y ejecútalo en el editor", "- commande en file rappelée ; examinez-la et lancez-la depuis la saisie"),
	catalog(MsgFollowupQueued, "- queued follow-up {count}", "- 后续指令已排队，共 {count} 条", "- フォローアップを追加: {count} 件", "- 후속 요청 대기열 추가: {count}개", "- seguimiento en cola: {count}", "- suite ajoutée à la file : {count}"),
	catalog(MsgFollowupRecalled, "- recalled latest follow-up for editing", "- 已取回最新后续指令以供编辑", "- 最新のフォローアップを編集用に戻しました", "- 최신 후속 요청을 편집용으로 불러옴", "- último seguimiento recuperado para editar", "- dernière suite rappelée pour modification"),
	catalog(MsgFollowupRetryRecalled, "- unacknowledged queued submission recalled; Enter retries idempotently", "- 已取回未确认的排队提交；按 Enter 以幂等方式重试", "- 未確認の待機送信を戻しました。Enter で冪等に再試行", "- 확인되지 않은 대기 제출 불러옴. Enter로 멱등 재시도", "- envío sin confirmar recuperado; Enter reintenta de forma idempotente", "- envoi non confirmé rappelé ; Entrée réessaie de façon idempotente"),
	catalog(MsgSubmissionRecoveryFailed, "{glyph} submission recovery: {error}", "{glyph} 提交恢复：{error}", "{glyph} 送信復旧: {error}", "{glyph} 제출 복구: {error}", "{glyph} recuperación del envío: {error}", "{glyph} reprise de l’envoi : {error}"),
	catalog(MsgSubmissionRestored, "- restored an unacknowledged submission; reconciling it with the daemon", "- 已恢复未确认提交；正在与守护进程核对", "- 未確認の送信を復元し daemon と照合中", "- 확인되지 않은 제출 복원, 데몬과 확인 중", "- envío sin confirmar restaurado; conciliando con el daemon", "- envoi non confirmé restauré ; vérification avec le daemon"),
	catalog(MsgSubmissionReconciling, "- reconciling an unacknowledged submission in the background; current draft preserved", "- 正在后台核对未确认提交；当前草稿已保留", "- 未確認の送信をバックグラウンドで照合中。現在の下書きを保持", "- 확인되지 않은 제출을 백그라운드에서 확인 중, 현재 초안 유지", "- conciliando un envío en segundo plano; borrador actual conservado", "- vérification d’un envoi en arrière-plan ; brouillon actuel conservé"),
	catalog(MsgTranscriptArtifact, "artifact: {ids}", "产物：{ids}", "成果物: {ids}", "아티팩트: {ids}", "artefacto: {ids}", "artefact : {ids}"),
	catalog(MsgTranscriptOpenArtifact, "open: carina artifact read <session_id> <artifact_id>", "打开：carina artifact read <session_id> <artifact_id>", "開く: carina artifact read <session_id> <artifact_id>", "열기: carina artifact read <session_id> <artifact_id>", "abrir: carina artifact read <session_id> <artifact_id>", "ouvrir : carina artifact read <session_id> <artifact_id>"),
}
