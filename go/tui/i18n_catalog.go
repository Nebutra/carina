package tui

const (
	MsgPlaceholderInstruction MessageID = "composer.placeholder.instruction"
	MsgKeybindingsError       MessageID = "composer.keybindings.error"
	MsgSuggestFiles           MessageID = "suggest.files"
	MsgSuggestCommands        MessageID = "suggest.commands"
	MsgSuggestHeader          MessageID = "suggest.header"
	MsgPasteHeader            MessageID = "paste.header"
	MsgPasteEarlier           MessageID = "paste.earlier"
	MsgPasteBlankFirstLine    MessageID = "paste.blank_first_line"
	MsgPasteItem              MessageID = "paste.item"
	MsgPasteKindPaste         MessageID = "paste.kind.paste"
	MsgPasteKindRestored      MessageID = "paste.kind.restored"
	MsgQueueHeader            MessageID = "queue.header"
	MsgQueuePastedContent     MessageID = "queue.pasted_content"
	MsgQueuePasteItems        MessageID = "queue.paste_items"
	MsgQueueItem              MessageID = "queue.item"
	MsgReconnectAttempt       MessageID = "connection.reconnect_attempt"
	MsgConnecting             MessageID = "connection.connecting"
	MsgOverlayDisconnected    MessageID = "connection.overlay_disconnected"
	MsgStatusNotAttached      MessageID = "status.not_attached"
	MsgStatusSession          MessageID = "status.session"
	MsgStatusReady            MessageID = "status.ready"
	MsgStatusEditingDraft     MessageID = "status.editing_draft"
	MsgStatusSending          MessageID = "status.sending"
	MsgStatusRunning          MessageID = "status.running"
	MsgStatusNew              MessageID = "status.new"
	MsgStatusQueued           MessageID = "status.queued"
	MsgStatusAttention        MessageID = "status.attention"
	MsgStatusChord            MessageID = "status.chord"
	MsgStatusFooter           MessageID = "status.footer"
	MsgStatusGoal             MessageID = "status.goal"
	MsgStatusRunningModel     MessageID = "status.running_model"
	MsgStatusSettingsHint     MessageID = "status.settings_hint"
	MsgStatusHelpHint         MessageID = "status.help_hint"

	MsgHelpTitle                MessageID = "help.title"
	MsgHelpCommands             MessageID = "help.commands"
	MsgHelpKeybindings          MessageID = "help.keybindings"
	MsgHelpCloseScroll          MessageID = "help.close_scroll"
	MsgHelpCommandHelp          MessageID = "help.command.help"
	MsgHelpCommandEditor        MessageID = "help.command.editor"
	MsgHelpCommandCopy          MessageID = "help.command.copy"
	MsgHelpCommandTranscript    MessageID = "help.command.transcript"
	MsgHelpCommandKeymap        MessageID = "help.command.keymap"
	MsgHelpCommandAgents        MessageID = "help.command.agents"
	MsgHelpCommandCheckpoints   MessageID = "help.command.checkpoints"
	MsgHelpCommandResume        MessageID = "help.command.resume"
	MsgHelpCommandNew           MessageID = "help.command.new"
	MsgHelpCommandFork          MessageID = "help.command.fork"
	MsgHelpCommandTaskResume    MessageID = "help.command.task_resume"
	MsgHelpCommandSearch        MessageID = "help.command.search"
	MsgHelpCommandRecap         MessageID = "help.command.recap"
	MsgHelpCommandStatus        MessageID = "help.command.status"
	MsgHelpCommandPermissions   MessageID = "help.command.permissions"
	MsgHelpCommandContext       MessageID = "help.command.context"
	MsgHelpCommandCompact       MessageID = "help.command.compact"
	MsgHelpCommandConfig        MessageID = "help.command.config"
	MsgHelpCommandUsage         MessageID = "help.command.usage"
	MsgHelpCommandReview        MessageID = "help.command.review"
	MsgHelpCommandSessionReview MessageID = "help.command.session_review"
	MsgHelpCommandMemory        MessageID = "help.command.memory"
	MsgHelpCommandDoctor        MessageID = "help.command.doctor"
	MsgHelpCommandSkills        MessageID = "help.command.skills"
	MsgHelpCommandHooks         MessageID = "help.command.hooks"
	MsgHelpCommandExtensions    MessageID = "help.command.extensions"
	MsgHelpCommandDiff          MessageID = "help.command.diff"
	MsgHelpCommandMCP           MessageID = "help.command.mcp"
	MsgHelpCommandLoop          MessageID = "help.command.loop"
	MsgHelpCommandGoal          MessageID = "help.command.goal"
	MsgHelpCommandMode          MessageID = "help.command.mode"
	MsgHelpCommandModel         MessageID = "help.command.model"
	MsgHelpCommandEffort        MessageID = "help.command.effort"
	MsgHelpCommandShell         MessageID = "help.command.shell"
	MsgHelpCommandMention       MessageID = "help.command.mention"

	MsgApprovalRPCFailed    MessageID = "approval.rpc_failed"
	MsgApprovalRetry        MessageID = "approval.retry"
	MsgApprovalResource     MessageID = "approval.resource"
	MsgApprovalPolicy       MessageID = "approval.policy"
	MsgApprovalFooterWide   MessageID = "approval.footer.wide"
	MsgApprovalFooterMedium MessageID = "approval.footer.medium"
	MsgApprovalFooterNarrow MessageID = "approval.footer.narrow"
	MsgApprovalResolving    MessageID = "approval.resolving"
	MsgApprovalScroll       MessageID = "approval.scroll"
	MsgApprovalPolicyDetail MessageID = "approval.policy_detail"

	MsgQuestionTitle            MessageID = "question.title"
	MsgQuestionAnswerFailed     MessageID = "question.answer_failed"
	MsgQuestionAnswerLogFail    MessageID = "question.answer_log_failed"
	MsgQuestionAnswered         MessageID = "question.answered"
	MsgQuestionNoOptions        MessageID = "question.no_options"
	MsgQuestionCannotDismiss    MessageID = "question.cannot_dismiss"
	MsgQuestionFooterWide       MessageID = "question.footer.wide"
	MsgQuestionFooterMedium     MessageID = "question.footer.medium"
	MsgQuestionFooterNarrow     MessageID = "question.footer.narrow"
	MsgQuestionSending          MessageID = "question.sending"
	MsgQuestionScroll           MessageID = "question.scroll"
	MsgQuestionFreeTextHint     MessageID = "question.free_text_hint"
	MsgQuestionFreeTextFooter   MessageID = "question.free_text_footer"
	MsgQuestionFreeTextRequired MessageID = "question.free_text_required"

	MsgHistoryTypeToSearch   MessageID = "history.type_to_search"
	MsgHistoryMatch          MessageID = "history.match"
	MsgHistoryNoMatch        MessageID = "history.no_match"
	MsgHistoryLoading        MessageID = "history.loading"
	MsgHistoryLoadKept       MessageID = "history.load_kept"
	MsgHistoryLoadCleared    MessageID = "history.load_cleared"
	MsgHistoryWide           MessageID = "history.wide"
	MsgHistoryMedium         MessageID = "history.medium"
	MsgHistoryTiny           MessageID = "history.tiny"
	MsgHistoryScopeSession   MessageID = "history.scope.session"
	MsgHistoryScopeWorkspace MessageID = "history.scope.workspace"
	MsgHistoryScopeGlobal    MessageID = "history.scope.global"

	MsgAttentionApproval     MessageID = "attention.approval"
	MsgAttentionInput        MessageID = "attention.input"
	MsgAttentionTaskFinished MessageID = "attention.task_finished"
	MsgAttentionMemorySync   MessageID = "attention.memory_sync"
)

