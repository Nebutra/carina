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
	MsgUpdateUsageModel            MessageID = "update.usage_model"
	MsgUpdateUsageLoop             MessageID = "update.usage_loop"
	MsgUpdateLoopHeader            MessageID = "update.loop_header"
	MsgUpdateLoopItem              MessageID = "update.loop_item"
	MsgUpdateLoopEmpty             MessageID = "update.loop_empty"
	MsgUpdateLoopChanged           MessageID = "update.loop_changed"
	MsgUpdateGoalUsage             MessageID = "update.goal_usage"
	MsgUpdateGoalFailed            MessageID = "update.goal_failed"
	MsgUpdateGoalCleared           MessageID = "update.goal_cleared"
	MsgUpdateGoalNone              MessageID = "update.goal_none"
	MsgUpdateGoalState             MessageID = "update.goal_state"
	MsgUpdateGoalBudgetUnlimited   MessageID = "update.goal_budget_unlimited"
	MsgUpdateGoalBudgetTokens      MessageID = "update.goal_budget_tokens"
	MsgCanonicalTranscriptTitle    MessageID = "canonical.transcript_title"
	MsgCanonicalLoading            MessageID = "canonical.loading"
	MsgCanonicalUnavailable        MessageID = "canonical.unavailable"
	MsgCanonicalEmpty              MessageID = "canonical.empty"
	MsgCanonicalSearchTitle        MessageID = "canonical.search_title"
	MsgCanonicalSearchEmpty        MessageID = "canonical.search_empty"
	MsgCanonicalRecapEmpty         MessageID = "canonical.recap_empty"
	MsgOperationalEmpty            MessageID = "operational.empty"
	MsgOperationalStatusTitle      MessageID = "operational.status_title"
	MsgOperationalPermissionsTitle MessageID = "operational.permissions_title"
	MsgOperationalContextTitle     MessageID = "operational.context_title"
	MsgOperationalConfigTitle      MessageID = "operational.config_title"
	MsgOperationalMCPTitle         MessageID = "operational.mcp_title"
	MsgOperationalCompactTitle     MessageID = "operational.compact_title"
	MsgUpdateUsageCompact          MessageID = "update.usage_compact"
	MsgUpdateUsageDiff             MessageID = "update.usage_diff"
	MsgUpdateUsageMCP              MessageID = "update.usage_mcp"
	MsgDiffTitle                   MessageID = "diff.title"
	MsgDiffLoading                 MessageID = "diff.loading"
	MsgDiffFile                    MessageID = "diff.file"
	MsgDiffBinary                  MessageID = "diff.binary"
	MsgDiffTruncated               MessageID = "diff.truncated"
	MsgDiffTotalTruncated          MessageID = "diff.total_truncated"
	MsgDiffClean                   MessageID = "diff.clean"
	MsgUpdateModelCurrent          MessageID = "update.model_current"
	MsgUpdateModelChanged          MessageID = "update.model_changed"
	MsgModelPickerTitle            MessageID = "model_picker.title"
	MsgModelPickerLoading          MessageID = "model_picker.loading"
	MsgModelPickerFailed           MessageID = "model_picker.failed"
	MsgModelPickerDefault          MessageID = "model_picker.default"
	MsgModelPickerHelp             MessageID = "model_picker.help"
	MsgModelPickerPage             MessageID = "model_picker.page"
	MsgModelPickerEmpty            MessageID = "model_picker.empty"
	MsgSessionPickerTitle          MessageID = "session_picker.title"
	MsgSessionPickerLoading        MessageID = "session_picker.loading"
	MsgSessionPickerFailed         MessageID = "session_picker.failed"
	MsgSessionPickerEmpty          MessageID = "session_picker.empty"
	MsgSessionPickerHelp           MessageID = "session_picker.help"
	MsgSessionPickerForkOf         MessageID = "session_picker.fork_of"
	MsgSessionSwitchBlocked        MessageID = "session_switch.blocked"
	MsgSessionSwitchDraft          MessageID = "session_switch.blocker.draft"
	MsgSessionSwitchTask           MessageID = "session_switch.blocker.task"
	MsgSessionSwitchSubmission     MessageID = "session_switch.blocker.submission"
	MsgSessionSwitchRetry          MessageID = "session_switch.blocker.retry"
	MsgSessionSwitchQueue          MessageID = "session_switch.blocker.queue"
	MsgSessionSwitchGovernance     MessageID = "session_switch.blocker.governance"
	MsgSessionSwitchEditor         MessageID = "session_switch.blocker.editor"
	MsgSessionSwitchGoal           MessageID = "session_switch.blocker.goal"
	MsgSessionActionFailed         MessageID = "session_switch.action_failed"
	MsgSessionActionInvalid        MessageID = "session_switch.action_invalid"
	MsgSessionSwitchUnavailable    MessageID = "session_switch.unavailable"
	MsgSessionSwitchLeaseBlocked   MessageID = "session_switch.lease_blocked"
	MsgSessionSwitchFailed         MessageID = "session_switch.failed"
	MsgSessionSwitching            MessageID = "session_switch.switching"
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
	catalog(MsgUpdateUsageModel, "usage: /model <provider/model|default>", "用法：/model <厂商/模型|default>", "使用法: /model <provider/model|default>", "사용법: /model <provider/model|default>", "uso: /model <proveedor/modelo|default>", "utilisation : /model <fournisseur/modèle|default>"),
	catalog(MsgUpdateUsageLoop, "usage: /loop [list | <duration> <prompt> | pause|resume|delete <schedule_id>]", "用法：/loop [list | <时长> <指令> | pause|resume|delete <计划ID>]", "使用法: /loop [list | <期間> <指示> | pause|resume|delete <ID>]", "사용법: /loop [list | <기간> <지시> | pause|resume|delete <ID>]", "uso: /loop [list | <duración> <instrucción> | pause|resume|delete <id>]", "utilisation : /loop [list | <durée> <instruction> | pause|resume|delete <id>]"),
	catalog(MsgUpdateLoopHeader, "loops for this session", "当前会话的循环任务", "このセッションのループ", "이 세션의 반복 작업", "bucles de esta sesión", "boucles de cette session"),
	catalog(MsgUpdateLoopItem, "- {id} · {state} · every {interval} · next {next} · {prompt}", "- {id} · {state} · 每 {interval} · 下次 {next} · {prompt}", "- {id} · {state} · {interval} ごと · 次回 {next} · {prompt}", "- {id} · {state} · {interval}마다 · 다음 {next} · {prompt}", "- {id} · {state} · cada {interval} · próxima {next} · {prompt}", "- {id} · {state} · toutes les {interval} · prochaine {next} · {prompt}"),
	catalog(MsgUpdateLoopEmpty, "  none", "  无", "  なし", "  없음", "  ninguno", "  aucune"),
	catalog(MsgUpdateLoopChanged, "loop {action}: {id}", "循环任务 {action}：{id}", "ループ {action}: {id}", "반복 작업 {action}: {id}", "bucle {action}: {id}", "boucle {action} : {id}"),
	catalog(MsgUpdateGoalUsage, "usage: /goal [--tokens N] <objective> | clear|pause|resume|complete|continue", "用法：/goal [--tokens N] <目标> | clear|pause|resume|complete|continue", "使用法: /goal [--tokens N] <目標> | clear|pause|resume|complete|continue", "사용법: /goal [--tokens N] <목표> | clear|pause|resume|complete|continue", "uso: /goal [--tokens N] <objetivo> | clear|pause|resume|complete|continue", "utilisation : /goal [--tokens N] <objectif> | clear|pause|resume|complete|continue"),
	catalog(MsgUpdateGoalFailed, "goal {action} failed: {error}", "目标 {action} 失败：{error}", "目標の {action} に失敗: {error}", "목표 {action} 실패: {error}", "falló la acción {action} del objetivo: {error}", "échec de l’action {action} sur l’objectif : {error}"),
	catalog(MsgUpdateGoalCleared, "goal cleared", "目标已清除", "目標を消去しました", "목표를 지웠습니다", "objetivo eliminado", "objectif effacé"),
	catalog(MsgUpdateGoalNone, "no persistent goal", "没有持久目标", "永続目標はありません", "영구 목표가 없습니다", "no hay objetivo persistente", "aucun objectif persistant"),
	catalog(MsgUpdateGoalState, "goal [{status}] {objective} · {budget} · {seconds}s · continuation {used}/{max}", "目标 [{status}] {objective} · {budget} · {seconds}秒 · 续接 {used}/{max}", "目標 [{status}] {objective} · {budget} · {seconds}秒 · 継続 {used}/{max}", "목표 [{status}] {objective} · {budget} · {seconds}초 · 계속 {used}/{max}", "objetivo [{status}] {objective} · {budget} · {seconds}s · continuación {used}/{max}", "objectif [{status}] {objective} · {budget} · {seconds}s · reprise {used}/{max}"),
	catalog(MsgUpdateGoalBudgetUnlimited, "unlimited", "不限", "無制限", "무제한", "sin límite", "illimité"),
	catalog(MsgUpdateGoalBudgetTokens, "{used}/{max} tokens", "{used}/{max} Token", "{used}/{max} トークン", "{used}/{max} 토큰", "{used}/{max} tokens", "{used}/{max} jetons"),
	catalog(MsgCanonicalTranscriptTitle, "canonical transcript", "规范会话记录", "正規セッション履歴", "표준 세션 기록", "historial canónico", "historique canonique"),
	catalog(MsgCanonicalLoading, "Loading canonical session items...", "正在加载规范会话记录……", "正規セッション項目を読み込み中…", "표준 세션 항목을 불러오는 중...", "Cargando elementos canónicos...", "Chargement des éléments canoniques…"),
	catalog(MsgCanonicalUnavailable, "Canonical transcript unavailable: {error}", "规范会话记录不可用：{error}", "正規セッション履歴を利用できません: {error}", "표준 세션 기록을 사용할 수 없음: {error}", "Historial canónico no disponible: {error}", "Historique canonique indisponible : {error}"),
	catalog(MsgCanonicalEmpty, "No canonical session items yet.", "暂无规范会话记录。", "正規セッション項目はまだありません。", "아직 표준 세션 항목이 없습니다.", "Aún no hay elementos canónicos.", "Aucun élément canonique pour le moment."),
	catalog(MsgCanonicalSearchTitle, "canonical search ({count})", "规范记录搜索（{count}）", "正規履歴検索（{count}）", "표준 기록 검색({count})", "búsqueda canónica ({count})", "recherche canonique ({count})"),
	catalog(MsgCanonicalSearchEmpty, "No canonical session items matched.", "没有匹配的规范会话记录。", "一致する正規セッション項目はありません。", "일치하는 표준 세션 항목이 없습니다.", "No hay elementos canónicos coincidentes.", "Aucun élément canonique correspondant."),
	catalog(MsgCanonicalRecapEmpty, "No canonical session items yet.", "暂无可回顾的规范会话记录。", "要約できる正規セッション項目はまだありません。", "요약할 표준 세션 항목이 없습니다.", "Aún no hay elementos canónicos para resumir.", "Aucun élément canonique à récapituler."),
	catalog(MsgOperationalEmpty, "No data reported by the daemon.", "daemon 未报告数据。", "daemon からデータが報告されていません。", "daemon이 보고한 데이터가 없습니다.", "El daemon no informó datos.", "Aucune donnée signalée par le daemon."),
	catalog(MsgOperationalStatusTitle, "session status", "会话状态", "セッション状態", "세션 상태", "estado de la sesión", "état de la session"),
	catalog(MsgOperationalPermissionsTitle, "effective permissions", "有效权限", "有効な権限", "유효 권한", "permisos efectivos", "autorisations effectives"),
	catalog(MsgOperationalContextTitle, "persisted context summary", "持久上下文摘要", "永続コンテキスト要約", "영구 컨텍스트 요약", "resumen de contexto persistido", "résumé du contexte persistant"),
	catalog(MsgOperationalConfigTitle, "effective runtime (read-only)", "有效运行时状态（只读）", "有効なランタイム（読み取り専用）", "유효 런타임(읽기 전용)", "runtime efectivo (solo lectura)", "runtime effectif (lecture seule)"),
	catalog(MsgOperationalMCPTitle, "MCP inventory (read-only)", "MCP 清单（只读）", "MCP インベントリ（読み取り専用）", "MCP 인벤토리(읽기 전용)", "inventario MCP (solo lectura)", "inventaire MCP (lecture seule)"),
	catalog(MsgOperationalCompactTitle, "checkpoint compact", "检查点压缩", "チェックポイント圧縮", "체크포인트 압축", "compactación del checkpoint", "compaction du checkpoint"),
	catalog(MsgUpdateUsageCompact, "usage: /compact", "用法：/compact", "使用法: /compact", "사용법: /compact", "uso: /compact", "utilisation : /compact"),
	catalog(MsgUpdateUsageDiff, "usage: /diff", "用法：/diff", "使用法: /diff", "사용법: /diff", "uso: /diff", "utilisation : /diff"),
	catalog(MsgUpdateUsageMCP, "usage: /mcp [verbose]", "用法：/mcp [verbose]", "使用法: /mcp [verbose]", "사용법: /mcp [verbose]", "uso: /mcp [verbose]", "utilisation : /mcp [verbose]"),
	catalog(MsgDiffTitle, "workspace diff (read-only)", "工作区差异（只读）", "ワークスペース差分（読み取り専用）", "작업 공간 diff(읽기 전용)", "diff del espacio de trabajo (solo lectura)", "diff de l’espace de travail (lecture seule)"),
	catalog(MsgDiffLoading, "Loading workspace diff...", "正在加载工作区差异……", "ワークスペース差分を読み込み中…", "작업 공간 diff 불러오는 중...", "Cargando el diff del espacio de trabajo...", "Chargement du diff de l’espace de travail…"),
	catalog(MsgDiffFile, "{status} {path}", "{status} {path}", "{status} {path}", "{status} {path}", "{status} {path}", "{status} {path}"),
	catalog(MsgDiffBinary, "binary content omitted", "二进制内容已省略", "バイナリ内容を省略", "바이너리 내용 생략", "contenido binario omitido", "contenu binaire omis"),
	catalog(MsgDiffTruncated, "file diff truncated", "文件差异已截断", "ファイル差分を切り詰め", "파일 diff 잘림", "diff de archivo truncado", "diff du fichier tronqué"),
	catalog(MsgDiffTotalTruncated, "total diff limit reached; remaining files omitted", "已达到差异总大小上限；其余文件已省略", "差分の合計上限に達したため残りを省略", "전체 diff 제한에 도달하여 나머지 파일 생략", "se alcanzó el límite total; se omitieron los archivos restantes", "limite totale atteinte ; fichiers restants omis"),
	catalog(MsgDiffClean, "working tree clean", "工作区干净", "作業ツリーに変更なし", "작업 트리 변경 없음", "árbol de trabajo limpio", "arbre de travail propre"),
	catalog(MsgUpdateModelCurrent, "model: {model}", "模型：{model}", "モデル: {model}", "모델: {model}", "modelo: {model}", "modèle : {model}"),
	catalog(MsgUpdateModelChanged, "model switched to {model}; applies to new tasks", "模型已切换为 {model}；对新任务生效", "モデルを {model} に変更しました。新しいタスクに適用されます", "모델을 {model}(으)로 전환했습니다. 새 작업부터 적용됩니다", "modelo cambiado a {model}; se aplica a tareas nuevas", "modèle changé pour {model} ; appliqué aux nouvelles tâches"),
	catalog(MsgModelPickerTitle, "Select model", "选择模型", "モデルを選択", "모델 선택", "Seleccionar modelo", "Choisir un modèle"),
	catalog(MsgModelPickerLoading, "Loading available models...", "正在加载可用模型……", "利用可能なモデルを読み込み中…", "사용 가능한 모델을 불러오는 중...", "Cargando modelos disponibles...", "Chargement des modèles disponibles…"),
	catalog(MsgModelPickerFailed, "Unable to load models: {error}", "无法加载模型：{error}", "モデルを読み込めません: {error}", "모델을 불러올 수 없습니다: {error}", "No se pudieron cargar los modelos: {error}", "Impossible de charger les modèles : {error}"),
	catalog(MsgModelPickerDefault, "Daemon default", "守护进程默认模型", "デーモンの既定値", "데몬 기본값", "Predeterminado del daemon", "Valeur par défaut du daemon"),
	catalog(MsgModelPickerHelp, "Enter selects · E changes reasoning effort · Esc closes", "Enter 选择 · E 切换推理强度 · Esc 关闭", "Enter で選択 · E で推論強度を変更 · Esc で閉じる", "Enter 선택 · E로 추론 강도 변경 · Esc 닫기", "Enter selecciona · E cambia el esfuerzo de razonamiento · Esc cierra", "Entrée sélectionne · E change l’effort de raisonnement · Échap ferme"),
	catalog(MsgModelPickerPage, "{start}-{end} of {count}", "第 {start}-{end} 项，共 {count} 项", "{count} 件中 {start}-{end}", "{count}개 중 {start}-{end}", "{start}-{end} de {count}", "{start}-{end} sur {count}"),
	catalog(MsgModelPickerEmpty, "No enumerated provider models; use /model <provider/model> for a dynamic model.", "没有可枚举的厂商模型；动态模型请使用 /model <厂商/模型>。", "列挙可能なモデルはありません。動的モデルには /model <provider/model> を使用してください。", "열거 가능한 모델이 없습니다. 동적 모델은 /model <provider/model>을 사용하세요.", "No hay modelos enumerados; use /model <proveedor/modelo> para un modelo dinámico.", "Aucun modèle énuméré ; utilisez /model <fournisseur/modèle> pour un modèle dynamique."),
	catalog(MsgSessionPickerTitle, "Resume session", "恢复会话", "セッションを再開", "세션 재개", "Reanudar sesión", "Reprendre une session"),
	catalog(MsgSessionPickerLoading, "Loading sessions...", "正在加载会话……", "セッションを読み込み中…", "세션 불러오는 중...", "Cargando sesiones...", "Chargement des sessions…"),
	catalog(MsgSessionPickerFailed, "Unable to load sessions: {error} (r retries)", "无法加载会话：{error}（按 r 重试）", "セッションを読み込めません: {error}（r で再試行）", "세션을 불러올 수 없음: {error} (r로 재시도)", "No se pudieron cargar las sesiones: {error} (r reintenta)", "Impossible de charger les sessions : {error} (r réessaie)"),
	catalog(MsgSessionPickerEmpty, "No other resumable sessions.", "没有其他可恢复的会话。", "再開可能な他のセッションはありません。", "재개할 다른 세션이 없습니다.", "No hay otras sesiones reanudables.", "Aucune autre session ne peut être reprise."),
	catalog(MsgSessionPickerHelp, "Enter resumes · Esc closes", "Enter 恢复 · Esc 关闭", "Enter で再開 · Esc で閉じる", "Enter 재개 · Esc 닫기", "Enter reanuda · Esc cierra", "Entrée reprend · Échap ferme"),
	catalog(MsgSessionPickerForkOf, "fork of {parent}", "分叉自 {parent}", "{parent} のフォーク", "{parent}에서 포크", "bifurcación de {parent}", "branche de {parent}"),
	catalog(MsgSessionSwitchBlocked, "Cannot switch sessions: {reason}.", "无法切换会话：{reason}。", "セッションを切り替えられません: {reason}。", "세션을 전환할 수 없음: {reason}.", "No se puede cambiar de sesión: {reason}.", "Impossible de changer de session : {reason}."),
	catalog(MsgSessionSwitchDraft, "the current draft must be submitted or cleared", "必须先提交或清空当前草稿", "現在の下書きを送信または消去してください", "현재 초안을 제출하거나 지워야 합니다", "debe enviar o borrar el borrador actual", "le brouillon actuel doit être envoyé ou effacé"),
	catalog(MsgSessionSwitchTask, "an active task is still running", "仍有活动任务在运行", "実行中のタスクがあります", "활성 작업이 아직 실행 중입니다", "aún hay una tarea activa", "une tâche active est encore en cours"),
	catalog(MsgSessionSwitchSubmission, "a submission is still resolving", "提交仍在处理中", "送信処理がまだ完了していません", "제출이 아직 처리 중입니다", "un envío aún se está procesando", "un envoi est encore en cours"),
	catalog(MsgSessionSwitchRetry, "a submission retry must be resolved", "必须先处理提交重试", "送信の再試行を解決してください", "제출 재시도를 먼저 처리해야 합니다", "debe resolver el reintento del envío", "la nouvelle tentative d’envoi doit être résolue"),
	catalog(MsgSessionSwitchQueue, "queued drafts must be submitted or removed", "必须先提交或移除排队草稿", "待機中の下書きを送信または削除してください", "대기 중인 초안을 제출하거나 제거해야 합니다", "debe enviar o eliminar los borradores en cola", "les brouillons en file doivent être envoyés ou supprimés"),
	catalog(MsgSessionSwitchGovernance, "a governance decision is pending", "仍有治理决策待处理", "ガバナンス判断が保留中です", "거버넌스 결정이 대기 중입니다", "hay una decisión de gobernanza pendiente", "une décision de gouvernance est en attente"),
	catalog(MsgSessionSwitchEditor, "the external editor is active", "外部编辑器仍处于活动状态", "外部エディタが使用中です", "외부 편집기가 활성 상태입니다", "el editor externo está activo", "l’éditeur externe est actif"),
	catalog(MsgSessionSwitchGoal, "the current goal must be completed or cleared", "必须先完成或清除当前目标", "現在の目標を完了または消去してください", "현재 목표를 완료하거나 지워야 합니다", "debe completar o borrar el objetivo actual", "l’objectif actuel doit être terminé ou effacé"),
	catalog(MsgSessionActionFailed, "Session action failed: {error}", "会话操作失败：{error}", "セッション操作に失敗しました: {error}", "세션 작업 실패: {error}", "Falló la acción de sesión: {error}", "Échec de l’action de session : {error}"),
	catalog(MsgSessionActionInvalid, "Session action returned no session ID.", "会话操作未返回会话 ID。", "セッション操作から ID が返されませんでした。", "세션 작업에서 세션 ID가 반환되지 않았습니다.", "La acción no devolvió un ID de sesión.", "L’action n’a renvoyé aucun ID de session."),
	catalog(MsgSessionSwitchUnavailable, "Session switching is unavailable in this frontend.", "此客户端不支持切换会话。", "このフロントエンドではセッションを切り替えられません。", "이 프런트엔드에서는 세션 전환을 사용할 수 없습니다.", "El cambio de sesión no está disponible en este cliente.", "Le changement de session n’est pas disponible dans ce client."),
	catalog(MsgSessionSwitchLeaseBlocked, "Session switch blocked: {error}", "会话切换被阻止：{error}", "セッション切替が拒否されました: {error}", "세션 전환 차단됨: {error}", "Cambio de sesión bloqueado: {error}", "Changement de session bloqué : {error}"),
	catalog(MsgSessionSwitchFailed, "Session switch failed: {error}", "会话切换失败：{error}", "セッション切替に失敗しました: {error}", "세션 전환 실패: {error}", "Falló el cambio de sesión: {error}", "Échec du changement de session : {error}"),
	catalog(MsgSessionSwitching, "Switching to session {session}...", "正在切换到会话 {session}……", "セッション {session} に切り替え中…", "세션 {session}(으)로 전환 중...", "Cambiando a la sesión {session}...", "Passage à la session {session}…"),
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
