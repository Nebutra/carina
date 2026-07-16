package tui

type localizedText struct {
	en, zh, ja, ko, es, fr string
}

func (t localizedText) get(locale Locale) string {
	switch locale {
	case LocaleChinese:
		return t.zh
	case LocaleJapanese:
		return t.ja
	case LocaleKorean:
		return t.ko
	case LocaleSpanish:
		return t.es
	case LocaleFrench:
		return t.fr
	default:
		return t.en
	}
}

var keyContextCopy = map[KeyContext]localizedText{
	KeyContextGlobal:             {"Global", "全局", "全体", "전체", "Global", "Global"},
	KeyContextChat:               {"Chat", "对话", "チャット", "대화", "Chat", "Discussion"},
	KeyContextComposer:           {"Composer", "输入区", "入力", "작성기", "Editor", "Saisie"},
	KeyContextEditor:             {"Editor", "编辑器", "エディタ", "편집기", "Editor", "Éditeur"},
	KeyContextSuggestion:         {"Suggestions", "建议", "候補", "제안", "Sugerencias", "Suggestions"},
	KeyContextApproval:           {"Approval", "审批", "承認", "승인", "Aprobación", "Approbation"},
	KeyContextQuestion:           {"Questions", "问题", "質問", "질문", "Preguntas", "Questions"},
	KeyContextHistory:            {"History search", "历史搜索", "履歴検索", "기록 검색", "Historial", "Recherche d’historique"},
	KeyContextPager:              {"Pager", "查看器", "ページャ", "보기", "Visor", "Visionneuse"},
	KeyContextKeymap:             {"Keymap", "按键映射", "キーマップ", "키맵", "Mapa de teclas", "Raccourcis"},
	KeyContextKeymapAction:       {"Keymap action", "按键操作", "キーマップ操作", "키맵 작업", "Acción de tecla", "Action de raccourci"},
	KeyContextKeymapCapture:      {"Key capture", "按键录制", "キー入力", "키 캡처", "Captura de tecla", "Capture de touche"},
	KeyContextCheckpointList:     {"Checkpoint list", "检查点列表", "チェックポイント一覧", "체크포인트 목록", "Lista de puntos", "Liste des points"},
	KeyContextCheckpointPreview:  {"Checkpoint preview", "检查点预览", "チェックポイント確認", "체크포인트 미리보기", "Vista previa", "Aperçu du point"},
	KeyContextCheckpointRestored: {"Checkpoint restored", "检查点已恢复", "チェックポイント復元済み", "체크포인트 복원됨", "Punto restaurado", "Point restauré"},
}

var keyActionCopy = buildKeyActionCopy()