var baseCatalogRows = []catalogRow{
	catalog(MsgPlaceholderInstruction,
		"Type an instruction - {submit} submits, {newline} adds a line, {help} opens help",
		"输入指令 - {submit} 提交，{newline} 换行，{help} 打开帮助",
		"指示を入力 - {submit} で送信、{newline} で改行、{help} でヘルプ",
		"지시를 입력하세요 - {submit} 제출, {newline} 줄바꿈, {help} 도움말",
		"Escribe una instrucción: {submit} envía, {newline} añade una línea y {help} abre la ayuda",
		"Saisissez une instruction : {submit} envoie, {newline} ajoute une ligne et {help} ouvre l’aide"),
	catalog(MsgKeybindingsError, "Keybindings: {error}", "按键绑定：{error}", "キーバインド: {error}", "키 바인딩: {error}", "Atajos de teclado: {error}", "Raccourcis clavier : {error}"),
	catalog(MsgSuggestFiles, "files", "文件", "ファイル", "파일", "archivos", "fichiers"),
	catalog(MsgSuggestCommands, "commands", "命令", "コマンド", "명령", "comandos", "commandes"),
	catalog(MsgSuggestHeader, "{title} ({previous}/{next} select, {accept} complete, {dismiss} close)", "{title}（{previous}/{next} 选择，{accept} 补全，{dismiss} 关闭）", "{title}（{previous}/{next} 選択、{accept} 補完、{dismiss} 閉じる）", "{title} ({previous}/{next} 선택, {accept} 완성, {dismiss} 닫기)", "{title} ({previous}/{next} selecciona, {accept} completa, {dismiss} cierra)", "{title} ({previous}/{next} sélectionne, {accept} complète, {dismiss} ferme)"),
	catalog(MsgPasteHeader, "pasted draft items and restored turns ({undo} removes the latest)", "已粘贴的草稿项与恢复轮次（{undo} 移除最新一项）", "貼り付けた下書きと復元ターン（{undo} で最新を削除）", "붙여넣은 초안과 복원된 턴 ({undo}로 최신 항목 제거)", "Elementos pegados y turnos restaurados ({undo} elimina el último)", "Éléments collés et tours restaurés ({undo} supprime le dernier)"),
	{ID: MsgPasteEarlier, EN: "  ... {count} earlier items", ZH: "  ... 更早的 {count} 项", JA: "  ... 以前の {count} 件", KO: "  ... 이전 {count}개 항목", ES: "  ... {count} elementos anteriores", FR: "  ... {count} éléments précédents", ENOne: "  ... {count} earlier item", ZHOne: "  ... 更早的 {count} 项", JAOne: "  ... 以前の {count} 件", KOOne: "  ... 이전 {count}개 항목", ESOne: "  ... {count} elemento anterior", FROne: "  ... {count} élément précédent"},
	catalog(MsgPasteBlankFirstLine, "(blank first line)", "（首行为空）", "（先頭行は空白）", "(첫 줄 비어 있음)", "(primera línea vacía)", "(première ligne vide)"),
	catalog(MsgPasteKindPaste, "paste", "粘贴", "貼り付け", "붙여넣기", "pegado", "collé"),
	catalog(MsgPasteKindRestored, "restored", "已恢复", "復元", "복원됨", "restaurado", "restauré"),
	catalog(MsgPasteItem, "  [{index} {kind}] {lines} lines, {chars} chars: {summary}", "  [{index} {kind}] {lines} 行，{chars} 个字符：{summary}", "  [{index} {kind}] {lines} 行、{chars} 文字: {summary}", "  [{index} {kind}] {lines}줄, {chars}자: {summary}", "  [{index} {kind}] {lines} líneas, {chars} caracteres: {summary}", "  [{index} {kind}] {lines} lignes, {chars} caractères : {summary}"),
	{ID: MsgQueueHeader, EN: "queued follow-ups: {count} ({queue} queues, {edit} edits latest)", ZH: "排队中的后续指令：{count}（{queue} 入队，{edit} 编辑最新一项）", JA: "待機中のフォローアップ: {count}（{queue} で追加、{edit} で最新を編集）", KO: "대기 중인 후속 요청: {count} ({queue} 대기열 추가, {edit} 최신 항목 편집)", ES: "Seguimientos en cola: {count} ({queue} encola, {edit} edita el último)", FR: "Suites en file : {count} ({queue} ajoute, {edit} modifie la dernière)", ENOne: "queued follow-up: {count} ({queue} queues, {edit} edits it)", ZHOne: "排队中的后续指令：{count}（{queue} 入队，{edit} 编辑）", JAOne: "待機中のフォローアップ: {count}（{queue} で追加、{edit} で編集）", KOOne: "대기 중인 후속 요청: {count} ({queue} 대기열 추가, {edit} 편집)", ESOne: "Seguimiento en cola: {count} ({queue} encola, {edit} lo edita)", FROne: "Suite en file : {count} ({queue} ajoute, {edit} la modifie)"},
	catalog(MsgQueuePastedContent, "(pasted content)", "（粘贴内容）", "（貼り付け内容）", "(붙여넣은 내용)", "(contenido pegado)", "(contenu collé)"),
	{ID: MsgQueuePasteItems, EN: " +{count} paste items", ZH: " +{count} 个粘贴项", JA: " +{count} 件の貼り付け", KO: " +붙여넣기 {count}개", ES: " +{count} elementos pegados", FR: " +{count} éléments collés", ENOne: " +{count} paste item", ZHOne: " +{count} 个粘贴项", JAOne: " +{count} 件の貼り付け", KOOne: " +붙여넣기 {count}개", ESOne: " +{count} elemento pegado", FROne: " +{count} élément collé"},
	catalog(MsgQueueItem, "  {index}. {summary}", "  {index}. {summary}", "  {index}. {summary}", "  {index}. {summary}", "  {index}. {summary}", "  {index}. {summary}"),
	catalog(MsgReconnectAttempt, " (reconnecting, attempt {attempt})", "（正在重连，第 {attempt} 次）", "（再接続中、{attempt} 回目）", " (재연결 중, {attempt}번째 시도)", " (reconectando, intento {attempt})", " (reconnexion, tentative {attempt})"),
	catalog(MsgConnecting, "Connecting to {socket}...", "正在连接 {socket}……", "{socket} に接続中…", "{socket}에 연결 중...", "Conectando a {socket}...", "Connexion à {socket}…"),
	catalog(MsgOverlayDisconnected, "Connection unavailable; decisions can be retried after reconnect.", "连接不可用；重连后可重试操作。", "接続できません。再接続後に再試行できます。", "연결할 수 없습니다. 재연결 후 다시 시도할 수 있습니다.", "Conexión no disponible; reintenta tras reconectar.", "Connexion indisponible ; réessayez après reconnexion."),
	catalog(MsgStatusNotAttached, "not attached", "未连接会话", "未接続", "연결되지 않음", "sin sesión", "sans session"),
	catalog(MsgStatusSession, "session {id}", "会话 {id}", "セッション {id}", "세션 {id}", "sesión {id}", "session {id}"),
	catalog(MsgStatusReady, "ready", "就绪", "準備完了", "준비됨", "listo", "prêt"),
	catalog(MsgStatusEditingDraft, "editing draft", "正在编辑草稿", "下書きを編集中", "초안 편집 중", "editando borrador", "modification du brouillon"),
	catalog(MsgStatusSending, "sending {kind}", "正在发送 {kind}", "{kind} を送信中", "{kind} 전송 중", "enviando {kind}", "envoi de {kind}"),
	catalog(MsgStatusRunning, "running {task}", "正在运行 {task}", "実行中 {task}", "실행 중 {task}", "ejecutando {task}", "exécution de {task}"),
	{ID: MsgStatusNew, EN: "{count} new lines", ZH: "{count} 行新内容", JA: "新着 {count} 行", KO: "새 줄 {count}개", ES: "{count} líneas nuevas", FR: "{count} nouvelles lignes", ENOne: "{count} new line", ZHOne: "{count} 行新内容", JAOne: "新着 {count} 行", KOOne: "새 줄 {count}개", ESOne: "{count} línea nueva", FROne: "{count} nouvelle ligne"},
	{ID: MsgStatusQueued, EN: "{count} queued", ZH: "{count} 项排队中", JA: "{count} 件待機", KO: "{count}개 대기", ES: "{count} en cola", FR: "{count} en file", ENOne: "{count} queued", ZHOne: "{count} 项排队中", JAOne: "{count} 件待機", KOOne: "{count}개 대기", ESOne: "{count} en cola", FROne: "{count} en file"},
	{ID: MsgStatusAttention, EN: "{count} attention", ZH: "{count} 项待处理", JA: "要確認 {count} 件", KO: "확인 필요 {count}개", ES: "{count} requieren atención", FR: "{count} demandent votre attention", ENOne: "{count} attention", ZHOne: "{count} 项待处理", JAOne: "要確認 {count} 件", KOOne: "확인 필요 {count}개", ESOne: "{count} requiere atención", FROne: "{count} demande votre attention"},
	catalog(MsgStatusChord, "chord {hint}", "组合键 {hint}", "コード {hint}", "키 조합 {hint}", "secuencia {hint}", "séquence {hint}"),
	catalog(MsgStatusFooter, " carina · {session} · {mode} · next {model} · {activity} · {help} help", " carina · {session} · {mode} · 下个模型 {model} · {activity} · {help} 帮助", " carina · {session} · {mode} · 次のモデル {model} · {activity} · {help} ヘルプ", " carina · {session} · {mode} · 다음 모델 {model} · {activity} · {help} 도움말", " carina · {session} · {mode} · siguiente {model} · {activity} · {help} ayuda", " carina · {session} · {mode} · suivant {model} · {activity} · {help} aide"),
	catalog(MsgStatusSettingsHint, "{key} settings", "{key} 设置", "{key} 設定", "{key} 설정", "{key} ajustes", "{key} réglages"),
	catalog(MsgStatusHelpHint, "{key} help", "{key} 帮助", "{key} ヘルプ", "{key} 도움말", "{key} ayuda", "{key} aide"),
	catalog(MsgStatusGoal, "goal:{status}", "目标:{status}", "目標:{status}", "목표:{status}", "objetivo:{status}", "objectif:{status}"),
	catalog(MsgStatusRunningModel, "running {requested} → {effective}", "运行中 {requested} → {effective}", "実行中 {requested} → {effective}", "실행 중 {requested} → {effective}", "ejecutando {requested} → {effective}", "en cours {requested} → {effective}"),

	catalog(MsgHelpTitle, "Carina help", "Carina 帮助", "Carina ヘルプ", "Carina 도움말", "Ayuda de Carina", "Aide de Carina"),
	catalog(MsgHelpCommands, "Commands", "命令", "コマンド", "명령", "Comandos", "Commandes"),
	catalog(MsgHelpKeybindings, "Keybindings", "按键绑定", "キーバインド", "키 바인딩", "Atajos de teclado", "Raccourcis clavier"),
	catalog(MsgHelpCloseScroll, "[{close}] close  [{up}/{down}] scroll", "[{close}] 关闭  [{up}/{down}] 滚动", "[{close}] 閉じる  [{up}/{down}] スクロール", "[{close}] 닫기  [{up}/{down}] 스크롤", "[{close}] cerrar  [{up}/{down}] desplazar", "[{close}] fermer  [{up}/{down}] défiler"),
	catalog(MsgHelpCommandHelp, "  /help                 commands and keybindings", "  /help                 命令与按键绑定", "  /help                 コマンドとキーバインド", "  /help                 명령과 키 바인딩", "  /help                 comandos y atajos", "  /help                 commandes et raccourcis"),
	catalog(MsgHelpCommandEditor, "  /editor               edit the current draft in VISUAL/EDITOR", "  /editor               使用 VISUAL/EDITOR 编辑当前草稿", "  /editor               VISUAL/EDITOR で下書きを編集", "  /editor               VISUAL/EDITOR에서 현재 초안 편집", "  /editor               editar el borrador en VISUAL/EDITOR", "  /editor               modifier le brouillon dans VISUAL/EDITOR"),
	catalog(MsgHelpCommandCopy, "  /copy                 copy the latest rendered agent response", "  /copy                 复制最近渲染的 Agent 回复", "  /copy                 最新の Agent 応答をコピー", "  /copy                 최근 렌더링된 Agent 응답 복사", "  /copy                 copiar la última respuesta del Agent", "  /copy                 copier la dernière réponse de l’Agent"),
	catalog(MsgHelpCommandTranscript, "  /transcript           canonical session items (includes hidden events)", "  /transcript           规范会话记录（含隐藏事件）", "  /transcript           正規セッション履歴（非表示イベントを含む）", "  /transcript           표준 세션 기록(숨겨진 이벤트 포함)", "  /transcript           historial canónico (incluye eventos ocultos)", "  /transcript           historique canonique (événements masqués inclus)"),
	catalog(MsgHelpCommandKeymap, "  /keymap               inspect and edit active keybindings", "  /keymap               查看并编辑当前按键绑定", "  /keymap               現在のキーバインドを確認・編集", "  /keymap               활성 키 바인딩 확인 및 편집", "  /keymap               revisar y editar los atajos activos", "  /keymap               consulter et modifier les raccourcis actifs"),
	catalog(MsgHelpCommandAgents, "  /agents               available agent modes", "  /agents               可用的 Agent 模式", "  /agents               利用可能な Agent モード", "  /agents               사용 가능한 Agent 모드", "  /agents               modos de Agent disponibles", "  /agents               modes d’Agent disponibles"),
	catalog(MsgHelpCommandCheckpoints, "  /checkpoints          restore a rewind point into a paused task", "  /checkpoints          将回退点恢复为暂停任务", "  /checkpoints          巻き戻し点を一時停止タスクとして復元", "  /checkpoints          되돌리기 지점을 일시 중지 작업으로 복원", "  /checkpoints          restaurar un punto en una tarea pausada", "  /checkpoints          restaurer un point dans une tâche en pause"),
	catalog(MsgHelpCommandNew, "  /new                  create and switch to a new session", "  /new                  创建并切换到新会话", "  /new                  新しいセッションを作成して切替", "  /new                  새 세션 생성 및 전환", "  /new                  crear y cambiar a una sesión nueva", "  /new                  créer et ouvrir une nouvelle session"),
	catalog(MsgHelpCommandResume, "  /resume [session_id]  choose or resume a historical session", "  /resume [会话ID]       选择或恢复历史会话", "  /resume [session_id]  過去のセッションを選択・再開", "  /resume [session_id]  이전 세션 선택 또는 재개", "  /resume [session_id]  elegir o reanudar una sesión", "  /resume [session_id]  choisir ou reprendre une session"),
	catalog(MsgHelpCommandFork, "  /fork [task_id]       fork completed session lineage and switch", "  /fork [任务ID]         分叉已完成的会话谱系并切换", "  /fork [task_id]       完了セッションをフォークして切替", "  /fork [task_id]       완료된 세션을 포크하고 전환", "  /fork [task_id]       bifurcar la sesión completada", "  /fork [task_id]       bifurquer la session terminée"),
	catalog(MsgHelpCommandTaskResume, "  /task-resume [task_id] resume a checkpoint-restored task", "  /task-resume [任务ID]  继续从检查点恢复的任务", "  /task-resume [task_id] チェックポイント復元タスクを再開", "  /task-resume [task_id] 체크포인트 복원 작업 재개", "  /task-resume [task_id] reanudar una tarea restaurada", "  /task-resume [task_id] reprendre une tâche restaurée"),
	catalog(MsgHelpCommandSearch, "  /search <text>         search canonical session items", "  /search <text>         搜索规范会话记录", "  /search <text>         正規セッション履歴を検索", "  /search <text>         표준 세션 기록 검색", "  /search <text>         buscar elementos canónicos", "  /search <text>         rechercher les éléments canoniques"),
	catalog(MsgHelpCommandRecap, "  /recap                 latest canonical session items", "  /recap                 最近的规范会话记录", "  /recap                 最新の正規セッション項目", "  /recap                 최신 표준 세션 항목", "  /recap                 últimos elementos canónicos", "  /recap                 derniers éléments canoniques"),
	catalog(MsgHelpCommandStatus, "  /status                current daemon-backed session status", "  /status                当前会话状态（来自 daemon）", "  /status                daemon の現在のセッション状態", "  /status                daemon 기반 현재 세션 상태", "  /status                estado actual desde el daemon", "  /status                état courant fourni par le daemon"),
	catalog(MsgHelpCommandPermissions, "  /permissions [new <profile> [--yes]] inspect or create governed session", "  /permissions [new <配置> [--yes]] 查看权限或创建受治理会话", "  /permissions [new <profile> [--yes]] 権限確認・管理セッション作成", "  /permissions [new <profile> [--yes]] 권한 확인 또는 통제 세션 생성", "  /permissions [new <perfil> [--yes]] revisar o crear sesión gobernada", "  /permissions [new <profil> [--yes]] consulter ou créer une session gouvernée"),
	catalog(MsgHelpCommandContext, "  /context               exact persisted context summary", "  /context               精确的持久化上下文摘要", "  /context               永続コンテキストの正確な概要", "  /context               정확한 영구 컨텍스트 요약", "  /context               resumen exacto del contexto persistido", "  /context               résumé exact du contexte persistant"),
	catalog(MsgHelpCommandCompact, "  /compact               atomically compact the current paused checkpoint", "  /compact               原子压缩当前暂停任务的检查点", "  /compact               現在の一時停止チェックポイントをアトミックに圧縮", "  /compact               현재 일시 중지 체크포인트 원자적 압축", "  /compact               compactar atómicamente el checkpoint pausado", "  /compact               compacter atomiquement le checkpoint en pause"),
	catalog(MsgHelpCommandConfig, "  /config|/settings      settings shell (use /config raw for inventory)", "  /config|/settings      设置面板（/config raw 查看完整清单）", "  /config|/settings      設定シェル（一覧は /config raw）", "  /config|/settings      설정 셸(/config raw로 목록)", "  /config|/settings      panel de ajustes (/config raw para inventario)", "  /config|/settings      panneau de réglages (/config raw pour l’inventaire)"),
	catalog(MsgHelpCommandUsage, "  /usage                 session token usage and cost", "  /usage                 会话 Token 用量与成本", "  /usage                 セッションのトークン使用量とコスト", "  /usage                 세션 토큰 사용량 및 비용", "  /usage                 uso de tokens y coste de la sesión", "  /usage                 jetons et coût de la session"),
	catalog(MsgHelpCommandReview, "  /review [target]       run a code review task", "  /review [目标]         发起代码审查任务", "  /review [対象]         コードレビューを実行", "  /review [대상]         코드 검토 작업 실행", "  /review [objetivo]     ejecutar revisión de código", "  /review [cible]        lancer une revue de code"),
	catalog(MsgHelpCommandSessionReview, "  /session-review       read-only governance projection", "  /session-review       只读治理投影", "  /session-review       ガバナンス表示（読み取り専用）", "  /session-review       거버넌스 보기(읽기 전용)", "  /session-review       proyección de gobierno", "  /session-review       projection de gouvernance"),
	catalog(MsgHelpCommandMemory, "  /memory <status|list|search|read|verify|handoff|rollback>", "  /memory <status|list|search|read|verify|handoff|rollback> 持久记忆控制", "  /memory <status|list|search|read|verify|handoff|rollback> 永続メモリ制御", "  /memory <status|list|search|read|verify|handoff|rollback> 영구 메모리 제어", "  /memory <status|list|search|read|verify|handoff|rollback> control de memoria", "  /memory <status|list|search|read|verify|handoff|rollback> contrôle mémoire"),
	catalog(MsgHelpCommandDoctor, "  /doctor                runtime diagnostics", "  /doctor                运行时诊断", "  /doctor                ランタイム診断", "  /doctor                런타임 진단", "  /doctor                diagnósticos del runtime", "  /doctor                diagnostic du runtime"),
	catalog(MsgHelpCommandSkills, "  /skills                read-only skill inventory", "  /skills                只读技能清单", "  /skills                スキル一覧（読み取り専用）", "  /skills                기술 목록(읽기 전용)", "  /skills                inventario de skills", "  /skills                inventaire des skills"),
	catalog(MsgHelpCommandHooks, "  /hooks                 read-only hook inventory", "  /hooks                 只读 Hook 清单", "  /hooks                 フック一覧（読み取り専用）", "  /hooks                 훅 목록(읽기 전용)", "  /hooks                 inventario de hooks", "  /hooks                 inventaire des hooks"),
	catalog(MsgHelpCommandExtensions, "  /extensions            read-only extension inventory", "  /extensions            只读扩展清单", "  /extensions            拡張一覧（読み取り専用）", "  /extensions            확장 목록(읽기 전용)", "  /extensions            inventario de extensiones", "  /extensions            inventaire des extensions"),
	catalog(MsgHelpCommandDiff, "  /diff                  read-only tracked and untracked workspace diff", "  /diff                  只读查看已跟踪和未跟踪的工作区差异", "  /diff                  追跡済み・未追跡の差分を読み取り専用で表示", "  /diff                  추적/미추적 작업 공간 diff 읽기 전용 보기", "  /diff                  diff de cambios rastreados y no rastreados", "  /diff                  diff en lecture seule, suivi et non suivi"),
	catalog(MsgHelpCommandMCP, "  /mcp [verbose]         secret-free MCP server/tool health inventory", "  /mcp [verbose]         不含密钥的 MCP 服务/工具健康清单", "  /mcp [verbose]         秘密情報を含まない MCP サーバー・ツール状態", "  /mcp [verbose]         비밀 없는 MCP 서버/도구 상태", "  /mcp [verbose]         inventario MCP sin secretos", "  /mcp [verbose]         inventaire MCP sans secrets"),
	catalog(MsgHelpCommandLoop, "  /loop [list|<duration> [--concurrency policy] <prompt>|pause|resume|delete <id>]", "  /loop [list|<时长> [--concurrency 策略] <指令>|pause|resume|delete <id>]", "  /loop [list|<期間> [--concurrency policy] <指示>|pause|resume|delete <id>]", "  /loop [list|<기간> [--concurrency policy] <지시>|pause|resume|delete <id>]", "  /loop [list|<duración> [--concurrency política] <instrucción>|pause|resume|delete <id>]", "  /loop [list|<durée> [--concurrency politique] <instruction>|pause|resume|delete <id>]"),
	catalog(MsgHelpCommandGoal, "  /goal [--auto] [--tokens N] [--max-continuations N] <objective> | clear|pause|resume|complete|continue", "  /goal [--auto] [--tokens N] [--max-continuations N] <目标> | clear|pause|resume|complete|continue", "  /goal [--auto] [--tokens N] [--max-continuations N] <目標> | clear|pause|resume|complete|continue", "  /goal [--auto] [--tokens N] [--max-continuations N] <목표> | clear|pause|resume|complete|continue", "  /goal [--auto] [--tokens N] [--max-continuations N] <objetivo> | clear|pause|resume|complete|continue", "  /goal [--auto] [--tokens N] [--max-continuations N] <objectif> | clear|pause|resume|complete|continue"),
	catalog(MsgHelpCommandMode, "  /mode <build|plan|cycle> change interaction mode (Shift+Tab)", "  /mode <build|plan|cycle> 切换交互模式（Shift+Tab）", "  /mode <build|plan|cycle> 対話モード変更（Shift+Tab）", "  /mode <build|plan|cycle> 상호작용 모드 변경(Shift+Tab)", "  /mode <build|plan|cycle> cambiar modo (Shift+Tab)", "  /mode <build|plan|cycle> changer de mode (Shift+Tab)"),
	catalog(MsgHelpCommandModel, "  /model [provider/model] show or switch the task model", "  /model [厂商/模型]      查看或切换任务模型", "  /model [provider/model] タスクモデルを表示・切替", "  /model [provider/model] 작업 모델 보기/전환", "  /model [proveedor/modelo] ver o cambiar el modelo", "  /model [fournisseur/modèle] afficher ou changer le modèle"),
	catalog(MsgHelpCommandEffort, "  /effort [level]        show or change reasoning effort", "  /effort [级别]         查看或切换推理强度", "  /effort [レベル]       推論強度を表示・変更", "  /effort [수준]         추론 강도 보기/변경", "  /effort [nivel]        ver o cambiar esfuerzo", "  /effort [niveau]       afficher ou changer l’effort"),
	catalog(MsgHelpCommandShell, "  !<command>             governed argv command; quotes supported", "  !<command>             受治理的 argv 命令；支持引号", "  !<command>             管理対象の argv コマンド。引用符対応", "  !<command>             통제되는 argv 명령, 따옴표 지원", "  !<command>             comando argv gobernado; admite comillas", "  !<command>             commande argv gouvernée ; guillemets pris en charge"),
	catalog(MsgHelpCommandMention, "  @<path|agent>          reference a path or agent", "  @<path|agent>          引用路径或 Agent", "  @<path|agent>          パスまたは Agent を参照", "  @<path|agent>          경로 또는 Agent 참조", "  @<path|agent>          referenciar una ruta o un Agent", "  @<path|agent>          référencer un chemin ou un Agent"),

	catalog(MsgApprovalRPCFailed, "{glyph} approval request failed: {error}", "{glyph} 审批请求失败：{error}", "{glyph} 承認リクエスト失敗: {error}", "{glyph} 승인 요청 실패: {error}", "{glyph} falló la solicitud de aprobación: {error}", "{glyph} échec de la demande d’approbation : {error}"),
	catalog(MsgApprovalRetry, "Approval failed: {error}. Press the decision key to retry.", "审批失败：{error}。按决策键重试。", "承認に失敗: {error}。判断キーで再試行してください。", "승인 실패: {error}. 결정 키를 눌러 다시 시도하세요.", "La aprobación falló: {error}. Pulsa una tecla de decisión para reintentar.", "Échec de l’approbation : {error}. Appuyez sur une touche de décision pour réessayer."),
	catalog(MsgApprovalResource, "resource: {value}", "资源：{value}", "リソース: {value}", "리소스: {value}", "recurso: {value}", "ressource : {value}"),
	catalog(MsgApprovalPolicy, "policy: {value}", "策略：{value}", "ポリシー: {value}", "정책: {value}", "política: {value}", "politique : {value}"),
	catalog(MsgApprovalFooterWide, "[{once}] approve once  [{session}] session  [{project}] project  [{deny}] deny", "[{once}] 单次批准  [{session}] 会话  [{project}] 项目  [{deny}] 拒绝", "[{once}] 今回のみ  [{session}] セッション  [{project}] プロジェクト  [{deny}] 拒否", "[{once}] 한 번 승인  [{session}] 세션  [{project}] 프로젝트  [{deny}] 거부", "[{once}] aprobar una vez  [{session}] sesión  [{project}] proyecto  [{deny}] denegar", "[{once}] une fois  [{session}] session  [{project}] projet  [{deny}] refuser"),
	catalog(MsgApprovalFooterMedium, "[{once}] allow  [{session}/{project}] broader  [{deny}] deny", "[{once}] 允许  [{session}/{project}] 扩大范围  [{deny}] 拒绝", "[{once}] 許可  [{session}/{project}] 範囲拡大  [{deny}] 拒否", "[{once}] 허용  [{session}/{project}] 범위 확대  [{deny}] 거부", "[{once}] permitir  [{session}/{project}] ampliar  [{deny}] denegar", "[{once}] autoriser  [{session}/{project}] élargir  [{deny}] refuser"),
	catalog(MsgApprovalFooterNarrow, "[{once}] allow  [{deny}] deny", "[{once}] 允许  [{deny}] 拒绝", "[{once}] 許可  [{deny}] 拒否", "[{once}] 허용  [{deny}] 거부", "[{once}] permitir  [{deny}] denegar", "[{once}] autoriser  [{deny}] refuser"),
	catalog(MsgApprovalResolving, "Resolving decision...", "正在提交决策...", "判断を送信中...", "결정 처리 중...", "Resolviendo la decisión...", "Traitement de la décision..."),
	catalog(MsgApprovalScroll, "  [{up}/{down}/{page_up}/{page_down}] scroll {start}-{end}/{total}", "  [{up}/{down}/{page_up}/{page_down}] 滚动 {start}-{end}/{total}", "  [{up}/{down}/{page_up}/{page_down}] スクロール {start}-{end}/{total}", "  [{up}/{down}/{page_up}/{page_down}] 스크롤 {start}-{end}/{total}", "  [{up}/{down}/{page_up}/{page_down}] desplazar {start}-{end}/{total}", "  [{up}/{down}/{page_up}/{page_down}] défiler {start}-{end}/{total}"),
	catalog(MsgApprovalPolicyDetail, "{glyph} policy: {detail}", "{glyph} 策略：{detail}", "{glyph} ポリシー: {detail}", "{glyph} 정책: {detail}", "{glyph} política: {detail}", "{glyph} politique : {detail}"),

	catalog(MsgQuestionTitle, "Agent needs input", "Agent 需要输入", "Agent が入力を求めています", "Agent에 입력이 필요합니다", "El Agent necesita información", "L’Agent attend une réponse"),
	catalog(MsgQuestionAnswerFailed, "Answer failed: {error}. Press [{retry}] to retry.", "回答失败：{error}。按 [{retry}] 重试。", "回答に失敗: {error}。[{retry}] で再試行。", "응답 실패: {error}. [{retry}]로 다시 시도하세요.", "La respuesta falló: {error}. Pulsa [{retry}] para reintentar.", "Échec de la réponse : {error}. Appuyez sur [{retry}] pour réessayer."),
	catalog(MsgQuestionAnswerLogFail, "{glyph} answer failed for question {id}: {error}", "{glyph} 问题 {id} 回答失败：{error}", "{glyph} 質問 {id} の回答失敗: {error}", "{glyph} 질문 {id} 응답 실패: {error}", "{glyph} falló la respuesta a {id}: {error}", "{glyph} échec de la réponse à {id} : {error}"),
	catalog(MsgQuestionAnswered, "{glyph} answered {id}: {label}", "{glyph} 已回答 {id}：{label}", "{glyph} {id} に回答: {label}", "{glyph} {id} 응답 완료: {label}", "{glyph} respuesta a {id}: {label}", "{glyph} réponse à {id} : {label}"),
	catalog(MsgQuestionNoOptions, "No answer options are available. Waiting for the Agent to update this question.", "暂无可选答案。正在等待 Agent 更新问题。", "回答候補がありません。Agent による質問の更新を待っています。", "선택 가능한 답변이 없습니다. Agent의 질문 업데이트를 기다리는 중입니다.", "No hay opciones disponibles. Esperando a que el Agent actualice la pregunta.", "Aucune réponse n’est disponible. En attente de la mise à jour de l’Agent."),
	catalog(MsgQuestionCannotDismiss, "This question is still pending. Choose an answer; Esc cannot dismiss it.", "此问题仍在等待回答。请选择答案；Esc 无法关闭。", "この質問は未回答です。回答を選択してください。Esc では閉じられません。", "이 질문은 아직 대기 중입니다. 답변을 선택하세요. Esc로 닫을 수 없습니다.", "La pregunta sigue pendiente. Elige una respuesta; Esc no puede cerrarla.", "Cette question est toujours en attente. Choisissez une réponse ; Échap ne peut pas la fermer."),
	catalog(MsgQuestionFooterWide, "[{previous}/{next}] select  [{answer}] answer", "[{previous}/{next}] 选择  [{answer}] 回答", "[{previous}/{next}] 選択  [{answer}] 回答", "[{previous}/{next}] 선택  [{answer}] 응답", "[{previous}/{next}] seleccionar  [{answer}] responder", "[{previous}/{next}] sélectionner  [{answer}] répondre"),
	catalog(MsgQuestionFooterMedium, "[{previous}/{next}] pick  [{answer}] answer", "[{previous}/{next}] 选择  [{answer}] 回答", "[{previous}/{next}] 選択  [{answer}] 回答", "[{previous}/{next}] 선택  [{answer}] 응답", "[{previous}/{next}] elegir  [{answer}] responder", "[{previous}/{next}] choisir  [{answer}] répondre"),
	catalog(MsgQuestionFooterNarrow, "[{answer}] answer", "[{answer}] 回答", "[{answer}] 回答", "[{answer}] 응답", "[{answer}] responder", "[{answer}] répondre"),
	catalog(MsgQuestionSending, "Sending answer...", "正在发送回答...", "回答を送信中...", "응답 전송 중...", "Enviando la respuesta...", "Envoi de la réponse..."),
	catalog(MsgQuestionScroll, "  [{page_up}/{page_down}] scroll {start}-{end}/{total}", "  [{page_up}/{page_down}] 滚动 {start}-{end}/{total}", "  [{page_up}/{page_down}] スクロール {start}-{end}/{total}", "  [{page_up}/{page_down}] 스크롤 {start}-{end}/{total}", "  [{page_up}/{page_down}] desplazar {start}-{end}/{total}", "  [{page_up}/{page_down}] défiler {start}-{end}/{total}"),
	catalog(MsgQuestionFreeTextHint, "No choices were provided. Type your answer:", "未提供选项，请输入回答：", "選択肢がありません。回答を入力してください:", "선택지가 없습니다. 답변을 입력하세요:", "No se proporcionaron opciones. Escribe tu respuesta:", "Aucun choix n’est proposé. Saisissez votre réponse :"),
	catalog(MsgQuestionFreeTextFooter, "[enter] submit  [backspace] edit  [esc] keep pending", "[enter] 提交  [backspace] 编辑  [esc] 保持等待", "[enter] 送信  [backspace] 編集  [esc] 保留", "[enter] 제출  [backspace] 편집  [esc] 대기 유지", "[enter] enviar  [backspace] editar  [esc] mantener pendiente", "[enter] envoyer  [backspace] modifier  [esc] laisser en attente"),
	catalog(MsgQuestionFreeTextRequired, "Type an answer before submitting.", "请输入回答后再提交。", "回答を入力してから送信してください。", "답변을 입력한 후 제출하세요.", "Escribe una respuesta antes de enviarla.", "Saisissez une réponse avant l’envoi."),

	catalog(MsgHistoryTypeToSearch, "type to search", "输入以搜索", "入力して検索", "입력하여 검색", "escribe para buscar", "saisissez pour rechercher"),
	catalog(MsgHistoryMatch, "match", "匹配", "一致", "일치", "coincidencia", "résultat"),
	catalog(MsgHistoryNoMatch, "no match", "无匹配", "一致なし", "일치 없음", "sin coincidencias", "aucun résultat"),
	catalog(MsgHistoryLoading, "loading", "加载中", "読み込み中", "불러오는 중", "cargando", "chargement"),
	catalog(MsgHistoryLoadKept, "{scope} load failed; {kept} kept", "{scope} 加载失败；保留 {kept}", "{scope} の読み込み失敗。{kept} を保持", "{scope} 불러오기 실패, {kept} 유지", "falló la carga de {scope}; se conserva {kept}", "échec du chargement de {scope} ; {kept} conservé"),
	catalog(MsgHistoryLoadCleared, "{scope} load failed; results cleared", "{scope} 加载失败；结果已清空", "{scope} の読み込み失敗。結果を消去", "{scope} 불러오기 실패, 결과 삭제됨", "falló la carga de {scope}; resultados borrados", "échec du chargement de {scope} ; résultats effacés"),
	catalog(MsgHistoryWide, "reverse-i-search: {query}  {scope} · {status}  {older} older  {newer} newer  {run} run  {edit} edit  {cycle} scope  {cancel} cancel", "反向搜索：{query}  {scope} · {status}  {older} 更早  {newer} 更新  {run} 运行  {edit} 编辑  {cycle} 范围  {cancel} 取消", "履歴検索: {query}  {scope} · {status}  {older} 前へ  {newer} 次へ  {run} 実行  {edit} 編集  {cycle} 範囲  {cancel} 取消", "기록 검색: {query}  {scope} · {status}  {older} 이전  {newer} 다음  {run} 실행  {edit} 편집  {cycle} 범위  {cancel} 취소", "búsqueda inversa: {query}  {scope} · {status}  {older} anterior  {newer} siguiente  {run} ejecutar  {edit} editar  {cycle} ámbito  {cancel} cancelar", "recherche inverse : {query}  {scope} · {status}  {older} précédent  {newer} suivant  {run} exécuter  {edit} modifier  {cycle} portée  {cancel} annuler"),
	catalog(MsgHistoryMedium, "history {scope} · {status}: {query}", "历史 {scope} · {status}：{query}", "履歴 {scope} · {status}: {query}", "기록 {scope} · {status}: {query}", "historial {scope} · {status}: {query}", "historique {scope} · {status} : {query}"),
	catalog(MsgHistoryTiny, "?{status}:{query}", "?{status}:{query}", "?{status}:{query}", "?{status}:{query}", "?{status}:{query}", "?{status}:{query}"),
	catalog(MsgHistoryScopeSession, "session", "会话", "セッション", "세션", "sesión", "session"),
	catalog(MsgHistoryScopeWorkspace, "workspace", "工作区", "ワークスペース", "워크스페이스", "espacio", "espace"),
	catalog(MsgHistoryScopeGlobal, "global", "全局", "全体", "전체", "global", "global"),

	catalog(MsgAttentionApproval, "Approval required", "需要审批", "承認が必要です", "승인이 필요합니다", "Se requiere aprobación", "Approbation requise"),
	catalog(MsgAttentionInput, "Input required", "需要输入", "入力が必要です", "입력이 필요합니다", "Se requiere información", "Réponse requise"),
	catalog(MsgAttentionTaskFinished, "Task finished", "任务已结束", "タスクが終了しました", "작업이 완료되었습니다", "La tarea ha finalizado", "Tâche terminée"),
	catalog(MsgAttentionMemorySync, "Memory sync needs action", "记忆同步需要处理", "メモリ同期の対応が必要です", "메모리 동기화 조치가 필요합니다", "La sincronización de memoria requiere atención", "La synchronisation mémoire nécessite une action"),
}
