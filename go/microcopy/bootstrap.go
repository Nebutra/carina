package microcopy

import "os"

// BootstrapCode identifies copy needed before the full TUI catalog and config
// are available. This catalog intentionally stays small and dependency-free.
type BootstrapCode string

const (
	BootstrapTUIUsage            BootstrapCode = "tui.usage"
	BootstrapBareUsage           BootstrapCode = "bare.usage"
	BootstrapFlagSocket          BootstrapCode = "flag.socket"
	BootstrapFlagSession         BootstrapCode = "flag.session"
	BootstrapFlagWorkspace       BootstrapCode = "flag.workspace"
	BootstrapFlagLocale          BootstrapCode = "flag.locale"
	BootstrapFlagNoAltScreen     BootstrapCode = "flag.no_alt_screen"
	BootstrapInteractiveRequired BootstrapCode = "interactive.required"
	BootstrapResolveHomeFailed   BootstrapCode = "home.failed"
	BootstrapConfigFailed        BootstrapCode = "config.failed"
	BootstrapLocaleInvalid       BootstrapCode = "locale.invalid"
	BootstrapRecoveryFailed      BootstrapCode = "recovery.failed"
	BootstrapStartupFailed       BootstrapCode = "startup.failed"
	BootstrapRuntimeFailed       BootstrapCode = "runtime.failed"
)