func buildKeyActionCopy() map[KeyAction]localizedText {
	out := map[KeyAction]localizedText{}
	add := func(actions []KeyAction, copy localizedText) {
		for _, action := range actions {
			out[action] = copy
		}
	}
	add([]KeyAction{ActionGlobalHelp}, localizedText{"show keyboard help", "显示键盘帮助", "キーボードヘルプを表示", "키보드 도움말 표시", "mostrar ayuda del teclado", "afficher l’aide clavier"})
	add([]KeyAction{ActionGlobalInterrupt}, localizedText{"cancel, clear, or exit", "取消、清除或退出", "取消、消去、終了", "취소, 지우기 또는 종료", "cancelar, borrar o salir", "annuler, effacer ou quitter"})
	add([]KeyAction{ActionGlobalRedraw}, localizedText{"redraw terminal", "重绘终端", "端末を再描画", "터미널 다시 그리기", "redibujar el terminal", "redessiner le terminal"})
	add([]KeyAction{ActionGlobalExit}, localizedText{"exit when input is empty", "输入为空时退出", "入力が空なら終了", "입력이 비었을 때 종료", "salir si la entrada está vacía", "quitter si la saisie est vide"})
	add([]KeyAction{ActionGlobalTranscript}, localizedText{"open plain transcript", "打开纯文本记录", "プレーン履歴を開く", "일반 텍스트 기록 열기", "abrir historial en texto", "ouvrir l’historique en texte"})
	add([]KeyAction{ActionGlobalModeCycle}, localizedText{"cycle build/plan mode", "循环切换 build/plan 模式", "build/plan モードを切替", "build/plan 모드 전환", "alternar modo build/plan", "basculer mode build/plan"})
	add([]KeyAction{ActionGlobalSettings}, localizedText{"open settings shell", "打开设置面板", "設定シェルを開く", "설정 셸 열기", "abrir ajustes", "ouvrir les réglages"})
	add([]KeyAction{ActionChatInterrupt}, localizedText{"interrupt active turn", "中断当前轮次", "実行中のターンを中断", "활성 턴 중단", "interrumpir el turno activo", "interrompre le tour actif"})
	add([]KeyAction{ActionChatRewind}, localizedText{"rewind idle chat", "回退空闲对话", "待機中の会話を巻き戻す", "유휴 대화 되돌리기", "retroceder el chat inactivo", "revenir dans la discussion inactive"})
	add([]KeyAction{ActionComposerSubmit}, localizedText{"submit or steer", "提交或引导", "送信または指示追加", "제출 또는 방향 전환", "enviar u orientar", "envoyer ou orienter"})
	add([]KeyAction{ActionComposerSubmitNew}, localizedText{"force a distinct submission", "强制新建提交", "別の送信として実行", "별도 제출 강제", "forzar un envío independiente", "forcer un envoi distinct"})
	add([]KeyAction{ActionComposerNewline, ActionEditorInsertNewline}, localizedText{"insert newline", "插入换行", "改行を挿入", "줄바꿈 삽입", "insertar salto de línea", "insérer un saut de ligne"})
	add([]KeyAction{ActionComposerQueue}, localizedText{"queue next turn while running", "运行时将下一轮入队", "実行中に次のターンを追加", "실행 중 다음 턴 대기열 추가", "poner el siguiente turno en cola", "mettre le prochain tour en file"})
	add([]KeyAction{ActionComposerRecallQueue}, localizedText{"edit latest queued turn", "编辑最新排队轮次", "最新の待機ターンを編集", "최신 대기 턴 편집", "editar el último turno en cola", "modifier le dernier tour en file"})
	add([]KeyAction{ActionComposerExternalEditor}, localizedText{"edit draft externally", "使用外部编辑器编辑草稿", "外部エディタで下書きを編集", "외부 편집기로 초안 편집", "editar borrador externamente", "modifier le brouillon dans un éditeur externe"})
	add([]KeyAction{ActionComposerUndo}, localizedText{"undo paste or last edit", "撤销粘贴或上次编辑", "貼り付けまたは直前の編集を元に戻す", "붙여넣기 또는 마지막 편집 실행 취소", "deshacer pegado o última edición", "annuler le collage ou la dernière modification"})
	add([]KeyAction{ActionComposerRedo}, localizedText{"redo last edit", "重做上次编辑", "直前の編集をやり直す", "마지막 편집 다시 실행", "rehacer la última edición", "rétablir la dernière modification"})
	add([]KeyAction{ActionComposerHistoryPrevious, ActionSuggestionPrevious, ActionQuestionPrevious}, localizedText{"previous item", "上一项", "前の項目", "이전 항목", "elemento anterior", "élément précédent"})
	add([]KeyAction{ActionComposerHistoryNext, ActionSuggestionNext, ActionQuestionNext}, localizedText{"next item", "下一项", "次の項目", "다음 항목", "elemento siguiente", "élément suivant"})
	add([]KeyAction{ActionComposerHistorySearch}, localizedText{"search prompt history", "搜索提示历史", "プロンプト履歴を検索", "프롬프트 기록 검색", "buscar en el historial", "rechercher dans l’historique"})
	add([]KeyAction{ActionEditorMoveLeft, ActionEditorMoveWordLeft}, localizedText{"move left", "向左移动", "左へ移動", "왼쪽으로 이동", "mover a la izquierda", "déplacer à gauche"})
	add([]KeyAction{ActionEditorMoveRight, ActionEditorMoveWordRight}, localizedText{"move right", "向右移动", "右へ移動", "오른쪽으로 이동", "mover a la derecha", "déplacer à droite"})
	add([]KeyAction{ActionEditorMoveUp, ActionKeymapUp, ActionCheckpointListUp}, localizedText{"move up", "向上移动", "上へ移動", "위로 이동", "mover arriba", "déplacer vers le haut"})
	add([]KeyAction{ActionEditorMoveDown, ActionKeymapDown, ActionCheckpointListDown}, localizedText{"move down", "向下移动", "下へ移動", "아래로 이동", "mover abajo", "déplacer vers le bas"})
	add([]KeyAction{ActionEditorMoveLineStart}, localizedText{"move to line start", "移到行首", "行頭へ移動", "줄 시작으로 이동", "ir al inicio de línea", "aller au début de la ligne"})
	add([]KeyAction{ActionEditorMoveLineEnd}, localizedText{"move to line end", "移到行尾", "行末へ移動", "줄 끝으로 이동", "ir al final de línea", "aller à la fin de la ligne"})
	add([]KeyAction{ActionEditorDeleteBackward, ActionHistoryDelete}, localizedText{"delete backward", "向后删除", "後方削除", "뒤로 삭제", "borrar hacia atrás", "effacer en arrière"})
	add([]KeyAction{ActionEditorDeleteForward}, localizedText{"delete forward", "向前删除", "前方削除", "앞으로 삭제", "borrar hacia delante", "effacer en avant"})
	add([]KeyAction{ActionEditorDeleteWordBackward}, localizedText{"delete previous word", "删除前一个词", "前の単語を削除", "이전 단어 삭제", "borrar palabra anterior", "effacer le mot précédent"})
	add([]KeyAction{ActionEditorDeleteWordForward}, localizedText{"delete next word", "删除后一个词", "次の単語を削除", "다음 단어 삭제", "borrar palabra siguiente", "effacer le mot suivant"})
	add([]KeyAction{ActionEditorKillLineStart}, localizedText{"delete to line start", "删除到行首", "行頭まで削除", "줄 시작까지 삭제", "borrar hasta el inicio", "effacer jusqu’au début"})
	add([]KeyAction{ActionEditorKillLineEnd}, localizedText{"delete to line end", "删除到行尾", "行末まで削除", "줄 끝까지 삭제", "borrar hasta el final", "effacer jusqu’à la fin"})
	add([]KeyAction{ActionEditorYank}, localizedText{"paste from clipboard", "从剪贴板粘贴", "クリップボードから貼り付け", "클립보드에서 붙여넣기", "pegar desde el portapapeles", "coller depuis le presse-papiers"})
	add([]KeyAction{ActionSuggestionAccept}, localizedText{"complete suggestion", "补全建议", "候補を補完", "제안 완성", "completar sugerencia", "compléter la suggestion"})
	add([]KeyAction{ActionSuggestionDismiss}, localizedText{"dismiss suggestions", "关闭建议", "候補を閉じる", "제안 닫기", "cerrar sugerencias", "fermer les suggestions"})
	add([]KeyAction{ActionApprovalOnce}, localizedText{"approve once", "单次批准", "今回のみ承認", "한 번 승인", "aprobar una vez", "approuver une fois"})
	add([]KeyAction{ActionApprovalSession}, localizedText{"approve for session", "会话内批准", "セッション中承認", "세션 동안 승인", "aprobar para la sesión", "approuver pour la session"})
	add([]KeyAction{ActionApprovalProject}, localizedText{"approve for project", "项目内批准", "プロジェクトで承認", "프로젝트에서 승인", "aprobar para el proyecto", "approuver pour le projet"})
	add([]KeyAction{ActionApprovalDeny}, localizedText{"deny", "拒绝", "拒否", "거부", "denegar", "refuser"})
	add([]KeyAction{ActionApprovalUp, ActionPagerUp}, localizedText{"scroll up", "向上滚动", "上へスクロール", "위로 스크롤", "desplazar arriba", "défiler vers le haut"})
	add([]KeyAction{ActionApprovalDown, ActionPagerDown}, localizedText{"scroll down", "向下滚动", "下へスクロール", "아래로 스크롤", "desplazar abajo", "défiler vers le bas"})
	add([]KeyAction{ActionApprovalPageUp, ActionQuestionPageUp, ActionPagerPageUp, ActionKeymapPageUp, ActionCheckpointListPageUp}, localizedText{"page up", "向上翻页", "前ページ", "위 페이지", "página anterior", "page précédente"})
	add([]KeyAction{ActionApprovalPageDown, ActionQuestionPageDown, ActionPagerPageDown, ActionKeymapPageDown, ActionCheckpointListPageDown}, localizedText{"page down", "向下翻页", "次ページ", "아래 페이지", "página siguiente", "page suivante"})
	add([]KeyAction{ActionApprovalTop, ActionQuestionTop, ActionPagerTop, ActionKeymapTop, ActionCheckpointListTop}, localizedText{"jump to top", "跳到顶部", "先頭へ移動", "맨 위로 이동", "ir al inicio", "aller au début"})
	add([]KeyAction{ActionApprovalBottom, ActionQuestionBottom, ActionPagerBottom, ActionKeymapBottom, ActionCheckpointListBottom}, localizedText{"jump to bottom", "跳到底部", "末尾へ移動", "맨 아래로 이동", "ir al final", "aller à la fin"})
	add([]KeyAction{ActionQuestionAnswer}, localizedText{"answer", "回答", "回答", "응답", "responder", "répondre"})
	add([]KeyAction{ActionQuestionCancel}, localizedText{"cancel question", "取消问题", "質問を取消", "질문 취소", "cancelar pregunta", "annuler la question"})
	add([]KeyAction{ActionHistoryPrevious}, localizedText{"older match", "更早的匹配", "前の一致", "이전 일치", "coincidencia anterior", "résultat précédent"})
	add([]KeyAction{ActionHistoryNext}, localizedText{"newer match", "更新的匹配", "次の一致", "다음 일치", "coincidencia siguiente", "résultat suivant"})
	add([]KeyAction{ActionHistoryExecute}, localizedText{"accept and execute match", "接受并运行匹配项", "一致を採用して実行", "일치 항목 수락 및 실행", "aceptar y ejecutar", "accepter et exécuter"})
	add([]KeyAction{ActionHistoryAccept}, localizedText{"accept match for editing", "接受匹配项以编辑", "一致を編集用に採用", "일치 항목을 편집용으로 수락", "aceptar para editar", "accepter pour modifier"})
	add([]KeyAction{ActionHistoryCancel}, localizedText{"cancel and restore draft", "取消并恢复草稿", "取消して下書きを復元", "취소하고 초안 복원", "cancelar y restaurar borrador", "annuler et restaurer le brouillon"})
	add([]KeyAction{ActionHistoryClear}, localizedText{"clear search query", "清除搜索内容", "検索語を消去", "검색어 지우기", "borrar búsqueda", "effacer la recherche"})
	add([]KeyAction{ActionHistoryCycleScope}, localizedText{"cycle search scope", "切换搜索范围", "検索範囲を切替", "검색 범위 전환", "cambiar ámbito de búsqueda", "changer la portée de recherche"})
	add([]KeyAction{ActionPagerClose, ActionKeymapClose, ActionCheckpointListClose, ActionCheckpointPreviewClose, ActionCheckpointRestoredClose}, localizedText{"close", "关闭", "閉じる", "닫기", "cerrar", "fermer"})
	add([]KeyAction{ActionPagerToggleDetail}, localizedText{"toggle latest result details", "切换最新结果详情", "最新結果の詳細を切替", "최신 결과 세부 정보 전환", "alternar detalles del último resultado", "afficher ou masquer le dernier détail"})
	add([]KeyAction{ActionKeymapEdit}, localizedText{"edit selected action", "编辑所选操作", "選択した操作を編集", "선택한 작업 편집", "editar acción seleccionada", "modifier l’action sélectionnée"})
	add([]KeyAction{ActionKeymapActionBack}, localizedText{"return to binding list", "返回绑定列表", "設定一覧へ戻る", "바인딩 목록으로 돌아가기", "volver a la lista", "revenir à la liste"})
	add([]KeyAction{ActionKeymapActionReplace}, localizedText{"replace binding", "替换绑定", "設定を置換", "바인딩 교체", "sustituir atajo", "remplacer le raccourci"})
	add([]KeyAction{ActionKeymapActionAdd}, localizedText{"add alternate binding", "添加备用绑定", "代替設定を追加", "대체 바인딩 추가", "añadir atajo alternativo", "ajouter un raccourci alternatif"})
	add([]KeyAction{ActionKeymapActionRestore}, localizedText{"restore inherited binding", "恢复继承绑定", "継承設定を復元", "상속된 바인딩 복원", "restaurar atajo heredado", "restaurer le raccourci hérité"})
	add([]KeyAction{ActionKeymapCaptureCommit}, localizedText{"save pending chord", "保存待定组合键", "入力中のコードを保存", "대기 중인 키 조합 저장", "guardar secuencia pendiente", "enregistrer la séquence"})
	add([]KeyAction{ActionKeymapCaptureCancel}, localizedText{"cancel key capture", "取消按键录制", "キー入力を取消", "키 캡처 취소", "cancelar captura", "annuler la capture"})
	add([]KeyAction{ActionCheckpointListPreview}, localizedText{"preview checkpoint", "预览检查点", "チェックポイントを確認", "체크포인트 미리보기", "ver punto de control", "aperçu du point"})
	add([]KeyAction{ActionCheckpointPreviewBack}, localizedText{"return to checkpoint list", "返回检查点列表", "一覧へ戻る", "체크포인트 목록으로 돌아가기", "volver a la lista", "revenir à la liste"})
	add([]KeyAction{ActionCheckpointPreviewArm}, localizedText{"arm checkpoint restore", "准备检查点恢复", "復元を準備", "체크포인트 복원 준비", "preparar restauración", "armer la restauration"})
	add([]KeyAction{ActionCheckpointPreviewConfirm}, localizedText{"confirm checkpoint restore", "确认检查点恢复", "復元を確定", "체크포인트 복원 확인", "confirmar restauración", "confirmer la restauration"})
	add([]KeyAction{ActionCheckpointPreviewRetry}, localizedText{"retry checkpoint restore", "重试检查点恢复", "復元を再試行", "체크포인트 복원 재시도", "reintentar restauración", "réessayer la restauration"})
	add([]KeyAction{ActionCheckpointRestoredResume}, localizedText{"resume restored task", "继续恢复后的任务", "復元タスクを再開", "복원된 작업 재개", "reanudar tarea restaurada", "reprendre la tâche restaurée"})
	return out
}

func (m *Model) localizedKeyDescription(binding KeyBindingDescriptor) string {
	if copy, ok := keyActionCopy[binding.Action]; ok {
		if value := copy.get(normalizeUILocale(m.locale)); value != "" {
			return value
		}
	}
	return binding.Description
}

func localizedKeyContext(locale Locale, context KeyContext) string {
	if copy, ok := keyContextCopy[context]; ok {
		return copy.get(locale)
	}
	return string(context)
}
