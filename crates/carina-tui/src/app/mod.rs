mod render;

use std::collections::{HashMap, VecDeque};
use std::fs;
use std::io::{self, Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{self, Receiver, Sender};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, anyhow};
use crossterm::event::{
    self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers, MouseButton, MouseEvent,
    MouseEventKind,
};
use crossterm::event::{DisableBracketedPaste, DisableFocusChange, DisableMouseCapture};
use crossterm::event::{EnableBracketedPaste, EnableFocusChange, EnableMouseCapture};
use crossterm::execute;
use crossterm::terminal::{
    EnterAlternateScreen, LeaveAlternateScreen, disable_raw_mode, enable_raw_mode,
};
use ratatui::backend::CrosstermBackend;
use ratatui::layout::{Position, Rect};
use ratatui::text::Line;
use ratatui::widgets::{Paragraph, Widget, Wrap};
use ratatui::{TerminalOptions, Viewport};
use xai_ratatui_inline::{
    Terminal, emit_to_scrollback, resize_purge_rerender, with_synchronized_output,
};
use xai_ratatui_textarea::{ElementId, TextArea, TextAreaState, TextElementEventKind};

use crate::command::{self, CommandId, CommandSuggestion, SuggestionExecution};
use crate::component::{Action, InteractionMap};
use crate::context_completion::ContextCompletion;
use crate::context_completion::FILE_ELEMENT_KIND;
use crate::conversation::{ExecutionTimer, execution_status_animates};
use crate::density::DensityMode;
use crate::file_viewer::{
    FileViewer, FileViewerLoad, FileViewerOrigin, MAX_PREVIEW_BYTES, parse_file_reference,
};
use crate::frame_scheduler::{
    FeedbackMarker, FrameScheduler, RedrawReason, TickDemand, WaitPlan, wait_plan,
};
use crate::history_search::HistorySearchState;
use crate::hyperlink::{HyperlinkSupport, MarkdownLink, markdown_links};
use crate::i18n::{Locale, MessageId, Notice, format as tr_format, text as tr};
use crate::keybinding::KeyBindings;
use crate::media::{
    IMAGE_ELEMENT_KIND, MediaChipLabels, MediaComposer, MediaSourceLabel, MediaUploadWork,
    inspect_image, pasted_image_path,
};
use crate::native_scrollback::{
    ScrollbackLedger, ScrollbackStamp, ScrollbackWrap, TranscriptReflowState, history_for_width,
    is_plain_url_line, raw_block_text, reflow_line_cap,
};
use crate::overlay::{
    AgentDashboardOverlay, ApprovalScope, ChangesOverlay, HelpOverlay, Overlay, OverlayStack,
    PlanReviewOverlay, QueueOverlay, RetainedLoad, SettingsOverlay, StatusOverlay,
};
use crate::prerequisite::ProviderPickerState;
use crate::product_projection::ProductProjection;
use crate::rpc::{
    Client, EffectiveConfig, ExecutionLifecycle, ExecutionLifecycleReducer,
    ExecutionLifecycleReduction, ExecutionRun, GovernanceId, Model, ModelInventory, ReceivedEvent,
    RpcError, RuntimeInitialize, Session, SessionItemEvent, WireEvent, spawn_event_stream,
};
use crate::session_browser::{SessionBrowserState, SessionScope};
use crate::sync_output::SyncOutputSupport;
use crate::terminal_graphics::{MediaPreviewPlacement, TerminalGraphics};
use crate::terminal_writer::TerminalWriter;
use crate::theme::Theme;
use crate::transcript::{TranscriptBlock, TranscriptReducer};

const LOCALES: &[(&str, &str)] = &[
    ("en", "English"),
    ("zh-Hans", "简体中文"),
    ("zh-Hant", "繁體中文"),
    ("ja", "日本語"),
    ("ko", "한국어"),
    ("es", "Español"),
    ("fr", "Français"),
];
const DEFAULT_REWIND_PRIME_WINDOW: Duration = Duration::from_millis(800);
const REWIND_GRACE_ENV: &str = "CARINA_ESC_GRACE_MS";
const MIN_REWIND_GRACE_MS: u64 = 250;
const MAX_REWIND_GRACE_MS: u64 = 2_000;
const SETTINGS_ITEM_COUNT: usize = 9;
const IMPORT_ERROR_LIMIT: u64 = 1024;
const IMPORT_HELPER_TIMEOUT: Duration = Duration::from_secs(20);

#[derive(Debug, Clone, Copy)]
struct TranscriptHeightCacheEntry {
    revision: u64,
    width: u16,
    locale: Locale,
    density: DensityMode,
    expand_key: &'static str,
    height: usize,
    header_height: usize,
}

#[derive(Debug, Clone)]
struct TranscriptRenderCacheEntry {
    revision: u64,
    width: u16,
    locale: Locale,
    density: DensityMode,
    expand_key: &'static str,
    lines: Vec<Line<'static>>,
}

type TranscriptHeightCache = HashMap<String, TranscriptHeightCacheEntry>;
type TranscriptRenderCache = HashMap<String, TranscriptRenderCacheEntry>;

#[derive(Debug, Clone)]
pub struct Options {
    pub socket: PathBuf,
    pub workspace: PathBuf,
    pub session_id: Option<String>,
    pub locale: Option<String>,
    pub locale_path: Option<PathBuf>,
    pub density: DensityMode,
    pub density_path: Option<PathBuf>,
    pub carina_bin: Option<PathBuf>,
    pub no_alt_screen: bool,
    pub screen_mode: Option<ScreenMode>,
    pub screen_handoff: Option<ScreenModeHandoff>,
    pub alt_screen: AltScreenPolicy,
    pub scrollback_wrap: ScrollbackWrap,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum ScreenMode {
    #[default]
    Minimal,
    Fullscreen,
    Inline,
}

impl ScreenMode {
    pub fn as_arg(self) -> &'static str {
        match self {
            Self::Minimal => "minimal",
            Self::Fullscreen => "fullscreen",
            Self::Inline => "inline",
        }
    }
}

#[derive(Debug, Clone, Default, serde::Serialize, serde::Deserialize)]
pub struct ScreenModeHandoff {
    session_id: String,
    runtime_id: String,
    runtime_epoch: String,
    runtime_process_epoch: i64,
    runtime_pid: i64,
    draft: String,
    queued_prompts: Vec<String>,
    committed_scrollback: Vec<ScrollbackStamp>,
    pending_governance: Vec<GovernanceId>,
    selected_block_id: Option<String>,
    transcript_scroll: usize,
    transcript_follow_bottom: bool,
}

pub fn read_screen_handoff(path: &Path) -> Result<ScreenModeHandoff> {
    let raw = fs::read(path).context("read screen mode handoff")?;
    let _ = fs::remove_file(path);
    if raw.len() > 256 * 1024 {
        return Err(anyhow!("screen mode handoff exceeds 256 KiB"));
    }
    serde_json::from_slice(&raw).context("decode screen mode handoff")
}