var bootstrapCatalog = map[BootstrapCode]map[string]string{
	BootstrapTUIUsage: {
		"en": "Usage: carina [options]  (interactive shell; carina help for CLI).",
		"zh": "用法：carina [选项]（交互壳；CLI 请用 carina help）。",
		"ja": "使い方: carina [オプション]（対話シェル。CLI は carina help）。",
		"ko": "사용법: carina [옵션] (대화형 셸; CLI는 carina help).",
		"es": "Uso: carina [opciones] (shell interactivo; carina help para la CLI).",
		"fr": "Utilisation : carina [options] (shell interactif ; carina help pour la CLI).",
	},
	BootstrapBareUsage: {
		"en": "Usage: carina <command> [arguments]. Run `carina help` for the command list.",
		"zh": "用法：carina <命令> [参数]。运行 `carina help` 查看命令列表。",
		"ja": "使い方: carina <コマンド> [引数]。一覧は `carina help` で確認できます。",
		"ko": "사용법: carina <명령> [인수]. 명령 목록은 `carina help`로 확인하세요.",
		"es": "Uso: carina <comando> [argumentos]. Ejecute `carina help` para ver la lista de comandos.",
		"fr": "Utilisation : carina <commande> [arguments]. Exécutez `carina help` pour afficher les commandes.",
	},
	BootstrapFlagSocket: {
		"en": "Carina daemon Unix socket",
		"zh": "Carina 守护进程 Unix 套接字",
		"ja": "Carina デーモンの Unix ソケット",
		"ko": "Carina 데몬 Unix 소켓",
		"es": "Socket Unix del daemon de Carina",
		"fr": "Socket Unix du daemon Carina",
	},
	BootstrapFlagSession: {
		"en": "Attach to an existing session ID (default: create one)",
		"zh": "连接现有会话 ID（默认：新建）",
		"ja": "既存のセッション ID に接続（既定: 新規作成）",
		"ko": "기존 세션 ID에 연결(기본값: 새로 생성)",
		"es": "Conectarse a un ID de sesión existente (predeterminado: crear una)",
		"fr": "Se connecter à un ID de session existant (par défaut : en créer une)",
	},
	BootstrapFlagWorkspace: {
		"en": "Workspace root for session.create (default: current directory)",
		"zh": "session.create 的工作区根目录（默认：当前目录）",
		"ja": "session.create のワークスペースルート（既定: 現在のディレクトリ）",
		"ko": "session.create의 작업 공간 루트(기본값: 현재 디렉터리)",
		"es": "Raíz del espacio de trabajo para session.create (predeterminado: directorio actual)",
		"fr": "Racine de l’espace de travail pour session.create (par défaut : dossier courant)",
	},
	BootstrapFlagLocale: {
		"en": "Copy locale: en, zh-CN, ja, ko, es, fr (default: environment, config, or system)",
		"zh": "文案语言：en、zh-CN、ja、ko、es、fr（默认：环境、配置或系统）",
		"ja": "表示言語: en、zh-CN、ja、ko、es、fr（既定: 環境、設定、またはシステム）",
		"ko": "문구 언어: en, zh-CN, ja, ko, es, fr(기본값: 환경, 설정 또는 시스템)",
		"es": "Idioma del texto: en, zh-CN, ja, ko, es, fr (predeterminado: entorno, configuración o sistema)",
		"fr": "Langue des textes : en, zh-CN, ja, ko, es, fr (par défaut : environnement, configuration ou système)",
	},
	BootstrapFlagNoAltScreen: {
		"en": "Use the normal terminal buffer and preserve native scrollback",
		"zh": "使用普通终端缓冲区并保留原生滚动历史",
		"ja": "通常のターミナルバッファーを使用し、ネイティブのスクロール履歴を保持",
		"ko": "기본 터미널 버퍼를 사용하고 기본 스크롤백 기록 유지",
		"es": "Usar el búfer normal del terminal y conservar el historial nativo",
		"fr": "Utiliser le tampon normal du terminal et conserver l’historique natif",
	},
	BootstrapInteractiveRequired: {
		"en": "An interactive terminal is required. Use `carina watch --json` for pipes.",
		"zh": "需要交互式终端。管道请使用 `carina watch --json`。",
		"ja": "対話型ターミナルが必要です。パイプでは `carina watch --json` を使用してください。",
		"ko": "대화형 터미널이 필요합니다. 파이프에서는 `carina watch --json`을 사용하세요.",
		"es": "Se requiere un terminal interactivo. Para canalizaciones, use `carina watch --json`.",
		"fr": "Un terminal interactif est requis. Pour un pipeline, utilisez `carina watch --json`.",
	},
	BootstrapResolveHomeFailed: localizedReason(
		"Could not resolve the home directory: {reason}.", "无法解析主目录：{reason}。", "ホームディレクトリを確認できません: {reason}。",
		"홈 디렉터리를 확인할 수 없습니다: {reason}.", "No se pudo resolver el directorio personal: {reason}.", "Impossible de déterminer le dossier personnel : {reason}."),
	BootstrapConfigFailed: localizedReason(
		"Configuration could not be loaded: {reason}.", "无法加载配置：{reason}。", "設定を読み込めません: {reason}。",
		"설정을 불러올 수 없습니다: {reason}.", "No se pudo cargar la configuración: {reason}.", "Impossible de charger la configuration : {reason}."),
	BootstrapLocaleInvalid: {
		"en": "Locale selection is invalid. Supported values: en, zh-CN, zh-Hans, ja, ko, es, fr.",
		"zh": "语言选择无效。支持：en、zh-CN、zh-Hans、ja、ko、es、fr。",
		"ja": "ロケールの指定が無効です。対応値: en、zh-CN、zh-Hans、ja、ko、es、fr。",
		"ko": "언어 설정이 잘못되었습니다. 지원 값: en, zh-CN, zh-Hans, ja, ko, es, fr.",
		"es": "La configuración regional no es válida. Valores admitidos: en, zh-CN, zh-Hans, ja, ko, es, fr.",
		"fr": "Le choix de langue est invalide. Valeurs prises en charge : en, zh-CN, zh-Hans, ja, ko, es, fr.",
	},
	BootstrapRecoveryFailed: localizedReason(
		"Submission recovery failed: {reason}.", "提交恢复失败：{reason}。", "送信の復旧に失敗しました: {reason}。",
		"제출 복구에 실패했습니다: {reason}.", "No se pudo recuperar el envío: {reason}.", "La récupération de l’envoi a échoué : {reason}."),
	BootstrapStartupFailed: localizedReason(
		"Startup failed: {reason}.", "启动失败：{reason}。", "起動に失敗しました: {reason}。",
		"시작하지 못했습니다: {reason}.", "No se pudo iniciar: {reason}.", "Le démarrage a échoué : {reason}."),
	BootstrapRuntimeFailed: localizedReason(
		"The terminal session ended with an error: {reason}.", "终端会话因错误结束：{reason}。", "ターミナルセッションがエラーで終了しました: {reason}。",
		"터미널 세션이 오류로 종료되었습니다: {reason}.", "La sesión de terminal terminó con un error: {reason}.", "La session du terminal s’est terminée avec une erreur : {reason}."),
}

func localizedReason(en, zh, ja, ko, es, fr string) map[string]string {
	return map[string]string{"en": en, "zh": zh, "ja": ja, "ko": ko, "es": es, "fr": fr}
}

// Bootstrap renders startup copy through the same safe placeholder renderer.
func Bootstrap(code BootstrapCode, args Args, locale string) string {
	locale = NormalizeLocale(locale)
	locales, ok := bootstrapCatalog[code]
	if !ok {
		return governedFallback[locale]
	}
	line, valid := renderTemplate(locales[locale], args)
	if !valid || line == "" {
		return governedFallback[locale]
	}
	return line
}

// DetectBootstrapLocale uses an explicit valid CARINA_LOCALE when available;
// an invalid value falls back to the OS locale so the validation error itself
// can still be presented safely.
func DetectBootstrapLocale() string {
	if value := os.Getenv("CARINA_LOCALE"); value != "" {
		if locale, err := CanonicalLocale(value); err == nil {
			return locale
		}
	}
	return DetectSystemLocale()
}
