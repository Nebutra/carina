package tui

const (
	MsgCheckpointLoading             MessageID = "checkpoint.loading"
	MsgCheckpointOperationBusy       MessageID = "checkpoint.operation_busy"
	MsgCheckpointListFailed          MessageID = "checkpoint.list_failed"
	MsgCheckpointNone                MessageID = "checkpoint.none"
	MsgCheckpointPreviewFailed       MessageID = "checkpoint.preview_failed"
	MsgCheckpointReview              MessageID = "checkpoint.review"
	MsgCheckpointRestoreFailed       MessageID = "checkpoint.restore_failed"
	MsgCheckpointRestoredStatus      MessageID = "checkpoint.restored_status"
	MsgCheckpointRestoredLog         MessageID = "checkpoint.restored_log"
	MsgCheckpointResumeFailed        MessageID = "checkpoint.resume_failed"
	MsgCheckpointResumedLog          MessageID = "checkpoint.resumed_log"
	MsgCheckpointWaitRestore         MessageID = "checkpoint.wait_restore"
	MsgCheckpointWaitResume          MessageID = "checkpoint.wait_resume"
	MsgCheckpointArmed               MessageID = "checkpoint.armed"
	MsgCheckpointArmFirst            MessageID = "checkpoint.arm_first"
	MsgCheckpointDisarmed            MessageID = "checkpoint.disarmed"
	MsgCheckpointLoadingPreview      MessageID = "checkpoint.loading_preview"
	MsgCheckpointRestoring           MessageID = "checkpoint.restoring"
	MsgCheckpointResuming            MessageID = "checkpoint.resuming"
	MsgCheckpointNoRecent            MessageID = "checkpoint.no_recent"
	MsgCheckpointOtherActive         MessageID = "checkpoint.other_active"
	MsgCheckpointPaused              MessageID = "checkpoint.paused"
	MsgCheckpointTitle               MessageID = "checkpoint.title"
	MsgCheckpointRestoredTitle       MessageID = "checkpoint.restored_title"
	MsgCheckpointResumeTitle         MessageID = "checkpoint.resume_title"
	MsgCheckpointExplicitTask        MessageID = "checkpoint.explicit_task"
	MsgCheckpointTaskLine            MessageID = "checkpoint.task_line"
	MsgCheckpointRestoredLine        MessageID = "checkpoint.restored_line"
	MsgCheckpointContextRolledBack   MessageID = "checkpoint.context_rolled_back"
	MsgCheckpointAuditRetained       MessageID = "checkpoint.audit_retained"
	MsgCheckpointPausedNoAuto        MessageID = "checkpoint.paused_no_auto"
	MsgCheckpointResumeProgress      MessageID = "checkpoint.resume_progress"
	MsgCheckpointResumeActions       MessageID = "checkpoint.resume_actions"
	MsgCheckpointRetryResumeActions  MessageID = "checkpoint.retry_resume_actions"
	MsgCheckpointPreviewLine         MessageID = "checkpoint.preview_line"
	MsgCheckpointRollbackPatches     MessageID = "checkpoint.rollback_patches"
	MsgCheckpointNoPatches           MessageID = "checkpoint.no_patches"
	MsgCheckpointRestoreProgress     MessageID = "checkpoint.restore_progress"
	MsgCheckpointRestoreActions      MessageID = "checkpoint.restore_actions"
	MsgCheckpointRetryRestoreActions MessageID = "checkpoint.retry_restore_actions"
	MsgCheckpointDefaultSummary      MessageID = "checkpoint.default_summary"
	MsgCheckpointListItem            MessageID = "checkpoint.list_item"
	MsgCheckpointListActions         MessageID = "checkpoint.list_actions"

	MsgKeymapTitle            MessageID = "keymap.title"
	MsgKeymapUnavailable      MessageID = "keymap.unavailable"
	MsgKeymapChoose           MessageID = "keymap.choose"
	MsgKeymapCaptureStart     MessageID = "keymap.capture_start"
	MsgKeymapCaptureCancelled MessageID = "keymap.capture_cancelled"
	MsgKeymapCapturePending   MessageID = "keymap.capture_pending"
	MsgKeymapCaptureLiteral   MessageID = "keymap.capture_literal"
	MsgKeymapCaptureRetry     MessageID = "keymap.capture_retry"
	MsgKeymapAppliedProcess   MessageID = "keymap.applied_process"
	MsgKeymapCaptureTimeout   MessageID = "keymap.capture_timeout"
	MsgKeymapSaving           MessageID = "keymap.saving"
	MsgKeymapNotChanged       MessageID = "keymap.not_changed"
	MsgKeymapSavedRejected    MessageID = "keymap.saved_rejected"
	MsgKeymapSaved            MessageID = "keymap.saved"
	MsgKeymapReloadRejected   MessageID = "keymap.reload_rejected"
	MsgKeymapReloaded         MessageID = "keymap.reloaded"
	MsgKeymapActionFooter     MessageID = "keymap.action_footer"
	MsgKeymapPressKey         MessageID = "keymap.press_key"
	MsgKeymapPendingChord     MessageID = "keymap.pending_chord"
	MsgKeymapCaptureFooter    MessageID = "keymap.capture_footer"
	MsgKeymapBrowseFooter     MessageID = "keymap.browse_footer"

	MsgTranscriptOpen             MessageID = "transcript.open"
	MsgTranscriptFold             MessageID = "transcript.action.fold"
	MsgTranscriptInspect          MessageID = "transcript.action.inspect"
	MsgTranscriptCopy             MessageID = "transcript.action.copy"
	MsgTranscriptEdit             MessageID = "transcript.action.edit"
	MsgTranscriptRecovery         MessageID = "transcript.kind.recovery"
	MsgTranscriptCancel           MessageID = "transcript.action.cancel"
	MsgTranscriptInspectTitle     MessageID = "transcript.inspect_title"
	MsgTranscriptCollapsed        MessageID = "transcript.collapsed"
	MsgTranscriptRuntime          MessageID = "transcript.runtime"
	MsgTranscriptApproval         MessageID = "transcript.approval"
	MsgTranscriptQuestion         MessageID = "transcript.question"
	MsgTranscriptTask             MessageID = "transcript.task"
	MsgTranscriptModel            MessageID = "transcript.model"
	MsgTranscriptContextCompacted MessageID = "transcript.context_compacted"
	MsgTranscriptTool             MessageID = "transcript.tool"
	MsgTranscriptActivity         MessageID = "transcript.activity"
	MsgTranscriptWorkflow         MessageID = "transcript.workflow"
	MsgTranscriptSubagent         MessageID = "transcript.subagent"
	MsgTranscriptAgent            MessageID = "transcript.agent"
	MsgTranscriptCompleted        MessageID = "transcript.completed"
	MsgTranscriptSelected         MessageID = "transcript.selected"
	MsgTranscriptResponseReceived MessageID = "transcript.response_received"
	MsgTranscriptStarted          MessageID = "transcript.started"
	MsgTranscriptStep             MessageID = "transcript.step"
	MsgTranscriptCommand          MessageID = "transcript.command"
	MsgTranscriptOutput           MessageID = "transcript.output"
	MsgTranscriptExit             MessageID = "transcript.exit"
	MsgTranscriptKindFile         MessageID = "transcript.kind.file"
	MsgTranscriptKindContext      MessageID = "transcript.kind.context"
	MsgTranscriptKindGovernance   MessageID = "transcript.kind.governance"
	MsgTranscriptKindSystem       MessageID = "transcript.kind.system"
)