fn screen_handoff_identity_matches(
    session_id: Option<&str>,
    identity: &crate::rpc::RuntimeIdentity,
    handoff: &ScreenModeHandoff,
) -> bool {
    session_id == Some(handoff.session_id.as_str())
        && identity.runtime_id == handoff.runtime_id
        && identity.epoch == handoff.runtime_epoch
        && identity.process_epoch == handoff.runtime_process_epoch
        && identity.pid == handoff.runtime_pid
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub enum AltScreenPolicy {
    #[default]
    Auto,
    Always,
    Never,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Outcome {
    Ok,
    RuntimeError,
    Usage,
    Degraded,
}

impl Outcome {
    pub fn exit_code(self) -> i32 {
        match self {
            Self::Ok => 0,
            Self::RuntimeError => 1,
            Self::Usage => 2,
            Self::Degraded => 6,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Phase {
    Locale,
    Provider,
    Credential,
    Model,
    Session,
    Conversation,
    Diagnostic,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Focus {
    Scene,
    Composer,
}

#[derive(Debug, Clone, PartialEq, Eq)]
enum ProviderImportState {
    Idle,
    Reviewing {
        provider_id: String,
    },
    Validating {
        provider_id: String,
        generation: u64,
        started_at: Instant,
    },
    Failed {
        provider_id: String,
        message: String,
    },
}

impl ProviderImportState {
    fn provider_id(&self) -> Option<&str> {
        match self {
            Self::Idle => None,
            Self::Reviewing { provider_id }
            | Self::Validating { provider_id, .. }
            | Self::Failed { provider_id, .. } => Some(provider_id),
        }
    }

    fn is_reviewing(&self, provider_id: &str) -> bool {
        matches!(self, Self::Reviewing { provider_id: active } if active == provider_id)
    }

    fn validation_elapsed(&self, provider_id: &str) -> Option<Duration> {
        match self {
            Self::Validating {
                provider_id: active,
                started_at,
                ..
            } if active == provider_id => Some(started_at.elapsed()),
            _ => None,
        }
    }

    fn failure(&self, provider_id: &str) -> Option<&str> {
        match self {
            Self::Failed {
                provider_id: active,
                message,
            } if active == provider_id => Some(message),
            _ => None,
        }
    }

    fn accepts_result(&self, provider_id: &str, generation: u64) -> bool {
        matches!(
            self,
            Self::Validating {
                provider_id: active,
                generation: active_generation,
                ..
            } if active == provider_id && *active_generation == generation
        )
    }
}

enum AsyncMessage {
    Terminal(Result<Event, String>),
    AgentsLoaded {
        generation: u64,
        session_id: String,
        result: Result<Box<AgentsLoadOutcome>, String>,
    },
    AgentRecapLoaded {
        generation: u64,
        task_id: String,
        result: Result<crate::rpc::AgentRecap, String>,
    },
    ChangesLoaded {
        generation: u64,
        session_id: String,
        result: Result<Box<ProductProjection>, String>,
    },
    ContextSummaryLoaded {
        generation: u64,
        session_id: String,
        result: Result<crate::rpc::ContextSummary, String>,
    },
    CommandRegistryLoaded {
        generation: u64,
        session_id: String,
        result: Result<crate::rpc::CommandRegistry, String>,
    },
    CredentialStored {
        generation: u64,
        provider: String,
        result: Result<(), String>,
    },
    ProviderImported {
        generation: u64,
        provider: String,
        result: Result<(), String>,
    },
    MediaUploaded {
        element_id: ElementId,
        generation: u64,
        result: Result<crate::rpc::MediaRef, String>,
    },
    ClipboardCaptured {
        generation: u64,
        result: Result<crate::clipboard_image::ClipboardContent, String>,
    },
    WorkspaceFilesLoaded {
        generation: u64,
        session_id: String,
        result: Result<Vec<crate::rpc::WorkspaceFile>, String>,
    },
    WorkspaceFileLoaded {
        generation: u64,
        session_id: String,
        path: String,
        result: Result<crate::rpc::WorkspaceFileContent, String>,
    },
    Event {
        generation: u64,
        value: Box<Result<ReceivedEvent, RpcError>>,
    },
    Reconnect {
        generation: u64,
    },
    RuntimeReconnected {
        generation: u64,
        session_id: String,
        result: Result<Box<RuntimeReconnectOutcome>, String>,
    },
    SessionLoaded {
        generation: u64,
        target_id: String,
        result: Result<HistoryBranchOutcome, String>,
    },
    SessionPreviewLoaded {
        generation: u64,
        session_id: String,
        result: Result<Vec<SessionItemEvent>, String>,
    },
    ToolArtifactLoaded {
        generation: u64,
        session_id: String,
        call_id: String,
        result: Result<crate::rpc::ArtifactText, String>,
    },
    HistoryBranch {
        generation: u64,
        source_session_id: String,
        selected_block_id: String,
        result: Result<HistoryBranchOutcome, String>,
    },
    PausedResume {
        generation: u64,
        session_id: String,
        run_id: String,
        result: Result<PausedResumeOutcome, String>,
    },
}

impl AsyncMessage {
    fn redraw_reason(&self) -> RedrawReason {
        match self {
            Self::Terminal(Ok(Event::Resize(_, _))) => RedrawReason::Resize,
            Self::Terminal(Ok(Event::FocusGained | Event::FocusLost)) => RedrawReason::Focus,
            Self::Terminal(_) => RedrawReason::Input,
            Self::Event { .. } => RedrawReason::Stream,
            Self::MediaUploaded { .. } | Self::ClipboardCaptured { .. } => RedrawReason::Media,
            Self::Reconnect { .. } | Self::RuntimeReconnected { .. } => RedrawReason::Recovery,
            _ => RedrawReason::AsyncResult,
        }
    }
}

struct AgentsLoadOutcome {
    projection: ProductProjection,
}

struct PausedResumeOutcome {
    execution: ExecutionRun,
    session: Option<Session>,
    items: Option<Vec<SessionItemEvent>>,
    refresh_error: Option<String>,
}

struct HistoryBranchOutcome {
    session: Session,
    items: Vec<SessionItemEvent>,
    prompt_history: Vec<String>,
    prompt_history_unavailable: bool,
}

struct RuntimeReconnectOutcome {
    rpc: Client,
    runtime: RuntimeInitialize,
    inventory: ModelInventory,
    sessions: Vec<Session>,
    session: Session,
    items: Vec<SessionItemEvent>,
    prompt_history: Vec<String>,
    prompt_history_unavailable: bool,
    security_context: Option<EffectiveConfig>,
}

#[derive(Clone)]
struct PendingSubmission {
    session_id: String,
    prompt: String,
    model: String,
    agent: String,
    locale: String,
    submission_id: String,
    media_refs: Vec<crate::rpc::MediaRef>,
    local_id: String,
}

pub struct App {
    options: Options,
    rpc: Client,
    runtime: RuntimeInitialize,
    inventory: ModelInventory,
    sessions: Vec<Session>,
    models: Vec<Model>,
    phase: Phase,
    focus: Focus,
    locale_index: usize,
    provider_index: usize,
    provider_picker: ProviderPickerState,
    provider_import: ProviderImportState,
    model_index: usize,
    /// Reasoning effort for the highlighted model (session preference).
    selected_reasoning_effort: String,
    session_browser: SessionBrowserState,
    selected_model: String,
    active_session: Option<Session>,
    security_context: Option<EffectiveConfig>,
    density: DensityMode,
    tool_disclosure_overrides: HashMap<String, bool>,
    blocks: Vec<TranscriptBlock>,
    scrollback: ScrollbackLedger,
    transcript_reflow: TranscriptReflowState,
    transcript_reducer: TranscriptReducer,
    execution_lifecycle: ExecutionLifecycleReducer,
    composer: TextArea,
    composer_state: TextAreaState,
    media: MediaComposer,
    pending_submission: Option<PendingSubmission>,
    clipboard_generation: u64,
    pending_pastes: HashMap<u64, crate::clipboard_image::PendingPaste>,
    submit_after_paste: bool,
    composer_area: Rect,
    slash_selected: usize,
    slash_selected_id: Option<String>,
    slash_dismissed_input: Option<String>,
    command_registry: crate::rpc::CommandRegistry,
    command_registry_session: String,
    command_generation: u64,
    command_mru: Vec<String>,
    command_registry_stale: bool,
    persisted_prompt_history: Vec<String>,
    persisted_prompt_history_unavailable: bool,
    history_search: Option<HistorySearchState>,
    context_completion: ContextCompletion,
    file_viewer_generation: u64,
    product_generation: u64,
    context_generation: u64,
    context_summary: Option<crate::rpc::ContextSummary>,
    transcript_area: Rect,
    transcript_scroll: usize,
    transcript_max_scroll: usize,
    transcript_follow_bottom: bool,
    transcript_height_cache: TranscriptHeightCache,
    transcript_render_cache: TranscriptRenderCache,
    history_selected: Option<usize>,
    history_stashed_draft: Option<String>,
    history_original_scroll: Option<(usize, bool)>,
    history_generation: u64,
    history_branch_request_id: Option<String>,
    resume_generation: u64,
    resume_pending: bool,
    history_branch_pending: bool,
    rewind_primed_at: Option<Instant>,
    rewind_prime_window: Duration,
    /// First Ctrl-C (hard_cancel) while idle primes quit; second within grace exits.
    quit_primed_at: Option<Instant>,
    credential: String,
    credential_generation: u64,
    credential_pending: bool,
    credential_child: Arc<Mutex<Option<Child>>>,
    notice: Notice,
    interactions: InteractionMap,
    overlays: OverlayStack,
    active_run_id: Option<String>,
    execution_timer: ExecutionTimer,
    execution_status: String,
    execution_activity: Option<String>,
    keybindings: KeyBindings,
    queued_prompts: VecDeque<String>,
    event_generation: u64,
    event_cursor: usize,
    transcript_stale: bool,
    theme: Theme,
    async_tx: Sender<AsyncMessage>,
    async_rx: Receiver<AsyncMessage>,
    pending_async: VecDeque<AsyncMessage>,
    pending_feedback: Vec<FeedbackMarker>,
    terminal_focused: bool,
    terminal_resized: bool,
    redraw_reasons: Vec<RedrawReason>,
    quit: bool,
    outcome: Outcome,
    dirty: bool,
    graphics_enabled: bool,
    media_preview_placement: Option<MediaPreviewPlacement>,
    relaunch_screen_mode: Option<ScreenMode>,
    screen_handoff: Option<ScreenModeHandoff>,
    screen_handoff_failed: bool,
}

impl App {
    fn ui_locale(&self) -> Locale {
        self.options
            .locale
            .as_deref()
            .and_then(Locale::from_product_id)
            .unwrap_or_else(|| Locale::ALL[self.locale_index.min(Locale::ALL.len() - 1)])
    }

    fn effective_block_expanded(&self, block: &TranscriptBlock) -> bool {
        self.tool_disclosure_overrides
            .get(&block.id)
            .copied()
            .unwrap_or_else(|| {
                block.expanded
                    || self.density.profile().default_tool_expanded
                        && block.is_collapsible()
                        && matches!(
                            crate::semantic_cell::SemanticCellKind::from_block(block),
                            crate::semantic_cell::SemanticCellKind::Tool
                                | crate::semantic_cell::SemanticCellKind::ToolGroup
                        )
            })
    }

    fn clear_transcript_projection_caches(&mut self) {
        self.transcript_height_cache.clear();
        self.transcript_render_cache.clear();
    }

    fn set_block_disclosure(&mut self, id: &str, expanded: bool) -> bool {
        if !self
            .blocks
            .iter()
            .any(|block| block.id == id && block.is_collapsible())
        {
            return false;
        }
        self.tool_disclosure_overrides
            .insert(id.to_owned(), expanded);
        self.clear_transcript_projection_caches();
        true
    }

    fn toggle_density(&mut self) {
        let next = self.density.toggled();
        let path = self
            .options
            .density_path
            .clone()
            .or_else(default_locale_path);
        match path.and_then(|path| persist_density(&path, next).ok().map(|_| path)) {
            Some(_) => {
                self.density = next;
                self.clear_transcript_projection_caches();
            }
            None => self.notice = Notice::localized(MessageId::DensityPersistFailed),
        }
    }

    fn media_chip_labels(&self) -> MediaChipLabels<'static> {
        let locale = self.ui_locale();
        MediaChipLabels {
            uploading: tr(locale, MessageId::MediaUploadingLabel),
            image: tr(locale, MessageId::MediaImageLabel),
            failed: tr(locale, MessageId::MediaFailedLabel),
        }
    }

    fn bootstrap(options: Options) -> Result<Self> {
        crate::clipboard_image::cleanup_orphaned_temp_images();
        let mut rpc = Client::connect(&options.socket)
            .with_context(|| format!("connect {}", options.socket.display()))?;
        let runtime = rpc.initialize().context("initialize runtime protocol")?;
        if let Some(handoff) = options.screen_handoff.as_ref()
            && !screen_handoff_identity_matches(
                options.session_id.as_deref(),
                &runtime.runtime,
                handoff,
            )
        {
            return Err(anyhow!(
                "screen mode handoff identity no longer matches the runtime"
            ));
        }
        let inventory = rpc
            .model_inventory()
            .context("load provider/model inventory")?;
        let mut sessions = rpc.sessions().context("load sessions")?;
        sort_sessions_by_recency(&mut sessions);
        let models = inventory.available_models();
        let locale_index = locale_selection_index(options.locale.as_deref());
        let phase = startup_phase(
            options.locale.as_deref().is_some_and(is_supported_locale),
            &inventory,
            &models,
        );
        let model_index = models
            .iter()
            .position(|model| model.id == inventory.default_model)
            .unwrap_or(0);
        let selected_model = models
            .get(model_index)
            .map(|model| model.id.clone())
            .unwrap_or_default();
        let provider_index = inventory
            .providers
            .iter()
            .position(|provider| {
                provider
                    .models
                    .iter()
                    .any(|model| model.id == selected_model)
            })
            .or_else(|| {
                inventory
                    .providers
                    .iter()
                    .position(|provider| provider.registered && provider.available)
            })
            .or_else(|| {
                inventory.providers.iter().position(|provider| {
                    provider.source_kind == "cc-switch"
                        && provider.source_route == "managed_proxy"
                        && provider.source_importable
                })
            })
            .unwrap_or(0);
        let mut composer = TextArea::new();
        composer.show_scrollbar = false;
        composer.set_tab_width(4);
        let (async_tx, async_rx) = mpsc::channel();
        let density = options.density;

        let mut app = Self {
            options,
            rpc,
            runtime,
            inventory,
            sessions,
            models,
            phase,
            focus: Focus::Scene,
            locale_index,
            provider_index,
            provider_picker: ProviderPickerState::default(),
            provider_import: ProviderImportState::Idle,
            model_index,
            selected_reasoning_effort: String::new(),
            session_browser: SessionBrowserState::default(),
            selected_model,
            active_session: None,
            security_context: None,
            density,
            tool_disclosure_overrides: HashMap::new(),
            blocks: Vec::new(),
            scrollback: ScrollbackLedger::default(),
            transcript_reflow: TranscriptReflowState::default(),
            transcript_reducer: TranscriptReducer::default(),
            execution_lifecycle: ExecutionLifecycleReducer::default(),
            composer,
            composer_state: TextAreaState::default(),
            media: MediaComposer::default(),
            pending_submission: None,
            clipboard_generation: 0,
            pending_pastes: HashMap::new(),
            submit_after_paste: false,
            composer_area: Rect::default(),
            slash_selected: 0,
            slash_selected_id: None,
            slash_dismissed_input: None,
            command_registry: crate::rpc::CommandRegistry::default(),
            command_registry_session: String::new(),
            command_generation: 0,
            command_mru: Vec::new(),
            command_registry_stale: false,
            persisted_prompt_history: Vec::new(),
            persisted_prompt_history_unavailable: false,
            history_search: None,
            context_completion: ContextCompletion::default(),
            file_viewer_generation: 0,
            product_generation: 0,
            context_generation: 0,
            context_summary: None,
            transcript_area: Rect::default(),
            transcript_scroll: 0,
            transcript_max_scroll: 0,
            transcript_follow_bottom: true,
            transcript_height_cache: HashMap::new(),
            transcript_render_cache: HashMap::new(),
            history_selected: None,
            history_stashed_draft: None,
            history_original_scroll: None,
            history_generation: 0,
            history_branch_request_id: None,
            resume_generation: 0,
            resume_pending: false,
            history_branch_pending: false,
            rewind_primed_at: None,
            rewind_prime_window: rewind_prime_window(),
            quit_primed_at: None,
            credential: String::new(),
            credential_generation: 0,
            credential_pending: false,
            credential_child: Arc::new(Mutex::new(None)),
            notice: Notice::default(),
            interactions: InteractionMap::default(),
            overlays: OverlayStack::default(),
            active_run_id: None,
            execution_timer: ExecutionTimer::default(),
            execution_status: "ready".into(),
            execution_activity: None,
            keybindings: KeyBindings::default(),
            queued_prompts: VecDeque::new(),
            event_generation: 0,
            event_cursor: 0,
            transcript_stale: false,
            theme: Theme::detected(None),
            async_tx,
            async_rx,
            pending_async: VecDeque::new(),
            pending_feedback: Vec::new(),
            terminal_focused: true,
            terminal_resized: false,
            redraw_reasons: Vec::new(),
            quit: false,
            outcome: Outcome::Ok,
            dirty: true,
            graphics_enabled: false,
            media_preview_placement: None,
            relaunch_screen_mode: None,
            screen_handoff: None,
            screen_handoff_failed: false,
        };
        if let Some(handoff) = app.options.screen_handoff.take() {
            app.composer.set_text(&handoff.draft);
            app.composer.set_cursor(app.composer.text().len());
            app.queued_prompts = handoff.queued_prompts.iter().cloned().collect();
            app.screen_handoff = Some(handoff);
        }
        if matches!(app.phase, Phase::Model | Phase::Diagnostic) {
            app.route_after_locale();
        }
        Ok(app)
    }

    fn resume_requested_session(&mut self) {
        let Some(session_id) = self.options.session_id.clone() else {
            return;
        };
        let result = self
            .rpc
            .resume_session(&session_id)
            .map_err(anyhow::Error::new)
            .and_then(|session| self.open_session(session));
        if result.is_ok() {
            self.options.session_id = None;
        } else {
            self.open_session_browser();
            self.notice = Notice::localized(MessageId::ConversationUnavailableChooseAnother);
        }
    }

    fn route_after_locale(&mut self) {
        if !self.inventory.has_runnable_provider() {
            self.phase = Phase::Provider;
            self.focus = Focus::Scene;
            self.notice.clear();
            return;
        }
        if self.models.is_empty() {
            // Stay on Provider so the operator can import/repair another route
            // instead of a Diagnostic dead-end with only redetect/exit.
            self.phase = Phase::Provider;
            self.focus = Focus::Scene;
            self.notice = Notice::localized(MessageId::NoCompatibleModels);
            return;
        }
        if self.active_session.is_some() {
            self.phase = Phase::Conversation;
            self.focus = Focus::Composer;
            self.notice.clear();
            return;
        }
        if self.options.session_id.is_some() {
            self.resume_requested_session();
            return;
        }
        if needs_explicit_model_confirmation(&self.sessions, &self.options.workspace) {
            self.phase = Phase::Model;
            self.focus = Focus::Scene;
            self.model_index = self
                .models
                .iter()
                .position(|model| model.id == self.inventory.default_model)
                .unwrap_or(0);
            self.selected_model = self
                .models
                .get(self.model_index)
                .map(|model| model.id.clone())
                .unwrap_or_default();
            self.sync_reasoning_effort_for_selection();
            self.notice.clear();
            return;
        }
        match self.enter_workspace_conversation() {
            Ok(()) => self.notice.clear(),
            Err(error) => {
                self.open_session_browser();
                self.notice = Notice::localized_with(
                    MessageId::ConversationOpenFailed,
                    [("error", error.to_string())],
                );
            }
        }
    }

    fn open_session_browser(&mut self) {
        let refresh_error = match self.rpc.sessions() {
            Ok(mut sessions) => {
                sort_sessions_by_recency(&mut sessions);
                self.sessions = sessions;
                None
            }
            Err(error) => Some(tr_format(
                self.ui_locale(),
                MessageId::ConversationOpenFailed,
                &[("error", &error.to_string())],
            )),
        };
        self.session_browser
            .open(&self.sessions, &self.options.workspace);
        if let Some(error) = refresh_error {
            self.session_browser.show_error(error);
        }
        self.phase = Phase::Session;
        self.focus = Focus::Scene;
        self.request_session_preview();
    }

    fn open_selected_session(&mut self, visible_index: Option<usize>) {
        if let Some(index) = visible_index {
            self.session_browser
                .select_visible(index, &self.sessions, &self.options.workspace);
        }
        let Some(index) = self
            .session_browser
            .selected_index(&self.sessions, &self.options.workspace)
        else {
            self.session_browser
                .show_error(tr(self.ui_locale(), MessageId::NoWorkspaceConversations).into());
            return;
        };
        let session_id = self.sessions[index].session_id.clone();
        let generation = self.session_browser.begin_load(session_id.clone());
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = load_session_and_items(&socket, &session_id, None);
            let _ = tx.send(AsyncMessage::SessionLoaded {
                generation,
                target_id: session_id,
                result,
            });
        });
    }

    fn create_session_from_browser(&mut self) {
        let target_id = "new".to_owned();
        let generation = self.session_browser.begin_load(target_id.clone());
        let socket = self.options.socket.clone();
        let workspace = self.options.workspace.to_string_lossy().into_owned();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = load_session_and_items(&socket, &target_id, Some(&workspace));
            let _ = tx.send(AsyncMessage::SessionLoaded {
                generation,
                target_id,
                result,
            });
        });
    }

    fn begin_selected_session_rename(&mut self) {
        let Some(session) = self
            .session_browser
            .selected_index(&self.sessions, &self.options.workspace)
            .and_then(|index| self.sessions.get(index))
            .cloned()
        else {
            self.session_browser
                .show_error(tr(self.ui_locale(), MessageId::ConversationRequiredRename).into());
            return;
        };
        self.session_browser.begin_rename(&session);
    }

    fn confirm_session_rename(&mut self) {
        let Some(session_id) = self
            .session_browser
            .renaming_session_id()
            .map(str::to_owned)
        else {
            return;
        };
        let name = self.session_browser.rename_value().trim().to_owned();
        if name.is_empty() {
            self.session_browser
                .show_error(tr(self.ui_locale(), MessageId::ConversationNameEmpty).into());
            return;
        }
        match self.rpc.rename_session(&session_id, &name) {
            Ok(renamed) => {
                if let Some(session) = self
                    .sessions
                    .iter_mut()
                    .find(|session| session.session_id == session_id)
                {
                    session.name = renamed.name.clone();
                }
                if let Some(session) = self
                    .active_session
                    .as_mut()
                    .filter(|session| session.session_id == session_id)
                {
                    session.name = renamed.name.clone();
                }
                self.session_browser
                    .finish_rename(&self.sessions, &self.options.workspace);
                self.notice = Notice::localized_with(
                    MessageId::ConversationRenamed,
                    [("name", renamed.name)],
                );
            }
            Err(error) => self.session_browser.show_error(tr_format(
                self.ui_locale(),
                MessageId::ConversationRenameFailed,
                &[("error", &error.to_string())],
            )),
        }
    }

    fn handle_session_rename_key(&mut self, key: KeyEvent) {
        match key.code {
            KeyCode::Enter => self.confirm_session_rename(),
            KeyCode::Esc => self.session_browser.cancel_rename(),
            KeyCode::Backspace => self.session_browser.backspace_rename(),
            KeyCode::Char(character)
                if !key
                    .modifiers
                    .intersects(KeyModifiers::CONTROL | KeyModifiers::SUPER) =>
            {
                self.session_browser.push_rename(character);
            }
            _ => {}
        }
    }

    fn begin_selected_session_archive(&mut self) {
        let Some(session) = self
            .session_browser
            .selected_index(&self.sessions, &self.options.workspace)
            .and_then(|index| self.sessions.get(index))
            .cloned()
        else {
            self.session_browser
                .show_error(tr(self.ui_locale(), MessageId::ConversationRequiredArchive).into());
            return;
        };
        self.session_browser.begin_archive(&session);
    }

    fn confirm_session_archive(&mut self) {
        let Some(session_id) = self
            .session_browser
            .archive_confirmation_id()
            .map(str::to_owned)
        else {
            return;
        };
        match self.rpc.archive_session(&session_id) {
            Ok(archived) => {
                if let Some(session) = self
                    .sessions
                    .iter_mut()
                    .find(|session| session.session_id == session_id)
                {
                    session.status = archived.status;
                }
                if self
                    .active_session
                    .as_ref()
                    .is_some_and(|session| session.session_id == session_id)
                {
                    self.active_session = None;
                    self.active_run_id = None;
                    self.execution_timer.reset();
                    self.execution_activity = None;
                    self.event_generation = self.event_generation.saturating_add(1);
                }
                self.session_browser
                    .finish_archive_action(&self.sessions, &self.options.workspace);
                self.notice = Notice::localized(MessageId::ConversationArchived);
            }
            Err(error) => self.session_browser.show_error(tr_format(
                self.ui_locale(),
                MessageId::ConversationArchiveFailed,
                &[("error", &error.to_string())],
            )),
        }
    }

    fn unarchive_selected_session(&mut self) {
        let Some(session_id) = self
            .session_browser
            .selected_index(&self.sessions, &self.options.workspace)
            .and_then(|index| self.sessions.get(index))
            .filter(|session| session.status == "closed")
            .map(|session| session.session_id.clone())
        else {
            self.session_browser
                .show_error(tr(self.ui_locale(), MessageId::ArchivedConversationRequired).into());
            return;
        };
        match self.rpc.unarchive_session(&session_id) {
            Ok(restored) => {
                if let Some(session) = self
                    .sessions
                    .iter_mut()
                    .find(|session| session.session_id == session_id)
                {
                    session.status = restored.status;
                }
                self.session_browser
                    .finish_archive_action(&self.sessions, &self.options.workspace);
                self.notice = Notice::localized(MessageId::ConversationRestored);
            }
            Err(error) => self.session_browser.show_error(tr_format(
                self.ui_locale(),
                MessageId::ConversationRestoreFailed,
                &[("error", &error.to_string())],
            )),
        }
    }

    fn apply_session_load(
        &mut self,
        generation: u64,
        target_id: &str,
        result: Result<HistoryBranchOutcome, String>,
    ) {
        if !self.session_browser.accepts_load(generation, target_id) {
            return;
        }
        match result {
            Ok(outcome) => {
                self.session_browser.finish_load(generation, target_id);
                self.persisted_prompt_history = outcome.prompt_history;
                self.persisted_prompt_history_unavailable = outcome.prompt_history_unavailable;
                self.apply_open_session_state(outcome.session, outcome.items);
            }
            Err(error) => self.session_browser.fail_load(
                generation,
                target_id,
                tr_format(
                    self.ui_locale(),
                    MessageId::ConversationOpenFailed,
                    &[("error", &error)],
                ),
            ),
        }
    }

    fn request_session_preview(&mut self) {
        let Some(index) = self
            .session_browser
            .selected_index(&self.sessions, &self.options.workspace)
        else {
            return;
        };
        let session_id = self.sessions[index].session_id.clone();
        let generation = self.session_browser.begin_preview(session_id.clone());
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.items(&session_id))
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::SessionPreviewLoaded {
                generation,
                session_id,
                result,
            });
        });
    }

    fn request_tool_artifact(&self, reference: crate::rpc::ToolArtifactRef) {
        let generation = self.event_generation;
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let session_id = reference.session_id.clone();
            let call_id = reference.call_id.clone();
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.artifact_text(&reference, 256 * 1024))
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::ToolArtifactLoaded {
                generation,
                session_id,
                call_id,
                result,
            });
        });
    }

    fn selected_model_is_runnable(&self) -> bool {
        !self.selected_model.is_empty()
            && self.inventory.providers.iter().any(|provider| {
                self.inventory.is_provider_runnable(provider)
                    && provider
                        .models
                        .iter()
                        .any(|model| model.available && model.id == self.selected_model)
            })
    }

    fn open_models(&mut self) {
        let inventory = match self.rpc.model_inventory() {
            Ok(inventory) => inventory,
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::ReadinessCheckFailedDraftKept,
                    [("error", error.to_string())],
                );
                self.phase = Phase::Conversation;
                self.focus = Focus::Composer;
                return;
            }
        };
        self.inventory = inventory;
        self.provider_index = self
            .inventory
            .providers
            .iter()
            .position(|provider| {
                self.inventory.is_provider_runnable(provider)
                    && provider
                        .models
                        .iter()
                        .any(|model| model.available && model.id == self.selected_model)
            })
            .or_else(|| {
                self.inventory
                    .providers
                    .iter()
                    .position(|provider| self.inventory.is_provider_runnable(provider))
            })
            .unwrap_or(self.provider_index);
        self.models = self
            .inventory
            .providers
            .get(self.provider_index)
            .map(|provider| {
                provider
                    .models
                    .iter()
                    .filter(|model| model.available)
                    .cloned()
                    .collect()
            })
            .unwrap_or_default();
        self.model_index = self
            .models
            .iter()
            .position(|model| model.id == self.selected_model)
            .unwrap_or(0);
        self.phase = Phase::Model;
        self.focus = Focus::Scene;
        self.sync_reasoning_effort_for_selection();
    }

    fn return_to_conversation_or_repair(&mut self) -> bool {
        match self.rpc.model_inventory() {
            Ok(inventory) => self.inventory = inventory,
            Err(error) => {
                self.phase = Phase::Provider;
                self.focus = Focus::Scene;
                self.notice = Notice::localized_with(
                    MessageId::ReadinessCheckFailedDraftKept,
                    [("error", error.to_string())],
                );
                return false;
            }
        }
        if self.active_session.is_some() && self.selected_model_is_runnable() {
            self.phase = Phase::Conversation;
            self.focus = Focus::Composer;
            true
        } else {
            self.phase = Phase::Provider;
            self.focus = Focus::Scene;
            self.notice = Notice::localized(MessageId::ExecutionNotReadyDraftKept);
            false
        }
    }

    fn enter_workspace_conversation(&mut self) -> Result<()> {
        self.enter_workspace_conversation_with_model(None)
    }

    fn enter_workspace_conversation_with_model(
        &mut self,
        selected_model: Option<&str>,
    ) -> Result<()> {
        let mut target_session = None;
        for session_id in workspace_session_ids(&self.sessions, &self.options.workspace) {
            if let Ok(session) = self.rpc.resume_session(&session_id) {
                target_session = Some(session);
                break;
            }
        }

        let session = match target_session {
            Some(session) => session,
            None => self
                .rpc
                .create_session(&self.options.workspace.to_string_lossy())
                .context(
                    "create workspace session after existing conversations were unavailable",
                )?,
        };
        self.open_session_with_model(session, selected_model)
    }

    fn open_session(&mut self, session: Session) -> Result<()> {
        self.open_session_with_model(session, None)
    }

    fn open_session_with_model(
        &mut self,
        session: Session,
        selected_model: Option<&str>,
    ) -> Result<()> {
        let mut session = self.hydrate_session_snapshot(session);
        self.security_context = self
            .rpc
            .config_inventory(&session.session_id)
            .ok()
            .map(|config| config.effective);
        let items = self
            .rpc
            .items(&session.session_id)
            .with_context(|| format!("load session {}", session.session_id))?;
        match self.rpc.prompt_history(&session.session_id, 200) {
            Ok(history) => {
                self.persisted_prompt_history = history.entries;
                self.persisted_prompt_history_unavailable = false;
            }
            Err(_) => {
                self.persisted_prompt_history.clear();
                self.persisted_prompt_history_unavailable = true;
            }
        }
        if let Some(model) = selected_model {
            let effort = self.selected_reasoning_effort.clone();
            let selection = self
                .rpc
                .set_session_model(&session.session_id, model, &effort)
                .with_context(|| format!("select model for session {}", session.session_id))?;
            session.next_model = selection.next_model;
            session.next_reasoning_effort = selection.next_reasoning_effort;
            self.selected_reasoning_effort = session.next_reasoning_effort.clone();
        }
        self.apply_open_session_state(session, items);
        Ok(())
    }

    fn apply_open_session_state(&mut self, session: Session, items: Vec<SessionItemEvent>) {
        let session_changed = self
            .active_session
            .as_ref()
            .is_some_and(|active| active.session_id != session.session_id);
        if session_changed {
            self.cancel_pending_pastes();
        }
        self.context_completion.reset_session();
        if self
            .models
            .iter()
            .any(|model| model.id == session.next_model)
        {
            self.selected_model = session.next_model.clone();
        }
        let hydrated_overlays = OverlayStack::hydrate_governance(&items);
        self.execution_lifecycle.clear();
        self.tool_disclosure_overrides.clear();
        self.blocks = self.transcript_reducer.hydrate(items);
        self.scrollback.reset();
        self.reset_transcript_viewport();
        let handoff = self.screen_handoff.take();
        if let Some(handoff) = handoff {
            let stamps_match = self
                .scrollback
                .restore_committed_prefix(&self.blocks, handoff.committed_scrollback)
                .is_ok();
            let governance_matches =
                hydrated_overlays.governance_ids() == handoff.pending_governance;
            let selection = handoff
                .selected_block_id
                .as_deref()
                .map(|id| self.blocks.iter().position(|block| block.id == id));
            let selection_matches = selection.as_ref().is_none_or(Option::is_some);
            self.screen_handoff_failed = handoff.session_id != session.session_id
                || !stamps_match
                || !governance_matches
                || !selection_matches;
            if !self.screen_handoff_failed {
                self.history_selected = selection.flatten();
                self.transcript_scroll = handoff.transcript_scroll;
                self.transcript_follow_bottom = handoff.transcript_follow_bottom;
            } else {
                self.scrollback.reset();
                self.outcome = Outcome::Degraded;
            }
        }
        self.transcript_stale = false;
        self.active_run_id = matches!(
            session.execution_status.as_str(),
            "queued" | "running" | "waiting_input" | "waiting_approval"
        )
        .then(|| session.latest_run_id.clone())
        .filter(|run_id| !run_id.is_empty());
        self.execution_timer.reset();
        self.execution_activity = None;
        self.execution_status = if session.execution_status.is_empty() {
            "ready".into()
        } else {
            session.execution_status.clone()
        };
        self.seed_execution_lifecycle(&session.latest_run_id, &session.execution_status);
        if session.execution_status == "paused" && !session.latest_run_id.is_empty() {
            self.notice = Notice::localized(MessageId::ResumePausedDetail);
        }
        let plan_review = plan_review_overlay(&session);
        self.remember_session(session);
        if session_changed {
            let session_id = self
                .active_session
                .as_ref()
                .map(|active| active.session_id.clone())
                .expect("remembered session is active");
            let labels = self.media_chip_labels();
            for work in self.media.rebind_session(&mut self.composer, labels) {
                self.start_media_upload(session_id.clone(), work);
            }
        }
        self.overlays = hydrated_overlays;
        if let Some(review) = plan_review {
            self.overlays.push(Overlay::PlanReview(review));
        }
        self.return_to_conversation_or_repair();
        if self.screen_handoff_failed {
            self.notice = Notice::localized(MessageId::ScreenModeHandoffRejected);
        }
        self.context_summary = None;
        self.request_context_summary();
        self.event_cursor = 0;
        self.start_event_stream();
    }

    fn hydrate_session_snapshot(&self, mut session: Session) -> Session {
        let Some(snapshot) = self
            .sessions
            .iter()
            .find(|candidate| candidate.session_id == session.session_id)
        else {
            return session;
        };
        session.latest_run_id = snapshot.latest_run_id.clone();
        session.latest_run_agent = snapshot.latest_run_agent.clone();
        session.latest_run_result_kind = snapshot.latest_run_result_kind.clone();
        session.execution_status = snapshot.execution_status.clone();
        session.summary = snapshot.summary.clone();
        session.plan_mode = snapshot.plan_mode;
        session.continuity = snapshot.continuity.clone();
        if session.next_reasoning_effort.is_empty() {
            session.next_reasoning_effort = snapshot.next_reasoning_effort.clone();
        }
        session
    }

    fn remember_session(&mut self, session: Session) {
        let session_changed = self
            .active_session
            .as_ref()
            .is_some_and(|active| active.session_id != session.session_id);
        let command_registry_changed = self.command_registry_session != session.session_id;
        if let Some(existing) = self
            .sessions
            .iter_mut()
            .find(|candidate| candidate.session_id == session.session_id)
        {
            *existing = session.clone();
        } else {
            self.sessions.push(session.clone());
        }
        self.active_session = Some(session);
        if session_changed {
            self.command_mru.clear();
            self.command_registry = crate::rpc::CommandRegistry::default();
            self.command_registry_stale = false;
        }
        if command_registry_changed {
            self.request_command_registry();
        }
    }

    fn request_command_registry(&mut self) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.command_registry = crate::rpc::CommandRegistry::default();
            self.command_registry_session.clear();
            return;
        };
        self.command_generation = self.command_generation.saturating_add(1);
        let generation = self.command_generation;
        self.command_registry_session = session_id.clone();
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.command_registry(&session_id))
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::CommandRegistryLoaded {
                generation,
                session_id,
                result,
            });
        });
    }

    fn update_active_execution_metadata(
        &mut self,
        run_id: &str,
        status: &str,
        agent: Option<&str>,
        summary: Option<&str>,
        result_kind: Option<&str>,
    ) {
        let Some(mut session) = self.active_session.as_ref().cloned() else {
            return;
        };
        session.latest_run_id = run_id.to_owned();
        session.execution_status = status.to_owned();
        if let Some(agent) = agent.filter(|value| !value.is_empty()) {
            session.latest_run_agent = agent.to_owned();
        }
        if let Some(summary) = summary.filter(|value| !value.trim().is_empty()) {
            session.summary = summary.to_owned();
        }
        if let Some(result_kind) = result_kind {
            session.latest_run_result_kind = result_kind.to_owned();
        }
        self.remember_session(session);
    }

    fn seed_execution_lifecycle(&mut self, run_id: &str, status: &str) {
        if let Some(lifecycle) = ExecutionLifecycle::from_status(status) {
            self.execution_lifecycle.seed(run_id, lifecycle);
        }
    }

    fn start_event_stream(&mut self) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        self.event_generation = self.event_generation.saturating_add(1);
        let generation = self.event_generation;
        let (sender, receiver) = mpsc::channel();
        spawn_event_stream(
            self.options.socket.clone(),
            session_id,
            self.event_cursor,
            sender,
        );
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            for event in receiver {
                if tx
                    .send(AsyncMessage::Event {
                        generation,
                        value: Box::new(event),
                    })
                    .is_err()
                {
                    break;
                }
            }
        });
    }

    fn reconnect_runtime(&self, generation: u64) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = reconnect_runtime_and_session(&socket, &session_id).map(Box::new);
            let _ = tx.send(AsyncMessage::RuntimeReconnected {
                generation,
                session_id,
                result,
            });
        });
    }

    fn apply_runtime_reconnect(
        &mut self,
        generation: u64,
        session_id: &str,
        result: Result<Box<RuntimeReconnectOutcome>, String>,
    ) {
        if generation != self.event_generation
            || self
                .active_session
                .as_ref()
                .is_none_or(|session| session.session_id != session_id)
        {
            return;
        }
        let outcome = match result {
            Ok(outcome) => outcome,
            Err(_) => {
                self.notice = Notice::localized(MessageId::RuntimeUnavailable);
                let tx = self.async_tx.clone();
                std::thread::spawn(move || {
                    std::thread::sleep(Duration::from_millis(500));
                    let _ = tx.send(AsyncMessage::Reconnect { generation });
                });
                return;
            }
        };
        let RuntimeReconnectOutcome {
            rpc,
            runtime,
            inventory,
            mut sessions,
            session,
            items,
            prompt_history,
            prompt_history_unavailable,
            security_context,
        } = *outcome;
        sort_sessions_by_recency(&mut sessions);
        self.rpc = rpc;
        self.runtime = runtime;
        self.inventory = inventory;
        self.sessions = sessions;
        self.models = self.inventory.available_models();
        if let Some(index) = self
            .models
            .iter()
            .position(|model| model.id == session.next_model)
        {
            self.model_index = index;
            self.selected_model = session.next_model.clone();
        }
        self.tool_disclosure_overrides.clear();
        self.blocks = self.transcript_reducer.hydrate(items);
        self.scrollback.reset();
        self.transcript_stale = false;
        self.persisted_prompt_history = prompt_history;
        self.persisted_prompt_history_unavailable = prompt_history_unavailable;
        self.security_context = security_context;
        self.active_run_id = matches!(
            session.execution_status.as_str(),
            "queued" | "running" | "waiting_input" | "waiting_approval"
        )
        .then(|| session.latest_run_id.clone())
        .filter(|run_id| !run_id.is_empty());
        self.execution_timer.reset();
        self.execution_activity = None;
        self.execution_status = if session.execution_status.is_empty() {
            "ready".into()
        } else {
            session.execution_status.clone()
        };
        self.seed_execution_lifecycle(&session.latest_run_id, &session.execution_status);
        self.command_registry_session.clear();
        self.remember_session(session);
        self.notice.clear();
        self.start_event_stream();
    }

    fn select_locale(&mut self) {
        let locale = LOCALES[self.locale_index].0;
        let path = self
            .options
            .locale_path
            .clone()
            .or_else(default_locale_path);
        match path.and_then(|path| persist_locale(&path, locale).ok().map(|_| path)) {
            Some(_) => {
                self.options.locale = Some(locale.to_owned());
                let labels = self.media_chip_labels();
                self.media.relabel(&mut self.composer, labels);
                self.route_after_locale();
            }
            None => self.notice = Notice::localized(MessageId::LanguagePersistFailed),
        }
    }

    fn begin_credential(&mut self) {
        if self.inventory.providers.is_empty() {
            self.phase = Phase::Diagnostic;
            self.notice = Notice::localized(MessageId::ProviderDefinitionsUnavailable);
            return;
        }
        self.credential.clear();
        self.credential_pending = false;
        self.phase = Phase::Credential;
        self.notice.clear();
    }

    fn select_provider_and_continue(&mut self) {
        let Some(provider) = self.inventory.providers.get(self.provider_index).cloned() else {
            return;
        };
        if provider.source_kind == "cc-switch" && !provider.available {
            let provider_id = provider.id.clone();
            let provider_name = provider.name.clone();
            let importable = provider.source_importable;
            let reason = provider.source_reason.clone();
            if self.provider_import.provider_id() != Some(provider_id.as_str()) {
                self.clear_provider_import_state();
            }
            if !importable {
                self.provider_import = ProviderImportState::Idle;
                self.notice = if reason.is_empty() {
                    Notice::localized_with(
                        MessageId::ProviderImportUnavailable,
                        [("provider", provider_name)],
                    )
                } else {
                    Notice::localized_with(
                        MessageId::ProviderImportUnavailableReason,
                        [("provider", provider_name), ("reason", reason)],
                    )
                };
                return;
            }
            match self.provider_import.clone() {
                ProviderImportState::Idle => {
                    self.provider_import = ProviderImportState::Reviewing { provider_id };
                    self.notice = if provider.source_action == "use_active_route" {
                        Notice::localized_with(
                            MessageId::ProviderReviewActiveRoute,
                            [("provider", provider_name)],
                        )
                    } else {
                        Notice::localized_with(
                            MessageId::ProviderReviewSavedProfile,
                            [("provider", provider_name)],
                        )
                    };
                }
                ProviderImportState::Reviewing { .. } | ProviderImportState::Failed { .. } => {
                    self.confirm_provider_import();
                }
                ProviderImportState::Validating { .. } => {}
            }
            return;
        }
        self.clear_provider_import_state();
        if !(provider.registered && provider.available) {
            self.begin_credential();
            return;
        }

        if !self.inventory.reasoner.available {
            match self.rpc.model_inventory() {
                Ok(inventory) => {
                    self.inventory = inventory;
                    self.provider_index = self
                        .inventory
                        .providers
                        .iter()
                        .position(|candidate| candidate.id == provider.id)
                        .unwrap_or(self.provider_index);
                }
                Err(error) => {
                    self.phase = Phase::Provider;
                    self.focus = Focus::Scene;
                    self.notice = Notice::localized_with(
                        MessageId::ExecutionReadinessCheckFailed,
                        [("error", error.to_string())],
                    );
                    return;
                }
            }
        }

        let Some(provider) = self.inventory.providers.get(self.provider_index) else {
            self.phase = Phase::Provider;
            self.notice = Notice::localized(MessageId::ProviderSelectionExpired);
            return;
        };
        if !self.inventory.is_provider_runnable(provider) {
            self.phase = Phase::Provider;
            self.focus = Focus::Scene;
            self.notice = Notice::localized_with(
                MessageId::ProviderExecutionNotReady,
                [("provider", provider.name.clone())],
            );
            return;
        }

        self.models = provider
            .models
            .iter()
            .filter(|model| model.available)
            .cloned()
            .collect();
        if self.models.is_empty() {
            self.phase = Phase::Provider;
            self.focus = Focus::Scene;
            self.notice = Notice::localized_with(
                MessageId::ProviderNoCompatibleModels,
                [("provider", provider.name.clone())],
            );
            return;
        }
        self.model_index = self
            .models
            .iter()
            .position(|model| model.id == self.selected_model)
            .or_else(|| {
                self.models
                    .iter()
                    .position(|model| model.id == self.inventory.default_model)
            })
            .unwrap_or(0);
        self.selected_model = self.models[self.model_index].id.clone();
        self.phase = Phase::Model;
        self.focus = Focus::Scene;
        self.sync_reasoning_effort_for_selection();
        self.notice.clear();
    }

    fn confirm_provider_import(&mut self) {
        if matches!(
            &self.provider_import,
            ProviderImportState::Validating { .. }
        ) {
            return;
        }
        let Some(carina_bin) = self.options.carina_bin.clone() else {
            let Some(provider_id) = self.provider_import.provider_id().map(str::to_owned) else {
                return;
            };
            let message = tr(self.ui_locale(), MessageId::InternalCommandUnavailable).to_owned();
            self.provider_import = ProviderImportState::Failed {
                provider_id,
                message: message.clone(),
            };
            self.notice = Notice::localized(MessageId::InternalCommandUnavailable);
            return;
        };
        let Some(provider_id) = self.provider_import.provider_id().map(str::to_owned) else {
            return;
        };
        self.credential_generation += 1;
        let generation = self.credential_generation;
        self.provider_import = ProviderImportState::Validating {
            provider_id: provider_id.clone(),
            generation,
            started_at: Instant::now(),
        };
        self.notice.clear();
        let tx = self.async_tx.clone();
        let credential_child = Arc::clone(&self.credential_child);
        std::thread::spawn(move || {
            let result = import_ccswitch_provider(&carina_bin, &provider_id, &credential_child)
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::ProviderImported {
                generation,
                provider: provider_id,
                result,
            });
        });
    }

    fn cancel_provider_import(&mut self) {
        if matches!(
            &self.provider_import,
            ProviderImportState::Validating { .. }
        ) {
            self.credential_generation += 1;
            if let Some(child) = lock_child(&self.credential_child).as_mut() {
                let _ = child.kill();
            }
            self.notice = Notice::localized(MessageId::ProviderImportCancelled);
        } else {
            self.notice.clear();
        }
        self.provider_import = ProviderImportState::Idle;
    }

    fn clear_provider_import_state(&mut self) {
        if matches!(
            &self.provider_import,
            ProviderImportState::Validating { .. }
        ) {
            self.credential_generation += 1;
            if let Some(child) = lock_child(&self.credential_child).as_mut() {
                let _ = child.kill();
            }
        }
        self.provider_import = ProviderImportState::Idle;
        self.notice.clear();
    }

    fn submit_credential(&mut self) {
        if self.credential_pending || self.credential.trim().is_empty() {
            return;
        }
        let Some(carina_bin) = self.options.carina_bin.clone() else {
            self.notice = Notice::localized(MessageId::InternalCommandUnavailable);
            return;
        };
        let Some(provider) = self.inventory.providers.get(self.provider_index) else {
            return;
        };
        let provider_id = provider.id.clone();
        let secret = std::mem::take(&mut self.credential);
        self.credential_generation += 1;
        let generation = self.credential_generation;
        self.credential_pending = true;
        self.notice = Notice::localized(MessageId::CredentialValidationStarted);
        let tx = self.async_tx.clone();
        let credential_child = Arc::clone(&self.credential_child);
        std::thread::spawn(move || {
            let result =
                store_provider_credential(&carina_bin, &provider_id, secret, &credential_child)
                    .map_err(|_| "Credential validation failed".to_owned());
            let _ = tx.send(AsyncMessage::CredentialStored {
                generation,
                provider: provider_id,
                result,
            });
        });
    }

    fn apply_async(&mut self) {
        while let Some(message) = self
            .pending_async
            .pop_front()
            .or_else(|| self.async_rx.try_recv().ok())
        {
            let redraw_reason = message.redraw_reason();
            match message {
                AsyncMessage::Terminal(Ok(event)) => {
                    if let Event::Resize(width, _) = event {
                        self.terminal_resized = true;
                        self.transcript_reflow
                            .observe(width, self.active_run_id.is_some());
                    }
                    if let Err(error) = self.handle_event(event) {
                        self.notice = error.to_string().into();
                    }
                }
                AsyncMessage::Terminal(Err(error)) => {
                    self.notice = error.into();
                    self.quit = true;
                    self.outcome = Outcome::RuntimeError;
                }
                AsyncMessage::AgentsLoaded {
                    generation,
                    session_id,
                    result,
                } => {
                    let recap_index = {
                        let Some(Overlay::Agents(agents)) = self.overlays.active_mut() else {
                            continue;
                        };
                        if !agents.load.accepts(generation, &session_id) {
                            continue;
                        }
                        match result {
                            Ok(outcome) => {
                                let selected_id = agent_entries(&agents.projection)
                                    .get(agents.selected)
                                    .map(|agent| agent.task_id.as_str());
                                agents.selected = selected_id
                                    .and_then(|selected_id| {
                                        agent_entries(&outcome.projection)
                                            .iter()
                                            .position(|agent| agent.task_id == selected_id)
                                    })
                                    .unwrap_or(0);
                                agents.projection = outcome.projection;
                                agents.recap = None;
                                agents.recap_load = RetainedLoad::default();
                                agents.load.finish(None);
                                Some(agents.selected)
                            }
                            Err(error) => {
                                agents.load.finish(Some(error));
                                None
                            }
                        }
                    };
                    if let Some(index) = recap_index {
                        self.select_agent(index);
                    }
                }
                AsyncMessage::AgentRecapLoaded {
                    generation,
                    task_id,
                    result,
                } => {
                    let Some(Overlay::Agents(agents)) = self.overlays.active_mut() else {
                        continue;
                    };
                    if !agents.recap_load.accepts(generation, &task_id) {
                        continue;
                    }
                    match result {
                        Ok(recap) => {
                            agents.recap = Some(recap);
                            agents.recap_load.finish(None);
                        }
                        Err(error) => {
                            agents.recap = None;
                            agents.recap_load.finish(Some(error));
                        }
                    }
                }
                AsyncMessage::ChangesLoaded {
                    generation,
                    session_id,
                    result,
                } => {
                    let Some(Overlay::Changes(changes)) = self.overlays.active_mut() else {
                        continue;
                    };
                    if !changes.load.accepts(generation, &session_id) {
                        continue;
                    }
                    match result {
                        Ok(projection) => {
                            let selected_patch = changes
                                .projection
                                .patches
                                .get(changes.selected)
                                .map(|patch| patch.patch_id.as_str());
                            let selected_path = changes
                                .projection
                                .workspace_diff
                                .files
                                .get(changes.selected)
                                .map(|file| file.path.as_str());
                            let selected_review = changes
                                .projection
                                .review
                                .changes
                                .get(changes.selected)
                                .map(|change| change.id.as_str());
                            changes.selected = if !projection.patches.is_empty() {
                                selected_patch
                                    .and_then(|patch_id| {
                                        projection
                                            .patches
                                            .iter()
                                            .position(|patch| patch.patch_id == patch_id)
                                    })
                                    .unwrap_or(0)
                            } else if projection.workspace_diff.files.is_empty() {
                                selected_review
                                    .and_then(|id| {
                                        projection
                                            .review
                                            .changes
                                            .iter()
                                            .position(|change| change.id == id)
                                    })
                                    .unwrap_or(0)
                            } else {
                                selected_path
                                    .and_then(|path| {
                                        projection
                                            .workspace_diff
                                            .files
                                            .iter()
                                            .position(|file| file.path == path)
                                    })
                                    .unwrap_or(0)
                            };
                            changes.load.finish(
                                projection
                                    .patches_error
                                    .clone()
                                    .or_else(|| projection.workspace_diff_error.clone()),
                            );
                            changes.projection = *projection;
                            changes.scroll = 0;
                        }
                        Err(error) => changes.load.finish(Some(error)),
                    }
                }
                AsyncMessage::ContextSummaryLoaded {
                    generation,
                    session_id,
                    result,
                } => {
                    let active_session = self
                        .active_session
                        .as_ref()
                        .map(|session| session.session_id.as_str());
                    if generation != self.context_generation
                        || active_session != Some(session_id.as_str())
                    {
                        continue;
                    }
                    if let Ok(summary) = result {
                        if let Some(Overlay::Context(context)) = self.overlays.active_mut() {
                            *context = summary.clone();
                        }
                        self.context_summary = Some(summary);
                    }
                }
                AsyncMessage::CommandRegistryLoaded {
                    generation,
                    session_id,
                    result,
                } => {
                    let active_session = self
                        .active_session
                        .as_ref()
                        .map(|session| session.session_id.as_str());
                    if !command_registry_target_is_current(
                        generation,
                        self.command_generation,
                        active_session,
                        &session_id,
                    ) {
                        continue;
                    }
                    match result {
                        Ok(registry) => {
                            self.command_registry = registry;
                            self.command_registry_session = session_id;
                            self.command_registry_stale = false;
                            self.sync_slash_selection();
                        }
                        Err(error) => {
                            self.command_registry_stale = true;
                            self.command_registry_session = session_id;
                            self.sync_slash_selection();
                            if self.composer.text().trim_start().starts_with('/') {
                                self.notice = Notice::localized_with(
                                    MessageId::CommandRegistryLoadFailed,
                                    [("error", error)],
                                );
                            }
                        }
                    }
                }
                AsyncMessage::CredentialStored {
                    generation,
                    provider,
                    result,
                } => {
                    if generation != self.credential_generation {
                        continue;
                    }
                    self.apply_provider_setup_result(provider, result);
                }
                AsyncMessage::ProviderImported {
                    generation,
                    provider,
                    result,
                } => {
                    if generation != self.credential_generation
                        || !self.provider_import.accepts_result(&provider, generation)
                    {
                        continue;
                    }
                    match result {
                        Ok(()) => {
                            self.provider_import = ProviderImportState::Idle;
                            self.apply_provider_setup_result(provider, Ok(()));
                        }
                        Err(message) => {
                            self.provider_import = ProviderImportState::Failed {
                                provider_id: provider,
                                message: message.clone(),
                            };
                            self.phase = Phase::Provider;
                            self.focus = Focus::Scene;
                            self.notice = Notice::localized_with(
                                MessageId::ExecutionReadinessCheckFailed,
                                [("error", message)],
                            );
                        }
                    }
                }
                AsyncMessage::MediaUploaded {
                    element_id,
                    generation,
                    result,
                } => {
                    let labels = self.media_chip_labels();
                    if self.media.apply_upload(
                        &mut self.composer,
                        element_id,
                        generation,
                        result,
                        labels,
                    ) {
                        if self.media.failed_message().is_some() {
                            self.submit_after_paste = false;
                            // The retained media component owns failure and retry. Raw RPC
                            // diagnostics must not become conversation copy.
                            self.notice.clear();
                        } else if self.submit_after_paste
                            && self.pending_pastes.is_empty()
                            && !self.media.has_pending()
                        {
                            self.submit_after_paste = false;
                            if let Err(error) = self.submit_prompt() {
                                self.notice = Notice::localized_with(
                                    MessageId::CouldNotSendRetainedDraft,
                                    [("error", error.to_string())],
                                );
                            }
                        } else {
                            self.notice = Notice::localized(MessageId::ImageAttached);
                        }
                    }
                }
                AsyncMessage::ClipboardCaptured { generation, result } => {
                    let Some(request) = self.pending_pastes.remove(&generation) else {
                        continue;
                    };
                    let active_session = self
                        .active_session
                        .as_ref()
                        .map(|session| session.session_id.as_str());
                    if request.session_id.as_deref() != active_session {
                        self.submit_after_paste = false;
                        continue;
                    }
                    let resolved = match result {
                        Ok(crate::clipboard_image::ClipboardContent::Image(image)) => {
                            self.resolve_clipboard_image(&request, image)
                        }
                        Ok(crate::clipboard_image::ClipboardContent::Text(text)) => {
                            let resolved = request.resolve_text(&mut self.composer, &text);
                            self.media.reconcile(&self.composer);
                            self.context_completion.update_context(&self.composer);
                            resolved
                        }
                        Err(error) => {
                            request.remove(&mut self.composer);
                            self.notice = Notice::localized_with(
                                MessageId::ClipboardReadFailed,
                                [("error", error)],
                            );
                            false
                        }
                    };
                    if !resolved {
                        self.submit_after_paste = false;
                    } else if self.pending_pastes.is_empty() && self.submit_after_paste {
                        if self.media.has_pending() {
                            self.notice =
                                Notice::localized(MessageId::FinishingImageUploadBeforeSend);
                        } else {
                            self.submit_after_paste = false;
                            if let Err(error) = self.submit_prompt() {
                                self.notice = Notice::localized_with(
                                    MessageId::CouldNotSendRetainedDraft,
                                    [("error", error.to_string())],
                                );
                            }
                        }
                    } else if self.pending_pastes.is_empty() {
                        self.notice.clear();
                    }
                }
                AsyncMessage::WorkspaceFilesLoaded {
                    generation,
                    session_id,
                    result,
                } => {
                    let active_session = self
                        .active_session
                        .as_ref()
                        .map(|session| session.session_id.as_str());
                    if active_session == Some(session_id.as_str())
                        && self
                            .context_completion
                            .apply_load(generation, &session_id, result)
                    {
                        self.notice.clear();
                    }
                }
                AsyncMessage::WorkspaceFileLoaded {
                    generation,
                    session_id,
                    path,
                    result,
                } => {
                    let Some(Overlay::FileViewer(viewer)) = self.overlays.active_mut() else {
                        continue;
                    };
                    if viewer.generation != generation
                        || viewer.session_id != session_id
                        || viewer.path != path
                    {
                        continue;
                    }
                    match result {
                        Ok(file) => viewer.apply_content(file.content, file.hash),
                        Err(error) => viewer.fail(error),
                    }
                }
                AsyncMessage::Event { generation, value } => {
                    if generation != self.event_generation {
                        continue;
                    }
                    match *value {
                        Ok(received) => {
                            let feedback_milestone = received.feedback_milestone();
                            let ReceivedEvent {
                                event, received_at, ..
                            } = received;
                            self.event_cursor = self.event_cursor.max(event.raw_cursor);
                            let lifecycle = match self.execution_lifecycle.reduce(&event) {
                                ExecutionLifecycleReduction::Accepted(lifecycle) => Some(lifecycle),
                                ExecutionLifecycleReduction::NotLifecycle => None,
                                ExecutionLifecycleReduction::Ignored => continue,
                            };
                            let mut visual_changed = false;
                            let artifact_ref = event.tool_artifact_ref();
                            let terminal_summary = event_terminal_summary(&event);
                            let event_agent = event_agent(&event)
                                .or_else(|| {
                                    self.active_session
                                        .as_ref()
                                        .map(|session| session.latest_run_agent.as_str())
                                        .filter(|agent| !agent.is_empty())
                                })
                                .map(str::to_owned);
                            let governance_resolution = event.governance_resolution();
                            let governance_changed = self.overlays.reconcile_event(&event);
                            if governance_changed {
                                visual_changed = true;
                                if governance_resolution.is_some() {
                                    self.notice.clear();
                                }
                            }
                            let projected_status = lifecycle
                                .map(ExecutionLifecycle::status)
                                .unwrap_or_else(|| event.projected_status())
                                .to_owned();
                            if let Some(status) = lifecycle
                                .filter(|lifecycle| lifecycle.is_active())
                                .map(ExecutionLifecycle::status)
                            {
                                visual_changed = true;
                                if self.active_run_id.as_deref() != Some(event.run_id.as_str()) {
                                    self.execution_timer.start_new();
                                    self.execution_activity = None;
                                } else if matches!(status, "waiting_input" | "waiting_approval") {
                                    self.execution_timer.pause();
                                    self.execution_activity = None;
                                } else {
                                    self.execution_timer.resume();
                                }
                                self.active_run_id = Some(event.run_id.clone());
                                self.execution_status = status.to_owned();
                                self.update_active_execution_metadata(
                                    &event.run_id,
                                    status,
                                    event_agent.as_deref(),
                                    None,
                                    Some(""),
                                );
                            }
                            if let Some(activity) = event.live_activity_description() {
                                self.execution_activity = Some(activity);
                                visual_changed = true;
                            }
                            if lifecycle.is_some_and(ExecutionLifecycle::clears_active) {
                                visual_changed = true;
                                if self.active_run_id.as_deref() == Some(event.run_id.as_str()) {
                                    self.active_run_id = None;
                                    self.execution_timer.reset();
                                    self.execution_activity = None;
                                }
                                self.execution_status = if projected_status.is_empty() {
                                    "completed".into()
                                } else {
                                    projected_status
                                };
                                let execution_status = self.execution_status.clone();
                                self.update_active_execution_metadata(
                                    &event.run_id,
                                    &execution_status,
                                    event_agent.as_deref(),
                                    terminal_summary.as_deref(),
                                    event.execution_result_kind(),
                                );
                                clear_terminal_execution_notice(&mut self.notice, &event.run_id);
                            }
                            if matches!(event.kind.as_str(), "ModelResponded" | "model.responded")
                                || lifecycle.is_some_and(ExecutionLifecycle::is_terminal)
                            {
                                self.request_context_summary();
                            }
                            let plan_review = self.active_session.as_ref().and_then(|session| {
                                plan_review_overlay(session).filter(|review| {
                                    review.run_id == event.run_id
                                        && lifecycle == Some(ExecutionLifecycle::Completed)
                                })
                            });
                            visual_changed |= self
                                .transcript_reducer
                                .reduce_event(&mut self.blocks, event);
                            if let Some(reference) = artifact_ref {
                                self.scrollback
                                    .hold_tool_call(&self.blocks, &reference.call_id);
                                self.request_tool_artifact(reference);
                            }
                            if let Some(review) = plan_review {
                                visual_changed = true;
                                self.overlays.push(Overlay::PlanReview(review));
                                self.notice.clear();
                            }
                            if visual_changed && let Some(milestone) = feedback_milestone {
                                self.pending_feedback.push(FeedbackMarker::new(
                                    milestone.phase,
                                    milestone.key,
                                    received_at,
                                ));
                            }
                            if self.active_run_id.is_none()
                                && self.phase == Phase::Conversation
                                && let Some(prompt) = self.queued_prompts.pop_front()
                            {
                                visual_changed = true;
                                let retry_prompt = prompt.clone();
                                match self.submit_new_prompt(prompt, Vec::new()) {
                                    Ok(true) => {}
                                    Ok(false) => self.restore_queued_prompt(retry_prompt),
                                    Err(error) => {
                                        self.restore_queued_prompt(retry_prompt);
                                        self.notice = Notice::localized_with(
                                            MessageId::QueuedPromptFailedDraftKept,
                                            [("error", error.to_string())],
                                        );
                                    }
                                }
                            }
                            if !visual_changed {
                                continue;
                            }
                        }
                        Err(error) => {
                            if matches!(&error, RpcError::EventFrame(_)) {
                                self.notice = Notice::localized_with(
                                    MessageId::EventStreamInterrupted,
                                    [("error", error.to_string())],
                                );
                                self.dirty = true;
                                continue;
                            }
                            self.notice = Notice::localized_with(
                                MessageId::EventStreamInterrupted,
                                [("error", error.to_string())],
                            );
                            let tx = self.async_tx.clone();
                            std::thread::spawn(move || {
                                std::thread::sleep(Duration::from_millis(500));
                                let _ = tx.send(AsyncMessage::Reconnect { generation });
                            });
                        }
                    }
                }
                AsyncMessage::Reconnect { generation } => {
                    if generation == self.event_generation && self.active_session.is_some() {
                        self.reconnect_runtime(generation);
                        self.notice = Notice::localized(MessageId::RuntimeUnavailable);
                    }
                }
                AsyncMessage::RuntimeReconnected {
                    generation,
                    session_id,
                    result,
                } => self.apply_runtime_reconnect(generation, &session_id, result),
                AsyncMessage::SessionLoaded {
                    generation,
                    target_id,
                    result,
                } => self.apply_session_load(generation, &target_id, result),
                AsyncMessage::SessionPreviewLoaded {
                    generation,
                    session_id,
                    result,
                } => {
                    if let Ok(items) = result {
                        self.session_browser.apply_preview(
                            generation,
                            &session_id,
                            session_preview_lines(items),
                        );
                    }
                }
                AsyncMessage::ToolArtifactLoaded {
                    generation,
                    session_id,
                    call_id,
                    result,
                } => {
                    if !artifact_target_is_current(
                        generation,
                        self.event_generation,
                        self.active_session
                            .as_ref()
                            .map(|session| session.session_id.as_str()),
                        &session_id,
                    ) {
                        continue;
                    }
                    if let Ok(artifact) = result {
                        self.transcript_reducer.apply_tool_output(
                            &mut self.blocks,
                            &call_id,
                            artifact.content,
                            artifact.truncated,
                        );
                    }
                    self.scrollback
                        .release_tool_call_after_present(&self.blocks, &call_id);
                }
                AsyncMessage::HistoryBranch {
                    generation,
                    source_session_id,
                    selected_block_id,
                    result,
                } => self.apply_history_branch(
                    generation,
                    &source_session_id,
                    &selected_block_id,
                    result,
                ),
                AsyncMessage::PausedResume {
                    generation,
                    session_id,
                    run_id,
                    result,
                } => self.apply_paused_resume(generation, &session_id, &run_id, result),
            }
            self.redraw_reasons.push(redraw_reason);
            self.dirty = true;
        }
        if self
            .rewind_primed_at
            .is_some_and(|primed| primed.elapsed() > self.rewind_prime_window)
        {
            self.rewind_primed_at = None;
            if self.notice.is_localized(MessageId::HistoryPrime) {
                self.notice.clear();
            }
            self.dirty = true;
            self.redraw_reasons.push(RedrawReason::AsyncResult);
        }
        if self
            .quit_primed_at
            .is_some_and(|primed| primed.elapsed() > self.rewind_prime_window)
        {
            self.quit_primed_at = None;
            if self.notice.is_localized(MessageId::QuitPrime) {
                self.notice.clear();
            }
            self.dirty = true;
            self.redraw_reasons.push(RedrawReason::AsyncResult);
        }
    }

    fn wait_for_work(&mut self, deadline: Option<Instant>) -> bool {
        let message = match wait_plan(deadline, Instant::now()) {
            WaitPlan::For(timeout) => match self.async_rx.recv_timeout(timeout) {
                Ok(message) => Some(message),
                Err(mpsc::RecvTimeoutError::Timeout) => None,
                Err(mpsc::RecvTimeoutError::Disconnected) => return false,
            },
            WaitPlan::Block => match self.async_rx.recv() {
                Ok(message) => Some(message),
                Err(_) => return false,
            },
        };
        if let Some(message) = message {
            self.pending_async.push_back(message);
        }
        true
    }

    fn tick_demand(&self) -> TickDemand {
        animation_tick_demand(
            self.terminal_focused,
            self.active_run_id.is_some(),
            &self.execution_status,
            matches!(
                &self.provider_import,
                ProviderImportState::Validating { .. }
            ),
        )
    }

    fn next_wake_deadline(&self, frame_deadline: Option<Instant>) -> Option<Instant> {
        let rewind_deadline = self
            .rewind_primed_at
            .map(|primed| primed + self.rewind_prime_window);
        let quit_deadline = self
            .quit_primed_at
            .map(|primed| primed + self.rewind_prime_window);
        let prime_deadline = match (rewind_deadline, quit_deadline) {
            (Some(a), Some(b)) => Some(a.min(b)),
            (Some(deadline), None) | (None, Some(deadline)) => Some(deadline),
            (None, None) => None,
        };
        match (frame_deadline, prime_deadline) {
            (Some(frame), Some(prime)) => Some(frame.min(prime)),
            (Some(deadline), None) | (None, Some(deadline)) => Some(deadline),
            (None, None) => None,
        }
    }

    fn apply_provider_setup_result(&mut self, provider: String, result: Result<(), String>) {
        self.credential_pending = false;
        match result {
            Ok(()) => match self.rpc.model_inventory() {
                Ok(inventory) => {
                    self.inventory = inventory;
                    self.provider_index = self
                        .inventory
                        .providers
                        .iter()
                        .position(|candidate| candidate.id == provider)
                        .unwrap_or(self.provider_index);
                    self.models = self
                        .inventory
                        .providers
                        .get(self.provider_index)
                        .map(|candidate| {
                            candidate
                                .models
                                .iter()
                                .filter(|model| model.available)
                                .cloned()
                                .collect()
                        })
                        .unwrap_or_default();
                    self.model_index = self
                        .models
                        .iter()
                        .position(|model| model.id == self.inventory.default_model)
                        .unwrap_or(0);
                    self.selected_model = self
                        .models
                        .get(self.model_index)
                        .map(|model| model.id.clone())
                        .unwrap_or_default();
                    self.provider_import = ProviderImportState::Idle;
                    if self.inventory.has_runnable_provider() {
                        if self.models.is_empty() {
                            self.route_after_locale();
                        } else {
                            self.phase = Phase::Model;
                            self.focus = Focus::Scene;
                            self.sync_reasoning_effort_for_selection();
                            self.notice =
                                Notice::localized(MessageId::ProviderSetupCompleteChooseModel);
                        }
                    } else {
                        self.phase = Phase::Provider;
                        self.notice =
                            Notice::localized(MessageId::ProviderSetupCompleteNotRunnable);
                    }
                }
                Err(error) => {
                    self.phase = Phase::Provider;
                    self.notice = Notice::localized_with(
                        MessageId::ExecutionReadinessCheckFailed,
                        [("error", error.to_string())],
                    );
                }
            },
            Err(message) => {
                self.notice = Notice::localized_with(
                    MessageId::ExecutionReadinessCheckFailed,
                    [("error", message)],
                )
            }
        }
    }

    fn handle_event(&mut self, event: Event) -> Result<()> {
        if let Some(focused) = terminal_focus_transition(&event) {
            self.terminal_focused = focused;
        }
        match event {
            Event::Key(key) if key.kind != KeyEventKind::Release => self.handle_key(key)?,
            Event::Mouse(mouse) => self.handle_mouse(mouse),
            Event::Paste(value) if self.phase == Phase::Conversation => {
                if let Some(path) = pasted_image_path(&value) {
                    self.attach_image(path, false);
                } else {
                    self.composer.insert_str(&value.replace('\r', ""));
                    self.media.reconcile(&self.composer);
                    self.sync_context_completion();
                }
                self.focus = Focus::Composer;
            }
            Event::Resize(_, _) | Event::FocusGained | Event::FocusLost => self.dirty = true,
            _ => {}
        }
        Ok(())
    }

    fn attach_image(&mut self, path: PathBuf, temporary: bool) -> bool {
        if !self.selected_model_is_runnable() {
            self.notice = Notice::localized(MessageId::ImageRepairProvider);
            if temporary {
                let _ = fs::remove_file(&path);
            }
            return false;
        }
        if !self
            .models
            .iter()
            .find(|model| model.id == self.selected_model)
            .is_some_and(|model| model.image_input)
        {
            self.notice = Notice::localized(MessageId::ImageUnsupportedModel);
            if temporary {
                let _ = fs::remove_file(&path);
            }
            return false;
        }
        let (media_type, bytes) = match inspect_image(&path) {
            Ok(metadata) => metadata,
            Err(error) => {
                self.notice =
                    Notice::localized_with(MessageId::ImageAttachFailed, [("error", error)]);
                if temporary {
                    let _ = fs::remove_file(&path);
                }
                return false;
            }
        };
        let session_id = match self.active_session.as_ref() {
            Some(session) => session.session_id.clone(),
            None => {
                self.notice = Notice::localized(MessageId::ImageConversationRequired);
                if temporary {
                    let _ = fs::remove_file(&path);
                }
                return false;
            }
        };
        let labels = self.media_chip_labels();
        let attachment_label =
            temporary.then(|| tr(self.ui_locale(), MessageId::ClipboardImage).to_owned());
        let (element_id, generation) = match self.media.insert_pending(
            &mut self.composer,
            path.clone(),
            media_type.clone(),
            bytes,
            if temporary {
                MediaSourceLabel::Temporary(attachment_label)
            } else {
                MediaSourceLabel::User(attachment_label)
            },
            labels,
        ) {
            Ok(identity) => identity,
            Err(error) => {
                self.notice =
                    Notice::localized_with(MessageId::ImageAttachFailed, [("error", error)]);
                if temporary {
                    let _ = fs::remove_file(&path);
                }
                return false;
            }
        };
        let name = path
            .file_name()
            .and_then(|name| name.to_str())
            .unwrap_or("image")
            .to_owned();
        self.notice = Notice::localized_with(MessageId::ImageUploading, [("name", name)]);
        self.start_media_upload(
            session_id,
            MediaUploadWork {
                element_id,
                generation,
                path,
                media_type,
                temporary,
            },
        );
        true
    }

    fn start_media_upload(&self, session_id: String, work: MediaUploadWork) {
        let socket = self.rpc.socket().to_path_buf();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let origin = if work.temporary {
                "clipboard".into()
            } else {
                work.path
                    .file_name()
                    .and_then(|name| name.to_str())
                    .unwrap_or("pasted-image")
                    .to_owned()
            };
            let result = Client::connect(&socket)
                .and_then(|mut rpc| {
                    rpc.upload_media(&session_id, &work.path, &work.media_type, &origin)
                })
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::MediaUploaded {
                element_id: work.element_id,
                generation: work.generation,
                result,
            });
        });
    }

    fn retry_media(&mut self, element_id: xai_ratatui_textarea::ElementId) {
        let labels = self.media_chip_labels();
        let Some((generation, path, media_type, temporary)) =
            self.media
                .begin_retry(&mut self.composer, element_id, labels)
        else {
            return;
        };
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.media.apply_upload(
                &mut self.composer,
                element_id,
                generation,
                Err("Open a conversation before retrying the image".into()),
                labels,
            );
            return;
        };
        self.notice = Notice::localized(MessageId::Retry);
        self.start_media_upload(
            session_id,
            MediaUploadWork {
                element_id,
                generation,
                path,
                media_type,
                temporary,
            },
        );
    }

    fn resolve_clipboard_image(
        &mut self,
        request: &crate::clipboard_image::PendingPaste,
        image: crate::clipboard_image::TemporaryImage,
    ) -> bool {
        let Some(range) = request.range(&self.composer) else {
            return false;
        };
        let cursor_before = self.composer.cursor();
        let start = range.start;
        let end = range.end;
        self.composer.replace_range(range, "");
        let cursor_without_placeholder = self.composer.cursor();
        self.composer.set_cursor(start);
        if !self.attach_image(image.into_path(), true) {
            self.composer.set_cursor(cursor_without_placeholder);
            return false;
        }
        let cursor_after_image = self.composer.cursor();
        if cursor_before <= start {
            self.composer.set_cursor(cursor_before);
        } else if cursor_before > end {
            self.composer
                .set_cursor(cursor_without_placeholder.saturating_add(cursor_after_image - start));
        }
        true
    }

    fn handle_key(&mut self, key: KeyEvent) -> Result<()> {
        let key = normalize_shift_tab(key);
        if self.keybindings.hard_cancel.matches(key) {
            if self.close_top_non_governance() {
                self.quit_primed_at = None;
                self.dirty = true;
                return Ok(());
            }
            let now = Instant::now();
            match quit_hard_cancel_action(
                self.active_run_id.is_some(),
                self.quit_primed_at,
                now,
                self.rewind_prime_window,
            ) {
                QuitHardCancelAction::CancelRun => {
                    // Active turn: hard-cancel the run. Do not exit the TUI.
                    self.quit_primed_at = None;
                    if let Some(run_id) = self.active_run_id.clone() {
                        self.cancel_execution(&run_id);
                    }
                }
                QuitHardCancelAction::Prime => {
                    // Idle: require a second Ctrl-C inside the grace window to quit
                    // (same double-confirm pattern as Esc Esc history rewind).
                    self.quit_primed_at = Some(now);
                    self.notice = Notice::localized_with(
                        MessageId::QuitPrime,
                        [("key", self.keybindings.hard_cancel.label().to_owned())],
                    );
                }
                QuitHardCancelAction::Quit => {
                    self.quit_primed_at = None;
                    self.quit = true;
                }
            }
            self.dirty = true;
            return Ok(());
        }
        // Any other key clears a pending quit prime.
        if self.quit_primed_at.is_some() {
            self.quit_primed_at = None;
            if self.notice.is_localized(MessageId::QuitPrime) {
                self.notice.clear();
            }
        }
        if self.overlays.active().is_some() {
            self.handle_overlay_key(key);
            self.dirty = true;
            return Ok(());
        }
        if self.phase == Phase::Session && self.session_browser.renaming_session_id().is_some() {
            self.handle_session_rename_key(key);
            self.dirty = true;
            return Ok(());
        }
        if self.phase == Phase::Session && self.session_browser.archive_confirmation_id().is_some()
        {
            match key.code {
                KeyCode::Enter => self.confirm_session_archive(),
                KeyCode::Esc => self.session_browser.cancel_archive(),
                _ => {}
            }
            self.dirty = true;
            return Ok(());
        }
        match self.phase {
            Phase::Locale => match key.code {
                KeyCode::Up => self.locale_index = self.locale_index.saturating_sub(1),
                KeyCode::Down => self.locale_index = (self.locale_index + 1).min(LOCALES.len() - 1),
                KeyCode::Enter => self.select_locale(),
                KeyCode::Esc => {
                    if self.active_session.is_some() {
                        self.return_to_conversation_or_repair();
                    } else {
                        self.outcome = Outcome::Usage;
                        self.quit = true;
                    }
                }
                _ => {}
            },
            Phase::Provider => match key.code {
                KeyCode::Up => {
                    self.provider_index = self.provider_picker.move_selection(
                        &self.inventory.providers,
                        self.provider_index,
                        false,
                    );
                    self.clear_provider_import_state();
                }
                KeyCode::Down => {
                    self.provider_index = self.provider_picker.move_selection(
                        &self.inventory.providers,
                        self.provider_index,
                        true,
                    );
                    self.clear_provider_import_state();
                }
                KeyCode::Enter => self.select_provider_and_continue(),
                KeyCode::Char('/') if !self.provider_picker.search_active() => {
                    self.provider_picker.begin_search();
                }
                KeyCode::Char(character)
                    if self.provider_picker.search_active()
                        && !key
                            .modifiers
                            .intersects(KeyModifiers::CONTROL | KeyModifiers::SUPER) =>
                {
                    self.provider_picker.push(character);
                    self.provider_index = self
                        .provider_picker
                        .normalize_selection(&self.inventory.providers, self.provider_index);
                    self.clear_provider_import_state();
                }
                KeyCode::Backspace if self.provider_picker.search_active() => {
                    self.provider_picker.backspace();
                    self.provider_index = self
                        .provider_picker
                        .normalize_selection(&self.inventory.providers, self.provider_index);
                    self.clear_provider_import_state();
                }
                KeyCode::Char('d') => self.phase = Phase::Diagnostic,
                KeyCode::Esc if self.provider_import.provider_id().is_some() => {
                    self.cancel_provider_import();
                }
                KeyCode::Esc if !self.provider_picker.cancel_search() => {
                    if self.active_session.is_some() {
                        self.return_to_conversation_or_repair();
                    } else {
                        self.outcome = Outcome::Degraded;
                        self.quit = true;
                    }
                }
                KeyCode::Esc => {}
                _ => {}
            },
            Phase::Credential => self.handle_credential_key(key),
            Phase::Model => match key.code {
                KeyCode::Up => {
                    self.model_index = self.model_index.saturating_sub(1);
                    self.sync_reasoning_effort_for_selection();
                }
                KeyCode::Down => {
                    self.model_index =
                        (self.model_index + 1).min(self.models.len().saturating_sub(1));
                    self.sync_reasoning_effort_for_selection();
                }
                KeyCode::Tab | KeyCode::Right => self.cycle_reasoning_effort(true),
                KeyCode::BackTab | KeyCode::Left => self.cycle_reasoning_effort(false),
                KeyCode::Enter => self.select_model_and_continue(self.model_index),
                KeyCode::Esc => {
                    if self.active_session.is_some() {
                        self.return_to_conversation_or_repair();
                    } else {
                        self.phase = Phase::Provider;
                    }
                }
                _ => {}
            },
            Phase::Session => match key.code {
                KeyCode::Up => {
                    self.session_browser.move_selection(
                        &self.sessions,
                        &self.options.workspace,
                        false,
                    );
                    self.request_session_preview();
                }
                KeyCode::Down => {
                    self.session_browser.move_selection(
                        &self.sessions,
                        &self.options.workspace,
                        true,
                    );
                    self.request_session_preview();
                }
                KeyCode::Enter => self.open_selected_session(None),
                KeyCode::Char('n') if !self.session_browser.search_active() => {
                    self.create_session_from_browser()
                }
                KeyCode::Char('r') if !self.session_browser.search_active() => {
                    self.begin_selected_session_rename()
                }
                KeyCode::Char('a') if !self.session_browser.search_active() => {
                    self.begin_selected_session_archive()
                }
                KeyCode::Char('u')
                    if !self.session_browser.search_active()
                        && self.session_browser.scope() == SessionScope::Archived =>
                {
                    self.unarchive_selected_session()
                }
                KeyCode::Tab => {
                    self.session_browser
                        .toggle_scope(&self.sessions, &self.options.workspace);
                    self.request_session_preview();
                }
                KeyCode::BackTab => {
                    self.session_browser.cycle_scope(
                        &self.sessions,
                        &self.options.workspace,
                        false,
                    );
                    self.request_session_preview();
                }
                KeyCode::Char('/') if !self.session_browser.search_active() => {
                    self.session_browser.begin_search();
                }
                KeyCode::Char(character)
                    if self.session_browser.search_active()
                        && !key
                            .modifiers
                            .intersects(KeyModifiers::CONTROL | KeyModifiers::SUPER) =>
                {
                    self.session_browser
                        .push(character, &self.sessions, &self.options.workspace);
                    self.request_session_preview();
                }
                KeyCode::Backspace if self.session_browser.search_active() => {
                    self.session_browser
                        .backspace(&self.sessions, &self.options.workspace);
                    self.request_session_preview();
                }
                KeyCode::Esc if !self.session_browser.cancel_search() => {
                    if self.active_session.is_some() {
                        self.return_to_conversation_or_repair();
                    } else {
                        self.phase = Phase::Model;
                    }
                }
                KeyCode::Esc => {}
                _ => {}
            },
            Phase::Conversation => self.handle_conversation_key(key)?,
            Phase::Diagnostic => match key.code {
                KeyCode::Char('r') | KeyCode::Enter => {
                    self.inventory = self.rpc.model_inventory()?;
                    self.models = self.inventory.available_models();
                    self.route_after_locale();
                }
                KeyCode::Esc | KeyCode::Char('q') => {
                    self.outcome = Outcome::Degraded;
                    self.quit = true;
                }
                _ => {}
            },
        }
        self.dirty = true;
        Ok(())
    }

    fn handle_credential_key(&mut self, key: KeyEvent) {
        if self.credential_pending {
            if key.code == KeyCode::Esc {
                self.credential_generation += 1;
                if let Some(child) = lock_child(&self.credential_child).as_mut() {
                    let _ = child.kill();
                }
                self.credential_pending = false;
                self.phase = Phase::Provider;
                self.notice = Notice::localized(MessageId::CredentialValidationCancelled);
            }
            return;
        }
        match key.code {
            KeyCode::Char(character)
                if !key
                    .modifiers
                    .intersects(KeyModifiers::CONTROL | KeyModifiers::SUPER) =>
            {
                self.credential.push(character);
            }
            KeyCode::Backspace => {
                self.credential.pop();
            }
            KeyCode::Enter => self.submit_credential(),
            KeyCode::Esc => {
                self.credential.clear();
                self.phase = Phase::Provider;
            }
            _ => {}
        }
    }

    fn handle_conversation_key(&mut self, key: KeyEvent) -> Result<()> {
        if self.keybindings.expand_tools.matches(key) {
            let expand = self.blocks.iter().any(|block| {
                block.kind == crate::transcript::BlockKind::Tool
                    && block.is_collapsible()
                    && !self.effective_block_expanded(block)
            });
            for block in self.blocks.iter().filter(|block| {
                block.kind == crate::transcript::BlockKind::Tool && block.is_collapsible()
            }) {
                self.tool_disclosure_overrides
                    .insert(block.id.clone(), expand);
            }
            self.clear_transcript_projection_caches();
            return Ok(());
        }
        if self.history_search.is_some() {
            self.handle_prompt_history_search_key(key);
            return Ok(());
        }
        if self.history_selected.is_some() {
            return self.handle_history_key(key);
        }
        if self.context_completion.is_open() {
            match key.code {
                KeyCode::Up => {
                    self.context_completion.move_selection(-1);
                    return Ok(());
                }
                KeyCode::Down => {
                    self.context_completion.move_selection(1);
                    return Ok(());
                }
                KeyCode::BackTab => {
                    self.context_completion.move_selection(-1);
                    return Ok(());
                }
                KeyCode::PageUp => {
                    self.context_completion.page(-1, 8);
                    return Ok(());
                }
                KeyCode::PageDown => {
                    self.context_completion.page(1, 8);
                    return Ok(());
                }
                KeyCode::Enter | KeyCode::Tab if !self.context_completion.results().is_empty() => {
                    self.accept_context_completion();
                    return Ok(());
                }
                KeyCode::Char(':') => {
                    self.open_selected_file_viewer();
                    return Ok(());
                }
                KeyCode::Char('l') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                    self.open_selected_file_viewer();
                    return Ok(());
                }
                KeyCode::Esc => {
                    self.context_completion.dismiss(&self.composer);
                    return Ok(());
                }
                _ => {}
            }
        }
        if ((key.code == KeyCode::Char('l') && key.modifiers.contains(KeyModifiers::CONTROL))
            || key.code == KeyCode::Char(':'))
            && self.open_file_element_viewer()
        {
            return Ok(());
        }
        let slash_suggestions = self.slash_suggestions();
        let slash_count = slash_suggestions.len();
        self.slash_selected = command::selected_index(
            &slash_suggestions,
            self.slash_selected_id.as_deref(),
            self.slash_selected,
        );
        if let Some(mode) = crate::clipboard_image::paste_mode(key) {
            self.capture_clipboard(mode);
            return Ok(());
        }
        match key.code {
            KeyCode::Up if slash_count > 0 => {
                self.slash_selected = self.slash_selected.saturating_sub(1);
                self.slash_selected_id = slash_suggestions
                    .get(self.slash_selected)
                    .map(|command| command.id.clone());
            }
            KeyCode::Down if slash_count > 0 => {
                self.slash_selected = (self.slash_selected + 1).min(slash_count - 1);
                self.slash_selected_id = slash_suggestions
                    .get(self.slash_selected)
                    .map(|command| command.id.clone());
            }
            KeyCode::Tab if slash_count > 0 => {
                if let Some(command) = slash_suggestions.get(self.slash_selected) {
                    let completed =
                        command::complete_prompt_token(self.composer.text(), &command.name);
                    self.composer.set_text(&completed);
                    self.composer.set_cursor(self.composer.text().len());
                    self.composer_state = TextAreaState::default();
                    self.slash_selected_id = Some(command.id.clone());
                }
            }
            KeyCode::BackTab if slash_count > 0 => {
                self.slash_selected = self.slash_selected.saturating_sub(1);
                self.slash_selected_id = slash_suggestions
                    .get(self.slash_selected)
                    .map(|command| command.id.clone());
            }
            KeyCode::BackTab => self.cycle_conversation_mode(),
            KeyCode::Up if slash_count == 0 && self.composer.text().is_empty() => {
                self.open_prompt_history_browse();
            }
            KeyCode::Up
                if slash_count == 0
                    && self.composer.text().is_empty()
                    && self.prompt_history().is_empty() =>
            {
                self.notice = if self.persisted_prompt_history_unavailable {
                    Notice::localized(MessageId::WorkspaceHistoryUnavailableNoPrompts)
                } else {
                    Notice::localized(MessageId::NoPromptHistory)
                };
            }
            KeyCode::Enter
                if slash_count > 0
                    && !key.modifiers.contains(KeyModifiers::SHIFT)
                    && !self.composer.text().contains(char::is_whitespace)
                    && command::resolve(self.composer.text(), self.active_run_id.is_some())
                        .is_none() =>
            {
                if let Some(command) = slash_suggestions.get(self.slash_selected) {
                    self.execute_slash_suggestion(
                        &command.id,
                        command.registry_revision.as_deref(),
                    )?;
                }
            }
            KeyCode::Esc if slash_count > 0 => {
                self.slash_dismissed_input = Some(self.composer.text().trim().to_owned());
            }
            KeyCode::Esc => match self.active_run_id.clone() {
                Some(run_id) => self.interrupt_execution(&run_id),
                None => self.handle_rewind_escape(),
            },
            KeyCode::Char('?') if self.composer.text().is_empty() => {
                self.open_help_overlay();
            }
            KeyCode::Char(',') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                self.overlays
                    .replace(Overlay::Settings(SettingsOverlay { selected: 0 }));
            }
            KeyCode::Char('r') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                self.open_prompt_history_search();
            }
            KeyCode::Char('c') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                if let Some(run_id) = self.active_run_id.clone() {
                    self.cancel_execution(&run_id);
                }
            }
            KeyCode::Char('r') if key.modifiers.contains(KeyModifiers::ALT) => {
                if let Some(run_id) = self.blocks.iter().rev().find_map(|block| {
                    block.failure.as_ref().and_then(|failure| {
                        matches!(
                            failure.action,
                            crate::transcript::FailureAction::Retry
                                | crate::transcript::FailureAction::RunAgain
                        )
                        .then(|| failure.run_id.clone())
                    })
                }) {
                    self.retry_failed_execution(&run_id);
                }
            }
            KeyCode::Char('c') if key.modifiers.contains(KeyModifiers::ALT) => {
                if let Some(id) = self.blocks.iter().rev().find_map(|block| {
                    block.failure.as_ref().map(|failure| {
                        if failure.source_event_id.is_empty() {
                            failure.run_id.clone()
                        } else {
                            failure.source_event_id.clone()
                        }
                    })
                }) {
                    self.copy_failure_id(&id);
                }
            }
            KeyCode::PageUp => {
                self.scroll_transcript(-(self.transcript_page_size() as isize));
            }
            KeyCode::PageDown => {
                self.scroll_transcript(self.transcript_page_size() as isize);
            }
            KeyCode::Enter if self.keybindings.send_now.matches(key) => {
                self.submit_prompt()?;
            }
            KeyCode::Enter if self.keybindings.steer.matches(key) => {
                let failed_media = self
                    .media
                    .attachment_for_preview(&self.composer)
                    .filter(|attachment| {
                        matches!(attachment.state, crate::media::MediaState::Failed(_))
                    })
                    .map(|attachment| attachment.element_id);
                if let Some(element_id) = failed_media {
                    self.retry_media(element_id);
                } else {
                    self.submit_prompt()?;
                }
            }
            KeyCode::Tab if self.active_run_id.is_some() => {
                let prompt = self.composer.text().trim().to_owned();
                if !prompt.is_empty() {
                    self.queued_prompts.push_back(prompt);
                    self.composer.set_text("");
                    self.composer_state = TextAreaState::default();
                    self.notice = Notice::localized_with(
                        MessageId::FollowUpsQueued,
                        [("count", self.queued_prompts.len().to_string())],
                    );
                }
            }
            _ => {
                self.rewind_primed_at = None;
                self.composer.input(key);
                self.media.reconcile(&self.composer);
                self.slash_selected = 0;
                self.slash_selected_id = None;
                self.slash_dismissed_input = None;
            }
        }
        self.sync_context_completion();
        Ok(())
    }

    fn retry_failed_execution(&mut self, original_run_id: &str) {
        match self
            .rpc
            .retry_execution(original_run_id, &operation_id("retry"))
        {
            Ok(execution) => {
                self.active_run_id = Some(execution.run_id.clone());
                self.execution_timer.start_new();
                self.execution_activity = None;
                self.execution_status = if execution.status.is_empty() {
                    "queued".into()
                } else {
                    execution.status.clone()
                };
                self.seed_execution_lifecycle(&execution.run_id, &self.execution_status.clone());
                self.notice = Notice::localized_for_run(
                    MessageId::ExecutionWorking,
                    execution.run_id,
                    std::iter::empty::<(&str, &str)>(),
                );
            }
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::SubmitFailedDraftKept,
                    [("error", error.to_string())],
                );
            }
        }
    }

    fn copy_failure_id(&mut self, id: &str) {
        if id.is_empty() {
            return;
        }
        if let Ok(mut clipboard) = arboard::Clipboard::new() {
            let _ = clipboard.set_text(id.to_owned());
        }
    }

    fn sync_context_completion(&mut self) {
        if !self.context_completion.update_context(&self.composer) {
            return;
        }
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        let generation = self.context_completion.begin_load(session_id.clone());
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.workspace_files(&session_id))
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::WorkspaceFilesLoaded {
                generation,
                session_id,
                result,
            });
        });
    }

    fn accept_context_completion(&mut self) {
        if self.context_completion.accept(&mut self.composer) {
            self.composer_state = TextAreaState::default();
            self.media.reconcile(&self.composer);
            self.slash_selected = 0;
            self.slash_selected_id = None;
            self.slash_dismissed_input = None;
        }
    }

    fn open_selected_file_viewer(&mut self) {
        let Some((context, candidate)) = self.context_completion.viewer_target() else {
            return;
        };
        if candidate.binary {
            self.notice = Notice::localized_with(
                MessageId::FileBinaryPreviewUnavailable,
                [("path", candidate.path)],
            );
            return;
        }
        if candidate.large || candidate.size > MAX_PREVIEW_BYTES as u64 {
            self.notice = Notice::localized_with(
                MessageId::FileTooLargePreview,
                [
                    ("path", candidate.path),
                    ("bytes", candidate.size.to_string()),
                    ("limit", MAX_PREVIEW_BYTES.to_string()),
                ],
            );
            return;
        }
        self.open_file_viewer(
            candidate.path,
            FileViewerOrigin::Completion {
                range: context.range,
            },
            None,
        );
    }

    fn open_file_element_viewer(&mut self) -> bool {
        let cursor = self.composer.cursor();
        let element = self.composer.elements().iter().find(|element| {
            element.kind == FILE_ELEMENT_KIND
                && cursor >= element.range.start
                && cursor <= element.range.end
        });
        let Some(element) = element else {
            return false;
        };
        let Some(backing) = self.composer.text().get(element.range.clone()) else {
            return false;
        };
        let Some((path, initial_range)) = parse_file_reference(backing) else {
            return false;
        };
        self.open_file_viewer(
            path,
            FileViewerOrigin::Element {
                range: element.range.clone(),
            },
            initial_range,
        );
        true
    }

    fn open_file_viewer(
        &mut self,
        path: String,
        origin: FileViewerOrigin,
        initial_range: Option<std::ops::Range<usize>>,
    ) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.notice = Notice::localized(MessageId::WorkspaceConversationRequired);
            return;
        };
        self.file_viewer_generation = self.file_viewer_generation.saturating_add(1);
        let generation = self.file_viewer_generation;
        self.overlays
            .replace(Overlay::FileViewer(FileViewer::loading(
                session_id.clone(),
                path.clone(),
                origin,
                generation,
                initial_range,
            )));
        self.load_file_viewer(generation, session_id, path);
    }

    fn load_file_viewer(&self, generation: u64, session_id: String, path: String) {
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.workspace_file(&session_id, &path))
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::WorkspaceFileLoaded {
                generation,
                session_id,
                path,
                result,
            });
        });
    }

    fn capture_clipboard(&mut self, mode: crate::clipboard_image::PasteMode) {
        self.clipboard_generation = self.clipboard_generation.saturating_add(1);
        let generation = self.clipboard_generation;
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone());
        let locale = self.ui_locale();
        let paste_label = tr(locale, MessageId::ClipboardPasteLabel);
        let reading_label = tr(locale, MessageId::ClipboardReadingLabel);
        let pending = crate::clipboard_image::PendingPaste::insert(
            &mut self.composer,
            generation,
            session_id,
            paste_label,
            reading_label,
        );
        self.pending_pastes.insert(generation, pending);
        self.notice = match mode {
            crate::clipboard_image::PasteMode::Rich => {
                Notice::localized(MessageId::ReadingClipboard)
            }
            crate::clipboard_image::PasteMode::Text => {
                Notice::localized(MessageId::ReadingClipboardText)
            }
        };
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = crate::clipboard_image::capture(mode);
            let _ = tx.send(AsyncMessage::ClipboardCaptured { generation, result });
        });
    }

    fn cancel_pending_pastes(&mut self) {
        for (_, request) in self.pending_pastes.drain() {
            let _ = request.remove(&mut self.composer);
        }
        self.submit_after_paste = false;
    }

    fn slash_suggestions(&self) -> Vec<CommandSuggestion> {
        let input = self.composer.text().trim();
        if self.slash_dismissed_input.as_deref() == Some(input) {
            return Vec::new();
        }
        command::palette_matching(
            input,
            self.active_run_id.is_some(),
            &self.command_registry.commands,
            &self.command_registry.revision,
            &self.command_mru,
        )
    }

    fn sync_slash_selection(&mut self) {
        let suggestions = self.slash_suggestions();
        self.slash_selected = command::selected_index(
            &suggestions,
            self.slash_selected_id.as_deref(),
            self.slash_selected,
        );
        self.slash_selected_id = suggestions
            .get(self.slash_selected)
            .map(|command| command.id.clone());
    }

    fn remember_command_use(&mut self, id: String) {
        self.command_mru.retain(|candidate| candidate != &id);
        self.command_mru.insert(0, id);
        self.command_mru.truncate(20);
    }

    fn execute_slash_suggestion(
        &mut self,
        id: &str,
        expected_registry_revision: Option<&str>,
    ) -> Result<()> {
        let Some(command) = self
            .slash_suggestions()
            .into_iter()
            .find(|command| command.id == id)
        else {
            return Ok(());
        };
        if matches!(
            &command.execution,
            SuggestionExecution::PromptTemplate { .. }
        ) && expected_registry_revision != Some(self.command_registry.revision.as_str())
        {
            self.notice = Notice::localized(MessageId::CommandRegistryChanged);
            return Ok(());
        }
        if let Some(reason) = command.unavailable_reason {
            self.notice = Notice::localized(reason);
            return Ok(());
        }
        let is_prompt = matches!(
            &command.execution,
            SuggestionExecution::PromptTemplate { .. }
        );
        let text = if is_prompt {
            let completed = command::complete_prompt_token(self.composer.text(), &command.name);
            if completed == command.name {
                format!("{completed} ")
            } else {
                completed
            }
        } else {
            command.name
        };
        self.composer.set_text(&text);
        self.composer.set_cursor(self.composer.text().len());
        self.composer_state = TextAreaState::default();
        self.slash_selected_id = Some(command.id.clone());
        self.slash_dismissed_input = None;
        self.focus = Focus::Composer;
        if is_prompt {
            Ok(())
        } else {
            self.remember_command_use(command.id);
            self.submit_prompt()
        }
    }

    fn prompt_history(&self) -> Vec<String> {
        combined_prompt_history(&self.blocks, &self.persisted_prompt_history)
    }

    fn open_prompt_history_search(&mut self) {
        let history = self.prompt_history();
        if history.is_empty() && !self.persisted_prompt_history_unavailable {
            self.notice = Notice::localized(MessageId::NoPromptHistory);
            return;
        }
        self.history_search = Some(HistorySearchState::activate(
            history,
            self.composer.text().to_owned(),
            self.persisted_prompt_history_unavailable,
        ));
        self.preview_prompt_history_search();
    }

    fn open_prompt_history_browse(&mut self) {
        let history = self.prompt_history();
        if history.is_empty() {
            self.notice = if self.persisted_prompt_history_unavailable {
                Notice::localized(MessageId::WorkspaceHistoryUnavailableNoPrompts)
            } else {
                Notice::localized(MessageId::NoPromptHistory)
            };
            return;
        }
        self.history_search = Some(HistorySearchState::activate_browse(
            history,
            self.composer.text().to_owned(),
            self.persisted_prompt_history_unavailable,
        ));
        self.preview_prompt_history_search();
    }

    fn handle_prompt_history_search_key(&mut self, key: KeyEvent) {
        let visible_rows = self.transcript_area.height.saturating_sub(4).min(8) as usize;
        let mut accept = false;
        let mut cancel = false;
        let mut restore_and_close = false;
        let mut detach_and_edit = false;
        if let Some(search) = self.history_search.as_mut() {
            let browse = search.is_browse();
            match key.code {
                KeyCode::Esc => cancel = true,
                KeyCode::Enter | KeyCode::Tab => accept = true,
                KeyCode::BackTab => search.move_older(),
                KeyCode::Char('r') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                    search.move_older();
                }
                KeyCode::Up => search.move_older(),
                KeyCode::Down if browse && search.is_newest_selected() => restore_and_close = true,
                KeyCode::Down => search.move_newer(),
                KeyCode::PageUp => search.page(-1, visible_rows),
                KeyCode::PageDown => search.page(1, visible_rows),
                KeyCode::Backspace if browse => detach_and_edit = true,
                KeyCode::Backspace => search.backspace(),
                KeyCode::Char(character)
                    if !key
                        .modifiers
                        .intersects(KeyModifiers::CONTROL | KeyModifiers::SUPER) =>
                {
                    if browse {
                        detach_and_edit = true;
                    } else {
                        search.push(character);
                    }
                }
                _ if browse => detach_and_edit = true,
                _ => {}
            }
        }
        if cancel || restore_and_close {
            if let Some(search) = self.history_search.take() {
                self.composer.set_text(search.saved_draft());
                self.composer.set_cursor(self.composer.text().len());
                self.composer_state = TextAreaState::default();
            }
        } else if accept {
            self.accept_prompt_history_search();
        } else if detach_and_edit {
            self.history_search = None;
            self.composer.input(key);
            self.media.reconcile(&self.composer);
            self.slash_selected = 0;
            self.slash_selected_id = None;
            self.slash_dismissed_input = None;
        } else {
            self.preview_prompt_history_search();
        }
    }

    fn preview_prompt_history_search(&mut self) {
        let preview = self
            .history_search
            .as_ref()
            .and_then(|search| search.selected_text())
            .map(str::to_owned)
            .or_else(|| {
                self.history_search
                    .as_ref()
                    .map(|search| search.saved_draft().to_owned())
            });
        if let Some(preview) = preview {
            self.composer.set_text(&preview);
            self.composer.set_cursor(self.composer.text().len());
            self.composer_state = TextAreaState::default();
        }
    }

    fn accept_prompt_history_search(&mut self) {
        if self
            .history_search
            .as_ref()
            .and_then(|search| search.selected_text())
            .is_some()
        {
            self.history_search = None;
            self.composer.set_cursor(self.composer.text().len());
            self.composer_state = TextAreaState::default();
        }
    }

    fn submit_prompt(&mut self) -> Result<()> {
        if self.pending_submission.is_some() {
            return self.reconcile_pending_submission();
        }
        if !self.pending_pastes.is_empty() {
            self.submit_after_paste = true;
            self.notice = Notice::localized(MessageId::FinishingClipboardPasteBeforeSend);
            return Ok(());
        }
        self.media.reconcile(&self.composer);
        let prompt = self.media.prompt_text(&self.composer);
        if prompt.is_empty() && self.media.is_empty() {
            return Ok(());
        }
        if let Some(Err(error)) =
            command::validate_prompt_arguments(&prompt, &self.command_registry.commands)
        {
            self.notice = match error {
                command::PromptArgumentError::Required(argument) => Notice::localized_with(
                    MessageId::CommandArgumentRequired,
                    [("argument", argument)],
                ),
                command::PromptArgumentError::TooMany => {
                    Notice::localized(MessageId::CommandArgumentsTooMany)
                }
                command::PromptArgumentError::NotAccepted => {
                    Notice::localized(MessageId::CommandArgumentsNotAccepted)
                }
            };
            return Ok(());
        }
        if self.media.has_pending() {
            self.notice = Notice::localized(MessageId::WaitImageUploadsBeforeSend);
            return Ok(());
        }
        if self.media.failed_message().is_some() {
            // Enter on a failed media element retries it in place. Keep the
            // backend error inside component state instead of duplicating it
            // as a log-like notice.
            self.notice.clear();
            return Ok(());
        }
        let media_refs = self.media.ready_refs_in_text_order(&self.composer);
        if let Some(clear_composer) = self.handle_slash_command(&prompt) {
            if clear_composer {
                if media_refs.is_empty() {
                    self.composer.set_text("");
                } else {
                    self.media.clear_plain_text(&mut self.composer);
                }
                self.composer_state = TextAreaState::default();
            }
            return Ok(());
        }
        if let Some(run_id) = self.active_run_id.clone() {
            if !media_refs.is_empty() {
                self.notice = Notice::localized(MessageId::ImageWaitCurrentResponse);
                return Ok(());
            }
            return self.submit_steer(run_id, prompt);
        }
        self.submit_new_prompt(prompt, media_refs).map(|_| ())
    }

    fn submit_new_prompt(
        &mut self,
        prompt: String,
        media_refs: Vec<crate::rpc::MediaRef>,
    ) -> Result<bool> {
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
            .ok_or_else(|| anyhow!("conversation has no active session"))?;
        let locale = agent_locale(self.options.locale.as_deref().unwrap_or("en"));
        match self
            .rpc
            .model_inventory_for(&session_id, &self.selected_model, locale)
        {
            Ok(inventory) => self.inventory = inventory,
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::ReadinessCheckFailedDraftKept,
                    [("error", error.to_string())],
                );
                return Ok(false);
            }
        }
        let daemon_blocked =
            self.inventory.readiness.generation > 0 && !self.inventory.readiness.can_submit;
        if daemon_blocked || !self.selected_model_is_runnable() {
            self.phase = Phase::Provider;
            self.focus = Focus::Scene;
            self.notice = Notice::localized(MessageId::ExecutionNotReadyDraftKept);
            return Ok(false);
        }
        if !media_refs.is_empty()
            && !self
                .models
                .iter()
                .find(|model| model.id == self.selected_model)
                .is_some_and(|model| model.image_input)
        {
            self.notice = Notice::localized(MessageId::ImageModelIncompatibleDraftKept);
            return Ok(false);
        }
        let envelope = PendingSubmission {
            session_id,
            prompt,
            model: self.selected_model.clone(),
            agent: if self
                .active_session
                .as_ref()
                .is_some_and(|session| session.plan_mode)
            {
                "plan".into()
            } else {
                String::new()
            },
            locale: locale.into(),
            submission_id: operation_id("tui"),
            media_refs,
            local_id: operation_id("local"),
        };
        match self.rpc.submit(
            &envelope.session_id,
            &envelope.prompt,
            &envelope.model,
            &envelope.agent,
            &envelope.locale,
            &envelope.submission_id,
            &envelope.media_refs,
        ) {
            Ok(execution) => {
                self.finish_submission(envelope, execution, true);
                Ok(true)
            }
            Err(error) => {
                if error.is_ambiguous_delivery() {
                    self.pending_submission = Some(envelope);
                    self.notice = Notice::localized(MessageId::SubmissionUnknownDraftKept);
                } else {
                    self.notice = Notice::localized_with(
                        MessageId::SubmitFailedDraftKept,
                        [("error", error.to_string())],
                    );
                }
                Ok(false)
            }
        }
    }

    fn reconcile_pending_submission(&mut self) -> Result<()> {
        let Some(envelope) = self.pending_submission.clone() else {
            return Ok(());
        };
        if self
            .active_session
            .as_ref()
            .is_none_or(|session| session.session_id != envelope.session_id)
        {
            self.notice = Notice::localized(MessageId::SubmissionUnknownDraftKept);
            return Ok(());
        }
        let mut rpc = match Client::connect(&self.options.socket) {
            Ok(mut rpc) => match rpc.initialize() {
                Ok(_) => rpc,
                Err(_) => {
                    self.notice = Notice::localized(MessageId::SubmissionUnknownDraftKept);
                    return Ok(());
                }
            },
            Err(_) => {
                self.notice = Notice::localized(MessageId::SubmissionUnknownDraftKept);
                return Ok(());
            }
        };
        match rpc.submit(
            &envelope.session_id,
            &envelope.prompt,
            &envelope.model,
            &envelope.agent,
            &envelope.locale,
            &envelope.submission_id,
            &envelope.media_refs,
        ) {
            Ok(execution) => {
                let unchanged = self.media.prompt_text(&self.composer) == envelope.prompt
                    && self.media.ready_refs_in_text_order(&self.composer) == envelope.media_refs;
                self.pending_submission = None;
                self.rpc = rpc;
                self.finish_submission(envelope, execution, unchanged);
            }
            Err(error) if error.is_ambiguous_delivery() => {
                self.notice = Notice::localized(MessageId::SubmissionUnknownDraftKept);
            }
            Err(error) => {
                self.rpc = rpc;
                self.pending_submission = None;
                self.notice = Notice::localized_with(
                    MessageId::SubmitFailedDraftKept,
                    [("error", error.to_string())],
                );
            }
        }
        Ok(())
    }

    fn finish_submission(
        &mut self,
        envelope: PendingSubmission,
        execution: ExecutionRun,
        clear_matching_draft: bool,
    ) {
        if let Some(command_id) =
            command::prompt_command_id(&envelope.prompt, &self.command_registry.commands)
        {
            self.remember_command_use(command_id);
        }
        let mut block = TranscriptBlock::local_user(envelope.local_id, envelope.prompt);
        block.id = format!("user:{}", execution.run_id);
        block.run_id = execution.run_id.clone();
        block.branchable = true;
        self.blocks.push(block);
        if clear_matching_draft {
            self.composer.set_text("");
            self.media.clear();
            self.context_completion.update_context(&self.composer);
            self.composer_state = TextAreaState::default();
        }
        self.active_run_id = Some(execution.run_id.clone());
        self.execution_timer.start_new();
        self.execution_activity = None;
        self.execution_status = if execution.status.is_empty() {
            "queued".into()
        } else {
            execution.status.clone()
        };
        self.seed_execution_lifecycle(&execution.run_id, &self.execution_status.clone());
        let submitted_agent = if execution.agent.is_empty() {
            if envelope.agent == "plan" {
                "plan"
            } else {
                "build"
            }
        } else {
            execution.agent.as_str()
        };
        let execution_status = self.execution_status.clone();
        self.update_active_execution_metadata(
            &execution.run_id,
            &execution_status,
            Some(submitted_agent),
            None,
            Some(""),
        );
        self.notice.clear();
        self.follow_transcript_bottom();
    }

    fn restore_queued_prompt(&mut self, prompt: String) {
        if self.composer.text().trim().is_empty() {
            self.composer.set_text(&prompt);
            self.composer.set_cursor(self.composer.text().len());
            self.composer_state = TextAreaState::default();
        } else {
            self.queued_prompts.push_front(prompt);
        }
    }

    fn submit_steer(&mut self, run_id: String, prompt: String) -> Result<()> {
        if prompt.is_empty() {
            return Ok(());
        }
        let steer_id = operation_id("steer");
        self.rpc.steer(&run_id, &prompt, &steer_id)?;
        self.blocks.push(TranscriptBlock::local_steer(
            format!("steer:{steer_id}"),
            run_id,
            prompt,
        ));
        self.composer.set_text("");
        self.composer_state = TextAreaState::default();
        self.notice = Notice::localized(MessageId::SteeringQueued);
        self.follow_transcript_bottom();
        Ok(())
    }

    fn handle_slash_command(&mut self, prompt: &str) -> Option<bool> {
        let command = command::lookup(prompt)?;
        if let Some(reason) =
            command::command_unavailable_reason(command, self.active_run_id.is_some())
        {
            self.notice = Notice::localized(reason);
            return Some(false);
        }
        self.remember_command_use(command::operator_id(command.id));
        match command.id {
            CommandId::Settings => {
                self.overlays
                    .replace(Overlay::Settings(SettingsOverlay { selected: 0 }));
            }
            CommandId::Density => self.apply_action(Action::ToggleDensity),
            CommandId::Status => self.apply_action(Action::OpenStatus),
            CommandId::Context => {
                self.request_context_summary();
                self.overlays.replace(Overlay::Context(
                    self.context_summary.clone().unwrap_or_default(),
                ));
            }
            CommandId::Changes => self.apply_action(Action::OpenChanges),
            CommandId::Provider => {
                self.provider_index = self
                    .inventory
                    .providers
                    .iter()
                    .position(|provider| {
                        provider
                            .models
                            .iter()
                            .any(|model| model.id == self.selected_model)
                    })
                    .unwrap_or(self.provider_index);
                self.phase = Phase::Provider;
                self.focus = Focus::Scene;
            }
            CommandId::Model => self.open_models(),
            CommandId::Plan => self.request_conversation_mode(true),
            CommandId::Build => self.request_conversation_mode(false),
            CommandId::Sessions => self.open_session_browser(),
            CommandId::Resume => {
                let paused = self
                    .active_session
                    .as_ref()
                    .is_some_and(|session| session.execution_status == "paused");
                if paused {
                    self.resume_paused_execution();
                } else {
                    self.open_session_browser();
                }
            }
            CommandId::Cancel => {
                if let Some(run_id) = self.active_run_id.clone() {
                    self.cancel_execution(&run_id);
                } else {
                    self.notice = Notice::localized(MessageId::NoActiveExecutionRun);
                }
            }
            CommandId::Minimal => self.request_screen_mode(ScreenMode::Minimal),
            CommandId::Fullscreen => self.request_screen_mode(ScreenMode::Fullscreen),
            CommandId::Inline => self.request_screen_mode(ScreenMode::Inline),
            CommandId::Queue => self.apply_action(Action::OpenQueue),
            CommandId::Quit => self.quit = true,
            CommandId::Doctor => {
                self.composer.set_text("");
                self.composer_state = TextAreaState::default();
                self.slash_selected = 0;
                self.slash_selected_id = None;
                self.slash_dismissed_input = None;
                self.open_doctor_overlay();
            }
            CommandId::Keymap | CommandId::Help => {
                // Issue #22: real help surface, not a composer re-trigger of "/".
                self.composer.set_text("");
                self.composer_state = TextAreaState::default();
                self.slash_selected = 0;
                self.slash_selected_id = None;
                self.slash_dismissed_input = None;
                self.open_help_overlay();
            }
        }
        Some(true)
    }

    fn request_screen_mode(&mut self, mode: ScreenMode) {
        self.composer.set_text("");
        self.composer_state = TextAreaState::default();
        let scrollback = if screen_capabilities(mode).native_scrollback {
            "on"
        } else {
            "off"
        };
        self.notice = Notice::localized_with(
            MessageId::ScreenModeSwitched,
            [("mode", mode.as_arg()), ("scrollback", scrollback)],
        );
        self.relaunch_screen_mode = Some(mode);
        self.quit = true;
    }

    fn open_queue_overlay(&mut self) {
        let Some(run_id) = self.active_run_id.clone() else {
            self.notice = Notice::localized(MessageId::QueueNoActiveRun);
            return;
        };
        self.slash_selected = 0;
        self.slash_selected_id = None;
        self.slash_dismissed_input = None;
        match self.rpc.list_execution_queue(&run_id, 56) {
            Ok(listed) => {
                self.overlays.replace(Overlay::Queue(QueueOverlay {
                    run_id: listed.run_id,
                    items: listed.items,
                    selected: 0,
                    soft_interrupt_pending: listed.soft_interrupt_pending,
                    load: RetainedLoad::default(),
                    error: String::new(),
                }));
            }
            Err(error) => {
                self.notice =
                    Notice::localized_with(MessageId::CancelFailed, [("error", error.to_string())]);
            }
        }
    }

    fn open_help_overlay(&mut self) {
        self.overlays
            .replace(Overlay::Help(HelpOverlay { scroll: 0 }));
    }

    fn open_doctor_overlay(&mut self) {
        match self.rpc.doctor() {
            Ok(value) => {
                let revision = self
                    .overlays
                    .active()
                    .and_then(|overlay| match overlay {
                        Overlay::Doctor(screen) => Some(screen.report.revision.saturating_add(1)),
                        _ => None,
                    })
                    .unwrap_or(1);
                self.overlays
                    .replace(Overlay::Doctor(crate::doctor::DoctorScreen::from_value(
                        &value, revision,
                    )));
            }
            Err(error) => {
                self.notice =
                    Notice::localized_with(MessageId::CancelFailed, [("error", error.to_string())]);
            }
        }
    }

    fn cancel_execution(&mut self, run_id: &str) {
        match self.rpc.cancel_execution(run_id) {
            Ok(execution) => {
                self.active_run_id = None;
                self.execution_timer.reset();
                self.execution_activity = None;
                self.execution_status = if execution.status.is_empty() {
                    "cancelled".into()
                } else {
                    execution.status
                };
                self.notice = Notice::localized(MessageId::CancellationRequested);
            }
            Err(error) => {
                self.notice =
                    Notice::localized_with(MessageId::CancelFailed, [("error", error.to_string())])
            }
        }
    }

    fn interrupt_execution(&mut self, run_id: &str) {
        match self.rpc.interrupt_execution(run_id) {
            Ok(result) => {
                self.notice = Notice::localized_with(
                    if result.already_requested {
                        MessageId::SoftInterruptAlreadyRequested
                    } else {
                        MessageId::SoftInterruptRequested
                    },
                    [("queue", result.queue_depth.to_string())],
                );
            }
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::SoftInterruptFailed,
                    [("error", error.to_string())],
                );
            }
        }
    }

    fn enter_plan_mode(&mut self) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.notice = Notice::localized(MessageId::ModeConversationRequired);
            return;
        };
        match self.rpc.set_plan_mode(&session_id, true) {
            Ok(state) if state.session_id == session_id => {
                if let Some(mut session) = self.active_session.as_ref().cloned() {
                    session.plan_mode = state.plan_mode;
                    self.remember_session(session);
                }
                self.notice = Notice::localized(if state.plan_mode {
                    MessageId::PlanModeActive
                } else {
                    MessageId::BuildModeActive
                });
            }
            Ok(_) => self.notice = Notice::localized(MessageId::ModeMismatchedSession),
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::ModeChangeFailed,
                    [("error", error.to_string())],
                )
            }
        }
    }

    fn request_conversation_mode(&mut self, plan_mode: bool) {
        if self.active_run_id.is_some() {
            self.notice = Notice::localized(MessageId::ModeChangeBlocked);
            return;
        }
        let Some(current) = self
            .active_session
            .as_ref()
            .map(|session| session.plan_mode)
        else {
            self.notice = Notice::localized(MessageId::ModeConversationRequired);
            return;
        };
        if current == plan_mode {
            self.notice = Notice::localized(if plan_mode {
                MessageId::PlanModeActive
            } else {
                MessageId::BuildModeActive
            });
            return;
        }
        match conversation_mode_action(false, current) {
            Some(ConversationModeAction::EnterPlan) => self.enter_plan_mode(),
            Some(ConversationModeAction::ApprovePlan) => self.approve_plan(),
            None => unreachable!("idle conversation mode transition must have an action"),
        }
    }

    fn cycle_conversation_mode(&mut self) {
        let current = self
            .active_session
            .as_ref()
            .is_some_and(|session| session.plan_mode);
        let Some(action) = conversation_mode_action(self.active_run_id.is_some(), current) else {
            self.notice = Notice::localized(MessageId::ModeChangeBlocked);
            return;
        };
        match action {
            ConversationModeAction::EnterPlan => self.enter_plan_mode(),
            ConversationModeAction::ApprovePlan => self.approve_plan(),
        }
    }

    fn approve_plan(&mut self) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
            review.resolving = true;
            review.error.clear();
        }
        match self.rpc.approve_plan(&session_id) {
            Ok(result) if result.session_id == session_id && result.approved => {
                if let Some(mut session) = self.active_session.as_ref().cloned() {
                    session.plan_mode = result.plan_mode;
                    if let Some(execution) = result.execution.as_ref() {
                        session.latest_run_id = execution.run_id.clone();
                        session.latest_run_agent = if execution.agent.is_empty() {
                            "build".into()
                        } else {
                            execution.agent.clone()
                        };
                        session.execution_status = if execution.status.is_empty() {
                            "queued".into()
                        } else {
                            execution.status.clone()
                        };
                        session.summary.clear();
                        session.latest_run_result_kind.clear();
                    }
                    self.remember_session(session);
                }
                self.overlays.resolve_active();
                if let Some(execution) = result.execution {
                    self.active_run_id = Some(execution.run_id.clone());
                    self.execution_timer.start_new();
                    self.execution_activity = None;
                    self.execution_status = if execution.status.is_empty() {
                        "queued".into()
                    } else {
                        execution.status
                    };
                    self.seed_execution_lifecycle(
                        &execution.run_id,
                        &self.execution_status.clone(),
                    );
                    self.notice = Notice::localized_for_run(
                        MessageId::PlanApprovedQueued,
                        execution.run_id,
                        std::iter::empty::<(&str, &str)>(),
                    );
                } else {
                    self.notice = Notice::localized(MessageId::PlanApprovedBuild);
                }
                self.focus = Focus::Composer;
            }
            Ok(_) => {
                if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
                    review.resolving = false;
                    review.error = "The daemon did not confirm this plan approval.".into();
                }
            }
            Err(error) => {
                if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
                    review.resolving = false;
                    review.error = format!("Approval failed: {error}");
                }
            }
        }
    }

    fn revise_plan(&mut self) {
        self.overlays.resolve_active();
        self.focus = Focus::Composer;
        self.notice = Notice::localized(MessageId::PlanRevisionRequested);
    }

    fn cancel_plan(&mut self) {
        self.overlays.resolve_active();
        self.focus = Focus::Composer;
        self.notice = Notice::localized(MessageId::PlanCancelled);
    }

    fn handle_overlay_key(&mut self, key: KeyEvent) {
        let mut deferred = None;
        let mut retry_file = None;
        let mut deferred_doctor = None;
        let mut deferred_doctor_rerun = false;
        match self.overlays.active_mut() {
            Some(Overlay::Approval(approval)) => match key.code {
                KeyCode::Left | KeyCode::BackTab => {
                    let index = ApprovalScope::ALL
                        .iter()
                        .position(|scope| *scope == approval.scope)
                        .unwrap_or(0);
                    approval.scope = ApprovalScope::ALL[index.saturating_sub(1)];
                }
                KeyCode::Right | KeyCode::Tab => {
                    let index = ApprovalScope::ALL
                        .iter()
                        .position(|scope| *scope == approval.scope)
                        .unwrap_or(0);
                    approval.scope = ApprovalScope::ALL[(index + 1).min(2)];
                }
                KeyCode::Up => approval.scroll = approval.scroll.saturating_sub(1),
                KeyCode::Down => approval.scroll = approval.scroll.saturating_add(1),
                KeyCode::Char('y') | KeyCode::Char('a') | KeyCode::Enter => {
                    deferred = Some(Action::ApprovalAllow)
                }
                KeyCode::Char('n') | KeyCode::Char('d') => deferred = Some(Action::ApprovalDeny),
                KeyCode::Esc => {
                    approval.error = "The current response is waiting for a decision. Allow, deny, or press Ctrl+C to stop it.".into();
                }
                _ => {}
            },
            Some(Overlay::Question(question)) => {
                if question.resolving {
                    return;
                }
                if question.options.is_empty() {
                    match key.code {
                        KeyCode::Char(character)
                            if !key
                                .modifiers
                                .intersects(KeyModifiers::CONTROL | KeyModifiers::SUPER) =>
                        {
                            question.input.push(character);
                        }
                        KeyCode::Backspace => {
                            question.input.pop();
                        }
                        KeyCode::Enter => deferred = Some(Action::QuestionOption(usize::MAX)),
                        KeyCode::Esc => {
                            question.error =
                                "Answer the question or press Ctrl+C to stop the response.".into();
                        }
                        _ => {}
                    }
                } else {
                    match key.code {
                        KeyCode::Up | KeyCode::Left | KeyCode::BackTab => {
                            question.selected = question.selected.saturating_sub(1)
                        }
                        KeyCode::Down | KeyCode::Right | KeyCode::Tab => {
                            question.selected = (question.selected + 1)
                                .min(question.options.len().saturating_sub(1));
                        }
                        KeyCode::Enter => {
                            deferred = Some(Action::QuestionOption(question.selected))
                        }
                        KeyCode::Esc => {
                            question.error =
                                "Choose an answer or press Ctrl+C to stop the response.".into();
                        }
                        _ => {}
                    }
                }
            }
            Some(Overlay::PlanReview(review)) => {
                if review.resolving {
                    return;
                }
                match key.code {
                    KeyCode::Up => review.scroll = review.scroll.saturating_sub(1),
                    KeyCode::Down => review.scroll = review.scroll.saturating_add(1),
                    code => deferred = plan_review_key_action(code),
                }
            }
            Some(Overlay::Settings(settings)) => match key.code {
                KeyCode::Up | KeyCode::BackTab => {
                    settings.selected = settings.selected.saturating_sub(1)
                }
                KeyCode::Down | KeyCode::Tab => {
                    settings.selected =
                        (settings.selected + 1).min(SETTINGS_ITEM_COUNT.saturating_sub(1));
                }
                KeyCode::Enter => deferred = settings_action(settings.selected),
                KeyCode::Esc => self.overlays.resolve_active(),
                _ => {}
            },
            Some(Overlay::Status(_)) => match key.code {
                KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::CloseOverlay),
                KeyCode::Char('r') => deferred = Some(Action::OpenStatus),
                KeyCode::Char('a') => deferred = Some(Action::OpenAgents),
                KeyCode::Char('c') => deferred = Some(Action::OpenChanges),
                _ => {}
            },
            Some(Overlay::Context(_)) => match key.code {
                KeyCode::Char('r') => self.request_context_summary(),
                KeyCode::Esc | KeyCode::Char('q') | KeyCode::Enter => {
                    deferred = Some(Action::CloseOverlay)
                }
                _ => {}
            },
            Some(Overlay::Help(help)) => match key.code {
                KeyCode::Up | KeyCode::Char('k') => help.scroll = help.scroll.saturating_sub(1),
                KeyCode::Down | KeyCode::Char('j') => help.scroll = help.scroll.saturating_add(1),
                KeyCode::PageUp => help.scroll = help.scroll.saturating_sub(8),
                KeyCode::PageDown => help.scroll = help.scroll.saturating_add(8),
                KeyCode::Esc | KeyCode::Char('q') | KeyCode::Char('?') => {
                    deferred = Some(Action::CloseOverlay)
                }
                _ => {}
            },
            Some(Overlay::Doctor(doctor)) => {
                if doctor.confirm_pending.is_some() {
                    match key.code {
                        KeyCode::Enter => {
                            if let Some(action) = doctor.confirm_pending.take() {
                                deferred_doctor = Some(action);
                            }
                        }
                        KeyCode::Esc => doctor.confirm_pending = None,
                        _ => {}
                    }
                } else {
                    match key.code {
                        KeyCode::Left | KeyCode::Char('h') => doctor.move_section(-1),
                        KeyCode::Right | KeyCode::Char('l') => doctor.move_section(1),
                        KeyCode::Up | KeyCode::Char('k') => doctor.move_check(-1),
                        KeyCode::Down | KeyCode::Char('j') => doctor.move_check(1),
                        KeyCode::Tab => doctor.move_action(1),
                        KeyCode::BackTab => doctor.move_action(-1),
                        KeyCode::Enter => {
                            if let Some(action) = doctor.active_action() {
                                if action.requires_confirmation {
                                    doctor.confirm_pending = Some(action);
                                } else {
                                    deferred_doctor = Some(action);
                                }
                            }
                        }
                        KeyCode::Char('r') => deferred_doctor_rerun = true,
                        KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::CloseOverlay),
                        _ => {}
                    }
                }
            }
            Some(Overlay::Agents(agents)) => match key.code {
                KeyCode::Esc if agents.confirm_stop => agents.confirm_stop = false,
                KeyCode::Up | KeyCode::Char('k') => {
                    deferred = Some(Action::SelectAgent(agents.selected.saturating_sub(1)))
                }
                KeyCode::Down | KeyCode::Char('j') => {
                    let count = agents.projection.agents.needs_input.len()
                        + agents.projection.agents.working.len()
                        + agents.projection.agents.completed.len();
                    deferred = Some(Action::SelectAgent(
                        (agents.selected + 1).min(count.saturating_sub(1)),
                    ));
                }
                KeyCode::Enter if agents.confirm_stop => deferred = Some(Action::ConfirmStopAgent),
                KeyCode::Enter => deferred = Some(Action::OpenSelectedAgentSession),
                KeyCode::Char('s') => deferred = Some(Action::BeginStopAgent),
                KeyCode::Char('y') if agents.confirm_stop => {
                    deferred = Some(Action::ConfirmStopAgent)
                }
                KeyCode::Char('r') => deferred = Some(Action::RefreshAgents),
                KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::OpenStatus),
                _ => {}
            },
            Some(Overlay::Changes(changes)) => match key.code {
                KeyCode::Esc if changes.confirm_rollback => {
                    deferred = Some(Action::CancelPatchRollback)
                }
                KeyCode::Up | KeyCode::Char('k') => {
                    changes.selected = changes.selected.saturating_sub(1);
                    changes.scroll = 0;
                }
                KeyCode::Down | KeyCode::Char('j') => {
                    let count = if !changes.projection.patches.is_empty() {
                        changes.projection.patches.len()
                    } else if changes.projection.workspace_diff.files.is_empty() {
                        changes.projection.review.changes.len()
                    } else {
                        changes.projection.workspace_diff.files.len()
                    };
                    changes.selected = (changes.selected + 1).min(count.saturating_sub(1));
                    changes.scroll = 0;
                }
                KeyCode::PageDown => changes.scroll = changes.scroll.saturating_add(12),
                KeyCode::PageUp => changes.scroll = changes.scroll.saturating_sub(12),
                KeyCode::Home => changes.scroll = 0,
                KeyCode::Enter if changes.confirm_rollback => {
                    deferred = Some(Action::ConfirmPatchRollback)
                }
                KeyCode::Char('y') if changes.confirm_rollback => {
                    deferred = Some(Action::ConfirmPatchRollback)
                }
                KeyCode::Enter if !changes.projection.patches.is_empty() => {
                    deferred = Some(Action::BeginPatchRollback)
                }
                KeyCode::Char('r') => deferred = Some(Action::RefreshChanges),
                KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::OpenStatus),
                _ => {}
            },
            Some(Overlay::FileViewer(viewer)) => {
                let visible_rows = self.transcript_area.height.saturating_sub(8).max(1) as usize;
                if viewer.search.is_some() {
                    match key.code {
                        KeyCode::Esc => viewer.search = None,
                        KeyCode::Enter => {
                            viewer.search_next(false, visible_rows);
                            viewer.search = None;
                        }
                        KeyCode::Backspace => {
                            viewer.search.as_mut().map(String::pop);
                        }
                        KeyCode::Char(character)
                            if !key
                                .modifiers
                                .intersects(KeyModifiers::CONTROL | KeyModifiers::SUPER) =>
                        {
                            if let Some(query) = viewer.search.as_mut() {
                                query.push(character);
                            }
                        }
                        _ => {}
                    }
                } else {
                    match key.code {
                        KeyCode::Up | KeyCode::Char('k') => viewer.move_cursor(-1, visible_rows),
                        KeyCode::Down | KeyCode::Char('j') => viewer.move_cursor(1, visible_rows),
                        KeyCode::PageUp => viewer.page(-1, visible_rows),
                        KeyCode::PageDown => viewer.page(1, visible_rows),
                        KeyCode::Home => {
                            viewer.cursor = 0;
                            viewer.scroll = 0;
                        }
                        KeyCode::End if !viewer.lines.is_empty() => {
                            viewer.cursor = viewer.lines.len() - 1;
                            viewer.scroll = viewer.cursor.saturating_sub(visible_rows - 1);
                        }
                        KeyCode::Char('v') => viewer.toggle_range(),
                        KeyCode::Char('/') => viewer.begin_search(),
                        KeyCode::Enter if viewer.load == FileViewerLoad::Ready => {
                            deferred = Some(Action::ConfirmFileViewer)
                        }
                        KeyCode::Char('r') if matches!(viewer.load, FileViewerLoad::Failed(_)) => {
                            viewer.load = FileViewerLoad::Loading;
                            retry_file = Some((
                                viewer.generation,
                                viewer.session_id.clone(),
                                viewer.path.clone(),
                            ));
                        }
                        KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::CloseOverlay),
                        _ => {}
                    }
                }
            }
            Some(Overlay::Queue(queue)) => {
                if queue.load.loading {
                    if matches!(key.code, KeyCode::Esc | KeyCode::Char('q')) {
                        deferred = Some(Action::CloseOverlay);
                    }
                } else {
                    match key.code {
                        KeyCode::Up | KeyCode::Char('k') => {
                            queue.selected = queue.selected.saturating_sub(1);
                        }
                        KeyCode::Down | KeyCode::Char('j') => {
                            if !queue.items.is_empty() {
                                queue.selected =
                                    (queue.selected + 1).min(queue.items.len().saturating_sub(1));
                            }
                        }
                        KeyCode::Char('d') | KeyCode::Backspace | KeyCode::Delete => {
                            if let Some(item) = queue.items.get(queue.selected).cloned() {
                                let run_id = queue.run_id.clone();
                                match self.rpc.drop_execution_queue_item(&run_id, &item.steer_id) {
                                    Ok(result) => {
                                        if result.dropped {
                                            queue.items.remove(queue.selected);
                                            if queue.selected > 0
                                                && queue.selected >= queue.items.len()
                                            {
                                                queue.selected =
                                                    queue.items.len().saturating_sub(1);
                                            }
                                            self.notice = Notice::localized_with(
                                                MessageId::QueueDropped,
                                                [("remaining", result.queue_depth.to_string())],
                                            );
                                        }
                                    }
                                    Err(error) => {
                                        queue.error = error.to_string();
                                    }
                                }
                            }
                        }
                        KeyCode::Esc | KeyCode::Char('q') => {
                            deferred = Some(Action::CloseOverlay);
                        }
                        _ => {}
                    }
                }
            }
            None => {}
        }
        if let Some((generation, session_id, path)) = retry_file {
            self.load_file_viewer(generation, session_id, path);
        }
        if deferred_doctor_rerun {
            self.open_doctor_overlay();
        }
        if let Some(action) = deferred_doctor {
            self.apply_doctor_action(action);
        }
        if let Some(action) = deferred {
            match action {
                Action::QuestionOption(usize::MAX) => self.answer_active_question(None),
                other => self.apply_action(other),
            }
        }
    }

    fn apply_doctor_action(&mut self, action: crate::doctor::RecoveryAction) {
        use crate::doctor::{DoctorOperation, RecoveryActionKind, execute_recovery};
        match execute_recovery(&action) {
            Ok(DoctorOperation::Running) if action.kind == RecoveryActionKind::RerunDoctor => {
                self.open_doctor_overlay();
            }
            Ok(DoctorOperation::Succeeded) if action.kind == RecoveryActionKind::ExitDoctor => {
                self.overlays.resolve_active();
            }
            Ok(DoctorOperation::Succeeded) if action.kind == RecoveryActionKind::OpenSettings => {
                self.overlays
                    .replace(Overlay::Settings(SettingsOverlay { selected: 0 }));
            }
            Ok(DoctorOperation::Succeeded) if action.kind == RecoveryActionKind::CopyEvidence => {
                if let Some(Overlay::Doctor(screen)) = self.overlays.active() {
                    let payload = screen
                        .current_check()
                        .map(|check| {
                            check
                                .evidence
                                .iter()
                                .map(|item| format!("{}={}", item.label, item.value))
                                .collect::<Vec<_>>()
                                .join("\n")
                        })
                        .unwrap_or_default();
                    let _ = payload; // clipboard write is best-effort in product path
                    if let Some(Overlay::Doctor(screen)) = self.overlays.active_mut() {
                        screen.operation = DoctorOperation::Succeeded;
                        screen.operation_detail = "Evidence prepared for copy.".into();
                    }
                }
            }
            Ok(status) => {
                if let Some(Overlay::Doctor(screen)) = self.overlays.active_mut() {
                    screen.operation = status;
                    screen.operation_detail = action.detail.clone();
                    screen.confirm_pending = None;
                }
            }
            Err(error) => {
                if let Some(Overlay::Doctor(screen)) = self.overlays.active_mut() {
                    screen.operation = DoctorOperation::Failed;
                    screen.operation_detail = error;
                    screen.confirm_pending = None;
                }
            }
        }
    }

    fn resolve_active_approval(&mut self, approve: bool) {
        let Some(Overlay::Approval(approval)) = self.overlays.active() else {
            return;
        };
        if approval.resolving {
            return;
        }
        let decision_id = approval.decision_id.clone();
        let scope = approval.scope.as_str();
        if let Some(Overlay::Approval(approval)) = self.overlays.active_mut() {
            approval.resolving = true;
            approval.error.clear();
        }
        match self.rpc.resolve_approval(&decision_id, approve, scope) {
            Ok(_) => {
                self.overlays
                    .resolve_by_id(GovernanceId::Approval(decision_id.clone()));
                self.notice = Notice::localized(if approve {
                    MessageId::Allow
                } else {
                    MessageId::Deny
                });
            }
            Err(error) => {
                if let Some(Overlay::Approval(approval)) = self.overlays.active_mut() {
                    approval.resolving = false;
                    approval.error = format!("Decision failed: {error}");
                }
            }
        }
    }

    fn answer_active_question(&mut self, option_index: Option<usize>) {
        let Some(Overlay::Question(question)) = self.overlays.active() else {
            return;
        };
        let value = match option_index {
            Some(index) => question
                .options
                .get(index)
                .map(|option| option.value.trim().to_owned())
                .unwrap_or_default(),
            None => question.input.trim().to_owned(),
        };
        if value.is_empty() {
            let locale = self.ui_locale();
            if let Some(Overlay::Question(question)) = self.overlays.active_mut() {
                question.error = tr(locale, MessageId::AnswerRequired).into();
            }
            return;
        }
        let question_id = question.question_id.clone();
        if let Some(Overlay::Question(question)) = self.overlays.active_mut() {
            question.resolving = true;
            question.error.clear();
        }
        match self.rpc.answer_question(&question_id, &value) {
            Ok(_) => {
                self.overlays
                    .resolve_by_id(GovernanceId::Question(question_id.clone()));
                self.notice = Notice::localized(MessageId::AnsweredQuestion);
            }
            Err(error) => {
                let locale = self.ui_locale();
                if let Some(Overlay::Question(question)) = self.overlays.active_mut() {
                    question.resolving = false;
                    question.error = tr_format(
                        locale,
                        MessageId::AnswerFailed,
                        &[("error", &error.to_string())],
                    );
                }
            }
        }
    }

    fn resume_paused_execution(&mut self) {
        if let Some(blocker) = self.paused_resume_blocker() {
            self.notice = blocker;
            return;
        }
        let run_id = self
            .active_session
            .as_ref()
            .filter(|session| session.execution_status == "paused")
            .map(|session| session.latest_run_id.clone())
            .filter(|run_id| !run_id.is_empty());
        let Some(run_id) = run_id else {
            self.notice = Notice::localized(MessageId::NoPausedExecutionConversation);
            return;
        };
        if matches!(self.overlays.active(), Some(Overlay::Settings(_))) {
            self.overlays.resolve_active();
        }
        self.start_paused_resume(run_id);
    }

    fn paused_resume_blocker(&self) -> Option<Notice> {
        let recovery = &self.active_session.as_ref()?.continuity.as_ref()?.recovery;
        if !matches!(recovery.disposition.as_str(), "review_required" | "blocked") {
            return None;
        }
        let failed = recovery
            .proofs
            .iter()
            .filter_map(|(proof, passed)| (!passed).then_some(proof.as_str()))
            .collect::<Vec<_>>();
        let reason = if recovery.reason.is_empty() {
            "continuity proof is incomplete"
        } else {
            recovery.reason.as_str()
        };
        let proofs = if failed.is_empty() {
            String::new()
        } else {
            format!("; failed proofs: {}", failed.join(", "))
        };
        Some(Notice::localized_with(
            MessageId::ContinuityResumeBlocked,
            [("reason", reason.to_owned()), ("proofs", proofs)],
        ))
    }

    fn start_paused_resume(&mut self, run_id: String) {
        if self.resume_pending {
            return;
        }
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        self.resume_generation = self.resume_generation.saturating_add(1);
        let generation = self.resume_generation;
        self.resume_pending = true;
        self.notice = Notice::localized(MessageId::ResumingExecution);
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = resume_paused_execution_and_refresh(&socket, &session_id, &run_id);
            let _ = tx.send(AsyncMessage::PausedResume {
                generation,
                session_id,
                run_id,
                result,
            });
        });
    }

    fn apply_paused_resume(
        &mut self,
        generation: u64,
        session_id: &str,
        run_id: &str,
        result: Result<PausedResumeOutcome, String>,
    ) {
        if generation != self.resume_generation
            || self
                .active_session
                .as_ref()
                .map(|session| session.session_id.as_str())
                != Some(session_id)
        {
            return;
        }
        self.resume_pending = false;
        if self
            .active_session
            .as_ref()
            .is_some_and(|session| session.latest_run_id != run_id)
        {
            return;
        }
        let outcome = match result {
            Ok(outcome) => outcome,
            Err(error) => {
                self.notice =
                    Notice::localized_with(MessageId::ResumeExecutionFailed, [("error", error)]);
                return;
            }
        };
        self.active_run_id = Some(outcome.execution.run_id.clone());
        self.execution_timer.start_new();
        self.execution_activity = None;
        self.execution_status = if outcome.execution.status.is_empty() {
            "running".into()
        } else {
            outcome.execution.status.clone()
        };
        if let Some(mut session) = outcome
            .session
            .or_else(|| self.active_session.as_ref().cloned())
        {
            session.latest_run_id = outcome.execution.run_id.clone();
            session.execution_status = self.execution_status.clone();
            self.remember_session(session);
        }
        if let Some(items) = outcome.items {
            self.execution_lifecycle.clear();
            self.tool_disclosure_overrides.clear();
            self.blocks = self.transcript_reducer.hydrate(items);
            self.scrollback.reset();
            self.transcript_stale = false;
            self.reset_transcript_viewport();
        } else {
            self.execution_lifecycle.clear();
            self.tool_disclosure_overrides.clear();
            self.blocks = self.transcript_reducer.hydrate(Vec::new());
            self.scrollback.reset();
            self.transcript_stale = true;
            self.reset_transcript_viewport();
        }
        self.seed_execution_lifecycle(&outcome.execution.run_id, &self.execution_status.clone());
        self.event_cursor = 0;
        self.start_event_stream();
        self.notice = if let Some(error) = outcome.refresh_error {
            Notice::localized_with(MessageId::ResumeRefreshFailed, [("error", error)])
        } else {
            Notice::localized(MessageId::ExecutionResumed)
        };
    }

    fn close_top_non_governance(&mut self) -> bool {
        let Some(overlay) = self.overlays.active() else {
            return false;
        };
        if overlay.is_governance() {
            return false;
        }
        self.overlays.resolve_active();
        true
    }

    fn handle_rewind_escape(&mut self) {
        let eligible = self.eligible_history_indices();
        let now = Instant::now();
        match rewind_escape_action(
            self.active_run_id.is_some(),
            !eligible.is_empty(),
            self.rewind_primed_at,
            now,
            self.rewind_prime_window,
        ) {
            RewindEscapeAction::Busy => {
                self.notice = Notice::localized(MessageId::HistoryBusy);
                self.rewind_primed_at = None;
            }
            RewindEscapeAction::Unavailable => {
                self.notice = Notice::localized(MessageId::HistoryNoEarlierPrompt);
                self.rewind_primed_at = None;
            }
            RewindEscapeAction::Prime => {
                self.rewind_primed_at = Some(now);
                self.notice = Notice::localized(MessageId::HistoryPrime);
            }
            RewindEscapeAction::Open => {
                self.history_generation = self.history_generation.saturating_add(1);
                self.history_branch_request_id = None;
                self.history_branch_pending = false;
                self.history_stashed_draft = Some(self.composer.text().to_owned());
                self.history_original_scroll =
                    Some((self.transcript_scroll, self.transcript_follow_bottom));
                self.composer.set_text("");
                self.composer_state = TextAreaState::default();
                self.history_selected = eligible.last().copied();
                self.rewind_primed_at = None;
                self.sync_history_selection();
                self.notice = Notice::localized(MessageId::HistoryChoosePrompt);
            }
        }
    }

    fn handle_history_key(&mut self, key: KeyEvent) -> Result<()> {
        if self.history_branch_pending {
            if key.code == KeyCode::Esc {
                self.notice = Notice::localized(MessageId::HistoryBranchCreating);
            }
            return Ok(());
        }
        match key.code {
            KeyCode::Esc => self.cancel_history_selection(),
            KeyCode::Up | KeyCode::Left => self.move_history_selection(-1),
            KeyCode::Down | KeyCode::Right => self.move_history_selection(1),
            KeyCode::Enter => self.branch_from_history(),
            KeyCode::Char('q') => self.cancel_history_selection(),
            KeyCode::Char(_) => {
                self.cancel_history_selection();
                self.composer.input(key);
            }
            _ => {}
        }
        Ok(())
    }

    fn eligible_history_indices(&self) -> Vec<usize> {
        self.blocks
            .iter()
            .enumerate()
            .filter(|(_, block)| {
                block.kind == crate::transcript::BlockKind::User
                    && block.branchable
                    && !block.run_id.is_empty()
                    && !block.source_prompt.trim().is_empty()
            })
            .map(|(index, _)| index)
            .collect()
    }

    fn move_history_selection(&mut self, delta: isize) {
        let eligible = self.eligible_history_indices();
        if eligible.is_empty() {
            self.cancel_history_selection();
            return;
        }
        let current = self
            .history_selected
            .and_then(|selected| eligible.iter().position(|index| *index == selected))
            .unwrap_or(eligible.len() - 1);
        let next = current
            .saturating_add_signed(delta)
            .min(eligible.len().saturating_sub(1));
        let next = eligible[next];
        if self.history_selected != Some(next) {
            self.history_branch_request_id = None;
        }
        self.history_selected = Some(next);
        self.sync_history_selection();
    }

    fn sync_history_selection(&mut self) {
        for (index, block) in self.blocks.iter_mut().enumerate() {
            block.selected = self.history_selected == Some(index);
        }
    }

    fn cancel_history_selection(&mut self) {
        if self.history_branch_pending {
            self.notice = Notice::localized(MessageId::HistoryBranchCreating);
            return;
        }
        self.history_generation = self.history_generation.saturating_add(1);
        self.history_branch_request_id = None;
        self.history_selected = None;
        self.sync_history_selection();
        if let Some(draft) = self.history_stashed_draft.take() {
            self.composer.set_text(&draft);
            self.composer_state = TextAreaState::default();
        }
        if let Some((scroll, follow_bottom)) = self.history_original_scroll.take() {
            self.transcript_scroll = scroll;
            self.transcript_follow_bottom = follow_bottom;
        }
        self.notice.clear();
    }

    fn branch_from_history(&mut self) {
        let Some(selected) = self.history_selected else {
            return;
        };
        let Some(source_session) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.notice = Notice::localized(MessageId::HistoryMissingSource);
            return;
        };
        let eligible = self.eligible_history_indices();
        let Some(position) = eligible.iter().position(|index| *index == selected) else {
            self.notice = Notice::localized(MessageId::HistorySelectionExpired);
            return;
        };
        let selected_block_id = self.blocks[selected].id.clone();
        let previous_run_id = if position == 0 {
            None
        } else {
            Some(self.blocks[eligible[position - 1]].run_id.clone())
        };
        let client_fork_id = self
            .history_branch_request_id
            .get_or_insert_with(|| operation_id("history-fork"))
            .clone();
        let generation = self.history_generation;
        self.history_branch_pending = true;
        self.notice = Notice::localized(MessageId::HistoryBranchCreating);
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = branch_history_and_load(
                &socket,
                &source_session,
                previous_run_id.as_deref(),
                position == 0,
                &client_fork_id,
            );
            let _ = tx.send(AsyncMessage::HistoryBranch {
                generation,
                source_session_id: source_session,
                selected_block_id,
                result,
            });
        });
    }

    fn apply_history_branch(
        &mut self,
        generation: u64,
        source_session_id: &str,
        selected_block_id: &str,
        result: Result<HistoryBranchOutcome, String>,
    ) {
        if generation != self.history_generation
            || !self.history_branch_pending
            || self
                .active_session
                .as_ref()
                .map(|session| session.session_id.as_str())
                != Some(source_session_id)
        {
            return;
        }
        let selected_prompt = self
            .blocks
            .iter()
            .find(|block| block.id == selected_block_id && block.branchable)
            .map(|block| block.source_prompt.clone());
        let Some(selected_prompt) = selected_prompt else {
            self.history_branch_pending = false;
            self.history_branch_request_id = None;
            self.notice = Notice::localized(MessageId::HistorySelectionExpired);
            return;
        };
        let outcome = match result {
            Ok(outcome) => outcome,
            Err(error) => {
                self.history_branch_pending = false;
                self.notice =
                    Notice::localized_with(MessageId::HistoryBranchFailed, [("error", error)]);
                return;
            }
        };

        let draft = match self.history_stashed_draft.as_deref() {
            Some(stashed) if !stashed.trim().is_empty() => {
                format!("{selected_prompt}\n\n{stashed}")
            }
            _ => selected_prompt,
        };
        if self
            .models
            .iter()
            .any(|model| model.id == outcome.session.next_model)
        {
            self.selected_model = outcome.session.next_model.clone();
        }
        self.execution_lifecycle.clear();
        self.tool_disclosure_overrides.clear();
        self.blocks = self.transcript_reducer.hydrate(outcome.items);
        self.scrollback.reset();
        self.persisted_prompt_history = outcome.prompt_history;
        self.persisted_prompt_history_unavailable = outcome.prompt_history_unavailable;
        self.transcript_stale = false;
        self.reset_transcript_viewport();
        self.active_run_id = None;
        self.execution_timer.reset();
        self.execution_activity = None;
        self.execution_status = "ready".into();
        self.remember_session(outcome.session);
        self.overlays = OverlayStack::default();
        let conversation_ready = self.return_to_conversation_or_repair();
        self.event_cursor = 0;
        self.start_event_stream();
        self.composer.set_text(&draft);
        self.composer_state = TextAreaState::default();
        self.history_selected = None;
        self.history_stashed_draft = None;
        self.history_original_scroll = None;
        self.history_branch_request_id = None;
        self.history_branch_pending = false;
        self.history_generation = self.history_generation.saturating_add(1);
        if conversation_ready {
            self.notice = Notice::localized(MessageId::HistoryBranched);
        }
    }

    fn reset_transcript_viewport(&mut self) {
        self.transcript_height_cache.clear();
        self.transcript_render_cache.clear();
        self.transcript_scroll = 0;
        self.transcript_max_scroll = 0;
        self.transcript_follow_bottom = true;
    }

    fn follow_transcript_bottom(&mut self) {
        self.transcript_follow_bottom = true;
        self.transcript_scroll = self.transcript_max_scroll;
    }

    fn transcript_page_size(&self) -> usize {
        self.transcript_area.height.saturating_sub(2).max(1) as usize
    }

    fn scroll_transcript(&mut self, delta: isize) {
        if delta < 0 {
            let next = self.transcript_scroll.saturating_sub(delta.unsigned_abs());
            if next != self.transcript_scroll {
                self.transcript_scroll = next;
                self.transcript_follow_bottom = false;
            }
        } else {
            self.transcript_scroll = self
                .transcript_scroll
                .saturating_add(delta as usize)
                .min(self.transcript_max_scroll);
            self.transcript_follow_bottom = self.transcript_scroll >= self.transcript_max_scroll;
        }
    }

    fn handle_mouse(&mut self, mouse: MouseEvent) {
        let position = Position::new(mouse.column, mouse.row);
        match mouse.kind {
            MouseEventKind::Moved => {
                if self.interactions.update_hover(position) {
                    self.dirty = true;
                }
                if let Some(Action::PreviewMedia(element_id)) =
                    self.interactions.action_at(position)
                {
                    if self.media.set_hovered(Some(element_id)) {
                        self.dirty = true;
                    }
                } else if self.overlays.active().is_none()
                    && self.phase == Phase::Conversation
                    && self.composer_area.contains(position)
                {
                    let _ =
                        self.composer
                            .handle_mouse(mouse, self.composer_area, self.composer_state);
                    let hovered = self
                        .composer
                        .element_at_screen(
                            mouse.column,
                            mouse.row,
                            self.composer_area,
                            self.composer_state,
                        )
                        .filter(|element| element.kind == IMAGE_ELEMENT_KIND)
                        .map(|element| element.id);
                    while self.composer.poll_element_event().is_some() {}
                    if self.media.set_hovered(hovered) {
                        self.dirty = true;
                    }
                } else if self.media.set_hovered(None) {
                    self.dirty = true;
                }
            }
            MouseEventKind::Down(MouseButton::Left) => {
                let action = self.interactions.action_at(position);
                if let Some(action) = action {
                    self.media.set_hovered(None);
                    self.apply_action(action);
                } else if self.overlays.active().is_none()
                    && self.phase == Phase::Conversation
                    && self.composer_area.contains(position)
                {
                    self.media.set_hovered(None);
                    let _ =
                        self.composer
                            .handle_mouse(mouse, self.composer_area, self.composer_state);
                    while let Some(event) = self.composer.poll_element_event() {
                        if event.kind == TextElementEventKind::Click {
                            self.media.set_hovered(Some(event.id));
                        }
                    }
                    self.focus = Focus::Composer;
                } else {
                    self.media.set_hovered(None);
                }
                self.dirty = true;
            }
            MouseEventKind::ScrollUp
                if self.overlays.active().is_none() && self.transcript_area.contains(position) =>
            {
                self.scroll_transcript(-3);
                self.dirty = true;
            }
            MouseEventKind::ScrollDown
                if self.overlays.active().is_none() && self.transcript_area.contains(position) =>
            {
                self.scroll_transcript(3);
                self.dirty = true;
            }
            _ => {}
        }
    }

    fn request_agents(&mut self) {
        self.product_generation = self.product_generation.saturating_add(1);
        let generation = self.product_generation;
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
            .unwrap_or_default();
        match self.overlays.active_mut() {
            Some(Overlay::Agents(agents)) => {
                agents.load.refresh(generation, session_id.clone());
                agents.confirm_stop = false;
            }
            _ => self
                .overlays
                .replace(Overlay::Agents(Box::new(AgentDashboardOverlay {
                    projection: ProductProjection::default(),
                    selected: 0,
                    recap: None,
                    load: RetainedLoad::begin(generation, session_id.clone()),
                    recap_load: RetainedLoad::default(),
                    confirm_stop: false,
                }))),
        }
        let socket = self.rpc.socket().to_path_buf();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = ProductProjection::load_from_socket(
                &socket,
                (!session_id.is_empty()).then_some(session_id.as_str()),
            )
            .map(|projection| Box::new(AgentsLoadOutcome { projection }));
            let _ = tx.send(AsyncMessage::AgentsLoaded {
                generation,
                session_id,
                result,
            });
        });
    }

    fn request_changes(&mut self) {
        self.product_generation = self.product_generation.saturating_add(1);
        let generation = self.product_generation;
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
            .unwrap_or_default();
        match self.overlays.active_mut() {
            Some(Overlay::Changes(changes)) => {
                changes.load.refresh(generation, session_id.clone());
                changes.confirm_rollback = false;
                changes.rollback_preview = None;
                changes.rollback_error.clear();
            }
            _ => self
                .overlays
                .replace(Overlay::Changes(Box::new(ChangesOverlay {
                    projection: ProductProjection::default(),
                    selected: 0,
                    scroll: 0,
                    load: RetainedLoad::begin(generation, session_id.clone()),
                    confirm_rollback: false,
                    rollback_preview: None,
                    rollback_error: String::new(),
                }))),
        }
        let socket = self.rpc.socket().to_path_buf();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = ProductProjection::load_from_socket(
                &socket,
                (!session_id.is_empty()).then_some(session_id.as_str()),
            )
            .map(Box::new);
            let _ = tx.send(AsyncMessage::ChangesLoaded {
                generation,
                session_id,
                result,
            });
        });
    }

    fn request_context_summary(&mut self) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        self.context_generation = self.context_generation.saturating_add(1);
        let generation = self.context_generation;
        let socket = self.rpc.socket().to_path_buf();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut client| {
                    client.initialize()?;
                    client.context_summary(&session_id)
                })
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::ContextSummaryLoaded {
                generation,
                session_id,
                result,
            });
        });
    }

    fn select_agent(&mut self, index: usize) {
        let task_id = match self.overlays.active() {
            Some(Overlay::Agents(agents)) => agent_entries(&agents.projection)
                .get(index)
                .map(|agent| agent.task_id.clone()),
            _ => None,
        };
        let Some(task_id) = task_id else {
            return;
        };
        self.product_generation = self.product_generation.saturating_add(1);
        let generation = self.product_generation;
        if let Some(Overlay::Agents(agents)) = self.overlays.active_mut() {
            agents.selected = index;
            agents.confirm_stop = false;
            agents.recap = None;
            if task_id.is_empty() {
                agents.recap_load = RetainedLoad::default();
                return;
            }
            agents.recap_load.refresh(generation, task_id.clone());
        }
        let socket = self.rpc.socket().to_path_buf();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut client| {
                    client.initialize()?;
                    client.agent_recap(&task_id)
                })
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::AgentRecapLoaded {
                generation,
                task_id,
                result,
            });
        });
    }

    fn apply_action(&mut self, action: Action) {
        if (self.session_browser.renaming_session_id().is_some()
            || self.session_browser.archive_confirmation_id().is_some())
            && !matches!(
                action,
                Action::ConfirmRenameSession
                    | Action::CancelRenameSession
                    | Action::ConfirmArchiveSession
                    | Action::CancelArchiveSession
            )
        {
            return;
        }
        match action {
            Action::SelectLocale(index) => {
                self.locale_index = index;
                self.select_locale();
            }
            Action::SelectProvider(index) => {
                if self.provider_index != index {
                    self.clear_provider_import_state();
                }
                self.provider_index = index;
                self.select_provider_and_continue();
            }
            Action::ConfirmProviderImport => self.confirm_provider_import(),
            Action::CancelProviderImport => self.cancel_provider_import(),
            Action::FocusProviderSearch => self.provider_picker.begin_search(),
            Action::SelectModel(index) => {
                self.select_model_and_continue(index);
            }
            Action::SelectSession(index) => {
                self.open_selected_session(Some(index));
            }
            Action::CreateSession => self.create_session_from_browser(),
            Action::BeginRenameSession => self.begin_selected_session_rename(),
            Action::ConfirmRenameSession => self.confirm_session_rename(),
            Action::CancelRenameSession => self.session_browser.cancel_rename(),
            Action::BeginArchiveSession => self.begin_selected_session_archive(),
            Action::ConfirmArchiveSession => self.confirm_session_archive(),
            Action::CancelArchiveSession => self.session_browser.cancel_archive(),
            Action::UnarchiveSession => self.unarchive_selected_session(),
            Action::FocusSessionSearch => self.session_browser.begin_search(),
            Action::ToggleSessionScope => self
                .session_browser
                .toggle_scope(&self.sessions, &self.options.workspace),
            Action::ToggleBlock(id) => {
                if let Some(expanded) = self
                    .blocks
                    .iter()
                    .find(|block| block.id == id && block.is_collapsible())
                    .map(|block| !self.effective_block_expanded(block))
                {
                    self.set_block_disclosure(&id, expanded);
                }
            }
            Action::SelectHistory(index) => {
                if self.eligible_history_indices().contains(&index) {
                    if self.history_selected == Some(index) {
                        self.branch_from_history();
                    } else {
                        self.history_branch_request_id = None;
                        self.history_selected = Some(index);
                        self.sync_history_selection();
                    }
                }
            }
            Action::FocusComposer => self.focus = Focus::Composer,
            Action::PreviewMedia(element_id) => {
                self.media.set_hovered(Some(element_id));
                self.focus = Focus::Composer;
            }
            Action::RetryMedia(element_id) => {
                self.retry_media(element_id);
                self.focus = Focus::Composer;
            }
            Action::RetryExecution(run_id) => self.retry_failed_execution(&run_id),
            Action::CopyFailureId(id) => self.copy_failure_id(&id),
            Action::OpenSessions => {
                self.close_top_non_governance();
                self.open_session_browser();
            }
            Action::OpenModels => {
                self.close_top_non_governance();
                self.open_models();
            }
            Action::OpenSettings => self
                .overlays
                .replace(Overlay::Settings(SettingsOverlay { selected: 0 })),
            Action::ToggleDensity => self.toggle_density(),
            Action::OpenQueue => self.open_queue_overlay(),
            Action::OpenStatus => {
                let session_id = self
                    .active_session
                    .as_ref()
                    .map(|session| session.session_id.as_str());
                match ProductProjection::load(&mut self.rpc, session_id) {
                    Ok(projection) => self
                        .overlays
                        .replace(Overlay::Status(StatusOverlay { projection })),
                    Err(error) => {
                        self.notice = Notice::localized_with(
                            MessageId::LoadStatusFailed,
                            [("error", error.to_string())],
                        )
                    }
                }
            }
            Action::OpenAgents | Action::RefreshAgents => self.request_agents(),
            Action::OpenChanges | Action::RefreshChanges => self.request_changes(),
            Action::BeginPatchRollback => {
                let target = match self.overlays.active() {
                    Some(Overlay::Changes(changes)) => changes
                        .projection
                        .patches
                        .get(changes.selected)
                        .filter(|patch| {
                            !patch.rollback_pointer.is_empty()
                                && !matches!(
                                    patch.status.as_str(),
                                    "rolled_back" | "failed" | "proposed"
                                )
                        })
                        .map(|patch| (patch.session_id.clone(), patch.patch_id.clone())),
                    _ => None,
                };
                if let Some((session_id, patch_id)) = target {
                    let locale = self.ui_locale();
                    let preview = self
                        .rpc
                        .preview_workspace_patch_rollback(&session_id, &patch_id);
                    if let Some(Overlay::Changes(changes)) = self.overlays.active_mut() {
                        match preview {
                            Ok(preview) if preview.can_rollback => {
                                changes.confirm_rollback = true;
                                changes.rollback_preview = Some(preview);
                                changes.rollback_error.clear();
                            }
                            Ok(_) => {
                                changes.confirm_rollback = false;
                                changes.rollback_preview = None;
                                changes.rollback_error = tr_format(
                                    locale,
                                    MessageId::PatchRollbackUnavailable,
                                    &[("error", tr(locale, MessageId::NotAvailable))],
                                );
                            }
                            Err(error) => {
                                changes.confirm_rollback = false;
                                changes.rollback_preview = None;
                                changes.rollback_error = tr_format(
                                    locale,
                                    MessageId::PatchRollbackUnavailable,
                                    &[("error", &error.to_string())],
                                );
                            }
                        }
                    }
                }
            }
            Action::CancelPatchRollback => {
                if let Some(Overlay::Changes(changes)) = self.overlays.active_mut() {
                    changes.confirm_rollback = false;
                    changes.rollback_preview = None;
                    changes.rollback_error.clear();
                }
            }
            Action::ConfirmPatchRollback => {
                let target = match self.overlays.active() {
                    Some(Overlay::Changes(changes)) if changes.confirm_rollback => changes
                        .projection
                        .patches
                        .get(changes.selected)
                        .map(|patch| (patch.session_id.clone(), patch.patch_id.clone())),
                    _ => None,
                };
                if let Some((session_id, patch_id)) = target {
                    let locale = self.ui_locale();
                    match self.rpc.rollback_workspace_patch(&session_id, &patch_id) {
                        Ok(_) => self.request_changes(),
                        Err(error) => {
                            if let Some(Overlay::Changes(changes)) = self.overlays.active_mut() {
                                changes.confirm_rollback = false;
                                changes.rollback_preview = None;
                                changes.rollback_error = tr_format(
                                    locale,
                                    MessageId::PatchRollbackUnavailable,
                                    &[("error", &error.to_string())],
                                );
                            }
                        }
                    }
                }
            }
            Action::SelectAgent(index) => self.select_agent(index),
            Action::OpenSelectedAgentSession => {
                let session_id = match self.overlays.active() {
                    Some(Overlay::Agents(agents)) => agent_entries(&agents.projection)
                        .get(agents.selected)
                        .map(|agent| agent.session_id.clone()),
                    _ => None,
                };
                if let Some(session_id) = session_id {
                    match self.rpc.resume_session(&session_id).and_then(|session| {
                        self.open_session(session)
                            .map_err(|error| RpcError::Protocol(error.to_string()))
                    }) {
                        Ok(()) => self.overlays.resolve_active(),
                        Err(error) => {
                            self.notice = Notice::localized_with(
                                MessageId::OpenAgentSessionFailed,
                                [("error", error.to_string())],
                            )
                        }
                    }
                }
            }
            Action::BeginStopAgent => {
                if let Some(Overlay::Agents(agents)) = self.overlays.active_mut()
                    && agent_entries(&agents.projection)
                        .get(agents.selected)
                        .is_some_and(|agent| agent.category != "completed")
                {
                    agents.confirm_stop = true;
                }
            }
            Action::ConfirmStopAgent => {
                let task_id = match self.overlays.active() {
                    Some(Overlay::Agents(agents)) if agents.confirm_stop => {
                        agent_entries(&agents.projection)
                            .get(agents.selected)
                            .map(|agent| agent.task_id.clone())
                    }
                    _ => None,
                };
                if let Some(task_id) = task_id {
                    match self.rpc.agent_stop(&task_id) {
                        Ok(_) => self.apply_action(Action::RefreshAgents),
                        Err(error) => {
                            if let Some(Overlay::Agents(agents)) = self.overlays.active_mut() {
                                agents.load.error = error.to_string();
                                agents.confirm_stop = false;
                            }
                        }
                    }
                }
            }
            Action::SelectChange(index) => {
                if let Some(Overlay::Changes(changes)) = self.overlays.active_mut() {
                    changes.selected = index;
                    changes.scroll = 0;
                }
            }
            Action::SelectSlashCommand {
                id,
                registry_revision,
            } => {
                if let Err(error) = self.execute_slash_suggestion(&id, registry_revision.as_deref())
                {
                    self.notice = Notice::localized_with(
                        MessageId::RunCommandFailed,
                        [("error", error.to_string())],
                    );
                }
            }
            Action::SelectPromptHistory(index) => {
                let accept = self
                    .history_search
                    .as_mut()
                    .is_some_and(|search| search.select(index));
                self.preview_prompt_history_search();
                if accept {
                    self.accept_prompt_history_search();
                }
            }
            Action::SelectFileCandidate(index) => {
                let accept = self.context_completion.select(index);
                if accept {
                    self.accept_context_completion();
                }
            }
            Action::SelectFileViewerLine(index) => {
                if let Some(Overlay::FileViewer(viewer)) = self.overlays.active_mut() {
                    let visible_rows =
                        self.transcript_area.height.saturating_sub(8).max(1) as usize;
                    viewer.select_line(index, visible_rows);
                }
            }
            Action::ConfirmFileViewer => {
                let viewer = match self.overlays.active() {
                    Some(Overlay::FileViewer(viewer)) if viewer.load == FileViewerLoad::Ready => {
                        Some(viewer.clone())
                    }
                    _ => None,
                };
                if let Some(viewer) = viewer {
                    viewer.confirm(&mut self.composer);
                    self.composer_state = TextAreaState::default();
                    self.media.reconcile(&self.composer);
                    self.context_completion.update_context(&self.composer);
                    self.overlays.resolve_active();
                    self.focus = Focus::Composer;
                }
            }
            Action::OpenLocale => {
                self.close_top_non_governance();
                self.phase = Phase::Locale;
            }
            Action::OpenProvider => {
                self.close_top_non_governance();
                self.phase = Phase::Provider;
            }
            Action::TogglePlanMode => {
                self.close_top_non_governance();
                self.cycle_conversation_mode();
            }
            Action::ApprovalAllow => self.resolve_active_approval(true),
            Action::ApprovalDeny => self.resolve_active_approval(false),
            Action::QuestionOption(index) => self.answer_active_question(Some(index)),
            Action::ApprovePlan => self.approve_plan(),
            Action::RevisePlan => self.revise_plan(),
            Action::CancelPlan => self.cancel_plan(),
            Action::ResumePausedExecutionRun => self.resume_paused_execution(),
            Action::CloseOverlay => {
                self.close_top_non_governance();
            }
        }
    }

    /// Discrete effort ladder from model inventory only (no hard-coded global tiers).
    fn model_reasoning_efforts(&self) -> Vec<String> {
        self.models
            .get(self.model_index)
            .map(|model| model.reasoning_efforts.clone())
            .unwrap_or_default()
    }

    fn model_default_reasoning_effort(&self) -> String {
        let Some(model) = self.models.get(self.model_index) else {
            return String::new();
        };
        let default = model.default_reasoning_effort.trim();
        if !default.is_empty()
            && model
                .reasoning_efforts
                .iter()
                .any(|effort| effort == default)
        {
            return default.to_owned();
        }
        for candidate in ["medium", "high", "low"] {
            if model
                .reasoning_efforts
                .iter()
                .any(|effort| effort == candidate)
            {
                return candidate.to_owned();
            }
        }
        model.reasoning_efforts.first().cloned().unwrap_or_default()
    }

    /// Prefer exact match, then small cross-vendor remap, then model default.
    fn resolve_reasoning_effort_for_selection(&self, previous: &str) -> String {
        let efforts = self.model_reasoning_efforts();
        if efforts.is_empty() {
            return String::new();
        }
        let previous = previous.trim().to_ascii_lowercase();
        if !previous.is_empty() && efforts.iter().any(|effort| effort == &previous) {
            return previous;
        }
        if !previous.is_empty() {
            let mapped = remap_reasoning_effort(&previous);
            if mapped != previous && efforts.iter().any(|effort| effort == &mapped) {
                return mapped;
            }
        }
        self.model_default_reasoning_effort()
    }

    fn sync_reasoning_effort_for_selection(&mut self) {
        let previous = if !self.selected_reasoning_effort.is_empty() {
            self.selected_reasoning_effort.clone()
        } else {
            self.active_session
                .as_ref()
                .map(|session| session.next_reasoning_effort.clone())
                .unwrap_or_default()
        };
        self.selected_reasoning_effort = self.resolve_reasoning_effort_for_selection(&previous);
    }

    fn cycle_reasoning_effort(&mut self, forward: bool) {
        let efforts = self.model_reasoning_efforts();
        if efforts.len() < 2 {
            return;
        }
        let current = efforts
            .iter()
            .position(|effort| effort == &self.selected_reasoning_effort)
            .unwrap_or(0);
        let next = if forward {
            (current + 1) % efforts.len()
        } else {
            (current + efforts.len() - 1) % efforts.len()
        };
        self.selected_reasoning_effort = efforts[next].clone();
    }

    fn select_model_and_continue(&mut self, index: usize) {
        let Some(model_id) = self.models.get(index).map(|model| model.id.clone()) else {
            return;
        };
        self.model_index = index;
        self.sync_reasoning_effort_for_selection();
        let provider_id = self
            .inventory
            .providers
            .get(self.provider_index)
            .map(|provider| provider.id.clone());

        let inventory = match self.rpc.model_inventory() {
            Ok(inventory) => inventory,
            Err(error) => {
                self.phase = Phase::Model;
                self.focus = Focus::Scene;
                self.notice = Notice::localized_with(
                    MessageId::ModelVerifyFailed,
                    [("model", model_id.clone()), ("error", error.to_string())],
                );
                return;
            }
        };
        if !inventory.has_runnable_provider() {
            self.inventory = inventory;
            self.phase = Phase::Model;
            self.focus = Focus::Scene;
            self.notice = Notice::localized(MessageId::ExecutionReadinessChanged);
            return;
        }

        self.inventory = inventory;
        if let Some(provider_id) = provider_id {
            self.provider_index = self
                .inventory
                .providers
                .iter()
                .position(|provider| provider.id == provider_id)
                .unwrap_or(self.provider_index);
        }
        let refreshed_models = self
            .inventory
            .providers
            .get(self.provider_index)
            .map(|provider| {
                provider
                    .models
                    .iter()
                    .filter(|model| model.available)
                    .cloned()
                    .collect::<Vec<_>>()
            })
            .unwrap_or_default();
        let Some(refreshed_index) = refreshed_models
            .iter()
            .position(|model| model.id == model_id)
        else {
            self.models = refreshed_models;
            self.model_index = self.model_index.min(self.models.len().saturating_sub(1));
            self.phase = Phase::Model;
            self.focus = Focus::Scene;
            self.notice = Notice::localized(MessageId::SelectedModelUnavailable);
            return;
        };
        self.models = refreshed_models;
        self.model_index = refreshed_index;
        // Honesty before submit: surface circuit/rate-limit from the refreshed
        // inventory instead of opening a conversation that will fail immediately.
        if !self.models[refreshed_index].is_usable() {
            let status = crate::i18n::model_health_status_label(
                self.ui_locale(),
                self.models[refreshed_index].health_status(),
            );
            self.phase = Phase::Model;
            self.focus = Focus::Scene;
            self.notice = Notice::localized_with(
                MessageId::ModelUnusableBlocked,
                [("status", status.to_owned())],
            );
            return;
        }
        self.selected_model = model_id.clone();
        self.notice.clear();

        let result = if let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        {
            self.rpc
                .set_session_model(&session_id, &model_id, &self.selected_reasoning_effort)
                .map_err(anyhow::Error::new)
                .map(|selection| {
                    self.selected_model = selection.next_model.clone();
                    self.selected_reasoning_effort = selection.next_reasoning_effort.clone();
                    if let Some(session) = self.active_session.as_mut() {
                        session.next_model = selection.next_model;
                        session.next_reasoning_effort = selection.next_reasoning_effort;
                    }
                    self.phase = Phase::Conversation;
                    self.focus = Focus::Composer;
                })
        } else if let Some(session_id) = self.options.session_id.clone() {
            self.rpc
                .resume_session(&session_id)
                .map_err(anyhow::Error::new)
                .and_then(|session| self.open_session_with_model(session, Some(&model_id)))
                .map(|()| self.options.session_id = None)
        } else {
            self.enter_workspace_conversation_with_model(Some(&model_id))
        };

        if let Err(error) = result {
            self.phase = Phase::Model;
            self.focus = Focus::Scene;
            self.notice = Notice::localized_with(
                MessageId::OpenConversationModelFailed,
                [("model", model_id), ("error", error.to_string())],
            );
        }
    }
}

