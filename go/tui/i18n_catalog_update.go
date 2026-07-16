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
	MsgOperationalDoctorTitle      MessageID = "operational.doctor_title"
	MsgOperationalSkillsTitle      MessageID = "operational.skills_title"
	MsgOperationalHooksTitle       MessageID = "operational.hooks_title"
	MsgOperationalExtensionsTitle  MessageID = "operational.extensions_title"
	MsgOperationalUsageTitle       MessageID = "operational.usage_title"
	MsgOperationalReviewTitle      MessageID = "operational.review_title"
	MsgOperationalMemoryTitle      MessageID = "operational.memory_title"
	MsgUpdateUsageEffort           MessageID = "update.usage_effort"
	MsgUpdateEffortChanged         MessageID = "update.effort_changed"
	MsgUpdateUsageMemory           MessageID = "update.usage_memory"
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
	MsgSessionPickerForkTask       MessageID = "session_picker.fork_task"
	MsgSessionStatusActive         MessageID = "session.status.active"
	MsgSessionStatusPaused         MessageID = "session.status.paused"
	MsgSessionStatusClosed         MessageID = "session.status.closed"
	MsgSessionAgeNow               MessageID = "session.age.now"
	MsgSessionAgeMinutes           MessageID = "session.age.minutes"
	MsgSessionAgeHours             MessageID = "session.age.hours"
	MsgSessionAgeDays              MessageID = "session.age.days"
	MsgSessionRenameUsage          MessageID = "session.rename.usage"
	MsgSessionRenameFailed         MessageID = "session.rename.failed"
	MsgSessionRenamed              MessageID = "session.rename.done"
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
	MsgSessionActionResolving      MessageID = "session_switch.resolving"
	MsgSessionSwitchRecover        MessageID = "session_switch.recover"
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

	// Product UX shell (Grok/CC/Codex parity closeout)
	MsgSettingsTitle               MessageID = "settings.title"
	MsgSettingsFooter              MessageID = "settings.footer"
	MsgSettingsTabOverview         MessageID = "settings.tab.overview"
	MsgSettingsTabMode             MessageID = "settings.tab.mode"
	MsgSettingsTabModel            MessageID = "settings.tab.model"
	MsgSettingsTabExtensions       MessageID = "settings.tab.extensions"
	MsgSettingsRowSession          MessageID = "settings.row.session"
	MsgSettingsRowMode             MessageID = "settings.row.mode"
	MsgSettingsRowModel            MessageID = "settings.row.model"
	MsgSettingsRowProfile          MessageID = "settings.row.profile"
	MsgSettingsRowSandbox          MessageID = "settings.row.sandbox"
	MsgSettingsRowApproval         MessageID = "settings.row.approval"
	MsgSettingsRowContext          MessageID = "settings.row.context"
	MsgSettingsRowCompact          MessageID = "settings.row.compact"
	MsgSettingsActionRefresh       MessageID = "settings.action.refresh"
	MsgSettingsActionContext       MessageID = "settings.action.context"
	MsgSettingsActionUsage         MessageID = "settings.action.usage"
	MsgSettingsActionCompactMode   MessageID = "settings.action.compact_mode"
	MsgSettingsActionModelPicker   MessageID = "settings.action.model_picker"
	MsgSettingsActionPlan          MessageID = "settings.action.plan"
	MsgSettingsActionBuild         MessageID = "settings.action.build"
	MsgSettingsActionPermissions   MessageID = "settings.action.permissions"
	MsgSettingsActionSafeEdit      MessageID = "settings.action.safe_edit"
	MsgSettingsActionFullWorkspace MessageID = "settings.action.full_workspace"
	MsgSettingsActionEffort        MessageID = "settings.action.effort"
	MsgSettingsActionKeymap        MessageID = "settings.action.keymap"
	MsgSettingsActionSkills        MessageID = "settings.action.skills"
	MsgSettingsActionHooks         MessageID = "settings.action.hooks"
	MsgSettingsActionMCP           MessageID = "settings.action.mcp"
	MsgSettingsActionExtensions    MessageID = "settings.action.extensions"
	MsgSettingsActionDoctor        MessageID = "settings.action.doctor"
	MsgContextSummaryHeader        MessageID = "context.summary_header"
	MsgContextSource               MessageID = "context.source"
	MsgContextRemaining            MessageID = "context.remaining"
	MsgContextUnavailable          MessageID = "context.unavailable"
	MsgContextCompactReady         MessageID = "context.compact_ready"
	MsgContextCompactBlocked       MessageID = "context.compact_blocked"
	MsgOperationalDetails          MessageID = "operational.details"
	MsgConfigSummaryHeader         MessageID = "config.summary_header"
	MsgConfigHintSettings          MessageID = "config.hint_settings"
	MsgPermissionsSummaryHeader    MessageID = "permissions.summary_header"
	MsgPermissionsProfile          MessageID = "permissions.profile"
	MsgPermissionsSource           MessageID = "permissions.source"
	MsgPermissionsChoices          MessageID = "permissions.choices"
	MsgSessionStatusHeader         MessageID = "session.status_header"
	MsgTasksTitle                  MessageID = "tasks.title"
	MsgTasksEmpty                  MessageID = "tasks.empty"
	MsgUpdateExportDone            MessageID = "update.export_done"
	MsgUpdateUsageRemember         MessageID = "update.usage_remember"
	MsgUpdateInitExists            MessageID = "update.init_exists"
	MsgUpdateInitCreated           MessageID = "update.init_created"
	MsgUpdateCompactMode           MessageID = "update.compact_mode"
	MsgUpdateUsageBtw              MessageID = "update.usage_btw"
	MsgViewPlanTitle               MessageID = "view_plan.title"
	MsgViewPlanMode                MessageID = "view_plan.mode"
	MsgViewPlanActive              MessageID = "view_plan.active"
	MsgViewPlanInactive            MessageID = "view_plan.inactive"
	MsgViewPlanHint                MessageID = "view_plan.hint"

	MsgSettingsActionApprovePlan   MessageID = "settings.action.approve_plan"
	MsgSettingsActionViewPlan      MessageID = "settings.action.view_plan"
	MsgSettingsActionExplain       MessageID = "settings.action.explain"
	MsgSettingsActionInspect       MessageID = "settings.action.inspect"
	MsgViewPlanPath                MessageID = "view_plan.path"
	MsgViewPlanEmpty               MessageID = "view_plan.empty"
	MsgViewPlanMissing             MessageID = "view_plan.missing"
	MsgViewPlanPreview             MessageID = "view_plan.preview"
	MsgUpdateBtwStarted            MessageID = "update.btw_started"
	MsgExplainTitle                MessageID = "explain.title"
	MsgExplainMode                 MessageID = "explain.mode"
	MsgExplainProfile              MessageID = "explain.profile"
	MsgExplainSandbox              MessageID = "explain.sandbox"
	MsgExplainApproval             MessageID = "explain.approval"
	MsgExplainSandboxWhy           MessageID = "explain.sandbox_why"
	MsgExplainHowToChange          MessageID = "explain.how_to_change"
	MsgInspectHeader               MessageID = "inspect.header"
	MsgInspectHint                 MessageID = "inspect.hint"
	MsgTasksLoopHint               MessageID = "tasks.loop_hint"
	MsgTasksLoopsHeader            MessageID = "tasks.loops_header"
	MsgUpdateUsageExtension        MessageID = "update.usage_extension"

	MsgContextPressureWarning  MessageID = "context.pressure_warning"
	MsgContextPressureCritical MessageID = "context.pressure_critical"
	MsgContextAutoCompact      MessageID = "context.auto_compact"
	MsgUpdateBtwForkStart      MessageID = "update.btw_fork_start"
	MsgUpdateBtwForkReady      MessageID = "update.btw_fork_ready"
	MsgUpdateBtwForkBusy       MessageID = "update.btw_fork_busy"

	MsgAlwaysApproveEnabled      MessageID = "always_approve.enabled"
	MsgAlwaysApproveDisabled     MessageID = "always_approve.disabled"
	MsgAlwaysApproveWarning      MessageID = "always_approve.warning"
	MsgUpdateUsageAlwaysApprove  MessageID = "update.usage_always_approve"
	MsgSettingsActionAlwaysApprove MessageID = "settings.action.always_approve"
	MsgDontAskEnabled            MessageID = "dont_ask.enabled"
	MsgDontAskWarning            MessageID = "dont_ask.warning"
	MsgApprovalModeAsk           MessageID = "approval_mode.ask"
	MsgApprovalModeCurrent       MessageID = "approval_mode.current"
	MsgUpdateUsageApprovalMode   MessageID = "update.usage_approval_mode"
	MsgUpdateUsageDontAsk        MessageID = "update.usage_dont_ask"
	MsgAcceptEditsEnabled        MessageID = "accept_edits.enabled"
	MsgAcceptEditsWarning        MessageID = "accept_edits.warning"
	MsgUpdateUsageAcceptEdits    MessageID = "update.usage_accept_edits"
	MsgPlanReviewTitle           MessageID = "plan_review.title"
	MsgPlanReviewFooter          MessageID = "plan_review.footer"
	MsgPlanReviewBusy            MessageID = "plan_review.busy"
	MsgPlanReviewBusyBlocked     MessageID = "plan_review.busy_blocked"
	MsgPlanReviewReviseSeed      MessageID = "plan_review.revise_seed"
	MsgPlanReviewRequestChanges  MessageID = "plan_review.request_changes"
	MsgPlanReviewApproved        MessageID = "plan_review.approved"
	MsgPlanReviewQuit            MessageID = "plan_review.quit"
	MsgAgentsSummaryHeader       MessageID = "agents.summary_header"
	MsgAgentsHint                MessageID = "agents.hint"
	MsgExplainAlwaysApprove      MessageID = "explain.always_approve"
	MsgExplainApprovalModes      MessageID = "explain.approval_modes"
	MsgOperationalAgentsTitle    MessageID = "operational.agents_title"

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
	catalog(MsgUpdateUsageMode, "usage: /mode <build|plan|cycle>", "用法：/mode <build|plan|cycle>", "使用法: /mode <build|plan|cycle>", "사용법: /mode <build|plan|cycle>", "uso: /mode <build|plan|cycle>", "utilisation : /mode <build|plan|cycle>"),
	catalog(MsgUpdateMode, "mode {mode}", "模式 {mode}", "モード {mode}", "모드 {mode}", "modo {mode}", "mode {mode}"),
	catalog(MsgUpdateUsageModel, "usage: /model <provider/model|default>", "用法：/model <厂商/模型|default>", "使用法: /model <provider/model|default>", "사용법: /model <provider/model|default>", "uso: /model <proveedor/modelo|default>", "utilisation : /model <fournisseur/modèle|default>"),
	catalog(MsgUpdateUsageLoop, "usage: /loop [list | <duration> [--concurrency forbid|queue|replace|allow] <prompt> | pause|resume|delete <schedule_id>]", "用法：/loop [list | <时长> [--concurrency forbid|queue|replace|allow] <指令> | pause|resume|delete <计划ID>]", "使用法: /loop [list | <期間> [--concurrency forbid|queue|replace|allow] <指示> | pause|resume|delete <ID>]", "사용법: /loop [list | <기간> [--concurrency forbid|queue|replace|allow] <지시> | pause|resume|delete <ID>]", "uso: /loop [list | <duración> [--concurrency forbid|queue|replace|allow] <instrucción> | pause|resume|delete <id>]", "utilisation : /loop [list | <durée> [--concurrency forbid|queue|replace|allow] <instruction> | pause|resume|delete <id>]"),
	catalog(MsgUpdateLoopHeader, "loops for this session", "当前会话的循环任务", "このセッションのループ", "이 세션의 반복 작업", "bucles de esta sesión", "boucles de cette session"),
	catalog(MsgUpdateLoopItem, "- {id} · {state} · every {interval} · next {next} · {prompt}", "- {id} · {state} · 每 {interval} · 下次 {next} · {prompt}", "- {id} · {state} · {interval} ごと · 次回 {next} · {prompt}", "- {id} · {state} · {interval}마다 · 다음 {next} · {prompt}", "- {id} · {state} · cada {interval} · próxima {next} · {prompt}", "- {id} · {state} · toutes les {interval} · prochaine {next} · {prompt}"),
	catalog(MsgUpdateLoopEmpty, "  none", "  无", "  なし", "  없음", "  ninguno", "  aucune"),
	catalog(MsgUpdateLoopChanged, "loop {action}: {id}", "循环任务 {action}：{id}", "ループ {action}: {id}", "반복 작업 {action}: {id}", "bucle {action}: {id}", "boucle {action} : {id}"),
	catalog(MsgUpdateGoalUsage, "usage: /goal [--auto] [--tokens N] [--max-continuations N] <objective> | clear|pause|resume|complete|continue", "用法：/goal [--auto] [--tokens N] [--max-continuations N] <目标> | clear|pause|resume|complete|continue", "使用法: /goal [--auto] [--tokens N] [--max-continuations N] <目標> | clear|pause|resume|complete|continue", "사용법: /goal [--auto] [--tokens N] [--max-continuations N] <목표> | clear|pause|resume|complete|continue", "uso: /goal [--auto] [--tokens N] [--max-continuations N] <objetivo> | clear|pause|resume|complete|continue", "utilisation : /goal [--auto] [--tokens N] [--max-continuations N] <objectif> | clear|pause|resume|complete|continue"),
	catalog(MsgUpdateGoalFailed, "goal {action} failed: {error}", "目标 {action} 失败：{error}", "目標の {action} に失敗: {error}", "목표 {action} 실패: {error}", "falló la acción {action} del objetivo: {error}", "échec de l’action {action} sur l’objectif : {error}"),
	catalog(MsgUpdateGoalCleared, "goal cleared", "目标已清除", "目標を消去しました", "목표를 지웠습니다", "objetivo eliminado", "objectif effacé"),
	catalog(MsgUpdateGoalNone, "no persistent goal", "没有持久目标", "永続目標はありません", "영구 목표가 없습니다", "no hay objetivo persistente", "aucun objectif persistant"),
	catalog(MsgUpdateGoalState, "goal [{status}] {objective} · {budget} · {seconds}s · {mode} continuation {used}/{max}", "目标 [{status}] {objective} · {budget} · {seconds}秒 · {mode} 续接 {used}/{max}", "目標 [{status}] {objective} · {budget} · {seconds}秒 · {mode} 継続 {used}/{max}", "목표 [{status}] {objective} · {budget} · {seconds}초 · {mode} 계속 {used}/{max}", "objetivo [{status}] {objective} · {budget} · {seconds}s · continuación {mode} {used}/{max}", "objectif [{status}] {objective} · {budget} · {seconds}s · reprise {mode} {used}/{max}"),
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
	catalog(MsgOperationalDoctorTitle, "runtime diagnostics", "运行时诊断", "ランタイム診断", "런타임 진단", "diagnósticos del runtime", "diagnostic du runtime"),
	catalog(MsgOperationalSkillsTitle, "skills (read-only)", "技能（只读）", "スキル（読み取り専用）", "기술(읽기 전용)", "skills (solo lectura)", "skills (lecture seule)"),
	catalog(MsgOperationalHooksTitle, "hooks (read-only)", "Hooks（只读）", "フック（読み取り専用）", "훅(읽기 전용)", "hooks (solo lectura)", "hooks (lecture seule)"),
	catalog(MsgOperationalExtensionsTitle, "extensions (read-only)", "扩展（只读）", "拡張（読み取り専用）", "확장(읽기 전용)", "extensiones (solo lectura)", "extensions (lecture seule)"),
	catalog(MsgOperationalUsageTitle, "session usage and cost", "会话用量与成本", "セッション使用量とコスト", "세션 사용량 및 비용", "uso y coste de la sesión", "usage et coût de la session"),
	catalog(MsgOperationalReviewTitle, "session review (read-only)", "会话审查（只读）", "セッションレビュー（読み取り専用）", "세션 검토(읽기 전용)", "revisión de sesión (solo lectura)", "revue de session (lecture seule)"),
	catalog(MsgOperationalMemoryTitle, "persistent memory status", "持久记忆状态", "永続メモリの状態", "영구 메모리 상태", "estado de memoria persistente", "état de la mémoire persistante"),
	catalog(MsgUpdateUsageEffort, "usage: /effort [default|low|medium|high|max|auto]", "用法：/effort [default|low|medium|high|max|auto]", "使用法: /effort [default|low|medium|high|max|auto]", "사용법: /effort [default|low|medium|high|max|auto]", "uso: /effort [default|low|medium|high|max|auto]", "utilisation : /effort [default|low|medium|high|max|auto]"),
	catalog(MsgUpdateEffortChanged, "reasoning effort {effort}", "推理强度 {effort}", "推論強度 {effort}", "추론 강도 {effort}", "esfuerzo de razonamiento {effort}", "effort de raisonnement {effort}"),
	catalog(MsgUpdateUsageMemory, "usage: /memory status|list|search <query>|read [target]|verify [target] [revision]|rollback <target> <revision> <expected> <idempotency> --yes|handoff <session> <target> <expected|-> <idempotency> --yes", "用法：/memory status|list|search <查询>|read [目标]|verify [目标] [版本]|rollback <目标> <版本> <预期版本> <幂等键> --yes|handoff <会话> <目标> <预期版本|-> <幂等键> --yes", "使用法: /memory status|list|search <検索>|read [target]|verify [target] [revision]|rollback <target> <revision> <expected> <idempotency> --yes|handoff <session> <target> <expected|-> <idempotency> --yes", "사용법: /memory status|list|search <검색>|read [target]|verify [target] [revision]|rollback <target> <revision> <expected> <idempotency> --yes|handoff <session> <target> <expected|-> <idempotency> --yes", "uso: /memory status|list|search <consulta>|read [objetivo]|verify [objetivo] [revisión]|rollback <objetivo> <revisión> <esperada> <idempotencia> --yes|handoff <sesión> <objetivo> <esperada|-> <idempotencia> --yes", "utilisation : /memory status|list|search <requête>|read [cible]|verify [cible] [révision]|rollback <cible> <révision> <attendue> <idempotence> --yes|handoff <session> <cible> <attendue|-> <idempotence> --yes"),
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
	catalog(MsgSessionPickerForkTask, "at {task}", "于 {task}", "{task} 時点", "{task}에서", "en {task}", "à {task}"),
	catalog(MsgSessionStatusActive, "active", "活跃", "アクティブ", "활성", "activa", "active"),
	catalog(MsgSessionStatusPaused, "paused", "已暂停", "一時停止", "일시 중지", "pausada", "en pause"),
	catalog(MsgSessionStatusClosed, "closed", "已关闭", "終了", "종료", "cerrada", "fermée"),
	catalog(MsgSessionAgeNow, "just now", "刚刚", "たった今", "방금", "ahora", "à l’instant"),
	catalog(MsgSessionAgeMinutes, "{count}m ago", "{count} 分钟前", "{count}分前", "{count}분 전", "hace {count} min", "il y a {count} min"),
	catalog(MsgSessionAgeHours, "{count}h ago", "{count} 小时前", "{count}時間前", "{count}시간 전", "hace {count} h", "il y a {count} h"),
	catalog(MsgSessionAgeDays, "{count}d ago", "{count} 天前", "{count}日前", "{count}일 전", "hace {count} d", "il y a {count} j"),
	catalog(MsgSessionRenameUsage, "usage: /rename <name>", "用法：/rename <名称>", "使用法: /rename <名前>", "사용법: /rename <이름>", "uso: /rename <nombre>", "utilisation : /rename <nom>"),
	catalog(MsgSessionRenameFailed, "Unable to rename session: {error}", "无法重命名会话：{error}", "セッション名を変更できません: {error}", "세션 이름 변경 실패: {error}", "No se pudo renombrar la sesión: {error}", "Impossible de renommer la session : {error}"),
	catalog(MsgSessionRenamed, "Session renamed to {name}.", "会话已重命名为 {name}。", "セッション名を {name} に変更しました。", "세션 이름을 {name}(으)로 변경했습니다.", "Sesión renombrada a {name}.", "Session renommée en {name}."),
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
	catalog(MsgSessionActionResolving, "Session action in progress; wait for its result.", "会话操作正在进行；请等待结果。", "セッション操作の完了を待ってください。", "세션 작업이 진행 중입니다. 결과를 기다리세요.", "Acción de sesión en curso; espera el resultado.", "Action de session en cours ; attendez le résultat."),
	catalog(MsgSessionSwitchRecover, "Connection failed: {error} · r retries · b returns", "连接失败：{error} · r 重试 · b 返回", "接続失敗: {error} · r 再試行 · b 戻る", "연결 실패: {error} · r 재시도 · b 돌아가기", "Falló la conexión: {error} · r reintenta · b vuelve", "Échec de connexion : {error} · r réessaie · b revient"),
	catalog(MsgUpdateAgents, "agents", "Agent", "Agent", "Agent", "Agents", "Agents"),
	catalog(MsgUpdateUsageResume, "usage: /task-resume [task_id]", "用法：/task-resume [task_id]", "使用法: /task-resume [task_id]", "사용법: /task-resume [task_id]", "uso: /task-resume [task_id]", "utilisation : /task-resume [task_id]"),
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

	catalog(MsgSettingsTitle, "Settings", "设置", "設定", "설정", "Ajustes", "Réglages"),
	catalog(MsgSettingsFooter, "[{close}] close · ←/→ tabs · ↑/↓ select · Enter run", "[{close}] 关闭 · ←/→ 标签 · ↑/↓ 选择 · Enter 执行", "[{close}] 閉じる · ←/→ タブ · ↑/↓ 選択 · Enter 実行", "[{close}] 닫기 · ←/→ 탭 · ↑/↓ 선택 · Enter 실행", "[{close}] cerrar · ←/→ pestañas · ↑/↓ elegir · Enter ejecutar", "[{close}] fermer · ←/→ onglets · ↑/↓ choisir · Entrée exécuter"),
	catalog(MsgSettingsTabOverview, "Overview", "概览", "概要", "개요", "Resumen", "Aperçu"),
	catalog(MsgSettingsTabMode, "Mode", "模式", "モード", "모드", "Modo", "Mode"),
	catalog(MsgSettingsTabModel, "Model", "模型", "モデル", "모델", "Modelo", "Modèle"),
	catalog(MsgSettingsTabExtensions, "Extensions", "扩展", "拡張", "확장", "Extensiones", "Extensions"),
	catalog(MsgSettingsRowSession, "session: {session}", "会话：{session}", "セッション: {session}", "세션: {session}", "sesión: {session}", "session : {session}"),
	catalog(MsgSettingsRowMode, "mode: {mode}", "模式：{mode}", "モード: {mode}", "모드: {mode}", "modo: {mode}", "mode : {mode}"),
	catalog(MsgSettingsRowModel, "model: {model} · effort: {effort}", "模型：{model} · 强度：{effort}", "モデル: {model} · 強度: {effort}", "모델: {model} · 강도: {effort}", "modelo: {model} · esfuerzo: {effort}", "modèle : {model} · effort : {effort}"),
	catalog(MsgSettingsRowProfile, "profile: {profile}", "权限配置：{profile}", "プロファイル: {profile}", "프로필: {profile}", "perfil: {profile}", "profil : {profile}"),
	catalog(MsgSettingsRowSandbox, "sandbox: {sandbox}", "沙箱：{sandbox}", "サンドボックス: {sandbox}", "샌드박스: {sandbox}", "sandbox: {sandbox}", "sandbox : {sandbox}"),
	catalog(MsgSettingsRowApproval, "interactive approval: {approval}", "交互审批：{approval}", "対話承認: {approval}", "대화형 승인: {approval}", "aprobación interactiva: {approval}", "approbation interactive : {approval}"),
	catalog(MsgSettingsRowContext, "context: {context}", "上下文：{context}", "コンテキスト: {context}", "컨텍스트: {context}", "contexto: {context}", "contexte : {context}"),
	catalog(MsgSettingsRowCompact, "compact UI: {state}", "紧凑界面：{state}", "コンパクト UI: {state}", "컴팩트 UI: {state}", "UI compacta: {state}", "UI compacte : {state}"),
	catalog(MsgSettingsActionRefresh, "Refresh runtime status", "刷新运行时状态", "ランタイム状態を更新", "런타임 상태 새로고침", "Actualizar estado", "Actualiser l’état"),
	catalog(MsgSettingsActionContext, "Show context usage", "显示上下文用量", "コンテキスト使用量を表示", "컨텍스트 사용량 표시", "Mostrar contexto", "Afficher le contexte"),
	catalog(MsgSettingsActionUsage, "Show usage and cost", "显示用量与成本", "使用量とコストを表示", "사용량 및 비용 표시", "Mostrar uso y coste", "Afficher l’usage et le coût"),
	catalog(MsgSettingsActionCompactMode, "Toggle compact UI", "切换紧凑界面", "コンパクト UI を切替", "컴팩트 UI 전환", "Alternar UI compacta", "Basculer l’UI compacte"),
	catalog(MsgSettingsActionModelPicker, "Open model picker", "打开模型选择器", "モデル選択を開く", "모델 선택기 열기", "Abrir selector de modelo", "Ouvrir le sélecteur de modèle"),
	catalog(MsgSettingsActionPlan, "Switch to plan mode", "切换到计划模式", "プランモードへ", "계획 모드로 전환", "Cambiar a modo plan", "Passer en mode plan"),
	catalog(MsgSettingsActionBuild, "Switch to build mode", "切换到构建模式", "ビルドモードへ", "빌드 모드로 전환", "Cambiar a modo build", "Passer en mode build"),
	catalog(MsgSettingsActionPermissions, "Inspect permissions", "查看权限", "権限を確認", "권한 확인", "Inspeccionar permisos", "Inspecter les autorisations"),
	catalog(MsgSettingsActionSafeEdit, "New safe-edit session", "新建 safe-edit 会话", "safe-edit セッションを新規作成", "safe-edit 세션 새로 만들기", "Nueva sesión safe-edit", "Nouvelle session safe-edit"),
	catalog(MsgSettingsActionFullWorkspace, "New full-workspace session", "新建 full-workspace 会话", "full-workspace セッションを新規作成", "full-workspace 세션 새로 만들기", "Nueva sesión full-workspace", "Nouvelle session full-workspace"),
	catalog(MsgSettingsActionEffort, "Change reasoning effort", "更改推理强度", "推論強度を変更", "추론 강도 변경", "Cambiar esfuerzo", "Changer l’effort"),
	catalog(MsgSettingsActionKeymap, "Edit keybindings", "编辑按键绑定", "キーバインドを編集", "키 바인딩 편집", "Editar atajos", "Modifier les raccourcis"),
	catalog(MsgSettingsActionSkills, "List skills", "列出技能", "スキル一覧", "기술 목록", "Listar skills", "Lister les skills"),
	catalog(MsgSettingsActionHooks, "List hooks", "列出 Hooks", "フック一覧", "훅 목록", "Listar hooks", "Lister les hooks"),
	catalog(MsgSettingsActionMCP, "List MCP servers", "列出 MCP 服务", "MCP サーバー一覧", "MCP 서버 목록", "Listar MCP", "Lister les MCP"),
	catalog(MsgSettingsActionExtensions, "List extensions", "列出扩展", "拡張一覧", "확장 목록", "Listar extensiones", "Lister les extensions"),
	catalog(MsgSettingsActionDoctor, "Run doctor", "运行诊断", "doctor を実行", "doctor 실행", "Ejecutar doctor", "Lancer doctor"),
	catalog(MsgContextSummaryHeader, "Context window", "上下文窗口", "コンテキスト窓", "컨텍스트 창", "Ventana de contexto", "Fenêtre de contexte"),
	catalog(MsgContextSource, "source: {source}", "来源：{source}", "ソース: {source}", "출처: {source}", "origen: {source}", "source : {source}"),
	catalog(MsgContextRemaining, "remaining: {remaining} tokens", "剩余：{remaining} token", "残り: {remaining} トークン", "남은 토큰: {remaining}", "restantes: {remaining} tokens", "restants : {remaining} jetons"),
	catalog(MsgContextUnavailable, "context tokens unavailable: {reason}", "上下文 token 不可用：{reason}", "コンテキストトークン利用不可: {reason}", "컨텍스트 토큰 사용 불가: {reason}", "tokens de contexto no disponibles: {reason}", "jetons de contexte indisponibles : {reason}"),
	catalog(MsgContextCompactReady, "compact available for checkpoint {checkpoint}", "检查点 {checkpoint} 可压缩", "チェックポイント {checkpoint} を圧縮可能", "체크포인트 {checkpoint} 압축 가능", "compactación disponible para {checkpoint}", "compaction disponible pour {checkpoint}"),
	catalog(MsgContextCompactBlocked, "compact unavailable: {reason}", "无法压缩：{reason}", "圧縮不可: {reason}", "압축 불가: {reason}", "compactación no disponible: {reason}", "compaction indisponible : {reason}"),
	catalog(MsgOperationalDetails, "details:", "详情：", "詳細:", "세부정보:", "detalles:", "détails :"),
	catalog(MsgConfigSummaryHeader, "Runtime configuration", "运行时配置", "ランタイム設定", "런타임 구성", "Configuración del runtime", "Configuration runtime"),
	catalog(MsgConfigHintSettings, "Tip: /settings opens the control shell; /config raw dumps inventory.", "提示：/settings 打开控制面板；/config raw 输出完整清单。", "ヒント: /settings で制御シェル、/config raw で一覧出力。", "팁: /settings는 제어 셸, /config raw는 전체 목록.", "Consejo: /settings abre el panel; /config raw vuelca el inventario.", "Astuce : /settings ouvre le panneau ; /config raw affiche l’inventaire."),
	catalog(MsgPermissionsSummaryHeader, "Permissions", "权限", "権限", "권한", "Permisos", "Autorisations"),
	catalog(MsgPermissionsProfile, "profile: {profile}", "配置：{profile}", "プロファイル: {profile}", "프로필: {profile}", "perfil: {profile}", "profil : {profile}"),
	catalog(MsgPermissionsSource, "source: {source}", "来源：{source}", "ソース: {source}", "출처: {source}", "origen: {source}", "source : {source}"),
	catalog(MsgPermissionsChoices, "governed session choices:", "受治理会话选项：", "管理セッションの選択肢:", "통제 세션 선택지:", "opciones de sesión gobernada:", "choix de session gouvernée :"),
	catalog(MsgSessionStatusHeader, "Session", "会话", "セッション", "세션", "Sesión", "Session"),
	catalog(MsgTasksTitle, "Tasks & queue", "任务与队列", "タスクと待機列", "작업 및 대기열", "Tareas y cola", "Tâches et file"),
	catalog(MsgTasksEmpty, "No active tasks.", "没有活动任务。", "実行中のタスクはありません。", "활성 작업이 없습니다.", "No hay tareas activas.", "Aucune tâche active."),
	catalog(MsgUpdateExportDone, "exported transcript to {path}", "已导出记录到 {path}", "履歴を {path} に書き出しました", "기록을 {path}에 내보냄", "historial exportado a {path}", "historique exporté vers {path}"),
	catalog(MsgUpdateUsageRemember, "usage: /remember <note>", "用法：/remember <笔记>", "使用法: /remember <メモ>", "사용법: /remember <메모>", "uso: /remember <nota>", "utilisation : /remember <note>"),
	catalog(MsgUpdateInitExists, "AGENTS.md already exists at {path}", "AGENTS.md 已存在：{path}", "AGENTS.md は既にあります: {path}", "AGENTS.md가 이미 있음: {path}", "AGENTS.md ya existe en {path}", "AGENTS.md existe déjà : {path}"),
	catalog(MsgUpdateInitCreated, "created {path}", "已创建 {path}", "{path} を作成しました", "{path} 생성됨", "creado {path}", "créé {path}"),
	catalog(MsgUpdateCompactMode, "compact UI {state}", "紧凑界面 {state}", "コンパクト UI {state}", "컴팩트 UI {state}", "UI compacta {state}", "UI compacte {state}"),
	catalog(MsgUpdateUsageBtw, "usage: /btw [--fork|-f] <question>  or  /side <question>", "用法：/btw [--fork|-f] <问题>  或  /side <问题>", "使用法: /btw [--fork|-f] <質問>  または  /side <質問>", "사용법: /btw [--fork|-f] <질문>  또는  /side <질문>", "uso: /btw [--fork|-f] <pregunta>  o  /side <pregunta>", "utilisation : /btw [--fork|-f] <question>  ou  /side <question>"),
	catalog(MsgViewPlanTitle, "Plan mode", "计划模式", "プランモード", "계획 모드", "Modo plan", "Mode plan"),
	catalog(MsgViewPlanMode, "current mode: {mode}", "当前模式：{mode}", "現在のモード: {mode}", "현재 모드: {mode}", "modo actual: {mode}", "mode actuel : {mode}"),
	catalog(MsgViewPlanActive, "Plan mode is ON. Edits, shell, and memory writes stay blocked until you approve the plan.", "计划模式已开启。在批准计划前，编辑、Shell 与记忆写入保持阻断。", "プランモード ON。承認まで編集・Shell・メモリ書き込みは遮断。", "계획 모드 ON. 승인 전 편집/Shell/메모리 쓰기 차단.", "Modo plan ON. Ediciones, shell y memoria bloqueados hasta aprobar.", "Mode plan ON. Éditions, shell et mémoire bloqués jusqu’à approbation."),
	catalog(MsgViewPlanInactive, "Plan mode is OFF (build). Use /plan to enter planning first.", "计划模式关闭（build）。使用 /plan 先做规划。", "プランモード OFF（build）。先に /plan で計画。", "계획 모드 OFF(build). /plan으로 먼저 계획.", "Modo plan OFF (build). Use /plan para planificar primero.", "Mode plan OFF (build). Utilisez /plan pour planifier d’abord."),
	catalog(MsgViewPlanHint, "Hint: run /approve-plan to exit plan mode and allow edits, or /build to leave without approving.", "提示：运行 /approve-plan 退出计划模式并允许编辑，或 /build 直接离开。", "ヒント: /approve-plan で計画承認、/build で退出。", "팁: /approve-plan으로 승인하거나 /build로 종료.", "Consejo: use /approve-plan o /build.", "Astuce : utilisez /approve-plan ou /build."),

	catalog(MsgSettingsActionApprovePlan, "Approve plan (exit plan mode)", "批准计划（退出计划模式）", "計画を承認（プランモード終了）", "계획 승인(계획 모드 종료)", "Aprobar plan (salir de plan)", "Approuver le plan (quitter le mode plan)"),
	catalog(MsgSettingsActionViewPlan, "View plan file", "查看计划文件", "計画ファイルを表示", "계획 파일 보기", "Ver archivo de plan", "Voir le fichier de plan"),
	catalog(MsgSettingsActionExplain, "Explain sandbox and permissions", "解释沙箱与权限", "サンドボックスと権限を説明", "샌드박스 및 권한 설명", "Explicar sandbox y permisos", "Expliquer sandbox et autorisations"),
	catalog(MsgSettingsActionInspect, "Run readiness inspect", "运行就绪检查", "準備状態を検査", "준비 상태 검사", "Inspeccionar preparación", "Inspecter l’état de préparation"),
	catalog(MsgViewPlanPath, "plan file: {path}", "计划文件：{path}", "計画ファイル: {path}", "계획 파일: {path}", "archivo de plan: {path}", "fichier de plan : {path}"),
	catalog(MsgViewPlanEmpty, "Plan file exists but is empty.", "计划文件存在但为空。", "計画ファイルは空です。", "계획 파일이 비어 있습니다.", "El archivo de plan está vacío.", "Le fichier de plan est vide."),
	catalog(MsgViewPlanMissing, "No plan file yet. /plan scaffolds one under .carina/plans/.", "尚无计划文件。/plan 会在 .carina/plans/ 下创建脚手架。", "計画ファイルはまだありません。/plan で .carina/plans/ に作成。", "계획 파일 없음. /plan이 .carina/plans/에 생성합니다.", "Aún no hay plan. /plan crea uno en .carina/plans/.", "Pas encore de plan. /plan en crée un sous .carina/plans/."),
	catalog(MsgViewPlanPreview, "preview:", "预览：", "プレビュー:", "미리보기:", "vista previa:", "aperçu :"),
	catalog(MsgUpdateBtwStarted, "side Q&A turn (answer-only; not a session fork)", "侧问轮次（仅回答；非会话分叉）", "脇質問ターン（回答のみ・セッション分岐なし）", "곁질문 턴(답변 전용, 세션 포크 아님)", "turno lateral (solo respuesta; no es un fork)", "tour latéral (réponse seule ; pas de fork)"),
	catalog(MsgExplainTitle, "Runtime explanation", "运行时说明", "ランタイム説明", "런타임 설명", "Explicación del runtime", "Explication du runtime"),
	catalog(MsgExplainMode, "interaction mode: {mode}", "交互模式：{mode}", "対話モード: {mode}", "상호작용 모드: {mode}", "modo de interacción: {mode}", "mode d’interaction : {mode}"),
	catalog(MsgExplainProfile, "permission profile: {profile}", "权限配置：{profile}", "権限プロファイル: {profile}", "권한 프로필: {profile}", "perfil de permisos: {profile}", "profil d’autorisation : {profile}"),
	catalog(MsgExplainSandbox, "OS command sandbox: {sandbox}", "OS 命令沙箱：{sandbox}", "OS コマンドサンドボックス: {sandbox}", "OS 명령 샌드박스: {sandbox}", "sandbox de comandos OS: {sandbox}", "sandbox des commandes OS : {sandbox}"),
	catalog(MsgExplainApproval, "interactive tool approval: {approval}", "交互工具审批：{approval}", "対話ツール承認: {approval}", "대화형 도구 승인: {approval}", "aprobación interactiva de herramientas: {approval}", "approbation interactive des outils : {approval}"),
	catalog(MsgExplainSandboxWhy, "Sandbox isolates agent shell commands when enabled by daemon policy. It is not the same as plan mode (which blocks edits until /approve-plan).", "沙箱在 daemon 策略启用时隔离 Agent 的 Shell 命令。它不同于计划模式（计划模式在 /approve-plan 前阻止编辑）。", "サンドボックスは daemon 方針で有効なとき Agent の Shell を隔離します。プランモード（/approve-plan まで編集遮断）とは別です。", "샌드박스는 daemon 정책이 켜져 있을 때 Agent 셸을 격리합니다. 계획 모드(/approve-plan 전 편집 차단)와는 다릅니다.", "El sandbox aísla el shell del Agent cuando lo exige el daemon. No es el modo plan (bloquea ediciones hasta /approve-plan).", "Le sandbox isole le shell de l’Agent si la politique du daemon l’exige. Ce n’est pas le mode plan (bloque les éditions jusqu’à /approve-plan)."),
	catalog(MsgExplainHowToChange, "Change profile with /permissions new <safe-edit|full-workspace> [--yes]. Toggle plan with /plan or Shift+Tab. Sandbox is daemon-level (not a silent per-turn YOLO).", "用 /permissions new <safe-edit|full-workspace> [--yes] 更改配置。用 /plan 或 Shift+Tab 切换计划模式。沙箱是 daemon 级策略（不是静默的每轮 YOLO）。", "プロファイルは /permissions new …。プランは /plan または Shift+Tab。サンドボックスは daemon 方針です。", "프로필은 /permissions new …, 계획 모드는 /plan 또는 Shift+Tab. 샌드박스는 daemon 정책입니다.", "Cambie el perfil con /permissions new …. Plan con /plan o Shift+Tab. Sandbox a nivel daemon.", "Profil via /permissions new …. Plan via /plan ou Shift+Tab. Sandbox au niveau daemon."),
	catalog(MsgInspectHeader, "Readiness inspect", "就绪检查", "準備検査", "준비 검사", "Inspección de preparación", "Inspection de préparation"),
	catalog(MsgInspectHint, "Next: /settings for control shell, /model to pick a model, /explain for sandbox/permissions.", "下一步：/settings 打开控制面板，/model 选择模型，/explain 查看沙箱与权限。", "次: /settings、/model、/explain。", "다음: /settings, /model, /explain.", "Siguiente: /settings, /model, /explain.", "Ensuite : /settings, /model, /explain."),
	catalog(MsgTasksLoopHint, "Loops: /loop list  ·  cancel task: Esc while running", "循环任务：/loop list  ·  取消任务：运行中按 Esc", "ループ: /loop list  ·  取消: 実行中 Esc", "루프: /loop list  ·  취소: 실행 중 Esc", "Bucles: /loop list  ·  cancelar: Esc en ejecución", "Boucles : /loop list  ·  annuler : Échap pendant l’exécution"),
	catalog(MsgTasksLoopsHeader, "scheduled loops", "定时循环", "スケジュール済みループ", "예약된 루프", "bucles programados", "boucles planifiées"),
	catalog(MsgUpdateUsageExtension, "usage: /extension <enable|disable> <name>", "用法：/extension <enable|disable> <名称>", "使用法: /extension <enable|disable> <name>", "사용법: /extension <enable|disable> <name>", "uso: /extension <enable|disable> <nombre>", "utilisation : /extension <enable|disable> <nom>"),

	catalog(MsgContextPressureWarning, "context pressure {percent}% — consider /compact when a paused checkpoint is available", "上下文压力 {percent}% — 有暂停检查点时可用 /compact", "コンテキスト負荷 {percent}% — 一時停止チェックポイントがあれば /compact", "컨텍스트 부하 {percent}% — 일시 중지 체크포인트가 있으면 /compact", "presión de contexto {percent}% — use /compact si hay checkpoint pausado", "pression contexte {percent}% — utilisez /compact si un checkpoint est en pause"),
	catalog(MsgContextPressureCritical, "context critical {percent}% — compact unavailable: {reason}", "上下文危急 {percent}% — 无法压缩：{reason}", "コンテキスト危険 {percent}% — 圧縮不可: {reason}", "컨텍스트 위험 {percent}% — 압축 불가: {reason}", "contexto crítico {percent}% — compactación no disponible: {reason}", "contexte critique {percent}% — compaction indisponible : {reason}"),
	catalog(MsgContextAutoCompact, "context {percent}% — auto-compacting paused checkpoint {checkpoint}", "上下文 {percent}% — 正在自动压缩暂停检查点 {checkpoint}", "コンテキスト {percent}% — 一時停止チェックポイント {checkpoint} を自動圧縮", "컨텍스트 {percent}% — 일시 중지 체크포인트 {checkpoint} 자동 압축", "contexto {percent}% — compactando automáticamente {checkpoint}", "contexte {percent}% — compaction auto de {checkpoint}"),
	catalog(MsgUpdateBtwForkStart, "forking session for side Q&A…", "正在分叉会话以进行侧问…", "脇質問のためセッションをフォーク中…", "곁질문을 위해 세션 포크 중…", "bifurcando sesión para pregunta lateral…", "bifurcation de session pour question latérale…"),
	catalog(MsgUpdateBtwForkReady, "side Q&A on forked session", "已在分叉会话上开始侧问", "フォーク先で脇質問を開始", "포크된 세션에서 곁질문 시작", "pregunta lateral en sesión bifurcada", "question latérale sur session bifurquée"),
	catalog(MsgUpdateBtwForkBusy, "cannot /btw --fork while a task is running; wait or use /btw without --fork", "任务运行中无法 /btw --fork；请等待或使用不带 --fork 的 /btw", "タスク実行中は /btw --fork 不可。完了を待つか --fork なしで。", "작업 실행 중에는 /btw --fork 불가. 대기하거나 --fork 없이 사용.", "no se puede /btw --fork con una tarea en curso", "impossible d’utiliser /btw --fork pendant une tâche"),

	catalog(MsgAlwaysApproveEnabled, "always-approve ON", "always-approve 已开启", "always-approve ON", "always-approve 켜짐", "always-approve ACTIVADO", "always-approve ACTIVÉ"),
	catalog(MsgAlwaysApproveDisabled, "always-approve OFF — back to ask mode", "always-approve 已关闭 — 恢复为 ask", "always-approve OFF — ask に戻る", "always-approve 꺼짐 — ask 모드", "always-approve DESACTIVADO — modo ask", "always-approve DÉSACTIVÉ — mode ask"),
	catalog(MsgAlwaysApproveWarning, "WARNING: tools with requires_approval will auto-run. Deny rules, plan mode, and OS sandbox still apply. Use /always-approve off to restore prompts.", "警告：requires_approval 工具将自动执行。拒绝规则、计划模式与 OS 沙箱仍然有效。使用 /always-approve off 恢复确认。", "警告: requires_approval ツールは自動実行されます。deny・プランモード・サンドボックスは有効。/always-approve off で確認に戻す。", "경고: requires_approval 도구가 자동 실행됩니다. deny/계획 모드/샌드박스는 유지. /always-approve off로 복원.", "AVISO: las herramientas requires_approval se ejecutan solas. Deny, plan y sandbox siguen. /always-approve off restaura.", "AVERTISSEMENT : les outils requires_approval s’exécutent seuls. Deny, plan et sandbox restent. /always-approve off pour rétablir."),
	catalog(MsgUpdateUsageAlwaysApprove, "usage: /always-approve [on|off|toggle]", "用法：/always-approve [on|off|toggle]", "使用法: /always-approve [on|off|toggle]", "사용법: /always-approve [on|off|toggle]", "uso: /always-approve [on|off|toggle]", "utilisation : /always-approve [on|off|toggle]"),
	catalog(MsgDontAskEnabled, "dont-ask ON — requires_approval denied without exact grant", "dont-ask 已开启 — 无精确授权则拒绝 requires_approval", "dont-ask ON — 正確な grant なしでは requires_approval を拒否", "dont-ask 켜짐 — 정확한 grant 없으면 requires_approval 거부", "dont-ask ACTIVADO — se deniega requires_approval sin grant exacto", "dont-ask ACTIVÉ — requires_approval refusé sans grant exact"),
	catalog(MsgDontAskWarning, "WARNING: tools needing approval are denied unless a session/project grant already exists. No operator prompt. Use /approval-mode ask to restore prompts.", "警告：需要审批的工具在无 session/project 授权时将被拒绝，不会弹窗。使用 /approval-mode ask 恢复确认。", "警告: 承認が必要なツールは grant が無いと拒否され、確認しません。/approval-mode ask で確認に戻す。", "경고: 승인이 필요한 도구는 grant가 없으면 거부되며 확인하지 않습니다. /approval-mode ask로 복원.", "AVISO: sin grant session/project se deniegan; sin prompt. /approval-mode ask restaura.", "AVERTISSEMENT : sans grant session/project, refus sans invite. /approval-mode ask pour rétablir."),
	catalog(MsgApprovalModeAsk, "approval mode: ask — requires_approval pauses for operator", "审批模式：ask — requires_approval 等待操作者", "承認モード: ask — requires_approval はオペレーター待ち", "승인 모드: ask — requires_approval는 운영자 대기", "modo aprobación: ask — requires_approval espera al operador", "mode d’approbation : ask — requires_approval attend l’opérateur"),
	catalog(MsgApprovalModeCurrent, "approval mode: {{mode}}  (use /approval-mode ask|always-approve|dont-ask)", "审批模式：{{mode}}（/approval-mode ask|always-approve|dont-ask）", "承認モード: {{mode}}（/approval-mode ask|always-approve|dont-ask）", "승인 모드: {{mode}} (/approval-mode ask|always-approve|dont-ask)", "modo aprobación: {{mode}} (/approval-mode ask|always-approve|dont-ask)", "mode d’approbation : {{mode}} (/approval-mode ask|always-approve|dont-ask)"),
	catalog(MsgUpdateUsageApprovalMode, "usage: /approval-mode [ask|always-approve|dont-ask|accept-edits]", "用法：/approval-mode [ask|always-approve|dont-ask|accept-edits]", "使用法: /approval-mode [ask|always-approve|dont-ask|accept-edits]", "사용법: /approval-mode [ask|always-approve|dont-ask|accept-edits]", "uso: /approval-mode [ask|always-approve|dont-ask|accept-edits]", "utilisation : /approval-mode [ask|always-approve|dont-ask|accept-edits]"),
	catalog(MsgUpdateUsageDontAsk, "usage: /dont-ask [on|off|toggle]", "用法：/dont-ask [on|off|toggle]", "使用法: /dont-ask [on|off|toggle]", "사용법: /dont-ask [on|off|toggle]", "uso: /dont-ask [on|off|toggle]", "utilisation : /dont-ask [on|off|toggle]"),
	catalog(MsgAcceptEditsEnabled, "accept-edits ON — file edits auto-run; shell still prompts", "accept-edits 已开启 — 文件编辑自动执行；Shell 仍需确认", "accept-edits ON — ファイル編集は自動、Shell は確認", "accept-edits 켜짐 — 파일 편집 자동, 셸은 확인", "accept-edits ACTIVADO — ediciones auto; shell pide", "accept-edits ACTIVÉ — éditions auto ; shell demande"),
	catalog(MsgAcceptEditsWarning, "WARNING: FileWrite/PatchApply with requires_approval auto-run. Shell, network, secrets still prompt. Deny rules, plan mode, and sandbox still apply.", "警告：FileWrite/PatchApply 的 requires_approval 将自动执行。Shell、网络与 secret 仍需确认。拒绝规则、计划模式与沙箱仍有效。", "警告: FileWrite/PatchApply の requires_approval は自動。Shell・ネットワーク・secret は確認。deny・プラン・サンドボックスは有効。", "경고: FileWrite/PatchApply requires_approval 자동. 셸/네트워크/시크릿은 확인. deny/계획/샌드박스 유지.", "AVISO: FileWrite/PatchApply se auto-ejecutan. Shell/red/secretos piden. Deny/plan/sandbox siguen.", "AVERTISSEMENT : FileWrite/PatchApply auto. Shell/réseau/secrets demandent. Deny/plan/sandbox restent."),
	catalog(MsgUpdateUsageAcceptEdits, "usage: /accept-edits [on|off|toggle]", "用法：/accept-edits [on|off|toggle]", "使用法: /accept-edits [on|off|toggle]", "사용법: /accept-edits [on|off|toggle]", "uso: /accept-edits [on|off|toggle]", "utilisation : /accept-edits [on|off|toggle]"),
	catalog(MsgPlanReviewTitle, "Plan review", "计划审阅", "プラン審査", "계획 검토", "Revisión del plan", "Revue du plan"),
	catalog(MsgPlanReviewFooter, "a approve  ·  s request changes  ·  q quit plan  ·  esc close  ·  j/k scroll", "a 批准  ·  s 请求修改  ·  q 退出计划  ·  esc 关闭  ·  j/k 滚动", "a 承認  ·  s 修正依頼  ·  q プラン終了  ·  esc 閉じる  ·  j/k スクロール", "a 승인  ·  s 수정 요청  ·  q 계획 종료  ·  esc 닫기  ·  j/k 스크롤", "a aprobar  ·  s pedir cambios  ·  q salir del plan  ·  esc cerrar  ·  j/k", "a approuver  ·  s demander des changements  ·  q quitter le plan  ·  esc fermer  ·  j/k"),
	catalog(MsgPlanReviewBusy, "Working…", "处理中…", "処理中…", "처리 중…", "Trabajando…", "Traitement…"),
	catalog(MsgPlanReviewBusyBlocked, "Close the other overlay first, then run /view-plan.", "请先关闭其他浮层，再运行 /view-plan。", "先に他のオーバーレイを閉じてから /view-plan。", "다른 오버레이를 닫은 뒤 /view-plan.", "Cierre el otro overlay y use /view-plan.", "Fermez l’autre overlay puis /view-plan."),
	catalog(MsgPlanReviewReviseSeed, "Please revise the plan: ", "请修订计划：", "プランを改訂してください: ", "계획을 수정해 주세요: ", "Revise el plan: ", "Veuillez réviser le plan : "),
	catalog(MsgPlanReviewRequestChanges, "Composer seeded for plan revisions. Send when ready.", "已填入修订草稿，编辑后发送。", "修正用の下書きを入力欄に入れました。", "수정 초안을 입력창에 넣었습니다.", "Borrador de revisión listo en el compositor.", "Brouillon de révision prêt dans le compositeur."),
	catalog(MsgPlanReviewApproved, "{glyph} Plan approved — plan mode off; implementation may proceed", "{glyph} 计划已批准 — 计划模式关闭，可开始实施", "{glyph} プラン承認 — プランモード終了、実装可能", "{glyph} 계획 승인 — 계획 모드 종료, 구현 가능", "{glyph} Plan aprobado — modo plan off", "{glyph} Plan approuvé — mode plan désactivé"),
	catalog(MsgPlanReviewQuit, "{glyph} Left plan mode without approving", "{glyph} 已退出计划模式（未批准）", "{glyph} プランモードを承認せず終了", "{glyph} 승인 없이 계획 모드 종료", "{glyph} Salió del modo plan sin aprobar", "{glyph} Sortie du mode plan sans approbation"),
	catalog(MsgSettingsActionAlwaysApprove, "Toggle always-approve (with warning)", "切换 always-approve（带警告）", "always-approve を切替（警告あり）", "always-approve 전환(경고)", "Alternar always-approve (con aviso)", "Basculer always-approve (avec avertissement)"),
	catalog(MsgAgentsSummaryHeader, "Available agents", "可用 Agent", "利用可能な Agent", "사용 가능한 Agent", "Agents disponibles", "Agents disponibles"),
	catalog(MsgAgentsHint, "Tip: pass agent via task/model settings; use /settings for control shell.", "提示：任务可通过 agent 设置指定；/settings 打开控制面板。", "ヒント: タスクの agent 設定、/settings で制御シェル。", "팁: 작업 agent 설정 또는 /settings.", "Consejo: configure agent en la tarea; /settings abre el panel.", "Astuce : agent via les réglages de tâche ; /settings pour le panneau."),
	catalog(MsgExplainAlwaysApprove, "Approval mode: ask pauses on requires_approval; always-approve auto-allows those (WARNING). Toggle with /always-approve. Deny rules and plan mode still block.", "审批模式：ask 在 requires_approval 时暂停；always-approve 自动放行（有警告）。用 /always-approve 切换。拒绝规则与计划模式仍会阻断。", "承認: ask は requires_approval で停止、always-approve は自動許可（警告）。/always-approve で切替。deny とプランは有効。", "승인: ask는 requires_approval 시 대기, always-approve는 자동 허용(경고). /always-approve로 전환. deny/계획 모드 유지.", "Aprobación: ask pausa; always-approve auto-permite (AVISO). /always-approve. Deny y plan siguen.", "Approbation : ask met en pause ; always-approve auto-autorise (AVERTISSEMENT). /always-approve. Deny et plan restent."),
	catalog(MsgExplainApprovalModes, "Product HITL: ask | always-approve | dont-ask | accept-edits (/approval-mode). Org may set disable_always_approve. Separate session/kernel axis: untrusted|on_request|never on session create — not product approval_mode.", "产品 HITL：ask | always-approve | dont-ask | accept-edits（/approval-mode）。组织可设 disable_always_approve。会话/内核轴 untrusted|on_request|never 在 session 创建时设置，不是产品 approval_mode。", "製品 HITL: ask | always-approve | dont-ask | accept-edits。組織は disable_always_approve 可。セッション/kernel 軸 untrusted|on_request|never は session 作成時 — 製品 approval_mode ではない。", "제품 HITL: ask | always-approve | dont-ask | accept-edits. org는 disable_always_approve 가능. 세션/kernel 축 untrusted|on_request|never는 세션 생성용 — 제품 approval_mode 아님.", "HITL de producto: ask | always-approve | dont-ask | accept-edits. Org puede disable_always_approve. Eje sesión/kernel untrusted|on_request|never al crear sesión — no es approval_mode de producto.", "HITL produit : ask | always-approve | dont-ask | accept-edits. Org : disable_always_approve. Axe session/kernel untrusted|on_request|never à la création — pas le approval_mode produit."),
	catalog(MsgOperationalAgentsTitle, "agents", "agents", "agents", "agents", "agents", "agents"),
}