var operatorCatalogRows = []catalogRow{
	catalog(MsgCheckpointLoading, "Loading checkpoints...", "正在加载检查点...", "チェックポイントを読み込み中...", "체크포인트 불러오는 중...", "Cargando puntos de control...", "Chargement des points de contrôle..."),
	catalog(MsgCheckpointOperationBusy, "{operation} is still in progress; this dialog cannot close until it finishes", "{operation} 仍在进行；完成前无法关闭此对话框", "{operation} は進行中です。完了まで閉じられません", "{operation} 진행 중입니다. 완료될 때까지 닫을 수 없습니다", "{operation} sigue en curso; no se puede cerrar hasta que termine", "{operation} est en cours ; fermeture impossible avant la fin"),
	catalog(MsgCheckpointListFailed, "Checkpoint list failed: {error}", "检查点列表加载失败：{error}", "チェックポイント一覧の取得失敗: {error}", "체크포인트 목록 실패: {error}", "Falló la lista de puntos de control: {error}", "Échec de la liste des points de contrôle : {error}"),
	catalog(MsgCheckpointNone, "No rewind points are available for this session", "此会话没有可用的回退点", "このセッションに巻き戻し点はありません", "이 세션에는 되돌리기 지점이 없습니다", "No hay puntos de retorno para esta sesión", "Aucun point de retour pour cette session"),
	catalog(MsgCheckpointPreviewFailed, "Checkpoint preview failed: {error}", "检查点预览失败：{error}", "チェックポイントのプレビュー失敗: {error}", "체크포인트 미리보기 실패: {error}", "Falló la vista previa: {error}", "Échec de l’aperçu : {error}"),
	catalog(MsgCheckpointReview, "Review the rollback, then press {arm} and {confirm} to restore", "审阅回退内容，然后按 {arm} 和 {confirm} 恢复", "ロールバック内容を確認し、{arm}、{confirm} の順に押して復元", "롤백을 검토한 뒤 {arm}, {confirm} 순서로 눌러 복원하세요", "Revisa la reversión y pulsa {arm} y {confirm} para restaurar", "Examinez le retour, puis appuyez sur {arm} et {confirm} pour restaurer"),
	catalog(MsgCheckpointRestoreFailed, "Restore failed: {error}; press {retry} to retry the same checkpoint", "恢复失败：{error}；按 {retry} 重试同一检查点", "復元失敗: {error}。{retry} で同じチェックポイントを再試行", "복원 실패: {error}. {retry}로 같은 체크포인트를 다시 시도하세요", "La restauración falló: {error}; pulsa {retry} para reintentar", "Échec de la restauration : {error} ; appuyez sur {retry} pour réessayer"),
	catalog(MsgCheckpointRestoredStatus, "Model context rolled back; the audit transcript remains visible in this TUI", "模型上下文已回退；审计记录仍显示在此 TUI 中", "モデルコンテキストを巻き戻しました。監査履歴はこの TUI に残ります", "모델 컨텍스트가 롤백되었습니다. 감사 기록은 이 TUI에 유지됩니다", "Se revirtió el contexto del modelo; el historial de auditoría sigue visible", "Le contexte du modèle a été restauré ; l’historique d’audit reste visible"),
	catalog(MsgCheckpointRestoredLog, "- restored checkpoint {checkpoint} at turn {turn}: model context rolled back; audit transcript retained; task {task} is paused", "- 已恢复检查点 {checkpoint}（第 {turn} 轮）：模型上下文已回退；审计记录保留；任务 {task} 已暂停", "- チェックポイント {checkpoint}（ターン {turn}）を復元: コンテキスト巻き戻し、監査履歴保持、タスク {task} は一時停止", "- 체크포인트 {checkpoint} 복원(턴 {turn}): 모델 컨텍스트 롤백, 감사 기록 유지, 작업 {task} 일시 중지", "- punto {checkpoint} restaurado en el turno {turn}: contexto revertido, auditoría conservada, tarea {task} pausada", "- point {checkpoint} restauré au tour {turn} : contexte restauré, audit conservé, tâche {task} en pause"),
	catalog(MsgCheckpointResumeFailed, "Resume failed: {error}; press {retry} to retry", "继续失败：{error}；按 {retry} 重试", "再開失敗: {error}。{retry} で再試行", "재개 실패: {error}. {retry}로 다시 시도하세요", "No se pudo reanudar: {error}; pulsa {retry}", "Échec de la reprise : {error} ; appuyez sur {retry}"),
	catalog(MsgCheckpointResumedLog, "- resumed restored task {task}", "- 已继续恢复后的任务 {task}", "- 復元タスク {task} を再開", "- 복원된 작업 {task} 재개", "- tarea restaurada {task} reanudada", "- tâche restaurée {task} reprise"),
	catalog(MsgCheckpointWaitRestore, "Restore is in progress; wait for success or failure before closing", "正在恢复；请等待成功或失败后再关闭", "復元中です。結果が出るまで閉じないでください", "복원 중입니다. 결과가 나올 때까지 기다리세요", "La restauración está en curso; espera el resultado antes de cerrar", "Restauration en cours ; attendez le résultat avant de fermer"),
	catalog(MsgCheckpointWaitResume, "Resume is in progress; wait for success or failure before closing", "正在继续任务；请等待成功或失败后再关闭", "再開中です。結果が出るまで閉じないでください", "재개 중입니다. 결과가 나올 때까지 기다리세요", "La reanudación está en curso; espera el resultado antes de cerrar", "Reprise en cours ; attendez le résultat avant de fermer"),
	catalog(MsgCheckpointArmed, "Restore armed; press {confirm} to confirm or {cancel} to cancel", "恢复已就绪；按 {confirm} 确认，或按 {cancel} 取消", "復元準備完了。{confirm} で確定、{cancel} で取消", "복원 준비 완료. {confirm} 확인, {cancel} 취소", "Restauración preparada; pulsa {confirm} para confirmar o {cancel} para cancelar", "Restauration armée ; appuyez sur {confirm} pour confirmer ou {cancel} pour annuler"),
	catalog(MsgCheckpointArmFirst, "Press {arm} first to arm this destructive restore", "请先按 {arm} 准备此破坏性恢复", "破壊的な復元を準備するには先に {arm} を押してください", "파괴적 복원을 준비하려면 먼저 {arm}을 누르세요", "Pulsa primero {arm} para preparar esta restauración destructiva", "Appuyez d’abord sur {arm} pour armer cette restauration destructive"),
	catalog(MsgCheckpointDisarmed, "Restore disarmed; review the rollback before arming it again", "恢复已取消准备；请重新审阅回退内容", "復元準備を解除しました。再度準備する前に内容を確認してください", "복원 준비가 해제되었습니다. 다시 준비하기 전에 롤백을 검토하세요", "Restauración desarmada; revisa la reversión antes de prepararla de nuevo", "Restauration désarmée ; examinez le retour avant de la réarmer"),
	catalog(MsgCheckpointLoadingPreview, "Loading rollback preview...", "正在加载回退预览...", "ロールバックのプレビューを読み込み中...", "롤백 미리보기 불러오는 중...", "Cargando vista previa de la reversión...", "Chargement de l’aperçu du retour..."),
	catalog(MsgCheckpointRestoring, "Restoring checkpoint... this dialog remains open until the daemon confirms the result", "正在恢复检查点... 守护进程确认结果前此对话框将保持打开", "チェックポイントを復元中... daemon の確認までこの画面は開いたままです", "체크포인트 복원 중... 데몬 확인 전까지 창이 열려 있습니다", "Restaurando... el diálogo seguirá abierto hasta la confirmación del daemon", "Restauration... la fenêtre reste ouverte jusqu’à la confirmation du daemon"),
	catalog(MsgCheckpointResuming, "Resuming restored task... this dialog remains open until the daemon confirms the result", "正在继续恢复后的任务... 守护进程确认结果前此对话框将保持打开", "復元タスクを再開中... daemon の確認までこの画面は開いたままです", "복원된 작업 재개 중... 데몬 확인 전까지 창이 열려 있습니다", "Reanudando la tarea... el diálogo seguirá abierto hasta la confirmación del daemon", "Reprise de la tâche... la fenêtre reste ouverte jusqu’à la confirmation du daemon"),
	catalog(MsgCheckpointNoRecent, "{glyph} no recent restored task is known; use /task-resume <task_id> after restarting the TUI", "{glyph} 没有已知的近期恢复任务；重启 TUI 后请使用 /task-resume <task_id>", "{glyph} 最近の復元タスクがありません。TUI 再起動後は /task-resume <task_id> を使用", "{glyph} 최근 복원된 작업이 없습니다. TUI 재시작 후 /task-resume <task_id>를 사용하세요", "{glyph} no se conoce una tarea restaurada reciente; usa /task-resume <task_id> tras reiniciar", "{glyph} aucune tâche restaurée récente ; utilisez /task-resume <task_id> après redémarrage"),
	catalog(MsgCheckpointOtherActive, "{glyph} cannot resume task {task} while task {active} is active", "{glyph} 任务 {active} 活动期间无法继续任务 {task}", "{glyph} タスク {active} の実行中は {task} を再開できません", "{glyph} 작업 {active} 활성 중에는 {task}를 재개할 수 없습니다", "{glyph} no se puede reanudar {task} mientras {active} está activa", "{glyph} impossible de reprendre {task} pendant l’exécution de {active}"),
	catalog(MsgCheckpointPaused, "Task is paused after checkpoint restore; resume only when you are ready", "任务在检查点恢复后已暂停；准备好后再继续", "チェックポイント復元後、タスクは一時停止中です。準備ができたら再開してください", "체크포인트 복원 후 작업이 일시 중지되었습니다. 준비되면 재개하세요", "La tarea está pausada tras restaurar; reanúdala cuando estés listo", "La tâche est en pause après restauration ; reprenez-la lorsque vous êtes prêt"),
	catalog(MsgCheckpointTitle, "Rewind to checkpoint", "回退到检查点", "チェックポイントへ巻き戻す", "체크포인트로 되돌리기", "Volver a un punto de control", "Revenir à un point de contrôle"),
	catalog(MsgCheckpointRestoredTitle, "Checkpoint restored", "检查点已恢复", "チェックポイントを復元しました", "체크포인트 복원됨", "Punto de control restaurado", "Point de contrôle restauré"),
	catalog(MsgCheckpointResumeTitle, "Resume paused task", "继续暂停任务", "一時停止タスクを再開", "일시 중지된 작업 재개", "Reanudar tarea pausada", "Reprendre la tâche en pause"),
	catalog(MsgCheckpointExplicitTask, "This task ID was supplied explicitly; the daemon will verify that it can resume", "此任务 ID 由用户明确提供；守护进程将验证能否继续", "このタスク ID は明示指定です。daemon が再開可能か検証します", "이 작업 ID는 명시적으로 제공되었습니다. 데몬이 재개 가능 여부를 확인합니다", "El ID se indicó explícitamente; el daemon verificará si puede reanudarse", "Cet ID a été fourni explicitement ; le daemon vérifiera la reprise"),
	catalog(MsgCheckpointTaskLine, "task {task}", "任务 {task}", "タスク {task}", "작업 {task}", "tarea {task}", "tâche {task}"),
	catalog(MsgCheckpointRestoredLine, "{checkpoint} · turn {turn} · task {task}", "{checkpoint} · 第 {turn} 轮 · 任务 {task}", "{checkpoint} · ターン {turn} · タスク {task}", "{checkpoint} · 턴 {turn} · 작업 {task}", "{checkpoint} · turno {turn} · tarea {task}", "{checkpoint} · tour {turn} · tâche {task}"),
	catalog(MsgCheckpointContextRolledBack, "Model context has been rolled back.", "模型上下文已回退。", "モデルコンテキストを巻き戻しました。", "모델 컨텍스트가 롤백되었습니다.", "Se revirtió el contexto del modelo.", "Le contexte du modèle a été restauré."),
	catalog(MsgCheckpointAuditRetained, "The audit transcript remains visible in this TUI; it was not physically trimmed.", "审计记录仍显示在此 TUI 中；物理记录未被裁剪。", "監査履歴はこの TUI に残り、物理的には削除されていません。", "감사 기록은 이 TUI에 유지되며 물리적으로 잘리지 않았습니다.", "El historial de auditoría sigue visible; no se recortó físicamente.", "L’historique d’audit reste visible ; il n’a pas été tronqué physiquement."),
	catalog(MsgCheckpointPausedNoAuto, "The restored task is paused and will not resume automatically.", "恢复后的任务已暂停，不会自动继续。", "復元タスクは一時停止中で、自動再開しません。", "복원된 작업은 일시 중지되며 자동 재개되지 않습니다.", "La tarea restaurada está pausada y no se reanudará automáticamente.", "La tâche restaurée est en pause et ne reprendra pas automatiquement."),
	catalog(MsgCheckpointResumeProgress, "Resume in progress · close disabled", "正在继续 · 无法关闭", "再開中 · 閉じる操作は無効", "재개 중 · 닫기 비활성화", "Reanudación en curso · cierre desactivado", "Reprise en cours · fermeture désactivée"),
	catalog(MsgCheckpointResumeActions, "[{resume}] resume task  [{close}] close", "[{resume}] 继续任务  [{close}] 关闭", "[{resume}] タスク再開  [{close}] 閉じる", "[{resume}] 작업 재개  [{close}] 닫기", "[{resume}] reanudar  [{close}] cerrar", "[{resume}] reprendre  [{close}] fermer"),
	catalog(MsgCheckpointRetryResumeActions, "[{resume}] retry resume  [{close}] close", "[{resume}] 重试继续  [{close}] 关闭", "[{resume}] 再開を再試行  [{close}] 閉じる", "[{resume}] 재개 재시도  [{close}] 닫기", "[{resume}] reintentar  [{close}] cerrar", "[{resume}] réessayer  [{close}] fermer"),
	{ID: MsgCheckpointPreviewLine, EN: "{checkpoint} · turn {turn} · {count} conversation turns", ZH: "{checkpoint} · 第 {turn} 轮 · {count} 个对话轮次", JA: "{checkpoint} · ターン {turn} · 会話 {count} ターン", KO: "{checkpoint} · 턴 {turn} · 대화 {count}턴", ES: "{checkpoint} · turno {turn} · {count} turnos de conversación", FR: "{checkpoint} · tour {turn} · {count} tours de conversation", ENOne: "{checkpoint} · turn {turn} · {count} conversation turn", ZHOne: "{checkpoint} · 第 {turn} 轮 · {count} 个对话轮次", JAOne: "{checkpoint} · ターン {turn} · 会話 {count} ターン", KOOne: "{checkpoint} · 턴 {turn} · 대화 {count}턴", ESOne: "{checkpoint} · turno {turn} · {count} turno de conversación", FROne: "{checkpoint} · tour {turn} · {count} tour de conversation"},
	catalog(MsgCheckpointRollbackPatches, "Rollback patches:", "回退补丁：", "ロールバック対象パッチ:", "롤백 패치:", "Parches que se revertirán:", "Correctifs à annuler :"),
	catalog(MsgCheckpointNoPatches, "  none (model context only)", "  无（仅模型上下文）", "  なし（モデルコンテキストのみ）", "  없음 (모델 컨텍스트만)", "  ninguno (solo contexto del modelo)", "  aucun (contexte du modèle uniquement)"),
	catalog(MsgCheckpointRestoreProgress, "Restore in progress · close disabled", "正在恢复 · 无法关闭", "復元中 · 閉じる操作は無効", "복원 중 · 닫기 비활성화", "Restauración en curso · cierre desactivado", "Restauration en cours · fermeture désactivée"),
	catalog(MsgCheckpointRestoreActions, "[{arm}] arm  [{confirm}] confirm  [{back}] back  [{close}] close", "[{arm}] 准备  [{confirm}] 确认  [{back}] 返回  [{close}] 关闭", "[{arm}] 準備  [{confirm}] 確定  [{back}] 戻る  [{close}] 閉じる", "[{arm}] 준비  [{confirm}] 확인  [{back}] 뒤로  [{close}] 닫기", "[{arm}] preparar  [{confirm}] confirmar  [{back}] volver  [{close}] cerrar", "[{arm}] armer  [{confirm}] confirmer  [{back}] retour  [{close}] fermer"),
	catalog(MsgCheckpointRetryRestoreActions, "[{retry}] retry restore  [{back}] back  [{close}] close", "[{retry}] 重试恢复  [{back}] 返回  [{close}] 关闭", "[{retry}] 復元を再試行  [{back}] 戻る  [{close}] 閉じる", "[{retry}] 복원 재시도  [{back}] 뒤로  [{close}] 닫기", "[{retry}] reintentar  [{back}] volver  [{close}] cerrar", "[{retry}] réessayer  [{back}] retour  [{close}] fermer"),
	catalog(MsgCheckpointDefaultSummary, "checkpoint", "检查点", "チェックポイント", "체크포인트", "punto de control", "point de contrôle"),
	catalog(MsgCheckpointListItem, "{prefix}turn {turn}  {summary}", "{prefix}第 {turn} 轮  {summary}", "{prefix}ターン {turn}  {summary}", "{prefix}턴 {turn}  {summary}", "{prefix}turno {turn}  {summary}", "{prefix}tour {turn}  {summary}"),
	catalog(MsgCheckpointListActions, "[{preview}] preview  [{up}/{down}] move  [{close}] close", "[{preview}] 预览  [{up}/{down}] 移动  [{close}] 关闭", "[{preview}] プレビュー  [{up}/{down}] 移動  [{close}] 閉じる", "[{preview}] 미리보기  [{up}/{down}] 이동  [{close}] 닫기", "[{preview}] vista previa  [{up}/{down}] mover  [{close}] cerrar", "[{preview}] aperçu  [{up}/{down}] déplacer  [{close}] fermer"),

	catalog(MsgKeymapTitle, "Keymap", "按键映射", "キーマップ", "키맵", "Mapa de teclas", "Raccourcis"),
	catalog(MsgKeymapUnavailable, "Removing a persisted override is unavailable in this frontend", "此前端无法移除已持久化的覆盖", "このフロントエンドでは保存済み設定を削除できません", "이 프런트엔드에서는 저장된 재정의를 제거할 수 없습니다", "Este frontend no puede eliminar una personalización guardada", "Ce frontend ne peut pas supprimer un remplacement enregistré"),
	catalog(MsgKeymapChoose, "Replace, add an alternate, or restore the inherited binding", "替换、添加备用键，或恢复继承的绑定", "置換、代替キー追加、継承設定の復元を選択", "교체, 대체 키 추가 또는 상속된 바인딩 복원", "Sustituye, añade una alternativa o restaura el atajo heredado", "Remplacez, ajoutez une alternative ou restaurez le raccourci hérité"),
	catalog(MsgKeymapCaptureStart, "Press a key; modified prefixes can record up to 3 steps · {literal} quotes the next key · {cancel} cancels", "按下按键；带修饰键的前缀最多可录制 3 步 · {literal} 按字面录制下一键 · {cancel} 取消", "キーを入力。修飾キー開始なら最大 3 ステップ · {literal} で次のキーをそのまま記録 · {cancel} で取消", "키를 누르세요. 수정 키 접두사는 최대 3단계 기록 · {literal} 다음 키 그대로 기록 · {cancel} 취소", "Pulsa una tecla; las secuencias admiten hasta 3 pasos · {literal} captura literalmente la siguiente tecla · {cancel} cancela", "Appuyez sur une touche ; les séquences acceptent 3 étapes · {literal} capture littéralement la touche suivante · {cancel} annule"),
	catalog(MsgKeymapCaptureCancelled, "Capture cancelled", "录制已取消", "入力を取り消しました", "캡처 취소됨", "Captura cancelada", "Capture annulée"),
	catalog(MsgKeymapCapturePending, "Pending {chord} · another step or {save} saves · {literal} quotes next · {cancel} cancels", "待定 {chord} · 按下一步，或 {save} 保存 · {literal} 按字面录制下一键 · {cancel} 取消", "入力中 {chord} · 次のキー、または {save} で保存 · {literal} で次をそのまま記録 · {cancel} で取消", "대기 중 {chord} · 다음 키 또는 {save} 저장 · {literal} 다음 키 그대로 기록 · {cancel} 취소", "Pendiente {chord} · otro paso o {save} guarda · {literal} captura la siguiente literalmente · {cancel} cancela", "En attente {chord} · autre étape ou {save} enregistre · {literal} capture la suivante littéralement · {cancel} annule"),
	catalog(MsgKeymapCaptureLiteral, "Quoted insert armed · press the key to capture literally ({literal} {literal} records {literal})", "已启用字面录制 · 按下要录制的键（{literal} {literal} 可录制 {literal}）", "そのまま記録するモード · 記録するキーを入力（{literal} {literal} で {literal} を記録）", "리터럴 기록 준비됨 · 기록할 키를 누르세요({literal} {literal}로 {literal} 기록)", "Captura literal preparada · pulsa la tecla ({literal} {literal} graba {literal})", "Capture littérale activée · appuyez sur la touche ({literal} {literal} enregistre {literal})"),
	catalog(MsgKeymapCaptureRetry, "{error}; press a new key to retry", "{error}；按新按键重试", "{error}。新しいキーで再試行", "{error}. 새 키를 눌러 다시 시도하세요", "{error}; pulsa otra tecla para reintentar", "{error} ; appuyez sur une nouvelle touche pour réessayer"),
	catalog(MsgKeymapAppliedProcess, "Binding applied for this process", "绑定已应用于当前进程", "このプロセスに設定を適用しました", "현재 프로세스에 바인딩 적용됨", "Atajo aplicado a este proceso", "Raccourci appliqué à ce processus"),
	catalog(MsgKeymapCaptureTimeout, "Chord capture timed out; no binding was changed", "组合键录制超时；未更改绑定", "コード入力がタイムアウトしました。設定は未変更です", "키 조합 캡처 시간 초과, 바인딩이 변경되지 않았습니다", "La captura caducó; no se cambió ningún atajo", "La capture a expiré ; aucun raccourci n’a changé"),
	catalog(MsgKeymapSaving, "Saving keymap...", "正在保存按键映射...", "キーマップを保存中...", "키맵 저장 중...", "Guardando mapa de teclas...", "Enregistrement des raccourcis..."),
	catalog(MsgKeymapNotChanged, "Keymap not changed: {error}", "按键映射未更改：{error}", "キーマップは未変更: {error}", "키맵 변경 안 됨: {error}", "No se cambió el mapa: {error}", "Raccourcis inchangés : {error}"),
	catalog(MsgKeymapSavedRejected, "Saved keymap rejected: {error}", "已保存的按键映射被拒绝：{error}", "保存したキーマップが拒否されました: {error}", "저장된 키맵 거부됨: {error}", "Se rechazó el mapa guardado: {error}", "Raccourcis enregistrés refusés : {error}"),
	catalog(MsgKeymapSaved, "Keymap saved and applied", "按键映射已保存并应用", "キーマップを保存して適用しました", "키맵 저장 및 적용 완료", "Mapa guardado y aplicado", "Raccourcis enregistrés et appliqués"),
	catalog(MsgKeymapReloadRejected, "{glyph} keymap reload rejected; last-good bindings kept: {error}", "{glyph} 按键映射重载被拒绝；保留最近可用绑定：{error}", "{glyph} キーマップ再読込を拒否。最後の有効設定を保持: {error}", "{glyph} 키맵 다시 불러오기 거부됨. 마지막 정상 바인딩 유지: {error}", "{glyph} recarga rechazada; se conservan los últimos atajos válidos: {error}", "{glyph} rechargement refusé ; derniers raccourcis valides conservés : {error}"),
	catalog(MsgKeymapReloaded, "- keymap reloaded from config", "- 已从配置重载按键映射", "- 設定からキーマップを再読込", "- 설정에서 키맵 다시 불러옴", "- mapa recargado desde la configuración", "- raccourcis rechargés depuis la configuration"),
	catalog(MsgKeymapActionFooter, "[{replace}] replace  [{add}] add alternate  [{restore}] restore inherited  [{back}] back", "[{replace}] 替换  [{add}] 添加备用  [{restore}] 恢复继承  [{back}] 返回", "[{replace}] 置換  [{add}] 代替追加  [{restore}] 継承復元  [{back}] 戻る", "[{replace}] 교체  [{add}] 대체 추가  [{restore}] 상속 복원  [{back}] 뒤로", "[{replace}] sustituir  [{add}] añadir  [{restore}] restaurar  [{back}] volver", "[{replace}] remplacer  [{add}] ajouter  [{restore}] restaurer  [{back}] retour"),
	catalog(MsgKeymapPressKey, "Press the new key now", "现在按下新按键", "新しいキーを押してください", "새 키를 누르세요", "Pulsa ahora la nueva tecla", "Appuyez maintenant sur la nouvelle touche"),
	catalog(MsgKeymapPendingChord, "Pending chord: {chord}", "待定组合键：{chord}", "入力中のコード: {chord}", "대기 중인 키 조합: {chord}", "Secuencia pendiente: {chord}", "Séquence en attente : {chord}"),
	catalog(MsgKeymapCaptureFooter, "{save} save chord · {literal} quote next · {cancel} cancel", "{save} 保存组合键 · {literal} 按字面录制下一键 · {cancel} 取消", "{save} コード保存 · {literal} 次をそのまま記録 · {cancel} 取消", "{save} 키 조합 저장 · {literal} 다음 키 그대로 기록 · {cancel} 취소", "{save} guardar secuencia · {literal} captura literal · {cancel} cancelar", "{save} enregistrer · {literal} capture littérale · {cancel} annuler"),
	catalog(MsgKeymapBrowseFooter, "[{edit}] edit  [{up}/{down}] move  [{close}] close", "[{edit}] 编辑  [{up}/{down}] 移动  [{close}] 关闭", "[{edit}] 編集  [{up}/{down}] 移動  [{close}] 閉じる", "[{edit}] 편집  [{up}/{down}] 이동  [{close}] 닫기", "[{edit}] editar  [{up}/{down}] mover  [{close}] cerrar", "[{edit}] modifier  [{up}/{down}] déplacer  [{close}] fermer"),

	catalog(MsgTranscriptOpen, "open", "展开", "開く", "열기", "abrir", "ouvrir"),
	catalog(MsgTranscriptFold, "fold", "收起", "折りたたむ", "접기", "plegar", "replier"),
	catalog(MsgTranscriptInspect, "inspect", "查看", "詳細", "검사", "inspeccionar", "inspecter"),
	catalog(MsgTranscriptCopy, "copy", "复制", "コピー", "복사", "copiar", "copier"),
	catalog(MsgTranscriptEdit, "edit", "编辑", "編集", "편집", "editar", "modifier"),
	catalog(MsgTranscriptRecovery, "recovery", "恢复", "復旧", "복구", "recuperación", "récupération"),
	catalog(MsgTranscriptCancel, "cancel", "取消", "取消", "취소", "cancelar", "annuler"),
	catalog(MsgTranscriptInspectTitle, "transcript detail", "会话详情", "セッション詳細", "대화 상세", "detalle de conversación", "détail de conversation"),
	catalog(MsgTranscriptCollapsed, "+{count}", "+{count}", "+{count}", "+{count}", "+{count}", "+{count}"),
	catalog(MsgTranscriptRuntime, "runtime", "运行时", "ランタイム", "런타임", "runtime", "runtime"),
	catalog(MsgTranscriptApproval, "approval {id}", "审批 {id}", "承認 {id}", "승인 {id}", "aprobación {id}", "approbation {id}"),
	catalog(MsgTranscriptQuestion, "question", "问题", "質問", "질문", "pregunta", "question"),
	catalog(MsgTranscriptTask, "task", "任务", "タスク", "작업", "tarea", "tâche"),
	catalog(MsgTranscriptModel, "model", "模型", "モデル", "모델", "modelo", "modèle"),
	catalog(MsgTranscriptContextCompacted, "context compacted", "上下文已压缩", "コンテキストを圧縮", "컨텍스트 압축됨", "contexto compactado", "contexte compacté"),
	catalog(MsgTranscriptTool, "tool", "工具", "ツール", "도구", "herramienta", "outil"),
	catalog(MsgTranscriptActivity, "activity", "活动", "アクティビティ", "활동", "actividad", "activité"),
	catalog(MsgTranscriptWorkflow, "workflow", "工作流", "ワークフロー", "워크플로", "flujo", "workflow"),
	catalog(MsgTranscriptSubagent, "subagent", "子 Agent", "サブ Agent", "하위 Agent", "sub-Agent", "sous-Agent"),
	catalog(MsgTranscriptAgent, "agent", "Agent", "Agent", "Agent", "Agent", "Agent"),
	catalog(MsgTranscriptCompleted, "completed", "已完成", "完了", "완료됨", "completado", "terminé"),
	catalog(MsgTranscriptSelected, "selected {tool}", "已选择 {tool}", "{tool} を選択", "{tool} 선택됨", "seleccionó {tool}", "{tool} sélectionné"),
	catalog(MsgTranscriptResponseReceived, "response received", "已收到回复", "応答を受信", "응답 수신됨", "respuesta recibida", "réponse reçue"),
	catalog(MsgTranscriptStarted, "{agent} started", "{agent} 已启动", "{agent} を開始", "{agent} 시작됨", "{agent} iniciado", "{agent} démarré"),
	catalog(MsgTranscriptStep, "step", "步骤", "ステップ", "단계", "paso", "étape"),
	catalog(MsgTranscriptCommand, "command", "命令", "コマンド", "명령", "comando", "commande"),
	catalog(MsgTranscriptOutput, "output", "输出", "出力", "출력", "salida", "sortie"),
	catalog(MsgTranscriptExit, "exit {code}", "退出 {code}", "終了 {code}", "종료 {code}", "salida {code}", "sortie {code}"),
	catalog(MsgTranscriptKindFile, "file", "文件", "ファイル", "파일", "archivo", "fichier"),
	catalog(MsgTranscriptKindContext, "context", "上下文", "コンテキスト", "컨텍스트", "contexto", "contexte"),
	catalog(MsgTranscriptKindGovernance, "governance", "治理", "ガバナンス", "거버넌스", "gobernanza", "gouvernance"),
	catalog(MsgTranscriptKindSystem, "system", "系统", "システム", "시스템", "sistema", "système"),
}

var catalogRowsData = append(append(append([]catalogRow(nil), baseCatalogRows...), operatorCatalogRows...), updateCatalogRows...)