fn prompt_history_from_blocks(blocks: &[TranscriptBlock]) -> Vec<String> {
    let mut history = Vec::new();
    for prompt in blocks
        .iter()
        .rev()
        .filter(|block| block.kind == crate::transcript::BlockKind::User)
        .map(|block| {
            let source = block.source_prompt.trim();
            if source.is_empty() {
                block.body.trim()
            } else {
                source
            }
        })
        .filter(|prompt| !prompt.is_empty())
    {
        if !history.iter().any(|existing| existing == prompt) {
            history.push(prompt.to_owned());
        }
    }
    history
}

/// Small cross-vendor effort aliases (must stay aligned with go/daemon remap).
fn remap_reasoning_effort(effort: &str) -> String {
    match effort.trim().to_ascii_lowercase().as_str() {
        "xhigh" | "extra_high" | "extra-high" => "max".into(),
        "max" => "xhigh".into(),
        "minimal" | "min" => "low".into(),
        "none" | "off" => "low".into(),
        other => other.to_owned(),
    }
}

fn combined_prompt_history(
    blocks: &[TranscriptBlock],
    persisted_prompt_history: &[String],
) -> Vec<String> {
    let mut history = prompt_history_from_blocks(blocks);
    for prompt in persisted_prompt_history.iter().rev() {
        let prompt = prompt.trim();
        if !prompt.is_empty() && !history.iter().any(|existing| existing == prompt) {
            history.push(prompt.to_owned());
        }
    }
    history
}

fn agent_entries(projection: &ProductProjection) -> Vec<&crate::rpc::AgentViewEntry> {
    projection
        .agents
        .needs_input
        .iter()
        .chain(projection.agents.working.iter())
        .chain(projection.agents.completed.iter())
        .collect()
}

fn startup_phase(
    has_supported_locale: bool,
    inventory: &ModelInventory,
    models: &[Model],
) -> Phase {
    if !has_supported_locale {
        Phase::Locale
    } else if !inventory.has_runnable_provider() || models.is_empty() {
        // Empty model inventory is a provider-repair problem, not a hard
        // diagnostic lockout (stale CC Switch managed proxy with no models).
        Phase::Provider
    } else {
        Phase::Model
    }
}

fn is_supported_locale(locale: &str) -> bool {
    LOCALES.iter().any(|(id, _)| *id == locale)
}

fn agent_locale(locale: &str) -> &str {
    match locale {
        "zh-Hans" => "zh",
        value => value,
    }
}

fn clear_terminal_execution_notice(notice: &mut Notice, run_id: &str) {
    if !run_id.is_empty()
        && (notice.is_owned_by_run(run_id)
            || notice.raw_starts_with(&format!("ExecutionRun {run_id} ")))
    {
        notice.clear();
    }
}

fn event_terminal_summary(event: &WireEvent) -> Option<String> {
    let summary = if event.summary.trim().is_empty() {
        event
            .payload
            .get("summary")
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default()
    } else {
        event.summary.as_str()
    };
    (!summary.trim().is_empty()).then(|| summary.trim().to_owned())
}

fn event_agent(event: &WireEvent) -> Option<&str> {
    if !event.agent.is_empty() {
        return Some(event.agent.as_str());
    }
    event
        .payload
        .get("agent")
        .and_then(serde_json::Value::as_str)
        .filter(|agent| !agent.is_empty())
}

fn plan_review_overlay(session: &Session) -> Option<PlanReviewOverlay> {
    (session.plan_mode
        && session.execution_status == "completed"
        && session.latest_run_agent == "plan"
        && session.latest_run_result_kind == "plan"
        && !session.latest_run_id.is_empty()
        && !session.summary.trim().is_empty())
    .then(|| PlanReviewOverlay {
        run_id: session.latest_run_id.clone(),
        summary: session.summary.trim().to_owned(),
        resolving: false,
        error: String::new(),
        scroll: 0,
    })
}

fn locale_selection_index(locale: Option<&str>) -> usize {
    locale
        .and_then(|locale| LOCALES.iter().position(|(id, _)| *id == locale))
        .or_else(|| match locale {
            Some("zh" | "zh-CN" | "zh-SG") => LOCALES.iter().position(|(id, _)| *id == "zh-Hans"),
            Some("zh-TW" | "zh-HK" | "zh-MO") => {
                LOCALES.iter().position(|(id, _)| *id == "zh-Hant")
            }
            _ => None,
        })
        .unwrap_or(0)
}

fn settings_action(index: usize) -> Option<Action> {
    match index {
        0 => Some(Action::OpenLocale),
        1 => Some(Action::OpenProvider),
        2 => Some(Action::OpenModels),
        3 => Some(Action::TogglePlanMode),
        4 => Some(Action::ToggleDensity),
        5 => Some(Action::OpenStatus),
        6 => Some(Action::OpenSessions),
        7 => Some(Action::ResumePausedExecutionRun),
        8 => Some(Action::CloseOverlay),
        _ => None,
    }
}

fn normalize_shift_tab(mut key: KeyEvent) -> KeyEvent {
    if key.code == KeyCode::BackTab
        || (key.code == KeyCode::Tab && key.modifiers.contains(KeyModifiers::SHIFT))
    {
        key.code = KeyCode::BackTab;
        key.modifiers.remove(KeyModifiers::SHIFT);
    }
    key
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum ConversationModeAction {
    EnterPlan,
    ApprovePlan,
}

fn conversation_mode_action(
    execution_running: bool,
    plan_mode: bool,
) -> Option<ConversationModeAction> {
    if execution_running {
        None
    } else if plan_mode {
        Some(ConversationModeAction::ApprovePlan)
    } else {
        Some(ConversationModeAction::EnterPlan)
    }
}

fn plan_review_key_action(code: KeyCode) -> Option<Action> {
    match code {
        KeyCode::Enter | KeyCode::Char('a') | KeyCode::Char('y') => Some(Action::ApprovePlan),
        KeyCode::Char('r') | KeyCode::Esc => Some(Action::RevisePlan),
        KeyCode::Char('c') | KeyCode::Char('n') => Some(Action::CancelPlan),
        _ => None,
    }
}

/// Workspace sessions ordered most-recent first for cold-start resume.
fn workspace_session_ids(sessions: &[Session], workspace: &Path) -> Vec<String> {
    let mut ids: Vec<&Session> = sessions
        .iter()
        .filter(|session| same_workspace(&session.workspace_root, workspace))
        .filter(|session| session.status != "closed")
        .collect();
    ids.sort_by_key(|session| std::cmp::Reverse(session_recency_key(session)));
    ids.into_iter()
        .map(|session| session.session_id.clone())
        .collect()
}

fn session_recency_key(session: &Session) -> String {
    // ISO-8601 timestamps sort lexicographically when present.
    let updated = session.updated_at.trim();
    if !updated.is_empty() {
        return updated.to_owned();
    }
    session.created_at.trim().to_owned()
}

fn sort_sessions_by_recency(sessions: &mut [Session]) {
    sessions.sort_by(|left, right| {
        session_recency_key(right)
            .cmp(&session_recency_key(left))
            .then_with(|| left.session_id.cmp(&right.session_id))
    });
}

fn needs_explicit_model_confirmation(sessions: &[Session], workspace: &Path) -> bool {
    workspace_session_ids(sessions, workspace).is_empty()
}

fn load_session_and_items(
    socket: &Path,
    session_id: &str,
    create_workspace: Option<&str>,
) -> Result<HistoryBranchOutcome, String> {
    let mut rpc = Client::connect(socket).map_err(|error| error.to_string())?;
    let session = match create_workspace {
        Some(workspace) => rpc
            .create_session(workspace)
            .map_err(|error| error.to_string())?,
        None => rpc
            .resume_session(session_id)
            .map_err(|error| error.to_string())?,
    };
    let items = rpc
        .items(&session.session_id)
        .map_err(|error| error.to_string())?;
    let (prompt_history, prompt_history_unavailable) =
        match rpc.prompt_history(&session.session_id, 200) {
            Ok(history) => (history.entries, false),
            Err(_) => (Vec::new(), true),
        };
    Ok(HistoryBranchOutcome {
        session,
        items,
        prompt_history,
        prompt_history_unavailable,
    })
}

fn reconnect_runtime_and_session(
    socket: &Path,
    session_id: &str,
) -> Result<RuntimeReconnectOutcome, String> {
    let mut rpc = Client::connect(socket).map_err(|error| error.to_string())?;
    let runtime = rpc.initialize().map_err(|error| error.to_string())?;
    let inventory = rpc.model_inventory().map_err(|error| error.to_string())?;
    let sessions = rpc.sessions().map_err(|error| error.to_string())?;
    let session = rpc
        .resume_session(session_id)
        .map_err(|error| error.to_string())?;
    let security_context = rpc
        .config_inventory(session_id)
        .ok()
        .map(|config| config.effective);
    let items = rpc.items(session_id).map_err(|error| error.to_string())?;
    let (prompt_history, prompt_history_unavailable) = match rpc.prompt_history(session_id, 200) {
        Ok(history) => (history.entries, false),
        Err(_) => (Vec::new(), true),
    };
    Ok(RuntimeReconnectOutcome {
        rpc,
        runtime,
        inventory,
        sessions,
        session,
        items,
        prompt_history,
        prompt_history_unavailable,
        security_context,
    })
}

fn session_preview_lines(items: Vec<SessionItemEvent>) -> Vec<String> {
    let mut reducer = TranscriptReducer::default();
    let blocks = reducer.hydrate(items);
    let mut lines = blocks
        .into_iter()
        .filter(|block| {
            matches!(
                block.kind,
                crate::transcript::BlockKind::User | crate::transcript::BlockKind::Assistant
            ) && !block.body.trim().is_empty()
        })
        .rev()
        .take(4)
        .map(|block| {
            let speaker = if block.kind == crate::transcript::BlockKind::User {
                "You"
            } else {
                "Carina"
            };
            let body = block.body.split_whitespace().collect::<Vec<_>>().join(" ");
            let body = body.chars().take(160).collect::<String>();
            format!("{speaker}  {body}")
        })
        .collect::<Vec<_>>();
    lines.reverse();
    lines
}

fn branch_history_and_load(
    socket: &Path,
    source_session_id: &str,
    previous_run_id: Option<&str>,
    before_first: bool,
    client_fork_id: &str,
) -> Result<HistoryBranchOutcome, String> {
    let mut rpc = Client::connect(socket).map_err(|error| error.to_string())?;
    let session = rpc
        .fork_session(
            source_session_id,
            previous_run_id,
            before_first,
            client_fork_id,
        )
        .map_err(|error| error.to_string())?;
    let items = rpc
        .items(&session.session_id)
        .map_err(|error| error.to_string())?;
    let (prompt_history, prompt_history_unavailable) =
        match rpc.prompt_history(&session.session_id, 200) {
            Ok(history) => (history.entries, false),
            Err(_) => (Vec::new(), true),
        };
    Ok(HistoryBranchOutcome {
        session,
        items,
        prompt_history,
        prompt_history_unavailable,
    })
}

fn same_workspace(session_root: &str, workspace: &Path) -> bool {
    if session_root.trim().is_empty() {
        return false;
    }
    let session = Path::new(session_root);
    match (session.canonicalize(), workspace.canonicalize()) {
        (Ok(session), Ok(workspace)) => session == workspace,
        _ => session == workspace,
    }
}

fn resume_paused_execution_and_refresh(
    socket: &Path,
    session_id: &str,
    run_id: &str,
) -> Result<PausedResumeOutcome, String> {
    let mut rpc = Client::connect(socket).map_err(|error| error.to_string())?;
    let execution = rpc
        .resume_execution(run_id)
        .map_err(|error| error.to_string())?;
    let (session, items, refresh_error) = refresh_paused_projection(&mut rpc, session_id);
    Ok(PausedResumeOutcome {
        execution,
        session,
        items,
        refresh_error,
    })
}

fn refresh_paused_projection(
    rpc: &mut Client,
    session_id: &str,
) -> (
    Option<Session>,
    Option<Vec<SessionItemEvent>>,
    Option<String>,
) {
    let mut session = None;
    let mut items = None;
    let mut session_error = None;
    let mut items_error = None;
    for attempt in 0..3 {
        if session.is_none() {
            match refresh_session_projection(rpc, session_id) {
                Ok(value) => session = Some(value),
                Err(error) => session_error = Some(error.to_string()),
            }
        }
        if items.is_none() {
            match rpc.items(session_id) {
                Ok(value) => items = Some(value),
                Err(error) => items_error = Some(error.to_string()),
            }
        }
        if session.is_some() && items.is_some() {
            break;
        }
        if attempt < 2 {
            std::thread::sleep(Duration::from_millis(100));
        }
    }
    let mut errors = Vec::new();
    if session.is_none() {
        errors.push(format!(
            "session: {}",
            session_error.unwrap_or_else(|| "refresh failed".into())
        ));
    }
    if items.is_none() {
        errors.push(format!(
            "transcript: {}",
            items_error.unwrap_or_else(|| "refresh failed".into())
        ));
    }
    let error = (!errors.is_empty()).then(|| errors.join("; "));
    (session, items, error)
}

fn refresh_session_projection(rpc: &mut Client, session_id: &str) -> Result<Session, RpcError> {
    let resumed = rpc.resume_session(session_id)?;
    let projection = rpc.sessions().ok().and_then(|sessions| {
        sessions
            .into_iter()
            .find(|session| session.session_id == session_id)
    });
    Ok(projection.unwrap_or(resumed))
}

pub fn run(options: Options) -> Result<Outcome> {
    let background = crate::terminal_probe::background(Duration::from_millis(80));
    let mut app = App::bootstrap(options)?;
    app.theme = Theme::detected(background);
    let graphics = TerminalGraphics::detect();
    app.graphics_enabled = graphics.enabled();
    let mut terminal = TerminalHost::enter(
        app.options.screen_mode,
        app.options.no_alt_screen,
        app.options.alt_screen,
        graphics,
    )?;
    app.options.screen_mode = Some(terminal.mode);
    if terminal.mode == ScreenMode::Inline && app.options.screen_handoff.is_none() {
        let reason = inline_force_reason(app.options.alt_screen);
        app.notice =
            Notice::localized_with(MessageId::ScreenModeForcedInline, [("reason", reason)]);
    }
    let hyperlink_support = HyperlinkSupport::detect();
    let input_tx = app.async_tx.clone();
    std::thread::Builder::new()
        .name("terminal-input".into())
        .spawn(move || {
            loop {
                let message = event::read()
                    .map(|event| AsyncMessage::Terminal(Ok(event)))
                    .unwrap_or_else(|error| AsyncMessage::Terminal(Err(error.to_string())));
                let failed = matches!(message, AsyncMessage::Terminal(Err(_)));
                if input_tx.send(message).is_err() || failed {
                    break;
                }
            }
        })?;
    let mut scheduler = FrameScheduler::new(Instant::now());
    while !app.quit {
        app.apply_async();
        app.scrollback
            .stage_for_commit(&app.blocks, app.active_run_id.as_deref());
        let now = Instant::now();
        if app.active_run_id.is_none() && app.transcript_reflow.stream_finished() {
            scheduler.request_resize(now);
        }
        scheduler.set_tick_demand(app.tick_demand(), now);
        for marker in app.pending_feedback.drain(..) {
            scheduler.request_feedback(marker);
        }
        scheduler.advance_tick(now);
        if app.dirty {
            if app.redraw_reasons.is_empty() {
                app.redraw_reasons.push(RedrawReason::AsyncResult);
            }
            for reason in app.redraw_reasons.drain(..) {
                if reason == RedrawReason::Resize {
                    scheduler.request_resize(now);
                } else {
                    scheduler.request(reason, now);
                }
            }
            app.dirty = false;
        }
        if scheduler.resize_due(now) {
            terminal.refresh_graphics()?;
            if terminal.can_commit_scrollback()
                && app.phase == Phase::Conversation
                && app.transcript_reflow.needs_reflow()
                && !app.screen_handoff_failed
            {
                let committed = app.scrollback.committed_prefix_len(&app.blocks);
                terminal.reflow_scrollback(
                    &app.blocks[..committed],
                    app.options.scrollback_wrap,
                    reflow_line_cap(),
                )?;
                app.transcript_reflow
                    .mark_reflowed(app.active_run_id.is_some());
                scheduler.request(RedrawReason::Recovery, now);
            }
            app.terminal_resized = false;
        }
        if terminal.can_commit_scrollback()
            && app.phase == Phase::Conversation
            && app.active_run_id.is_none()
            && !app.screen_handoff_failed
        {
            let pending = app.scrollback.pending_finalized(&app.blocks).to_vec();
            if !pending.is_empty() {
                terminal.commit_scrollback(&pending, app.options.scrollback_wrap)?;
                app.scrollback.commit(&pending);
                app.reset_transcript_viewport();
                scheduler.request(RedrawReason::Recovery, now);
            }
        }
        if scheduler.should_present(now) {
            let workspace = app.options.workspace.clone();
            let markdown = app
                .blocks
                .iter()
                .flat_map(|block| markdown_links(&block.body))
                .collect::<Vec<_>>();
            let submitted = scheduler.try_present(now, || {
                terminal.draw_with_links(&hyperlink_support, &workspace, &markdown, |frame| {
                    app.render(frame)
                })
            })?;
            if !submitted {
                continue;
            }
            app.scrollback.observe_presented(&app.blocks);
            if let Err(error) = terminal.sync_media_preview(app.media_preview_placement.as_ref()) {
                app.graphics_enabled = false;
                app.media_preview_placement = None;
                app.notice = Notice::localized_with(
                    MessageId::ImagePreviewUnavailable,
                    [("error", error.to_string())],
                );
                scheduler.request(RedrawReason::Media, Instant::now());
            }
        }
        if !app.quit && !app.wait_for_work(app.next_wake_deadline(scheduler.deadline())) {
            break;
        }
    }
    if let Some(path) = std::env::var_os("CARINA_FRAME_STATS_PATH") {
        let _ = fs::write(
            path,
            serde_json::to_vec_pretty(&scheduler.stats().debug_report())?,
        );
    }
    let relaunch = app.relaunch_screen_mode.take();
    let outcome = app.outcome;
    drop(terminal);
    if let Some(mode) = relaunch {
        return relaunch_in_screen_mode(&app, mode);
    }
    Ok(outcome)
}

fn relaunch_in_screen_mode(app: &App, mode: ScreenMode) -> Result<Outcome> {
    let mut handoff = tempfile::Builder::new()
        .prefix("carina-screen-")
        .suffix(".json")
        .tempfile()
        .context("create screen mode handoff")?;
    serde_json::to_writer(
        &mut handoff,
        &ScreenModeHandoff {
            session_id: app
                .active_session
                .as_ref()
                .map(|session| session.session_id.clone())
                .unwrap_or_default(),
            runtime_id: app.runtime.runtime.runtime_id.clone(),
            runtime_epoch: app.runtime.runtime.epoch.clone(),
            runtime_process_epoch: app.runtime.runtime.process_epoch,
            runtime_pid: app.runtime.runtime.pid,
            draft: app.composer.text().to_owned(),
            queued_prompts: app.queued_prompts.iter().cloned().collect(),
            committed_scrollback: app.scrollback.committed_snapshot(),
            pending_governance: app.overlays.governance_ids(),
            selected_block_id: app
                .history_selected
                .and_then(|index| app.blocks.get(index))
                .map(|block| block.id.clone()),
            transcript_scroll: app.transcript_scroll,
            transcript_follow_bottom: app.transcript_follow_bottom,
        },
    )
    .context("write screen mode handoff")?;
    handoff.flush().context("flush screen mode handoff")?;
    let (_, handoff_path) = handoff.keep().context("persist screen mode handoff")?;

    let executable = std::env::current_exe().context("resolve carina-ui executable")?;
    let mut command = Command::new(executable);
    command
        .arg("--socket")
        .arg(&app.options.socket)
        .arg("--workspace")
        .arg(&app.options.workspace)
        .arg("--screen-mode")
        .arg(mode.as_arg())
        .arg("--screen-handoff")
        .arg(&handoff_path);
    if let Some(session_id) = app
        .active_session
        .as_ref()
        .map(|session| &session.session_id)
    {
        command.arg("--session").arg(session_id);
    }
    if let Some(locale) = app.options.locale.as_deref() {
        command.arg("--locale").arg(locale);
    }
    if let Some(path) = app.options.locale_path.as_ref() {
        command.arg("--locale-path").arg(path);
    }
    command.arg("--density").arg(app.density.as_config_value());
    if let Some(path) = app.options.density_path.as_ref() {
        command.arg("--density-path").arg(path);
    }
    if let Some(path) = app.options.carina_bin.as_ref() {
        command.arg("--carina-bin").arg(path);
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt;
        let error = command.exec();
        let _ = fs::remove_file(handoff_path);
        Err(error).context("re-exec Carina screen mode")
    }
    #[cfg(not(unix))]
    {
        let status = command.status();
        let _ = fs::remove_file(handoff_path);
        let status = status.context("relaunch Carina screen mode")?;
        Ok(match status.code() {
            Some(0) => Outcome::Ok,
            Some(2) => Outcome::Usage,
            Some(6) => Outcome::Degraded,
            _ => Outcome::RuntimeError,
        })
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RuntimeModeChoice {
    Workspace,
    Legacy,
}

impl RuntimeModeChoice {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Workspace => "workspace",
            Self::Legacy => "legacy",
        }
    }
}

pub fn choose_runtime_mode(no_alt_screen: bool) -> Result<Option<RuntimeModeChoice>> {
    use ratatui::layout::{Constraint, Direction, Layout};
    use ratatui::style::{Modifier, Style};
    use ratatui::text::{Line, Span};
    use ratatui::widgets::{Paragraph, Wrap};

    let mut terminal = TerminalHost::enter(
        None,
        no_alt_screen,
        AltScreenPolicy::Auto,
        TerminalGraphics::disabled(),
    )?;
    let theme = Theme::detected(crate::terminal_probe::background(Duration::from_millis(80)));
    let mut selected = 0_usize;
    loop {
        terminal.draw(|frame| {
            let area = frame.area();
            let width = area.width.min(72);
            let height = area.height.min(18);
            let shell = Rect::new(
                area.x + area.width.saturating_sub(width) / 2,
                area.y + area.height.saturating_sub(height) / 2,
                width,
                height,
            );
            let chunks = Layout::default()
                .direction(Direction::Vertical)
                .constraints([
                    Constraint::Length(5),
                    Constraint::Length(6),
                    Constraint::Min(2),
                ])
                .split(shell);
            frame.render_widget(
                Paragraph::new(vec![
                    Line::from(Span::styled(
                        "Choose runtime isolation",
                        theme.focus().add_modifier(Modifier::BOLD),
                    )),
                    Line::from(Span::styled(
                        "Legacy global state was found. Choose how this workspace should run.",
                        Style::default().fg(theme.text),
                    )),
                    Line::from(Span::styled(
                        "You can change this later with carina runtime mode.",
                        Style::default().fg(theme.muted),
                    )),
                ])
                .wrap(Wrap { trim: false }),
                chunks[0],
            );
            let rows = [
                (
                    "Workspace isolation",
                    "recommended; one runtime and state boundary per repository",
                ),
                (
                    "Legacy global runtime",
                    "keep the existing shared daemon and global state",
                ),
            ];
            for (index, (label, detail)) in rows.into_iter().enumerate() {
                let row = Rect::new(
                    chunks[1].x,
                    chunks[1].y + index as u16 * 3,
                    chunks[1].width,
                    2,
                );
                let style = if selected == index {
                    theme.selected()
                } else {
                    Style::default().fg(theme.text)
                };
                frame.render_widget(
                    Paragraph::new(vec![
                        Line::from(Span::styled(
                            format!("{} {label}", if selected == index { ">" } else { " " }),
                            style,
                        )),
                        Line::from(Span::styled(
                            format!("  {detail}"),
                            Style::default().fg(theme.muted),
                        )),
                    ]),
                    row,
                );
            }
            frame.render_widget(
                Paragraph::new("Up/Down select  Enter continue  Esc cancel")
                    .style(Style::default().fg(theme.muted)),
                chunks[2],
            );
        })?;
        match event::read()? {
            Event::Key(key) if key.kind != KeyEventKind::Release => match key.code {
                KeyCode::Up => selected = selected.saturating_sub(1),
                KeyCode::Down => selected = (selected + 1).min(1),
                KeyCode::Enter => {
                    return Ok(Some(if selected == 0 {
                        RuntimeModeChoice::Workspace
                    } else {
                        RuntimeModeChoice::Legacy
                    }));
                }
                KeyCode::Esc => return Ok(None),
                _ => {}
            },
            _ => {}
        }
    }
}

struct TerminalHost {
    terminal: Terminal<CrosstermBackend<TerminalWriter>>,
    sync_output: SyncOutputSupport,
    mode: ScreenMode,
    graphics: TerminalGraphics,
    preview_owner: Option<u64>,
    preview_area: Option<Rect>,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct ScreenCapabilities {
    alternate: bool,
    mouse: bool,
    focus: bool,
    graphics: bool,
    native_scrollback: bool,
}

fn screen_capabilities(mode: ScreenMode) -> ScreenCapabilities {
    match mode {
        ScreenMode::Minimal => ScreenCapabilities {
            alternate: false,
            mouse: false,
            focus: true,
            graphics: false,
            native_scrollback: true,
        },
        ScreenMode::Fullscreen => ScreenCapabilities {
            alternate: true,
            mouse: true,
            focus: true,
            graphics: true,
            native_scrollback: false,
        },
        ScreenMode::Inline => ScreenCapabilities {
            alternate: false,
            mouse: false,
            focus: false,
            graphics: false,
            native_scrollback: false,
        },
    }
}

impl TerminalHost {
    fn enter(
        requested_mode: Option<ScreenMode>,
        no_alt_screen: bool,
        alt_screen: AltScreenPolicy,
        graphics: TerminalGraphics,
    ) -> Result<Self> {
        enable_raw_mode()?;
        let mut stdout = io::stdout();
        let mode = resolve_screen_mode(requested_mode, no_alt_screen, alt_screen);
        let capabilities = screen_capabilities(mode);
        if capabilities.alternate {
            execute!(stdout, EnterAlternateScreen)?;
        }
        execute!(stdout, EnableBracketedPaste)?;
        if capabilities.mouse {
            execute!(stdout, EnableMouseCapture)?;
        }
        if capabilities.focus {
            execute!(stdout, EnableFocusChange)?;
        }
        let graphics = if capabilities.graphics {
            graphics
        } else {
            TerminalGraphics::disabled()
        };
        graphics.upload(&mut stdout)?;
        let backend = CrosstermBackend::new(TerminalWriter::spawn()?);
        let terminal = if !capabilities.alternate {
            let (_, height) = crossterm::terminal::size()?;
            Terminal::with_options(
                backend,
                TerminalOptions {
                    viewport: Viewport::Inline(height.max(1)),
                },
            )?
        } else {
            Terminal::new(backend)?
        };
        Ok(Self {
            terminal,
            sync_output: SyncOutputSupport::detect(),
            mode,
            graphics,
            preview_owner: None,
            preview_area: None,
        })
    }

    fn can_commit_scrollback(&self) -> bool {
        screen_capabilities(self.mode).native_scrollback && self.terminal.viewport_area().y > 0
    }

    fn commit_scrollback(
        &mut self,
        blocks: &[TranscriptBlock],
        wrap: ScrollbackWrap,
    ) -> Result<()> {
        let commit = |terminal: &mut Terminal<CrosstermBackend<TerminalWriter>>| {
            for block in blocks {
                let text = raw_block_text(block);
                let terminal_wrap =
                    wrap == ScrollbackWrap::Terminal || block.body.lines().any(is_plain_url_line);
                if terminal_wrap {
                    emit_to_scrollback(terminal, &text)?;
                    continue;
                }
                let width = terminal.viewport_area().width.max(1);
                let paragraph = Paragraph::new(text).wrap(Wrap { trim: false });
                let height = paragraph.line_count(width).min(u16::MAX as usize) as u16;
                terminal.insert_before(height.max(1), |buffer| {
                    paragraph.render(buffer.area, buffer);
                })?;
            }
            Ok(())
        };
        self.write_terminal_operation(commit)
    }

    fn reflow_scrollback(
        &mut self,
        blocks: &[TranscriptBlock],
        wrap: ScrollbackWrap,
        line_cap: usize,
    ) -> Result<()> {
        let width = self.terminal.size()?.width.max(1);
        let history = history_for_width(blocks, width, wrap, line_cap);
        self.write_terminal_operation(|terminal| resize_purge_rerender(terminal, &history))
    }

    fn write_terminal_operation(
        &mut self,
        operation: impl FnOnce(&mut Terminal<CrosstermBackend<TerminalWriter>>) -> io::Result<()>,
    ) -> Result<()> {
        let writer = self.terminal.backend_mut().writer_mut();
        writer.wait_for_in_flight();
        if !writer.begin_frame() {
            return Err(io::Error::new(
                io::ErrorKind::WouldBlock,
                "terminal frame is still in flight",
            )
            .into());
        }
        let result = if self.sync_output.enabled() {
            with_synchronized_output(&mut self.terminal, operation)
        } else {
            operation(&mut self.terminal)
        };
        if let Err(error) = result {
            self.terminal.backend_mut().writer_mut().abort_frame();
            return Err(error.into());
        }
        self.terminal.backend_mut().writer_mut().end_frame()?;
        Ok(())
    }

    fn draw(&mut self, render: impl FnOnce(&mut ratatui::Frame<'_>)) -> Result<bool> {
        if !self.terminal.backend_mut().writer_mut().begin_frame() {
            return Ok(false);
        }
        let result = if self.sync_output.enabled() {
            with_synchronized_output(&mut self.terminal, |terminal| {
                terminal.draw(render).map(|_| ())
            })
        } else {
            self.terminal.draw(render).map(|_| ())
        };
        if let Err(error) = result {
            self.terminal.backend_mut().writer_mut().abort_frame();
            return Err(error.into());
        }
        self.terminal.backend_mut().writer_mut().end_frame()?;
        Ok(true)
    }

    fn draw_with_links(
        &mut self,
        support: &HyperlinkSupport,
        workspace: &Path,
        markdown: &[MarkdownLink],
        render: impl FnOnce(&mut ratatui::Frame<'_>),
    ) -> Result<bool> {
        if !self.terminal.backend_mut().writer_mut().begin_frame() {
            return Ok(false);
        }
        let draw = |terminal: &mut Terminal<CrosstermBackend<TerminalWriter>>| {
            terminal
                .draw_with_links(|frame| {
                    render(frame);
                    support.links(frame.buffer_mut(), workspace, markdown)
                })
                .map(|_| ())
        };
        let result = if self.sync_output.enabled() {
            with_synchronized_output(&mut self.terminal, draw)
        } else {
            draw(&mut self.terminal)
        };
        if let Err(error) = result {
            self.terminal.backend_mut().writer_mut().abort_frame();
            return Err(error.into());
        }
        self.terminal.backend_mut().writer_mut().end_frame()?;
        Ok(true)
    }

    fn refresh_graphics(&mut self) -> Result<()> {
        self.terminal
            .backend_mut()
            .writer_mut()
            .wait_for_in_flight();
        self.graphics.refresh(self.terminal.backend_mut())?;
        self.preview_owner = None;
        self.preview_area = None;
        Ok(())
    }

    fn sync_media_preview(&mut self, placement: Option<&MediaPreviewPlacement>) -> Result<()> {
        let needs_update = match placement {
            Some(placement) => {
                self.preview_owner != Some(placement.owner_id)
                    || self.preview_area != Some(placement.area)
            }
            None => self.preview_owner.is_some(),
        };
        if !needs_update {
            return Ok(());
        }
        self.terminal
            .backend_mut()
            .writer_mut()
            .wait_for_in_flight();
        match placement {
            Some(placement) => {
                let retransmit = self.preview_owner != Some(placement.owner_id);
                let reposition = retransmit || self.preview_area != Some(placement.area);
                if reposition {
                    self.graphics.render_preview(
                        self.terminal.backend_mut(),
                        placement,
                        retransmit,
                    )?;
                    self.preview_owner = Some(placement.owner_id);
                    self.preview_area = Some(placement.area);
                }
            }
            None if self.preview_owner.take().is_some() => {
                self.graphics.clear_preview(self.terminal.backend_mut())?;
                self.preview_area = None;
            }
            None => {}
        }
        Ok(())
    }
}

impl Drop for TerminalHost {
    fn drop(&mut self) {
        let _ = disable_raw_mode();
        self.terminal
            .backend_mut()
            .writer_mut()
            .wait_for_in_flight();
        let _ = self.terminal.backend_mut().writer_mut().begin_frame();
        {
            let backend = self.terminal.backend_mut();
            let _ = self.graphics.cleanup(backend);
            let _ = execute!(
                backend,
                DisableFocusChange,
                DisableBracketedPaste,
                DisableMouseCapture
            );
            if screen_capabilities(self.mode).alternate {
                let _ = execute!(backend, LeaveAlternateScreen);
            }
        }
        let _ = self.terminal.show_cursor();
        let _ = self.terminal.backend_mut().writer_mut().end_frame();
        self.terminal
            .backend_mut()
            .writer_mut()
            .wait_for_in_flight();
    }
}

fn resolve_screen_mode(
    requested: Option<ScreenMode>,
    no_alt_screen: bool,
    policy: AltScreenPolicy,
) -> ScreenMode {
    let zellij =
        std::env::var_os("ZELLIJ").is_some() || std::env::var_os("ZELLIJ_SESSION_NAME").is_some();
    let tmux_control = std::env::var_os("TMUX_CONTROL_MODE").is_some()
        || (std::env::var_os("TMUX").is_some()
            && std::env::var("TERM_PROGRAM")
                .is_ok_and(|value| value.eq_ignore_ascii_case("iTerm.app")));
    let capability_poor = std::env::var_os("SSH_CONNECTION").is_some()
        || std::env::var("TERM").is_ok_and(|value| value.eq_ignore_ascii_case("dumb"));
    resolve_screen_mode_for(
        requested,
        no_alt_screen,
        policy,
        zellij,
        tmux_control || capability_poor,
    )
}

/// Resolve the product screen mode from the current terminal environment.
/// This is public only so the Unix PTY certification matrix can exercise the
/// same policy as the terminal host.
pub fn detected_screen_mode(no_alt_screen: bool, policy: AltScreenPolicy) -> ScreenMode {
    resolve_screen_mode(None, no_alt_screen, policy)
}

fn resolve_screen_mode_for(
    requested: Option<ScreenMode>,
    no_alt_screen: bool,
    policy: AltScreenPolicy,
    zellij: bool,
    tmux_control: bool,
) -> ScreenMode {
    if no_alt_screen {
        return ScreenMode::Minimal;
    }
    if let Some(requested) = requested {
        return requested;
    }
    match policy {
        AltScreenPolicy::Always => ScreenMode::Fullscreen,
        AltScreenPolicy::Never => ScreenMode::Minimal,
        // Poor multiplexers / control-mode hosts stay on Inline for safety.
        AltScreenPolicy::Auto if zellij || tmux_control => ScreenMode::Inline,
        // Default product surface is Fullscreen (alt-screen + mouse capture) so
        // trackpad scroll stays inside Carina instead of terminal history.
        AltScreenPolicy::Auto => ScreenMode::Fullscreen,
    }
}

fn inline_force_reason(policy: AltScreenPolicy) -> &'static str {
    if std::env::var_os("ZELLIJ").is_some() {
        return "zellij";
    }
    if std::env::var("TMUX")
        .ok()
        .filter(|value| !value.is_empty())
        .is_some()
        && std::env::var_os("TMUX_PANE").is_some()
    {
        return "tmux";
    }
    match std::env::var("TERM").unwrap_or_default().as_str() {
        "dumb" | "" => return "dumb-term",
        _ => {}
    }
    match policy {
        AltScreenPolicy::Never => "alt-screen-never",
        _ => "capability-fallback",
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum RewindEscapeAction {
    Busy,
    Unavailable,
    Prime,
    Open,
}

fn rewind_prime_window() -> Duration {
    rewind_prime_window_from(std::env::var(REWIND_GRACE_ENV).ok().as_deref())
}

fn rewind_prime_window_from(value: Option<&str>) -> Duration {
    value
        .and_then(|value| value.trim().parse::<u64>().ok())
        .filter(|millis| (MIN_REWIND_GRACE_MS..=MAX_REWIND_GRACE_MS).contains(millis))
        .map(Duration::from_millis)
        .unwrap_or(DEFAULT_REWIND_PRIME_WINDOW)
}

fn rewind_escape_action(
    active_run: bool,
    has_eligible_history: bool,
    primed_at: Option<Instant>,
    now: Instant,
    grace: Duration,
) -> RewindEscapeAction {
    if active_run {
        return RewindEscapeAction::Busy;
    }
    if !has_eligible_history {
        return RewindEscapeAction::Unavailable;
    }
    if primed_at.is_some_and(|primed| now.saturating_duration_since(primed) <= grace) {
        RewindEscapeAction::Open
    } else {
        RewindEscapeAction::Prime
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum QuitHardCancelAction {
    CancelRun,
    Prime,
    Quit,
}

fn quit_hard_cancel_action(
    active_run: bool,
    primed_at: Option<Instant>,
    now: Instant,
    grace: Duration,
) -> QuitHardCancelAction {
    if active_run {
        return QuitHardCancelAction::CancelRun;
    }
    if primed_at.is_some_and(|primed| now.saturating_duration_since(primed) <= grace) {
        QuitHardCancelAction::Quit
    } else {
        QuitHardCancelAction::Prime
    }
}

fn store_provider_credential(
    carina_bin: &Path,
    provider: &str,
    secret: String,
    child_slot: &Arc<Mutex<Option<Child>>>,
) -> Result<()> {
    let child = Command::new(carina_bin)
        .args(["auth", "login", provider, "-"])
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .context("start provider credential helper")?;
    let stdin = {
        let mut slot = lock_child(child_slot);
        *slot = Some(child);
        slot.as_mut().and_then(|child| child.stdin.take())
    };
    if let Some(mut stdin) = stdin {
        stdin.write_all(secret.as_bytes())?;
    }
    drop(secret);
    let status = loop {
        let status = {
            let mut slot = lock_child(child_slot);
            let child = slot
                .as_mut()
                .ok_or_else(|| anyhow!("credential helper ownership was lost"))?;
            child.try_wait()?
        };
        if let Some(status) = status {
            lock_child(child_slot).take();
            break status;
        }
        std::thread::sleep(Duration::from_millis(25));
    };
    if status.success() {
        Ok(())
    } else {
        Err(anyhow!(
            "provider credential helper rejected the credential"
        ))
    }
}

fn import_ccswitch_provider(
    carina_bin: &Path,
    provider: &str,
    child_slot: &Arc<Mutex<Option<Child>>>,
) -> Result<()> {
    let mut child = Command::new(carina_bin)
        .args(["auth", "import", "cc-switch", provider])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .context("start CC Switch import helper")?;
    let stderr = child
        .stderr
        .take()
        .ok_or_else(|| anyhow!("CC Switch import helper stderr was unavailable"))?;
    let stderr_reader = std::thread::spawn(move || {
        let mut bytes = Vec::new();
        let _ = stderr
            .take(IMPORT_ERROR_LIMIT.saturating_add(1))
            .read_to_end(&mut bytes);
        bytes
    });
    lock_child(child_slot).replace(child);
    let started_at = Instant::now();
    let (status, timed_out) = loop {
        let outcome = {
            let mut slot = lock_child(child_slot);
            let child = slot
                .as_mut()
                .ok_or_else(|| anyhow!("CC Switch import helper ownership was lost"))?;
            if started_at.elapsed() >= IMPORT_HELPER_TIMEOUT {
                let _ = child.kill();
                Some((child.wait()?, true))
            } else {
                child.try_wait()?.map(|status| (status, false))
            }
        };
        if let Some(outcome) = outcome {
            lock_child(child_slot).take();
            break outcome;
        }
        std::thread::sleep(Duration::from_millis(25));
    };
    let stderr = stderr_reader.join().unwrap_or_default();
    if timed_out {
        Err(anyhow!(
            "Provider validation timed out after 20 seconds. Retry or choose another provider."
        ))
    } else if status.success() {
        Ok(())
    } else {
        Err(anyhow!(ccswitch_import_error(&stderr, provider)))
    }
}

fn ccswitch_import_error(stderr: &[u8], provider: &str) -> String {
    let raw = String::from_utf8_lossy(stderr);
    let message = raw
        .lines()
        .rev()
        .map(str::trim)
        .find(|line| !line.is_empty())
        .unwrap_or("The provider could not be validated");
    let message = message.strip_prefix("carina: ").unwrap_or(message);
    let message = message.strip_prefix("provider setup: ").unwrap_or(message);
    let message = message.replace(provider, "selected provider");
    let message = message
        .chars()
        .filter(|character| !character.is_control() || character.is_whitespace())
        .collect::<String>()
        .split_whitespace()
        .collect::<Vec<_>>()
        .join(" ");
    let message = message.chars().take(320).collect::<String>();
    if message.is_empty() {
        "The provider could not be validated".into()
    } else {
        message
    }
}

fn lock_child(slot: &Arc<Mutex<Option<Child>>>) -> std::sync::MutexGuard<'_, Option<Child>> {
    slot.lock().unwrap_or_else(|poisoned| poisoned.into_inner())
}

fn default_locale_path() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .map(|home| home.join(".carina/config.json"))
}

fn operation_id(prefix: &str) -> String {
    format!(
        "{prefix}-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap_or_default()
            .as_nanos()
    )
}

fn artifact_target_is_current(
    generation: u64,
    current_generation: u64,
    active_session_id: Option<&str>,
    target_session_id: &str,
) -> bool {
    generation == current_generation && active_session_id == Some(target_session_id)
}

fn command_registry_target_is_current(
    generation: u64,
    current_generation: u64,
    active_session_id: Option<&str>,
    target_session_id: &str,
) -> bool {
    generation == current_generation && active_session_id == Some(target_session_id)
}

fn terminal_focus_transition(event: &Event) -> Option<bool> {
    match event {
        Event::FocusGained => Some(true),
        Event::FocusLost => Some(false),
        _ => None,
    }
}

fn animation_tick_demand(
    terminal_focused: bool,
    has_active_run: bool,
    execution_status: &str,
    provider_validating: bool,
) -> TickDemand {
    if !terminal_focused {
        TickDemand::None
    } else if has_active_run {
        if execution_status_animates(execution_status) {
            TickDemand::Activity
        } else {
            TickDemand::None
        }
    } else if provider_validating {
        TickDemand::Status
    } else {
        TickDemand::None
    }
}

#[cfg(test)]
fn load_density(path: Option<&Path>) -> Result<DensityMode> {
    let Some(path) = path else {
        return Ok(DensityMode::default());
    };
    let root = match fs::read(path) {
        Ok(data) => serde_json::from_slice::<serde_json::Map<String, serde_json::Value>>(&data)
            .with_context(|| format!("parse {}", path.display()))?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => {
            return Ok(DensityMode::default());
        }
        Err(error) => return Err(error.into()),
    };
    let Some(value) = root.get("tui_density") else {
        return Ok(DensityMode::default());
    };
    let value = value
        .as_str()
        .ok_or_else(|| anyhow!("{} tui_density must be a string", path.display()))?;
    DensityMode::parse(value)
        .ok_or_else(|| anyhow!("{} has invalid tui_density {value:?}", path.display()))
}

fn persist_config_string(path: &Path, key: &str, value: &str) -> Result<()> {
    let mut root = match fs::read(path) {
        Ok(data) => serde_json::from_slice::<serde_json::Map<String, serde_json::Value>>(&data)
            .with_context(|| format!("parse {}", path.display()))?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => serde_json::Map::new(),
        Err(error) => return Err(error.into()),
    };
    root.insert(key.into(), serde_json::Value::String(value.into()));
    let parent = path
        .parent()
        .ok_or_else(|| anyhow!("TUI config has no parent"))?;
    fs::create_dir_all(parent)?;
    let temp = parent.join(format!(".config.{}.tmp", std::process::id()));
    let data = serde_json::to_vec_pretty(&root)?;
    fs::write(&temp, data)?;
    fs::rename(&temp, path)?;
    Ok(())
}

fn persist_locale(path: &Path, locale: &str) -> Result<()> {
    persist_config_string(path, "tui_locale", locale)
}

fn persist_density(path: &Path, density: DensityMode) -> Result<()> {
    persist_config_string(path, "tui_density", density.as_config_value())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn focus_events_gate_decorative_tick_demand() {
        assert_eq!(terminal_focus_transition(&Event::FocusLost), Some(false));
        assert_eq!(terminal_focus_transition(&Event::FocusGained), Some(true));
        assert_eq!(terminal_focus_transition(&Event::Resize(80, 24)), None);

        assert_eq!(
            animation_tick_demand(true, true, "running", false),
            TickDemand::Activity
        );
        assert_eq!(
            animation_tick_demand(true, true, "running", true),
            TickDemand::Activity
        );
        assert_eq!(
            animation_tick_demand(false, true, "running", false),
            TickDemand::None
        );
        assert_eq!(
            animation_tick_demand(true, false, "ready", true),
            TickDemand::Status
        );
        assert_eq!(
            animation_tick_demand(false, false, "ready", true),
            TickDemand::None
        );
        assert_eq!(
            animation_tick_demand(true, false, "ready", false),
            TickDemand::None
        );
        for waiting in [
            "waiting_approval",
            "awaiting_approval",
            "blocked_on_approval",
            "waiting_input",
            "needs_input",
            "blocked_on_input",
        ] {
            assert_eq!(
                animation_tick_demand(true, true, waiting, false),
                TickDemand::None,
                "status={waiting}"
            );
            assert_eq!(
                animation_tick_demand(true, true, waiting, true),
                TickDemand::None,
                "provider validation must not animate through status={waiting}"
            );
        }
    }

    #[test]
    fn command_registry_results_are_fenced_by_generation_and_session() {
        assert!(command_registry_target_is_current(
            2,
            2,
            Some("sess_a"),
            "sess_a"
        ));
        assert!(!command_registry_target_is_current(
            1,
            2,
            Some("sess_a"),
            "sess_a"
        ));
        assert!(!command_registry_target_is_current(
            2,
            2,
            Some("sess_b"),
            "sess_a"
        ));
    }

    #[test]
    fn async_messages_keep_distinct_redraw_reasons() {
        assert_eq!(
            AsyncMessage::Terminal(Ok(Event::Resize(80, 24))).redraw_reason(),
            RedrawReason::Resize
        );
        assert_eq!(
            AsyncMessage::Event {
                generation: 1,
                value: Box::new(Err(RpcError::EventFrame("test".into()))),
            }
            .redraw_reason(),
            RedrawReason::Stream
        );
        assert_eq!(
            AsyncMessage::Reconnect { generation: 1 }.redraw_reason(),
            RedrawReason::Recovery
        );
    }

    #[test]
    fn screen_mode_is_first_class_and_fullscreen_by_default() {
        assert_eq!(
            resolve_screen_mode_for(None, false, AltScreenPolicy::Auto, false, false),
            ScreenMode::Fullscreen
        );
        assert_eq!(
            resolve_screen_mode_for(
                Some(ScreenMode::Fullscreen),
                false,
                AltScreenPolicy::Never,
                true,
                true,
            ),
            ScreenMode::Fullscreen
        );
        assert_eq!(
            resolve_screen_mode_for(
                Some(ScreenMode::Inline),
                false,
                AltScreenPolicy::Always,
                false,
                false,
            ),
            ScreenMode::Inline
        );
        assert_eq!(
            resolve_screen_mode_for(
                Some(ScreenMode::Fullscreen),
                true,
                AltScreenPolicy::Always,
                false,
                false,
            ),
            ScreenMode::Minimal
        );
        assert_eq!(
            resolve_screen_mode_for(None, false, AltScreenPolicy::Auto, true, false),
            ScreenMode::Inline
        );
        assert_eq!(
            resolve_screen_mode_for(None, false, AltScreenPolicy::Always, true, true),
            ScreenMode::Fullscreen
        );
        assert_eq!(
            resolve_screen_mode_for(None, false, AltScreenPolicy::Never, false, false),
            ScreenMode::Minimal
        );
    }

    #[test]
    fn screen_modes_own_one_explicit_capability_matrix() {
        assert_eq!(
            screen_capabilities(ScreenMode::Minimal),
            ScreenCapabilities {
                alternate: false,
                mouse: false,
                focus: true,
                graphics: false,
                native_scrollback: true,
            }
        );
        assert!(screen_capabilities(ScreenMode::Fullscreen).graphics);
        assert!(screen_capabilities(ScreenMode::Fullscreen).mouse);
        assert_eq!(
            screen_capabilities(ScreenMode::Inline),
            ScreenCapabilities {
                alternate: false,
                mouse: false,
                focus: false,
                graphics: false,
                native_scrollback: false,
            }
        );
    }

    #[test]
    fn screen_handoff_is_bounded_and_consumed_once() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("handoff.json");
        fs::write(
            &path,
            serde_json::to_vec(&ScreenModeHandoff {
                session_id: "sess_1".into(),
                runtime_id: "runtime_1".into(),
                runtime_epoch: "epoch_1".into(),
                runtime_process_epoch: 2,
                runtime_pid: 42,
                draft: "保留草稿".into(),
                queued_prompts: vec!["next".into()],
                committed_scrollback: Vec::new(),
                pending_governance: Vec::new(),
                selected_block_id: None,
                transcript_scroll: 0,
                transcript_follow_bottom: true,
            })
            .unwrap(),
        )
        .unwrap();
        let handoff = read_screen_handoff(&path).unwrap();
        assert_eq!(handoff.draft, "保留草稿");
        assert_eq!(handoff.queued_prompts, ["next"]);
        assert!(!path.exists());
    }

    #[test]
    fn screen_handoff_identity_is_fenced_by_session_and_runtime_epoch() {
        let handoff = ScreenModeHandoff {
            session_id: "sess_1".into(),
            runtime_id: "runtime_1".into(),
            runtime_epoch: "epoch_1".into(),
            runtime_process_epoch: 2,
            runtime_pid: 42,
            ..ScreenModeHandoff::default()
        };
        let identity = crate::rpc::RuntimeIdentity {
            runtime_id: "runtime_1".into(),
            epoch: "epoch_1".into(),
            process_epoch: 2,
            pid: 42,
            ..crate::rpc::RuntimeIdentity::default()
        };
        assert!(screen_handoff_identity_matches(
            Some("sess_1"),
            &identity,
            &handoff
        ));
        let mut restarted = identity.clone();
        restarted.process_epoch = 3;
        assert!(!screen_handoff_identity_matches(
            Some("sess_1"),
            &restarted,
            &handoff
        ));
        assert!(!screen_handoff_identity_matches(
            Some("sess_2"),
            &identity,
            &handoff
        ));
    }

    #[test]
    fn journey_notices_are_owned_by_i18n() {
        let source = include_str!("mod.rs");
        let production = source
            .split("\n#[cfg(test)]")
            .next()
            .expect("production source precedes tests");
        for raw_copy in [
            "That conversation is no longer available",
            "Conversation archived. Open Archived",
            "Conversation restored to the active list",
            "Could not persist the language choice",
            "No provider definitions are available",
            "Validating credential...",
            "Repair the provider before attaching an image",
            "Open a conversation before changing mode",
            "There is no paused execution in this conversation",
            "Resuming execution...",
            "Stop or wait for the current response before editing history",
            "Creating a source-preserving branch",
            "Could not load status",
            "Could not load agents",
            "Could not load changes",
            "Could not run command",
            "Image preview unavailable",
        ] {
            assert!(
                !production.contains(raw_copy),
                "journey notice must use MessageId instead of {raw_copy:?}"
            );
        }
    }

    #[test]
    fn outcome_codes_match_the_renderer_neutral_process_contract() {
        assert_eq!(Outcome::Ok.exit_code(), 0);
        assert_eq!(Outcome::RuntimeError.exit_code(), 1);
        assert_eq!(Outcome::Usage.exit_code(), 2);
        assert_eq!(Outcome::Degraded.exit_code(), 6);
    }

    #[test]
    fn locale_update_preserves_other_fields() {
        let root = std::env::temp_dir().join(format!(
            "carina-tui-locale-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&root).unwrap();
        let path = root.join("config.json");
        fs::write(&path, br#"{"max_concurrent_tasks":4}"#).unwrap();
        persist_locale(&path, "zh-Hans").unwrap();
        let value: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        assert_eq!(value["max_concurrent_tasks"], 4);
        assert_eq!(value["tui_locale"], "zh-Hans");
        let _ = fs::remove_dir_all(root);
    }

    #[test]
    fn density_update_preserves_config_and_survives_reload() {
        let root = std::env::temp_dir().join(format!(
            "carina-tui-density-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&root).unwrap();
        let path = root.join("config.json");
        fs::write(
            &path,
            br#"{"max_concurrent_tasks":4,"tui_locale":"zh-Hans"}"#,
        )
        .unwrap();

        assert_eq!(load_density(Some(&path)).unwrap(), DensityMode::Compact);
        persist_density(&path, DensityMode::Comfortable).unwrap();
        assert_eq!(load_density(Some(&path)).unwrap(), DensityMode::Comfortable);
        let value: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        assert_eq!(value["max_concurrent_tasks"], 4);
        assert_eq!(value["tui_locale"], "zh-Hans");
        assert_eq!(value["tui_density"], "comfortable");
        let _ = fs::remove_dir_all(root);
    }

    #[test]
    fn invalid_persisted_density_is_explicit() {
        let root = std::env::temp_dir().join(format!(
            "carina-tui-density-invalid-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&root).unwrap();
        let path = root.join("config.json");
        fs::write(&path, br#"{"tui_density":"dense"}"#).unwrap();
        assert!(
            load_density(Some(&path))
                .unwrap_err()
                .to_string()
                .contains("dense")
        );
        let _ = fs::remove_dir_all(root);
    }

    #[test]
    fn workspace_matching_does_not_resume_an_unrelated_session() {
        assert!(same_workspace("/tmp/carina", Path::new("/tmp/carina")));
        assert!(!same_workspace("/tmp/other", Path::new("/tmp/carina")));
        assert!(!same_workspace("", Path::new("/tmp/carina")));
    }

    #[test]
    fn runnable_provider_without_models_stays_out_of_conversation() {
        let inventory = ModelInventory {
            default_model: String::new(),
            providers: vec![crate::rpc::ModelProvider {
                id: "configured".into(),
                name: "Configured".into(),
                registered: true,
                available: true,
                auth_source: "credential_store".into(),
                source_kind: String::new(),
                source_label: String::new(),
                source_app: String::new(),
                source_route: String::new(),
                source_auth_mode: String::new(),
                source_action: String::new(),
                source_current: false,
                source_importable: false,
                source_reason: String::new(),
                models: Vec::new(),
            }],
            reasoner: crate::rpc::ModelReasoner {
                backend: "model-router".into(),
                available: true,
                ..Default::default()
            },
            readiness: crate::rpc::ExecutionReadiness::default(),
        };

        assert_eq!(startup_phase(true, &inventory, &[]), Phase::Provider);

        let mut unavailable = inventory.clone();
        unavailable.reasoner.available = false;
        assert_eq!(startup_phase(true, &unavailable, &[]), Phase::Provider);
    }

    #[test]
    fn unsupported_configured_locale_remains_an_unresolved_prerequisite() {
        assert!(is_supported_locale("zh-Hans"));
        assert!(!is_supported_locale("zh"));
        assert_eq!(locale_selection_index(Some("zh")), 1);
        assert_eq!(locale_selection_index(Some("zh-TW")), 2);
        assert_eq!(agent_locale("zh-Hans"), "zh");
        assert_eq!(agent_locale("zh-Hant"), "zh-Hant");
    }

    #[test]
    fn reasoning_effort_remap_covers_cross_vendor_aliases() {
        assert_eq!(remap_reasoning_effort("xhigh"), "max");
        assert_eq!(remap_reasoning_effort("max"), "xhigh");
        assert_eq!(remap_reasoning_effort("minimal"), "low");
        assert_eq!(remap_reasoning_effort("medium"), "medium");
    }

    #[test]
    fn terminal_execution_clears_only_its_submission_notice() {
        let mut notice: Notice = "ExecutionRun run_1 queued".into();
        clear_terminal_execution_notice(&mut notice, "run_1");
        assert!(notice.is_empty());

        let mut unrelated: Notice = "Provider validation failed".into();
        clear_terminal_execution_notice(&mut unrelated, "run_1");
        assert!(unrelated.raw_eq("Provider validation failed"));
    }

    #[test]
    fn workspace_session_candidates_prefer_most_recent_first() {
        let session = |id: &str, workspace_root: &str, updated_at: &str| Session {
            session_id: id.into(),
            name: String::new(),
            workspace_id: String::new(),
            workspace_root: workspace_root.into(),
            status: "active".into(),
            next_model: String::new(),
            next_reasoning_effort: String::new(),
            plan_mode: false,
            permission_profile: "safe-edit".into(),
            approval_mode: "on_request".into(),
            created_at: updated_at.into(),
            updated_at: updated_at.into(),
            latest_run_id: String::new(),
            latest_run_agent: String::new(),
            latest_run_result_kind: String::new(),
            execution_status: String::new(),
            summary: String::new(),
            continuity: None,
        };
        // Deliberately unordered input: older listed before newer.
        let sessions = vec![
            session("older", "/tmp/carina", "2026-07-01T00:00:00Z"),
            session("other", "/tmp/other", "2026-08-01T00:00:00Z"),
            session("newest", "/tmp/carina", "2026-08-02T12:00:00Z"),
            session("mid", "/tmp/carina", "2026-07-15T00:00:00Z"),
        ];

        assert_eq!(
            workspace_session_ids(&sessions, Path::new("/tmp/carina")),
            vec!["newest", "mid", "older"]
        );
        assert!(!needs_explicit_model_confirmation(
            &sessions,
            Path::new("/tmp/carina")
        ));
        assert!(needs_explicit_model_confirmation(
            &sessions,
            Path::new("/tmp/new-workspace")
        ));
    }

    #[test]
    fn settings_keyboard_and_pointer_share_provider_action_order() {
        assert_eq!(SETTINGS_ITEM_COUNT, 9);
        assert_eq!(settings_action(1), Some(Action::OpenProvider));
        assert_eq!(settings_action(3), Some(Action::TogglePlanMode));
        assert_eq!(settings_action(4), Some(Action::ToggleDensity));
        assert_eq!(settings_action(5), Some(Action::OpenStatus));
        assert_eq!(settings_action(8), Some(Action::CloseOverlay));
        assert_eq!(settings_action(9), None);
    }

    #[test]
    fn shift_tab_terminal_encodings_never_become_plain_tab() {
        for key in [
            KeyEvent::new(KeyCode::BackTab, KeyModifiers::NONE),
            KeyEvent::new(KeyCode::BackTab, KeyModifiers::SHIFT),
            KeyEvent::new(KeyCode::Tab, KeyModifiers::SHIFT),
        ] {
            let normalized = normalize_shift_tab(key);
            assert_eq!(normalized.code, KeyCode::BackTab);
            assert!(!normalized.modifiers.contains(KeyModifiers::SHIFT));
        }
        assert_eq!(
            normalize_shift_tab(KeyEvent::new(KeyCode::Tab, KeyModifiers::NONE)).code,
            KeyCode::Tab
        );
        assert_eq!(
            conversation_mode_action(false, false),
            Some(ConversationModeAction::EnterPlan)
        );
        assert_eq!(
            conversation_mode_action(false, true),
            Some(ConversationModeAction::ApprovePlan)
        );
        assert_eq!(conversation_mode_action(true, false), None);
        assert_eq!(conversation_mode_action(true, true), None);
    }

    #[test]
    fn escape_grace_is_configurable_but_rejects_unsafe_values() {
        assert_eq!(
            rewind_prime_window_from(Some(" 1000 ")),
            Duration::from_millis(1_000)
        );
        for value in [None, Some(""), Some("249"), Some("2001"), Some("invalid")] {
            assert_eq!(rewind_prime_window_from(value), DEFAULT_REWIND_PRIME_WINDOW);
        }
    }

    #[test]
    fn escape_state_machine_requires_two_idle_escapes_inside_grace() {
        let now = Instant::now();
        let grace = Duration::from_millis(800);
        assert_eq!(
            rewind_escape_action(false, true, None, now, grace),
            RewindEscapeAction::Prime
        );
        assert_eq!(
            rewind_escape_action(
                false,
                true,
                Some(now - Duration::from_millis(799)),
                now,
                grace,
            ),
            RewindEscapeAction::Open
        );
        assert_eq!(
            rewind_escape_action(
                false,
                true,
                Some(now - Duration::from_millis(801)),
                now,
                grace,
            ),
            RewindEscapeAction::Prime
        );
    }

    #[test]
    fn escape_state_machine_never_opens_history_while_running_or_empty() {
        let now = Instant::now();
        let primed = Some(now - Duration::from_millis(1));
        assert_eq!(
            rewind_escape_action(true, true, primed, now, Duration::from_secs(1)),
            RewindEscapeAction::Busy
        );
        assert_eq!(
            rewind_escape_action(false, false, primed, now, Duration::from_secs(1)),
            RewindEscapeAction::Unavailable
        );
    }

    #[test]
    fn idle_hard_cancel_requires_two_presses_inside_grace() {
        let now = Instant::now();
        let grace = Duration::from_millis(800);
        assert_eq!(
            quit_hard_cancel_action(false, None, now, grace),
            QuitHardCancelAction::Prime
        );
        assert_eq!(
            quit_hard_cancel_action(false, Some(now - Duration::from_millis(799)), now, grace),
            QuitHardCancelAction::Quit
        );
        assert_eq!(
            quit_hard_cancel_action(false, Some(now - Duration::from_millis(801)), now, grace),
            QuitHardCancelAction::Prime
        );
        assert_eq!(
            quit_hard_cancel_action(true, Some(now), now, grace),
            QuitHardCancelAction::CancelRun
        );
    }

    #[test]
    fn prompt_history_is_newest_first_and_deduplicated() {
        let blocks = vec![
            TranscriptBlock::local_user("one".into(), "first prompt".into()),
            TranscriptBlock::local_user("two".into(), "second prompt".into()),
            TranscriptBlock::local_user("three".into(), "first prompt".into()),
        ];

        assert_eq!(
            prompt_history_from_blocks(&blocks),
            vec!["first prompt", "second prompt"]
        );
    }

    #[test]
    fn prompt_history_includes_steers_and_legacy_user_blocks() {
        let mut legacy = TranscriptBlock::local_user("legacy".into(), "older prompt".into());
        legacy.source_prompt.clear();
        let blocks = vec![
            legacy,
            TranscriptBlock::local_steer("steer".into(), "task".into(), "finish the tests".into()),
        ];

        assert_eq!(
            prompt_history_from_blocks(&blocks),
            vec!["finish the tests", "older prompt"]
        );
    }

    #[test]
    fn prompt_history_combines_live_and_persisted_entries_newest_first() {
        let blocks = vec![
            TranscriptBlock::local_user("one".into(), "live newest".into()),
            TranscriptBlock::local_user("two".into(), "shared".into()),
        ];
        let persisted = vec![
            "persisted oldest".into(),
            "shared".into(),
            "persisted newest".into(),
        ];

        assert_eq!(
            combined_prompt_history(&blocks, &persisted),
            vec![
                "shared",
                "live newest",
                "persisted newest",
                "persisted oldest"
            ]
        );
    }

    #[test]
    fn tool_artifact_results_are_fenced_by_generation_and_session() {
        assert!(artifact_target_is_current(
            4,
            4,
            Some("sess-current"),
            "sess-current"
        ));
        assert!(!artifact_target_is_current(
            3,
            4,
            Some("sess-current"),
            "sess-current"
        ));
        assert!(!artifact_target_is_current(
            4,
            4,
            Some("sess-other"),
            "sess-current"
        ));
        assert!(!artifact_target_is_current(4, 4, None, "sess-current"));
    }

    #[test]
    fn completed_plan_session_reopens_review_after_restart() {
        let session = Session {
            session_id: "sess_plan".into(),
            name: String::new(),
            workspace_id: "ws".into(),
            workspace_root: "/tmp/ws".into(),
            status: "active".into(),
            next_model: "provider/model".into(),
            next_reasoning_effort: "high".into(),
            plan_mode: true,
            permission_profile: "safe-edit".into(),
            approval_mode: "on_request".into(),
            created_at: String::new(),
            updated_at: String::new(),
            latest_run_id: "run_plan".into(),
            latest_run_agent: "plan".into(),
            latest_run_result_kind: "plan".into(),
            execution_status: "completed".into(),
            summary: "Inspect, change, verify.".into(),
            continuity: None,
        };
        let review = plan_review_overlay(&session).expect("completed plans remain reviewable");
        assert_eq!(review.run_id, "run_plan");
        assert_eq!(review.summary, "Inspect, change, verify.");

        let mut answer = session.clone();
        answer.latest_run_result_kind = "answer".into();
        assert!(plan_review_overlay(&answer).is_none());
        let mut legacy = session;
        legacy.latest_run_result_kind.clear();
        assert!(plan_review_overlay(&legacy).is_none());
    }

    #[test]
    fn plan_review_keys_keep_revision_distinct_from_cancellation() {
        assert_eq!(
            plan_review_key_action(KeyCode::Esc),
            Some(Action::RevisePlan)
        );
        assert_eq!(
            plan_review_key_action(KeyCode::Char('c')),
            Some(Action::CancelPlan)
        );
        assert_eq!(
            plan_review_key_action(KeyCode::Enter),
            Some(Action::ApprovePlan)
        );
    }

    #[test]
    fn provider_import_state_fences_stale_validation_results() {
        let state = ProviderImportState::Validating {
            provider_id: "ccswitch-private-id".into(),
            generation: 4,
            started_at: Instant::now(),
        };

        assert!(state.accepts_result("ccswitch-private-id", 4));
        assert!(!state.accepts_result("ccswitch-private-id", 3));
        assert!(!state.accepts_result("another-provider", 4));
    }

    #[test]
    fn provider_import_failure_is_scoped_to_the_selected_provider() {
        let state = ProviderImportState::Failed {
            provider_id: "ccswitch-private-id".into(),
            message: "endpoint rejects this client type".into(),
        };

        assert_eq!(
            state.failure("ccswitch-private-id"),
            Some("endpoint rejects this client type")
        );
        assert_eq!(state.failure("another-provider"), None);
        assert!(!state.is_reviewing("ccswitch-private-id"));
        assert_eq!(state.validation_elapsed("ccswitch-private-id"), None);
    }

    #[test]
    fn ccswitch_import_error_preserves_reason_without_internal_id() {
        let message = ccswitch_import_error(
            b"carina: provider setup: ccswitch-private-id: status 503; endpoint rejects this client type\n",
            "ccswitch-private-id",
        );

        assert_eq!(
            message,
            "selected provider: status 503; endpoint rejects this client type"
        );
        assert!(!message.contains("ccswitch-private-id"));
    }
}
