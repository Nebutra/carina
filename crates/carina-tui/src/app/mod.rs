mod composer_draft;
mod composer_paste;
mod reading_state;
mod render;
mod slash_dispatch;

use composer_draft::rewind_prime_window;
#[cfg(test)]
use composer_draft::{
    rewind_escape_action, rewind_prime_window_from, RewindEscapeAction, DEFAULT_REWIND_PRIME_WINDOW,
};

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
use serde::de::{self, MapAccess, SeqAccess, Visitor};
use xai_ratatui_inline::{
    Terminal, emit_to_scrollback, resize_purge_rerender, with_synchronized_output,
};
use xai_ratatui_textarea::{ElementId, TextArea, TextAreaState, TextElementEventKind};

use crate::command;
use crate::component::{Action, InteractionMap};
use crate::context_completion::ContextCompletion;
use crate::context_completion::FILE_ELEMENT_KIND;
use crate::conversation::{
    ActiveRunPresentation, ExecutionActivityTracker, ExecutionTimer, execution_status_animates,
};
use crate::density::DensityMode;
use crate::file_viewer::{
    FileViewer, FileViewerLoad, FileViewerOrigin, MAX_PREVIEW_BYTES, parse_file_reference,
};
use crate::frame_scheduler::{
    FeedbackMarker, FrameScheduler, RedrawReason, TickDemand, WaitPlan, wait_plan,
};
use crate::glyphs::{GlyphPreference, GlyphResolution, ResolvedGlyphs};
use crate::history_search::HistorySearchState;
use crate::hyperlink::{HyperlinkSupport, MarkdownLink, markdown_links};
use crate::i18n::{Locale, MessageId, Notice, format as tr_format, text as tr};
use crate::keybinding::KeyBindings;
use crate::layout_contract::{
    TranscriptGeometry, TranscriptScrollbar, TranscriptScrollbarInteraction,
};
use crate::media::{
    IMAGE_ELEMENT_KIND, MediaChipLabels, MediaComposer, MediaSourceLabel, MediaUploadWork,
    inspect_image, pasted_image_path,
};
use crate::native_scrollback::{
    ScrollbackLedger, ScrollbackStamp, ScrollbackWrap, TranscriptReflowState, history_for_width,
    is_plain_url_line, raw_block_text, reflow_line_cap,
};
use crate::overlay::{
    AgentDashboardOverlay, ApprovalScope, ChangesFocus, ChangesOverlay, HelpOverlay, Overlay,
    OverlayStack, PRODUCT_MENU_ITEMS, PlanReviewOverlay, PluginsOverlay, ProductMenuOverlay,
    QueueOverlay,
    RetainedLoad, SettingsOverlay, SettingsPage, SideQueryOverlay, StatusOverlay,
    ToolOutputOverlay,
};
use crate::patch_review::{PatchReview, project_patch_reviews};
use crate::prerequisite::ProviderPickerState;
use crate::product_projection::ProductProjection;
use crate::rpc::{
    Client, EffectiveConfig, ExecutionLifecycle, ExecutionLifecycleReducer,
    ExecutionLifecycleReduction, ExecutionRun, GovernanceId, Model, ModelInventory, ReceivedEvent,
    ReplayBoundaryV1, ReplayTailAttachRequest, RpcError, RuntimeInitialize, Session,
    SessionItemEvent, WireEvent, attach_replay_tail_v1, spawn_event_stream,
};
use crate::session_browser::{ConversationImportStage, SessionBrowserState, SessionScope};
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
const SETTINGS_ITEM_COUNT: usize = 10;
const SETTINGS_SYMBOLS_INDEX: usize = 5;
const PLAN_REVIEW_PAGE_LINES: usize = 8;
const IMPORT_ERROR_LIMIT: u64 = 1024;
const IMPORT_HELPER_TIMEOUT: Duration = Duration::from_secs(20);

#[derive(Debug, Clone, Copy)]
struct TranscriptHeightCacheEntry {
    revision: u64,
    width: u16,
    locale: Locale,
    density: DensityMode,
    glyph_mode: crate::glyphs::GlyphMode,
    expand_key: &'static str,
    inspect_key: &'static str,
    expanded_output_budget: usize,
    height: usize,
    header_height: usize,
}

#[derive(Debug, Clone)]
struct TranscriptRenderCacheEntry {
    revision: u64,
    width: u16,
    locale: Locale,
    density: DensityMode,
    glyph_mode: crate::glyphs::GlyphMode,
    expand_key: &'static str,
    inspect_key: &'static str,
    expanded_output_budget: usize,
    lines: Vec<Line<'static>>,
}

type TranscriptHeightCache = HashMap<String, TranscriptHeightCacheEntry>;
type TranscriptRenderCache = HashMap<String, TranscriptRenderCacheEntry>;

#[derive(Debug, Clone)]
pub struct Options {
    pub socket: PathBuf,
    pub workspace: PathBuf,
    pub runtime_expectation: Option<crate::rpc::RuntimeExpectation>,
    pub session_id: Option<String>,
    pub locale: Option<String>,
    pub locale_path: Option<PathBuf>,
    pub density: DensityMode,
    pub density_path: Option<PathBuf>,
    pub glyph_preference: GlyphPreference,
    pub glyphs_path: Option<PathBuf>,
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
    #[serde(default)]
    transcript_anchor: Option<TranscriptScrollAnchor>,
    #[serde(default)]
    reading_state: Option<reading_state::ReadingStateEnvelopeV1>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
struct TranscriptScrollAnchor {
    block_id: String,
    block_index: usize,
    logical_line: usize,
    sub_rows: usize,
    #[serde(default)]
    position_hint: usize,
    #[serde(default)]
    previous_block_id: Option<String>,
    #[serde(default)]
    next_block_id: Option<String>,
}

impl TranscriptScrollAnchor {
    fn from_logical(anchor: reading_state::LogicalTranscriptAnchorV1, block_index: usize) -> Self {
        Self {
            block_id: anchor.block_id,
            block_index,
            logical_line: anchor.logical_line,
            sub_rows: anchor.wrapped_sub_row,
            position_hint: anchor.position_hint,
            previous_block_id: anchor.previous_block_id,
            next_block_id: anchor.next_block_id,
        }
    }

    fn to_logical(&self) -> reading_state::LogicalTranscriptAnchorV1 {
        reading_state::LogicalTranscriptAnchorV1 {
            block_id: self.block_id.clone(),
            logical_line: self.logical_line,
            wrapped_sub_row: self.sub_rows,
            position_hint: self.position_hint,
            previous_block_id: self.previous_block_id.clone(),
            next_block_id: self.next_block_id.clone(),
        }
    }
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
enum ModelBackDestination {
    Provider,
    Conversation,
}

impl ModelBackDestination {
    fn message_id(self) -> MessageId {
        match self {
            Self::Provider => MessageId::BackToProvider,
            Self::Conversation => MessageId::BackToConversation,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Focus {
    Scene,
    Composer,
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct FailureActionFocus {
    block_id: String,
    selected: crate::transcript::FailureRecoveryAction,
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
        result: Result<Box<ChangesLoadOutcome>, String>,
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
    ConversationImportsDiscovered {
        generation: u64,
        result: Result<crate::rpc::ConversationImportDiscovery, String>,
    },
    ConversationImportsApplied {
        generation: u64,
        result: Result<Box<ConversationImportApplyOutcome>, String>,
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
    StartupInventory {
        result: Result<ModelInventory, String>,
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

struct ChangesLoadOutcome {
    projection: ProductProjection,
    patch_reviews: Vec<PatchReview>,
}

struct ConversationImportApplyOutcome {
    result: crate::rpc::ConversationImportApplyResult,
    sessions: Vec<Session>,
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
    active_run: Option<ExecutionRun>,
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
    active_run: Option<ExecutionRun>,
    prompt_history: Vec<String>,
    prompt_history_unavailable: bool,
    security_context: Option<EffectiveConfig>,
    watermark: usize,
    catch_up: Vec<ReceivedEvent>,
    live: Option<std::sync::mpsc::Receiver<Result<ReceivedEvent, RpcError>>>,
    boundary: Option<ReplayBoundaryV1>,
}

struct PendingFileAttach {
    generation: u64,
    session_id: String,
    path: String,
    lines: std::ops::Range<usize>,
    token: crate::context_completion::AtContext,
}

#[derive(Clone)]
struct PendingSubmission {
    session_id: String,
    prompt: String,
    model: String,
    reasoning_effort: String,
    model_preference_revision: u64,
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
    glyph_preference: GlyphPreference,
    glyph_resolution: GlyphResolution,
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
    product_menu_anchor: Option<Rect>,
    composer_pointer_captured: bool,
    slash_selected: usize,
    slash_selected_id: Option<String>,
    slash_dismissed_input: Option<String>,
    command_registry: crate::rpc::CommandRegistry,
    command_registry_session: String,
    command_generation: u64,
    command_mru: Vec<String>,
    last_submitted_draft: Option<String>,
    command_registry_stale: bool,
    persisted_prompt_history: Vec<String>,
    persisted_prompt_history_unavailable: bool,
    history_search: Option<HistorySearchState>,
    context_completion: ContextCompletion,
    pending_file_attach: Option<PendingFileAttach>,
    file_viewer_generation: u64,
    product_generation: u64,
    context_generation: u64,
    context_summary: Option<crate::rpc::ContextSummary>,
    transcript_geometry: TranscriptGeometry,
    transcript_scrollbar: TranscriptScrollbar,
    transcript_scrollbar_interaction: TranscriptScrollbarInteraction,
    transcript_scroll: usize,
    transcript_max_scroll: usize,
    transcript_follow_bottom: bool,
    transcript_anchor: Option<TranscriptScrollAnchor>,
    transcript_height_cache: TranscriptHeightCache,
    transcript_render_cache: TranscriptRenderCache,
    bounded_tool_output_blocks: Vec<String>,
    tool_artifact_refs: HashMap<String, crate::rpc::ToolArtifactRef>,
    tool_artifact_loads: HashMap<String, crate::overlay::RetainedLoad>,
    tool_output_max_scroll: usize,
    history_selected: Option<usize>,
    history_stashed_draft: Option<String>,
    history_original_scroll: Option<(usize, bool)>,
    history_generation: u64,
    history_branch_request_id: Option<String>,
    resume_generation: u64,
    resume_pending: bool,
    history_branch_pending: bool,
    failure_action_focus: Option<FailureActionFocus>,
    rewind_primed_at: Option<Instant>,
    rewind_prime_window: Duration,
    /// First Ctrl-C (hard_cancel) while idle primes quit; second within grace exits.
    quit_primed_at: Option<Instant>,
    credential: String,
    credential_generation: u64,
    credential_pending: bool,
    credential_child: Arc<Mutex<Option<Child>>>,
    notice: Notice,
    notice_seen_key: String,
    notice_seen: bool,
    interactions: InteractionMap,
    overlays: OverlayStack,
    active_run_id: Option<String>,
    active_run_presentation: ActiveRunPresentation,
    execution_timer: ExecutionTimer,
    execution_status: String,
    execution_activity: ExecutionActivityTracker,
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

fn session_needs_picker_model_write(session: &Session, model: &str) -> bool {
    let model = model.trim();
    !model.is_empty() && session.next_model.trim() != model
}

impl App {
    fn ui_locale(&self) -> Locale {
        self.options
            .locale
            .as_deref()
            .and_then(Locale::from_product_id)
            .unwrap_or_else(|| Locale::ALL[self.locale_index.min(Locale::ALL.len() - 1)])
    }

    fn retained_run_id(&self) -> Option<&str> {
        retained_execution_run_id(
            self.active_run_id.as_deref(),
            &self.active_run_presentation.run_id,
            &self.execution_status,
        )
    }

    fn has_retained_run(&self) -> bool {
        self.retained_run_id().is_some()
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

    fn reconcile_mandatory_disclosures(&mut self) -> bool {
        let mandatory = self
            .blocks
            .iter()
            .filter(|block| {
                block.failure.is_some()
                    || block
                        .tool_members
                        .iter()
                        .any(crate::transcript::ToolGroupMember::is_failure)
            })
            .map(|block| block.id.clone())
            .collect::<Vec<_>>();
        let mut changed = false;
        for id in mandatory {
            if self.tool_disclosure_overrides.get(&id) == Some(&false) {
                self.tool_disclosure_overrides.remove(&id);
                changed = true;
            }
        }
        changed
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

    fn resolved_glyphs(&self, preference: GlyphPreference) -> Option<GlyphResolution> {
        ResolvedGlyphs::detect(preference).ok()
    }

    fn apply_glyph_resolution(&mut self, resolution: GlyphResolution) {
        self.glyph_resolution = resolution;
        self.theme.glyphs = self.theme.glyphs.with_mode(resolution.mode);
        self.clear_transcript_projection_caches();
        self.dirty = true;
    }

    fn open_settings(&mut self) {
        if matches!(
            self.overlays.active(),
            Some(Overlay::Settings(SettingsOverlay {
                page: SettingsPage::Symbols,
                ..
            }))
        ) {
            self.cancel_glyph_preview();
        }
        self.overlays
            .replace(Overlay::Settings(SettingsOverlay::root(
                self.glyph_preference,
            )));
    }

    fn open_glyph_preview(&mut self) {
        match self.overlays.active_mut() {
            Some(Overlay::Settings(settings)) => {
                settings.page = SettingsPage::Symbols;
                settings.original_preference = self.glyph_preference;
                settings.symbol_selected = GlyphPreference::ALL
                    .iter()
                    .position(|candidate| *candidate == self.glyph_preference)
                    .unwrap_or_default();
            }
            _ => self
                .overlays
                .replace(Overlay::Settings(SettingsOverlay::symbols(
                    self.glyph_preference,
                ))),
        }
    }

    fn preview_glyph_preference(&mut self, preference: GlyphPreference) {
        let Some(resolution) = self.resolved_glyphs(preference) else {
            self.notice = Notice::localized(MessageId::SymbolsPersistFailed);
            return;
        };
        let Some(Overlay::Settings(settings)) = self.overlays.active_mut() else {
            return;
        };
        if settings.page != SettingsPage::Symbols {
            return;
        }
        settings.symbol_selected = GlyphPreference::ALL
            .iter()
            .position(|candidate| *candidate == preference)
            .unwrap_or_default();
        self.apply_glyph_resolution(resolution);
    }

    fn cancel_glyph_preview(&mut self) {
        let Some(Overlay::Settings(settings)) = self.overlays.active() else {
            return;
        };
        let original = settings.original_preference;
        let Some(resolution) = self.resolved_glyphs(original) else {
            self.notice = Notice::localized(MessageId::SymbolsPersistFailed);
            return;
        };
        self.apply_glyph_resolution(resolution);
        if let Some(Overlay::Settings(settings)) = self.overlays.active_mut() {
            settings.page = SettingsPage::Root;
            settings.symbol_selected = GlyphPreference::ALL
                .iter()
                .position(|candidate| *candidate == original)
                .unwrap_or_default();
        }
    }

    fn commit_glyph_preference(&mut self) {
        let Some(Overlay::Settings(settings)) = self.overlays.active() else {
            return;
        };
        let preference = settings.symbol_preference();
        let path = self
            .options
            .glyphs_path
            .clone()
            .or_else(default_locale_path);
        let Some(path) = path else {
            self.notice = Notice::localized(MessageId::SymbolsPersistFailed);
            return;
        };
        let Some(resolution) = self.resolved_glyphs(preference) else {
            self.notice = Notice::localized(MessageId::SymbolsPersistFailed);
            return;
        };
        if persist_glyph_preference(&path, preference).is_err() {
            self.notice = Notice::localized(MessageId::SymbolsPersistFailed);
            return;
        }
        self.glyph_preference = preference;
        self.apply_glyph_resolution(resolution);
        if let Some(Overlay::Settings(settings)) = self.overlays.active_mut() {
            settings.original_preference = preference;
            settings.page = SettingsPage::Root;
        }
        self.notice = Notice::localized_with(
            MessageId::SymbolsApplied,
            [
                ("preference", preference.as_config_value()),
                ("mode", resolution.mode.as_str()),
            ],
        );
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
        let runtime = rpc
            .initialize_expected(options.runtime_expectation.as_ref())
            .context("initialize runtime protocol")?;
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
        let glyph_preference = options.glyph_preference;
        let glyph_resolution = ResolvedGlyphs::detect(glyph_preference)
            .context("resolve terminal symbol preference")?;
        let mut theme = Theme::detected(None);
        theme.glyphs = theme.glyphs.with_mode(glyph_resolution.mode);

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
            glyph_preference,
            glyph_resolution,
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
            product_menu_anchor: None,
            composer_pointer_captured: false,
            slash_selected: 0,
            slash_selected_id: None,
            slash_dismissed_input: None,
            command_registry: crate::rpc::CommandRegistry::default(),
            command_registry_session: String::new(),
            command_generation: 0,
            command_mru: command::load_command_mru(),
            last_submitted_draft: None,
            command_registry_stale: false,
            persisted_prompt_history: Vec::new(),
            persisted_prompt_history_unavailable: false,
            history_search: None,
            context_completion: ContextCompletion::default(),
            pending_file_attach: None,
            file_viewer_generation: 0,
            product_generation: 0,
            context_generation: 0,
            context_summary: None,
            transcript_geometry: TranscriptGeometry::default(),
            transcript_scrollbar: TranscriptScrollbar::default(),
            transcript_scrollbar_interaction: TranscriptScrollbarInteraction::default(),
            transcript_scroll: 0,
            transcript_max_scroll: 0,
            transcript_follow_bottom: true,
            transcript_anchor: None,
            transcript_height_cache: HashMap::new(),
            transcript_render_cache: HashMap::new(),
            bounded_tool_output_blocks: Vec::new(),
            tool_artifact_refs: HashMap::new(),
            tool_artifact_loads: HashMap::new(),
            tool_output_max_scroll: 0,
            history_selected: None,
            history_stashed_draft: None,
            history_original_scroll: None,
            history_generation: 0,
            history_branch_request_id: None,
            resume_generation: 0,
            resume_pending: false,
            history_branch_pending: false,
            failure_action_focus: None,
            rewind_primed_at: None,
            rewind_prime_window: rewind_prime_window(),
            quit_primed_at: None,
            credential: String::new(),
            credential_generation: 0,
            credential_pending: false,
            credential_child: Arc::new(Mutex::new(None)),
            notice: Notice::default(),
            notice_seen_key: String::new(),
            notice_seen: false,
            interactions: InteractionMap::default(),
            overlays: OverlayStack::default(),
            active_run_id: None,
            active_run_presentation: ActiveRunPresentation::default(),
            execution_timer: ExecutionTimer::default(),
            execution_status: "ready".into(),
            execution_activity: ExecutionActivityTracker::default(),
            keybindings: KeyBindings::default(),
            queued_prompts: VecDeque::new(),
            event_generation: 0,
            event_cursor: 0,
            transcript_stale: false,
            theme,
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
        app.queue_startup_inventory_refresh();
        Ok(app)
    }

    fn queue_startup_inventory_refresh(&mut self) {
        let grok_ready = self.inventory.providers.iter().any(|provider| {
            provider.source_kind == "grok-build" && self.inventory.is_provider_runnable(provider)
        });
        if grok_ready {
            return;
        }
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let mut last = Err("startup inventory unavailable".into());
            for attempt in 0..20 {
                if attempt > 0 {
                    std::thread::sleep(Duration::from_millis(250));
                }
                last = Client::connect(&socket)
                    .and_then(|mut rpc| rpc.model_inventory())
                    .map_err(|error| error.to_string());
                match &last {
                    Ok(inventory)
                        if inventory.providers.iter().any(|provider| {
                            provider.source_kind == "grok-build" && provider.available
                        }) =>
                    {
                        break;
                    }
                    Err(_) => break,
                    Ok(_) => {}
                }
            }
            let _ = tx.send(AsyncMessage::StartupInventory { result: last });
        });
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

    fn begin_conversation_import(&mut self) {
        let generation = self.session_browser.conversation_import_mut().begin();
        self.request_conversation_import_discovery(generation);
        self.phase = Phase::Session;
        self.focus = Focus::Scene;
        self.notice.clear();
    }

    fn request_conversation_import_discovery(&self, generation: u64) {
        let import = self.session_browser.conversation_import();
        let source = import.source().rpc_value().map(str::to_owned);
        let all_workspaces = import.all_workspaces();
        let workspace = self.options.workspace.to_string_lossy().into_owned();
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| {
                    rpc.discover_conversation_imports(source.as_deref(), &workspace, all_workspaces)
                })
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::ConversationImportsDiscovered { generation, result });
        });
    }

    fn confirm_conversation_import(&mut self) {
        let Some((generation, selections)) =
            self.session_browser.conversation_import_mut().begin_apply()
        else {
            return;
        };
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| {
                    let result = rpc.apply_conversation_imports(&selections)?;
                    let mut sessions = rpc.sessions()?;
                    sort_sessions_by_recency(&mut sessions);
                    Ok(Box::new(ConversationImportApplyOutcome {
                        result,
                        sessions,
                    }))
                })
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::ConversationImportsApplied { generation, result });
        });
    }

    fn open_conversation_import_result(&mut self) {
        let Some(session_id) = self
            .session_browser
            .conversation_import()
            .selected_result_session_id()
            .map(str::to_owned)
        else {
            return;
        };
        self.session_browser.conversation_import_mut().close();
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
                    self.active_run_presentation = ActiveRunPresentation::default();
                    self.execution_timer.reset();
                    self.execution_activity.clear();
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
                self.apply_open_session_state(outcome.session, outcome.items, outcome.active_run);
                if target_id == "new" {
                    self.notice = Notice::localized(MessageId::ConversationCreated);
                } else if target_id == "fork" {
                    self.notice = Notice::localized(MessageId::ConversationForked);
                }
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

    fn request_tool_artifact(&mut self, reference: crate::rpc::ToolArtifactRef) {
        let generation = self.event_generation;
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        self.tool_artifact_refs
            .insert(reference.call_id.clone(), reference.clone());
        self.tool_artifact_loads.insert(
            reference.call_id.clone(),
            crate::overlay::RetainedLoad::begin(generation, reference.session_id.clone()),
        );
        std::thread::spawn(move || {
            let session_id = reference.session_id.clone();
            let call_id = reference.call_id.clone();
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.complete_artifact_text(&reference))
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

    fn model_back_destination(&self) -> ModelBackDestination {
        if self.active_session.is_some() {
            ModelBackDestination::Conversation
        } else {
            ModelBackDestination::Provider
        }
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
                .set_session_model(
                    &session.session_id,
                    model,
                    &effort,
                    session.model_preference_revision,
                )
                .with_context(|| format!("select model for session {}", session.session_id))?;
            session.next_model = selection.next_model;
            session.next_reasoning_effort = selection.next_reasoning_effort;
            session.model_preference_revision = selection.model_preference_revision;
            self.selected_reasoning_effort = session.next_reasoning_effort.clone();
        }
        let active_run = load_active_run(&mut self.rpc, &session);
        self.apply_open_session_state(session, items, active_run);
        Ok(())
    }

    fn apply_open_session_state(
        &mut self,
        session: Session,
        items: Vec<SessionItemEvent>,
        active_run: Option<ExecutionRun>,
    ) {
        let session_changed = self
            .active_session
            .as_ref()
            .is_some_and(|active| active.session_id != session.session_id);
        if session_changed {
            self.cancel_pending_pastes();
        }
        self.context_completion.reset_session();
        self.pending_file_attach = None;
        self.sync_selection_from_session(&session);
        let hydrated_overlays = OverlayStack::hydrate_governance(&items);
        self.execution_lifecycle.clear();
        self.tool_disclosure_overrides.clear();
        self.tool_artifact_refs = tool_artifact_refs_from_items(&items);
        self.tool_artifact_loads.clear();
        self.blocks = self.transcript_reducer.hydrate(items);
        self.scrollback.reset();
        self.reset_transcript_viewport();
        let handoff = self.screen_handoff.take();
        if let Some(handoff) = handoff {
            let governance_matches =
                hydrated_overlays.governance_ids() == handoff.pending_governance;
            self.screen_handoff_failed =
                handoff.session_id != session.session_id || !governance_matches;
            if !self.screen_handoff_failed {
                if let Some(envelope) = handoff.reading_state.as_ref() {
                    if self
                        .apply_reading_envelope(envelope, &session.session_id)
                        .is_err()
                    {
                        self.screen_handoff_failed = true;
                    }
                } else {
                    self.transcript_scroll = handoff.transcript_scroll;
                    self.transcript_follow_bottom = handoff.transcript_follow_bottom;
                }
            }
            if self.screen_handoff_failed {
                self.outcome = Outcome::Degraded;
            }
        }
        self.transcript_stale = false;
        self.active_run_id = execution_status_is_interactive(&session.execution_status)
            .then(|| session.latest_run_id.clone())
            .filter(|run_id| !run_id.is_empty());
        self.active_run_presentation = active_run
            .as_ref()
            .filter(|run| {
                execution_status_retains_run_truth(&session.execution_status)
                    && run.run_id == session.latest_run_id
            })
            .map(ActiveRunPresentation::from_execution)
            .unwrap_or_default();
        self.execution_timer.reset();
        self.execution_activity.clear();
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
            command::retain_operator_mru(&mut self.command_mru);
            self.last_submitted_draft = None;
            self.command_registry = crate::rpc::CommandRegistry::default();
            self.command_registry_stale = false;
        }
        if command_registry_changed {
            self.request_command_registry();
        }
    }

    fn apply_model_preference(&mut self, preference: crate::rpc::SessionModelSelection) -> bool {
        let Some(session) = self
            .active_session
            .as_ref()
            .filter(|session| session.session_id == preference.session_id)
        else {
            return false;
        };
        if preference.model_preference_revision <= session.model_preference_revision {
            return false;
        }
        self.adopt_session_model_selection(preference);
        true
    }

    fn adopt_session_model_selection(&mut self, preference: crate::rpc::SessionModelSelection) {
        let Some(mut session) = self
            .active_session
            .as_ref()
            .filter(|session| session.session_id == preference.session_id)
            .cloned()
        else {
            return;
        };
        session.next_model = preference.next_model;
        session.next_reasoning_effort = preference.next_reasoning_effort;
        session.model_preference_revision = preference.model_preference_revision;
        self.sync_selection_from_session(&session);
        self.remember_session(session);
    }

    fn write_picker_model_to_session(&mut self) -> bool {
        for _ in 0..2 {
            let Some(session) = self.active_session.as_ref() else {
                return true;
            };
            if !session_needs_picker_model_write(session, &self.selected_model) {
                return true;
            }
            let session_id = session.session_id.clone();
            let expected = session.model_preference_revision;
            let model = self.selected_model.clone();
            let effort = self.selected_reasoning_effort.clone();
            match self
                .rpc
                .set_session_model(&session_id, &model, &effort, expected)
            {
                Ok(selection) => {
                    self.adopt_session_model_selection(selection);
                    return true;
                }
                Err(error) => {
                    if !self.reconcile_model_preference_conflict(&error) {
                        self.notice = Notice::localized_with(
                            MessageId::SubmitFailedDraftKept,
                            [("error", error.to_string())],
                        );
                        return false;
                    }
                }
            }
        }
        self.active_session.as_ref().is_none_or(|session| {
            !session_needs_picker_model_write(session, &self.selected_model)
        })
    }

    fn reconcile_model_preference_conflict(&mut self, error: &RpcError) -> bool {
        let Some(preference) = error.model_preference_conflict() else {
            return false;
        };
        self.apply_model_preference(preference);
        self.notice = Notice::localized(MessageId::ModelPreferenceChangedDraftKept);
        true
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
            if result
                .as_ref()
                .is_ok_and(|registry| registry.state == "probing")
            {
                std::thread::sleep(Duration::from_millis(400));
            }
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
                self.retry_runtime_reconnect(generation);
                return;
            }
        };
        if let Some(boundary) = outcome.boundary.as_ref()
            && validate_reconnect_boundary(
                session_id,
                &outcome.runtime.runtime,
                outcome.watermark,
                boundary,
            )
            .is_err()
        {
            self.retry_runtime_reconnect(generation);
            return;
        }
        let RuntimeReconnectOutcome {
            rpc,
            runtime,
            inventory,
            mut sessions,
            session,
            items,
            active_run,
            prompt_history,
            prompt_history_unavailable,
            security_context,
            watermark,
            catch_up,
            live,
            boundary,
        } = *outcome;
        let reading = self.capture_reading_envelope();
        let (reducer, blocks, catch_up_cursor) = hydrate_reconnect_blocks(items.clone(), catch_up);
        sort_sessions_by_recency(&mut sessions);
        self.rpc = rpc;
        self.runtime = runtime;
        self.inventory = inventory;
        self.sessions = sessions;
        self.models = self.inventory.available_models();
        self.sync_selection_from_session(&session);
        self.tool_artifact_refs = tool_artifact_refs_from_items(&items);
        self.tool_artifact_loads.clear();
        self.transcript_reducer = reducer;
        self.blocks = blocks;
        self.transcript_stale = false;
        self.event_cursor = reconnect_event_cursor(
            self.event_cursor,
            watermark,
            catch_up_cursor,
            boundary.is_some(),
        );
        self.persisted_prompt_history = prompt_history;
        self.persisted_prompt_history_unavailable = prompt_history_unavailable;
        self.security_context = security_context;
        self.active_run_id = execution_status_is_interactive(&session.execution_status)
            .then(|| session.latest_run_id.clone())
            .filter(|run_id| !run_id.is_empty());
        self.active_run_presentation = active_run
            .as_ref()
            .filter(|run| {
                execution_status_retains_run_truth(&session.execution_status)
                    && run.run_id == session.latest_run_id
            })
            .map(ActiveRunPresentation::from_execution)
            .unwrap_or_default();
        self.execution_timer.reset();
        self.execution_activity.clear();
        self.execution_status = if session.execution_status.is_empty() {
            "ready".into()
        } else {
            session.execution_status.clone()
        };
        self.seed_execution_lifecycle(&session.latest_run_id, &session.execution_status);
        self.command_registry_session.clear();
        let reading_failed = reading.as_ref().is_some_and(|envelope| {
            self.apply_reading_envelope(envelope, &session.session_id)
                .is_err()
        });
        self.remember_session(session);
        self.notice.clear();
        if reading_failed {
            self.notice = Notice::localized(MessageId::ScreenModeHandoffRejected);
        }
        if let Some(live) = live {
            self.adopt_event_stream(generation, live);
        } else {
            self.start_event_stream();
        }
    }

    fn retry_runtime_reconnect(&mut self, generation: u64) {
        self.notice = Notice::localized(MessageId::RuntimeUnavailable);
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(500));
            let _ = tx.send(AsyncMessage::Reconnect { generation });
        });
    }

    fn adopt_event_stream(
        &mut self,
        generation: u64,
        live: std::sync::mpsc::Receiver<Result<ReceivedEvent, RpcError>>,
    ) {
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            for event in live {
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
        if provider.source_kind == "grok-build" && !provider.available {
            if provider.source_action == "disabled" {
                self.clear_provider_import_state();
                self.phase = Phase::Provider;
                self.focus = Focus::Scene;
                self.notice = Notice::localized(MessageId::GrokBuildDisabledDetail);
                return;
            }
            let provider_id = provider.id.clone();
            self.clear_provider_import_state();
            match self.rpc.model_inventory_refresh() {
                Ok(inventory) => {
                    self.inventory = inventory;
                    let Some(index) = self
                        .inventory
                        .providers
                        .iter()
                        .position(|candidate| candidate.id == provider_id)
                    else {
                        self.phase = Phase::Provider;
                        self.focus = Focus::Scene;
                        self.notice = Notice::localized(MessageId::ProviderSelectionExpired);
                        return;
                    };
                    self.provider_index = index;
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
            let Some(refreshed) = self.inventory.providers.get(self.provider_index) else {
                return;
            };
            if !refreshed.available {
                self.phase = Phase::Provider;
                self.focus = Focus::Scene;
                self.notice =
                    Notice::localized(grok_build_repair_message(refreshed.source_action.as_str()));
                return;
            }
            self.select_provider_and_continue();
            return;
        }
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
                                let parent = agents.load.session_id.clone();
                                let selected_id = agent_roster_entries(&agents.projection, &parent)
                                    .get(agents.selected)
                                    .map(|agent| agent.task_id.as_str());
                                agents.selected = selected_id
                                    .and_then(|selected_id| {
                                        agent_roster_entries(&outcome.projection, &parent)
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
                        Ok(outcome) => {
                            let selected_patch = changes
                                .projection
                                .patches
                                .get(changes.selected)
                                .map(|patch| patch.patch_id.clone());
                            let selected_patch_file = changes
                                .patch_reviews
                                .get(changes.selected)
                                .and_then(|review| review.files.get(changes.selected_file))
                                .map(|file| file.path.clone());
                            let selected_path = changes
                                .projection
                                .workspace_diff
                                .files
                                .get(changes.selected)
                                .map(|file| file.path.clone());
                            let selected_review = changes
                                .projection
                                .review
                                .changes
                                .get(changes.selected)
                                .map(|change| change.id.clone());
                            let ChangesLoadOutcome {
                                projection,
                                patch_reviews,
                            } = *outcome;
                            let retained_patch_selection = retain_patch_review_selection(
                                selected_patch.as_deref(),
                                selected_patch_file.as_deref(),
                                &projection.patches,
                                &patch_reviews,
                            );
                            changes.selected = if !projection.patches.is_empty() {
                                retained_patch_selection.0
                            } else if projection.workspace_diff.files.is_empty() {
                                selected_review
                                    .as_deref()
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
                                    .as_deref()
                                    .and_then(|path| {
                                        projection
                                            .workspace_diff
                                            .files
                                            .iter()
                                            .position(|file| file.path == path)
                                    })
                                    .unwrap_or(0)
                            };
                            changes.selected_file = if projection.patches.is_empty() {
                                0
                            } else {
                                retained_patch_selection.1
                            };
                            changes.load.finish(
                                projection
                                    .patches_error
                                    .clone()
                                    .or_else(|| projection.workspace_diff_error.clone()),
                            );
                            changes.projection = projection;
                            changes.patch_reviews = patch_reviews;
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
                            let probing = registry.state == "probing";
                            self.command_registry = registry;
                            self.command_registry_session = session_id;
                            self.command_registry_stale = false;
                            self.sync_slash_selection();
                            if probing {
                                self.request_command_registry();
                            }
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
                            let resolved = self.resolve_clipboard_text(&request, &text);
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
                    if self
                        .pending_file_attach
                        .as_ref()
                        .is_some_and(|pending| {
                            pending.generation == generation
                                && pending.session_id == session_id
                                && pending.path == path
                        })
                    {
                        self.finish_ranged_file_attach(result);
                        continue;
                    }
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
                            if let Some(cursor) = received.durable_raw_cursor() {
                                self.event_cursor = self.event_cursor.max(cursor);
                            }
                            let ReceivedEvent {
                                event,
                                received_at,
                                replayed,
                                ..
                            } = received;
                            if event.kind == "session.model.preference.changed" {
                                if event.session_id
                                    == self
                                        .active_session
                                        .as_ref()
                                        .map(|session| session.session_id.as_str())
                                        .unwrap_or_default()
                                    && let Ok(preference) =
                                        serde_json::from_value::<crate::rpc::SessionModelSelection>(
                                            serde_json::Value::Object(
                                                event.payload.clone().into_iter().collect(),
                                            ),
                                        )
                                    && self.apply_model_preference(preference)
                                {
                                    self.dirty = true;
                                }
                                continue;
                            }
                            let lifecycle = match self.execution_lifecycle.reduce(&event) {
                                ExecutionLifecycleReduction::Accepted(lifecycle) => Some(lifecycle),
                                ExecutionLifecycleReduction::NotLifecycle => None,
                                ExecutionLifecycleReduction::Ignored => continue,
                            };
                            let mut visual_changed = false;
                            let artifact_ref = event.tool_artifact_ref();
                            let terminal_summary = event_terminal_summary(&event);
                            if let Some(lifecycle) = lifecycle {
                                crate::desktop_notify::consider_desktop_notify(
                                    self.terminal_focused,
                                    replayed,
                                    self.ui_locale(),
                                    lifecycle,
                                    terminal_summary.as_deref(),
                                );
                            }
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
                            let projected_run_id = self.active_run_id.as_deref().or_else(|| {
                                (!self.active_run_presentation.run_id.is_empty())
                                    .then_some(self.active_run_presentation.run_id.as_str())
                            });
                            let latest_session_run_id = self
                                .active_session
                                .as_ref()
                                .map(|session| session.latest_run_id.as_str())
                                .filter(|run_id| !run_id.is_empty());
                            let event_owns_projection = execution_event_owns_projection(
                                projected_run_id,
                                latest_session_run_id,
                                &event.run_id,
                                lifecycle,
                            );
                            if let Some(status) = lifecycle
                                .filter(|lifecycle| lifecycle.is_active())
                                .filter(|_| event_owns_projection)
                                .map(ExecutionLifecycle::status)
                            {
                                visual_changed = true;
                                if self.active_run_id.as_deref() != Some(event.run_id.as_str()) {
                                    self.execution_timer.start_new();
                                    self.execution_activity.clear();
                                    self.active_run_presentation = ActiveRunPresentation {
                                        run_id: event.run_id.clone(),
                                        ..ActiveRunPresentation::default()
                                    };
                                } else if matches!(status, "waiting_input" | "waiting_approval") {
                                    self.execution_timer.pause();
                                    self.execution_activity.clear();
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
                            visual_changed |= self.active_run_presentation.apply_event(&event);
                            if self.active_run_id.as_deref() == Some(event.run_id.as_str())
                                && execution_status_animates(&self.execution_status)
                                && let Some(update) = event.live_activity_update()
                            {
                                visual_changed |= self.execution_activity.apply(update);
                            }
                            if lifecycle.is_some_and(ExecutionLifecycle::clears_active)
                                && event_owns_projection
                            {
                                visual_changed = true;
                                if self.active_run_id.as_deref() == Some(event.run_id.as_str()) {
                                    self.active_run_id = None;
                                    self.execution_timer.reset();
                                    self.execution_activity.clear();
                                }
                                if lifecycle.is_some_and(ExecutionLifecycle::is_terminal) {
                                    self.active_run_presentation = ActiveRunPresentation::default();
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
                            if event.kind == "ContextCompacted" {
                                self.request_context_summary();
                                let circuit_open = event.projected_status()
                                    == "summarizer_circuit_open"
                                    || event
                                        .payload
                                        .get("summarizer_circuit")
                                        .and_then(serde_json::Value::as_str)
                                        == Some("open");
                                if circuit_open {
                                    self.notice = Notice::localized(MessageId::SummarizerCircuitOpen);
                                    visual_changed = true;
                                }
                            }
                            let plan_review = self.active_session.as_ref().and_then(|session| {
                                plan_review_overlay(session).filter(|review| {
                                    review.run_id == event.run_id
                                        && lifecycle == Some(ExecutionLifecycle::Completed)
                                })
                            });
                            let transcript_changed = self
                                .transcript_reducer
                                .reduce_event(&mut self.blocks, event);
                            visual_changed |= transcript_changed;
                            if transcript_changed {
                                visual_changed |= self.reconcile_mandatory_disclosures();
                            }
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
                            if !self.has_retained_run()
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
                AsyncMessage::ConversationImportsDiscovered { generation, result } => {
                    self.session_browser
                        .conversation_import_mut()
                        .apply_discovery(generation, result);
                }
                AsyncMessage::ConversationImportsApplied { generation, result } => match result {
                    Ok(outcome) => {
                        let ConversationImportApplyOutcome { result, sessions } = *outcome;
                        self.sessions = sessions;
                        self.session_browser
                            .conversation_import_mut()
                            .apply_results(generation, Ok(result));
                    }
                    Err(error) => self
                        .session_browser
                        .conversation_import_mut()
                        .apply_results(generation, Err(error)),
                },
                AsyncMessage::StartupInventory { result } => {
                    if let Ok(inventory) = result {
                        self.inventory = inventory;
                        self.models = self.inventory.available_models();
                        if !self.models.is_empty() {
                            self.model_index = self
                                .models
                                .iter()
                                .position(|model| model.id == self.selected_model)
                                .or_else(|| {
                                    self.models.iter().position(|model| {
                                        model.id == self.inventory.default_model
                                    })
                                })
                                .unwrap_or(0);
                            self.selected_model = self
                                .models
                                .get(self.model_index)
                                .map(|model| model.id.clone())
                                .unwrap_or_default();
                        }
                        if matches!(
                            self.phase,
                            Phase::Provider | Phase::Model | Phase::Diagnostic
                        ) {
                            self.route_after_locale();
                        }
                    }
                }
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
                    if !self
                        .tool_artifact_loads
                        .get(&call_id)
                        .is_some_and(|load| load.accepts(generation, &session_id))
                    {
                        continue;
                    }
                    match result {
                        Ok(artifact) => {
                            self.tool_artifact_refs.remove(&call_id);
                            self.tool_artifact_loads.remove(&call_id);
                            self.transcript_reducer.apply_tool_output(
                                &mut self.blocks,
                                &call_id,
                                artifact.content,
                                artifact.truncated,
                            );
                        }
                        Err(error) => {
                            if let Some(load) = self.tool_artifact_loads.get_mut(&call_id) {
                                load.finish(Some(error.clone()));
                            }
                            self.notice = Notice::localized_with(
                                MessageId::ToolOutputLoadFailed,
                                [("error", error)],
                            );
                        }
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
                let plan_review_active =
                    matches!(self.overlays.active(), Some(Overlay::PlanReview(_)));
                if plan_review_active {
                    if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut()
                        && review.commenting
                    {
                        review.append_comment(&value.replace('\r', ""));
                    }
                } else if let Some(path) = pasted_image_path(&value) {
                    self.attach_image(path, false);
                } else {
                    self.insert_composer_paste(&value.replace('\r', ""));
                }
                if !plan_review_active {
                    self.focus = Focus::Composer;
                }
                self.dirty = true;
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
                self.has_retained_run(),
                self.quit_primed_at,
                now,
                self.rewind_prime_window,
            ) {
                QuitHardCancelAction::CancelRun => {
                    // Active turn: hard-cancel the run. Do not exit the TUI.
                    self.quit_primed_at = None;
                    if let Some(run_id) = self.retained_run_id().map(str::to_owned) {
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
        if self.phase == Phase::Session && self.session_browser.conversation_import().is_open() {
            self.handle_conversation_import_key(key);
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
                KeyCode::Char('p' | 'P')
                    if key.modifiers.is_empty() || key.modifiers == KeyModifiers::SHIFT =>
                {
                    self.apply_action(Action::OpenProvider);
                }
                KeyCode::Esc => match self.model_back_destination() {
                    ModelBackDestination::Conversation => {
                        self.return_to_conversation_or_repair();
                    }
                    ModelBackDestination::Provider => self.phase = Phase::Provider,
                },
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
                KeyCode::Char('i' | 'I') if !self.session_browser.search_active() => {
                    self.begin_conversation_import()
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

    fn handle_conversation_import_key(&mut self, key: KeyEvent) {
        let stage = self.session_browser.conversation_import().stage();
        match stage {
            ConversationImportStage::Closed => {}
            ConversationImportStage::Discovering | ConversationImportStage::Applying => {
                if key.code == KeyCode::Esc && stage == ConversationImportStage::Discovering {
                    self.session_browser.conversation_import_mut().close();
                }
            }
            ConversationImportStage::Selecting => match key.code {
                KeyCode::Up => self
                    .session_browser
                    .conversation_import_mut()
                    .move_candidate(false),
                KeyCode::Down => self
                    .session_browser
                    .conversation_import_mut()
                    .move_candidate(true),
                KeyCode::Char(' ') => self
                    .session_browser
                    .conversation_import_mut()
                    .toggle_candidate(None),
                KeyCode::Char('a' | 'A') => {
                    self.session_browser.conversation_import_mut().toggle_all()
                }
                KeyCode::Char('s' | 'S') => {
                    let generation = self
                        .session_browser
                        .conversation_import_mut()
                        .cycle_source();
                    self.request_conversation_import_discovery(generation);
                }
                KeyCode::Tab | KeyCode::BackTab => {
                    let generation = self
                        .session_browser
                        .conversation_import_mut()
                        .toggle_workspace_scope();
                    self.request_conversation_import_discovery(generation);
                }
                KeyCode::Char('r' | 'R') => {
                    let generation = self
                        .session_browser
                        .conversation_import_mut()
                        .begin_discovery();
                    self.request_conversation_import_discovery(generation);
                }
                KeyCode::Enter => {
                    if !self
                        .session_browser
                        .conversation_import_mut()
                        .begin_confirmation()
                    {
                        self.notice =
                            Notice::localized(MessageId::ConversationImportSelectRequired);
                    }
                }
                KeyCode::Esc => self.session_browser.conversation_import_mut().close(),
                _ => {}
            },
            ConversationImportStage::Confirming => match key.code {
                KeyCode::Enter => self.confirm_conversation_import(),
                KeyCode::Esc => self
                    .session_browser
                    .conversation_import_mut()
                    .cancel_confirmation(),
                _ => {}
            },
            ConversationImportStage::Results => match key.code {
                KeyCode::Up => self
                    .session_browser
                    .conversation_import_mut()
                    .move_result(false),
                KeyCode::Down => self
                    .session_browser
                    .conversation_import_mut()
                    .move_result(true),
                KeyCode::Enter => self.open_conversation_import_result(),
                KeyCode::Char('r' | 'R') => {
                    let generation = self
                        .session_browser
                        .conversation_import_mut()
                        .begin_discovery();
                    self.request_conversation_import_discovery(generation);
                }
                KeyCode::Esc => self.session_browser.conversation_import_mut().close(),
                _ => {}
            },
        }
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
        if self.keybindings.inspect_tool_output.matches(key) {
            if let Some(block_id) = self.bounded_tool_output_blocks.last().cloned() {
                self.apply_action(Action::OpenToolOutput(block_id));
            }
            return Ok(());
        }
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
        if slash_count == 0 && self.handle_failure_action_key(key) {
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
                    && command::resolve(self.composer.text(), self.has_retained_run())
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
                None if self.restore_submitted_draft_if_pristine() => {
                    self.notice = Notice::localized(MessageId::RestoredLastPrompt);
                }
                None => self.handle_rewind_escape(),
            },
            KeyCode::Char('?') if self.composer.text().is_empty() => {
                self.open_help_overlay();
            }
            KeyCode::Char(',') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                self.open_settings();
            }
            KeyCode::Char('r') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                self.open_prompt_history_search();
            }
            KeyCode::Char('c') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                if let Some(run_id) = self.retained_run_id().map(str::to_owned) {
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
                    self.retry_failed_execution(&run_id, crate::rpc::RetryRouting::Current);
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

    fn retry_failed_execution(&mut self, original_run_id: &str, routing: crate::rpc::RetryRouting) {
        if !retry_dispatch_allowed(&self.blocks, self.retained_run_id(), original_run_id) {
            return;
        }
        match self.rpc.retry_execution(
            original_run_id,
            routing,
            &self.selected_model,
            &self.selected_reasoning_effort,
            self.active_session
                .as_ref()
                .map(|session| session.model_preference_revision)
                .unwrap_or_default(),
            &operation_id("retry"),
        ) {
            Ok(execution) => {
                self.failure_action_focus = None;
                self.focus = Focus::Composer;
                let retry_root_run_id = self.blocks.iter().find_map(|block| {
                    block
                        .failure
                        .as_ref()
                        .filter(|failure| failure.run_id == original_run_id)
                        .map(|failure| failure.retry_root_run_id.clone())
                });
                let mut failure_changed = false;
                for block in &mut self.blocks {
                    let Some(failure) = block
                        .failure
                        .as_mut()
                        .filter(|failure| failure.run_id == original_run_id)
                    else {
                        continue;
                    };
                    block.run_id.clone_from(&execution.run_id);
                    failure.run_id.clone_from(&execution.run_id);
                    failure.action = crate::transcript::FailureAction::Recovering;
                    failure.attempt_count = failure.attempt_count.saturating_add(1);
                    failure.focused_action = None;
                    block.layout_revision = block.layout_revision.saturating_add(1);
                    failure_changed = true;
                }
                if let Some(retry_root_run_id) = retry_root_run_id
                    && let Some(user) = self
                        .blocks
                        .iter_mut()
                        .find(|block| block.id == format!("user:{retry_root_run_id}"))
                {
                    user.run_id.clone_from(&execution.run_id);
                }
                if failure_changed {
                    self.clear_transcript_projection_caches();
                }
                self.active_run_id = Some(execution.run_id.clone());
                self.active_run_presentation = ActiveRunPresentation::from_execution(&execution);
                self.execution_timer.start_new();
                self.execution_activity.clear();
                self.execution_status = if execution.status.is_empty() {
                    "queued".into()
                } else {
                    execution.status.clone()
                };
                self.seed_execution_lifecycle(&execution.run_id, &self.execution_status.clone());
                let execution_status = self.execution_status.clone();
                self.update_active_execution_metadata(
                    &execution.run_id,
                    &execution_status,
                    (!execution.agent.is_empty()).then_some(execution.agent.as_str()),
                    None,
                    Some(""),
                );
                self.notice = Notice::localized_for_run(
                    MessageId::ExecutionWorking,
                    execution.run_id,
                    std::iter::empty::<(&str, &str)>(),
                );
            }
            Err(error) => {
                if self.reconcile_model_preference_conflict(&error) {
                    return;
                }
                self.notice = Notice::localized_with(
                    MessageId::SubmitFailedDraftKept,
                    [("error", error.to_string())],
                );
            }
        }
    }

    fn failure_action_for(
        block: &TranscriptBlock,
        selected: crate::transcript::FailureRecoveryAction,
    ) -> Option<Action> {
        use crate::transcript::FailureRecoveryAction;

        let failure = block.failure.as_ref()?;
        match selected {
            FailureRecoveryAction::RetryCurrent => Some(Action::RetryExecution {
                run_id: failure.run_id.clone(),
                routing: crate::rpc::RetryRouting::Current,
            }),
            FailureRecoveryAction::ReplayOriginal => Some(Action::RetryExecution {
                run_id: failure.run_id.clone(),
                routing: crate::rpc::RetryRouting::Original,
            }),
            FailureRecoveryAction::Details => Some(Action::ToggleBlock(block.id.clone())),
            FailureRecoveryAction::CopyId => Some(Action::CopyFailureId(
                if failure.source_event_id.is_empty() {
                    failure.run_id.clone()
                } else {
                    failure.source_event_id.clone()
                },
            )),
        }
    }

    fn handle_failure_action_key(&mut self, key: KeyEvent) -> bool {
        use crate::transcript::FailureRecoveryAction;

        if let Some(focus) = self.failure_action_focus.clone() {
            let Some(block) = self.blocks.iter().find(|block| block.id == focus.block_id) else {
                self.failure_action_focus = None;
                self.focus = Focus::Composer;
                return false;
            };
            let Some(failure) = block.failure.as_ref() else {
                self.failure_action_focus = None;
                self.focus = Focus::Composer;
                return false;
            };
            let actions = failure.available_actions(&self.selected_model);
            if actions.is_empty() {
                self.failure_action_focus = None;
                self.focus = Focus::Composer;
                return false;
            }
            let index = actions
                .iter()
                .position(|action| *action == focus.selected)
                .unwrap_or(0);
            match key.code {
                KeyCode::Tab | KeyCode::Right | KeyCode::Down => {
                    self.failure_action_focus = Some(FailureActionFocus {
                        block_id: focus.block_id,
                        selected: actions[(index + 1) % actions.len()],
                    });
                    return true;
                }
                KeyCode::BackTab | KeyCode::Left | KeyCode::Up => {
                    self.failure_action_focus = Some(FailureActionFocus {
                        block_id: focus.block_id,
                        selected: actions[(index + actions.len() - 1) % actions.len()],
                    });
                    return true;
                }
                KeyCode::Enter if key.modifiers.is_empty() => {
                    let action = Self::failure_action_for(block, focus.selected);
                    if !matches!(focus.selected, FailureRecoveryAction::Details) {
                        self.failure_action_focus = None;
                        self.focus = Focus::Composer;
                    }
                    if let Some(action) = action {
                        self.apply_action(action);
                    }
                    return true;
                }
                KeyCode::Esc => {
                    self.failure_action_focus = None;
                    self.focus = Focus::Composer;
                    return true;
                }
                _ => {
                    self.failure_action_focus = None;
                    self.focus = Focus::Composer;
                    return false;
                }
            }
        }

        if key.code != KeyCode::Tab
            || !key.modifiers.is_empty()
            || self.has_retained_run()
            || !self.composer.text().is_empty()
        {
            return false;
        }
        let Some((block_id, selected)) = self.blocks.iter().rev().find_map(|block| {
            let failure = block.failure.as_ref()?;
            failure
                .available_actions(&self.selected_model)
                .contains(&FailureRecoveryAction::RetryCurrent)
                .then(|| (block.id.clone(), FailureRecoveryAction::RetryCurrent))
        }) else {
            return false;
        };
        self.failure_action_focus = Some(FailureActionFocus { block_id, selected });
        self.focus = Focus::Scene;
        true
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
        match self.context_completion.typed_line_range() {
            Ok(Some(lines)) => {
                self.start_ranged_file_attach(lines);
                return;
            }
            Err(_) => {
                let query = self.context_completion.query().to_owned();
                self.notice = Notice::localized_with(
                    MessageId::FileRangeAttachFailed,
                    [("path", query), ("lines", String::new())],
                );
                return;
            }
            Ok(None) => {}
        }
        if self.context_completion.accept(&mut self.composer) {
            self.composer_state = TextAreaState::default();
            self.media.reconcile(&self.composer);
            self.slash_selected = 0;
            self.slash_selected_id = None;
            self.slash_dismissed_input = None;
        }
    }

    fn start_ranged_file_attach(&mut self, lines: std::ops::Range<usize>) {
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
        self.pending_file_attach = Some(PendingFileAttach {
            generation,
            session_id: session_id.clone(),
            path: candidate.path.clone(),
            lines,
            token: context,
        });
        self.load_file_viewer(generation, session_id, candidate.path);
    }

    fn finish_ranged_file_attach(
        &mut self,
        result: Result<crate::rpc::WorkspaceFileContent, String>,
    ) -> bool {
        let Some(pending) = self.pending_file_attach.take() else {
            return false;
        };
        let label = if pending.lines.end == pending.lines.start + 1 {
            pending.lines.start.to_string()
        } else {
            format!("{}-{}", pending.lines.start, pending.lines.end - 1)
        };
        match result {
            Ok(file) => match self.context_completion.accept_with_content(
                &mut self.composer,
                &pending.token,
                &pending.path,
                pending.lines,
                &file.content,
            ) {
                Ok(true) => {
                    self.composer_state = TextAreaState::default();
                    self.media.reconcile(&self.composer);
                    self.slash_selected = 0;
                    self.slash_selected_id = None;
                    self.slash_dismissed_input = None;
                    self.notice.clear();
                }
                Ok(false) => {
                    self.notice = Notice::localized_with(
                        MessageId::FileRangeAttachFailed,
                        [("path", pending.path), ("lines", label)],
                    );
                }
                Err(_) => {
                    self.notice = Notice::localized_with(
                        MessageId::FileRangeAttachFailed,
                        [("path", pending.path), ("lines", label)],
                    );
                }
            },
            Err(_) => {
                self.notice = Notice::localized_with(
                    MessageId::FileRangeAttachFailed,
                    [("path", pending.path), ("lines", label)],
                );
            }
        }
        true
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
        let initial_range = self
            .context_completion
            .typed_line_range()
            .ok()
            .flatten();
        self.open_file_viewer(
            candidate.path,
            FileViewerOrigin::Completion {
                range: context.range,
            },
            initial_range,
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
        let visible_rows = self
            .transcript_geometry
            .viewport
            .height
            .saturating_sub(4)
            .min(8) as usize;
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
        if let Some(reason) =
            command::mcp_slash_unready(&prompt, &self.command_registry.state)
        {
            self.notice = Notice::localized(reason);
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
        if self.has_retained_run() {
            self.notice = Notice::localized(MessageId::CurrentExecutionPaused);
            return Ok(());
        }
        self.submit_new_prompt(prompt, media_refs).map(|_| ())
    }

    fn submit_new_prompt(
        &mut self,
        prompt: String,
        media_refs: Vec<crate::rpc::MediaRef>,
    ) -> Result<bool> {
        if self.has_retained_run() {
            self.notice = Notice::localized(MessageId::CurrentExecutionPaused);
            return Ok(false);
        }
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
            .ok_or_else(|| anyhow!("conversation has no active session"))?;
        let locale = agent_locale(self.options.locale.as_deref().unwrap_or("en")).to_owned();
        match self
            .rpc
            .model_inventory_for(&session_id, &self.selected_model, &locale)
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
        if !self.write_picker_model_to_session() {
            return Ok(false);
        }
        let envelope = self.new_prompt_envelope(session_id, prompt, &locale, media_refs);
        match self.dispatch_new_prompt(envelope.clone()) {
            Ok(true) => Ok(true),
            Ok(false) => {
                if self
                    .notice
                    .is_localized(MessageId::ModelPreferenceChangedDraftKept)
                    && self.write_picker_model_to_session()
                {
                    let retry = self.new_prompt_envelope(
                        envelope.session_id.clone(),
                        envelope.prompt.clone(),
                        &locale,
                        envelope.media_refs.clone(),
                    );
                    self.dispatch_new_prompt(retry)
                } else {
                    Ok(false)
                }
            }
            Err(error) => Err(error),
        }
    }

    fn new_prompt_envelope(
        &self,
        session_id: String,
        prompt: String,
        locale: &str,
        media_refs: Vec<crate::rpc::MediaRef>,
    ) -> PendingSubmission {
        PendingSubmission {
            session_id,
            prompt,
            model: self.selected_model.clone(),
            reasoning_effort: self.selected_reasoning_effort.clone(),
            model_preference_revision: self
                .active_session
                .as_ref()
                .map(|session| session.model_preference_revision)
                .unwrap_or_default(),
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
        }
    }

    fn dispatch_new_prompt(&mut self, envelope: PendingSubmission) -> Result<bool> {
        match self.rpc.submit(
            &envelope.session_id,
            &envelope.prompt,
            &envelope.model,
            envelope.model_preference_revision,
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
                } else if self.reconcile_model_preference_conflict(&error) {
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
            envelope.model_preference_revision,
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
                if !self.reconcile_model_preference_conflict(&error) {
                    self.notice = Notice::localized_with(
                        MessageId::SubmitFailedDraftKept,
                        [("error", error.to_string())],
                    );
                }
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
        self.remember_submitted_draft(&envelope.prompt);
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
        self.active_run_presentation = ActiveRunPresentation::from_execution(&execution);
        self.active_run_presentation
            .seed_request(&envelope.model, &envelope.reasoning_effort);
        self.execution_timer.start_new();
        self.execution_activity.clear();
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
        self.remember_submitted_draft(&prompt);
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

    fn open_plugins_overlay(&mut self) {
        self.slash_selected = 0;
        self.slash_selected_id = None;
        self.slash_dismissed_input = None;
        let workspace = self
            .active_session
            .as_ref()
            .map(|session| session.workspace_root.as_str());
        match self.rpc.extension_list(workspace) {
            Ok(inventory) => {
                self.overlays
                    .replace(Overlay::Plugins(PluginsOverlay {
                        inventory,
                        selected: 0,
                        error: String::new(),
                    }));
            }
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::LoadPluginsFailed,
                    [("error", error.to_string())],
                );
            }
        }
    }

    fn open_queue_overlay(&mut self) {
        let Some(run_id) = self.retained_run_id().map(str::to_owned) else {
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

    fn toggle_product_menu(&mut self) {
        match self.overlays.active() {
            Some(Overlay::ProductMenu(_)) => self.overlays.resolve_active(),
            None if self.phase == Phase::Conversation => {
                self.overlays
                    .replace(Overlay::ProductMenu(ProductMenuOverlay::default()));
            }
            Some(_) | None => {}
        }
    }

    fn open_plan_review(&mut self) {
        let review = (!self.has_retained_run())
            .then(|| self.active_session.as_ref().and_then(plan_review_overlay))
            .flatten();
        if let Some(review) = review {
            self.overlays.push(Overlay::PlanReview(review));
            self.notice.clear();
        } else {
            self.notice = Notice::localized(MessageId::PlanReviewUnavailable);
        }
    }

    fn conversation_switch_blocked(&mut self) -> bool {
        if self.has_retained_run() {
            self.notice = Notice::localized(MessageId::HistoryBusy);
            return true;
        }
        if self.overlays.active().is_some_and(Overlay::is_governance) {
            self.notice = Notice::localized(MessageId::HistoryBusy);
            return true;
        }
        if !self.composer.text().trim().is_empty() {
            self.notice = Notice::localized(MessageId::CommandRequiresIdleComposer);
            return true;
        }
        false
    }

    fn start_new_conversation(&mut self) {
        if self.conversation_switch_blocked() {
            return;
        }
        self.apply_action(Action::CreateSession);
    }

    fn fork_current_conversation(&mut self) {
        if self.conversation_switch_blocked() {
            return;
        }
        let Some(source_session) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.notice = Notice::localized(MessageId::ModeConversationRequired);
            return;
        };
        let target_id = "fork".to_owned();
        let generation = self.session_browser.begin_load(target_id.clone());
        let client_fork_id = operation_id("operator-fork");
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        self.notice = Notice::localized(MessageId::HistoryBranchCreating);
        std::thread::spawn(move || {
            let result = fork_latest_and_load(&socket, &source_session, &client_fork_id);
            let _ = tx.send(AsyncMessage::SessionLoaded {
                generation,
                target_id,
                result,
            });
        });
    }

    fn compact_current_checkpoint(&mut self) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.notice = Notice::localized(MessageId::ModeConversationRequired);
            return;
        };
        match self.rpc.compact_checkpoint(&session_id) {
            Ok(result) if result.compacted => {
                self.notice = Notice::localized(MessageId::Compacted);
            }
            Ok(result) => {
                let reason = if result.reason.trim().is_empty() {
                    "not compacted".to_owned()
                } else {
                    result.reason
                };
                self.notice =
                    Notice::localized_with(MessageId::CompactUnavailable, [("error", reason)]);
            }
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::CompactUnavailable,
                    [("error", error.to_string())],
                );
            }
        }
        self.request_context_summary();
        self.overlays.replace(Overlay::Context(
            self.context_summary.clone().unwrap_or_default(),
        ));
    }

    fn handle_approval_mode_command(&mut self, tail: &str) -> bool {
        let tail = tail.trim();
        if tail.is_empty() {
            let mode = self
                .security_context
                .as_ref()
                .map(|config| config.approval_mode.as_str())
                .filter(|value| !value.is_empty())
                .unwrap_or("unknown");
            let profile = self
                .security_context
                .as_ref()
                .map(|config| config.permission_profile.as_str())
                .or_else(|| {
                    self.active_session
                        .as_ref()
                        .map(|session| session.permission_profile.as_str())
                })
                .unwrap_or("");
            let preset = match (mode, profile) {
                ("ask", "read-only") => "read-only",
                ("ask", "safe-edit") | ("ask", "") => "agent",
                ("accept-edits", _) => "accept-edits",
                _ => "",
            };
            self.notice = Notice::localized_with(
                MessageId::ApprovalModePresets,
                [
                    ("mode", mode),
                    (
                        "preset",
                        if preset.is_empty() {
                            "none"
                        } else {
                            preset
                        },
                    ),
                    ("presets", "read-only, agent, accept-edits"),
                ],
            );
            return true;
        }
        if tail.split_whitespace().nth(1).is_some() {
            self.notice = Notice::localized(MessageId::CommandArgumentsNotAccepted);
            return false;
        }
        self.set_product_approval_mode(tail)
    }

    fn set_product_approval_mode(&mut self, mode: &str) -> bool {
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone());
        match self
            .rpc
            .set_interactive_approval(session_id.as_deref(), mode)
        {
            Ok(result) => {
                match self.security_context.as_mut() {
                    Some(config) => {
                        config.approval_mode = result.approval_mode.clone();
                        config.disable_always_approve = result.disable_always_approve;
                    }
                    None => {
                        self.security_context = Some(EffectiveConfig {
                            approval_mode: result.approval_mode.clone(),
                            disable_always_approve: result.disable_always_approve,
                            ..Default::default()
                        });
                    }
                }
                self.notice = Notice::localized(match result.preset.as_str() {
                    "read-only" => MessageId::ApprovalNowPresetReadOnly,
                    "agent" => MessageId::ApprovalNowPresetAgent,
                    "accept-edits" => MessageId::ApprovalNowAcceptEdits,
                    _ => match result.approval_mode.as_str() {
                        "always-approve" => MessageId::ApprovalNowAlwaysApprove,
                        "dont-ask" => MessageId::ApprovalNowDontAsk,
                        "accept-edits" => MessageId::ApprovalNowAcceptEdits,
                        _ => MessageId::ApprovalNowAsk,
                    },
                });
                true
            }
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::ApprovalModeUnavailable,
                    [("error", error.to_string())],
                );
                false
            }
        }
    }

    fn handle_btw_command(&mut self, tail: &str) -> bool {
        let tail = tail.trim();
        if tail.is_empty() {
            self.notice = Notice::localized(MessageId::BtwNeedsQuestion);
            return false;
        }
        if command::is_btw_fork_request(tail) {
            self.notice = Notice::localized(MessageId::BtwForkWithdrawn);
            return false;
        }
        let Some(run_id) = self.retained_run_id().map(str::to_owned) else {
            self.notice = Notice::localized(MessageId::CommandRequiresActiveExecution);
            return false;
        };
        match self.rpc.execution_btw(&run_id, tail) {
            Ok(result) => {
                self.notice.clear();
                self.overlays.replace(Overlay::SideQuery(SideQueryOverlay {
                    question: tail.to_owned(),
                    answer: result.answer,
                }));
                true
            }
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::BtwUnavailable,
                    [("error", error.to_string())],
                );
                false
            }
        }
    }

    fn handle_goal_command(&mut self, tail: &str) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.notice = Notice::localized(MessageId::ModeConversationRequired);
            return;
        };
        let verb = tail.split_whitespace().next().unwrap_or("");
        let result = match verb {
            "" => self.rpc.goal_get(&session_id).map(|got| got.goal),
            "clear" if tail.trim() == "clear" => self.rpc.goal_clear(&session_id).map(|_| None),
            "pause" if tail.trim() == "pause" => self.rpc.goal_pause(&session_id).map(Some),
            "resume" if tail.trim() == "resume" => self.rpc.goal_resume(&session_id).map(Some),
            "complete" if tail.trim() == "complete" => {
                self.rpc.goal_complete(&session_id).map(Some)
            }
            "continue" if tail.trim() == "continue" => match self.rpc.goal_continue(&session_id) {
                Ok(_) => {
                    self.notice = Notice::localized(MessageId::GoalContinued);
                    self.rpc.goal_get(&session_id).map(|got| got.goal)
                }
                Err(error) => Err(error),
            },
            _ => self.rpc.goal_set(&session_id, tail).map(Some),
        };
        match result {
            Ok(goal) => {
                if verb != "continue" {
                    self.notice = Notice::localized(match verb {
                        "" => MessageId::GoalTitle,
                        "clear" => MessageId::GoalCleared,
                        "pause" => MessageId::GoalPaused,
                        "resume" => MessageId::GoalResumed,
                        "complete" => MessageId::GoalCompleted,
                        _ => MessageId::GoalSet,
                    });
                }
                self.overlays
                    .replace(Overlay::Goal(crate::overlay::GoalOverlay { goal }));
            }
            Err(error) => {
                self.notice = Notice::localized_with(
                    MessageId::GoalUnavailable,
                    [("error", error.to_string())],
                );
            }
        }
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
                self.active_run_presentation = ActiveRunPresentation::default();
                self.execution_timer.reset();
                self.execution_activity.clear();
                self.execution_status = if execution.status.is_empty() {
                    "cancelled".into()
                } else {
                    execution.status
                };
                let execution_status = self.execution_status.clone();
                self.update_active_execution_metadata(
                    run_id,
                    &execution_status,
                    None,
                    None,
                    Some(&execution.result_kind),
                );
                self.notice = Notice::localized(MessageId::CancellationRequested);
                self.restore_submitted_draft_if_pristine();
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
                self.restore_submitted_draft_if_pristine();
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
        if self.has_retained_run() {
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
        let Some(action) = conversation_mode_action(self.has_retained_run(), current) else {
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
        let locale = self.ui_locale();
        let expected_run_id = match self.overlays.active() {
            Some(Overlay::PlanReview(review)) => Some(review.run_id.clone()),
            _ => None,
        };
        if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
            review.resolving = true;
            review.error.clear();
        }
        match self
            .rpc
            .approve_plan(&session_id, expected_run_id.as_deref())
        {
            Ok(result)
                if result.session_id == session_id
                    && result.approved
                    && !result.plan_mode
                    && result.task.as_ref().is_none_or(|execution| {
                        execution.session_id == session_id && !execution.run_id.is_empty()
                    }) =>
            {
                if let Some(mut session) = self.active_session.as_ref().cloned() {
                    session.plan_mode = result.plan_mode;
                    if let Some(execution) = result.task.as_ref() {
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
                if let Some(execution) = result.task {
                    self.active_run_id = Some(execution.run_id.clone());
                    self.active_run_presentation =
                        ActiveRunPresentation::from_execution(&execution);
                    self.execution_timer.start_new();
                    self.execution_activity.clear();
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
                    review.error = tr(locale, MessageId::PlanApprovalNotConfirmed).to_owned();
                } else {
                    self.notice = Notice::localized(MessageId::PlanApprovalNotConfirmed);
                }
            }
            Err(error) => {
                let message = error.to_string();
                let stale = matches!(
                    &error,
                    RpcError::Remote { message, .. }
                        if message.contains("plan run") || message.contains("latest plan")
                );
                if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
                    review.resolving = false;
                    review.error = if stale {
                        tr(locale, MessageId::PlanReviewStale).to_owned()
                    } else {
                        tr_format(
                            locale,
                            MessageId::PlanApprovalFailed,
                            &[("error", message.as_str())],
                        )
                    };
                } else if stale {
                    self.notice = Notice::localized(MessageId::PlanReviewStale);
                } else {
                    self.notice =
                        Notice::localized_with(MessageId::PlanApprovalFailed, [("error", message)]);
                }
            }
        }
    }

    fn revise_plan(&mut self) {
        let comments = match self.overlays.active() {
            Some(Overlay::PlanReview(review)) => review.revision_notes().to_vec(),
            _ => Vec::new(),
        };
        self.overlays.resolve_active();
        self.focus = Focus::Composer;
        let locale = self.ui_locale();
        let mut seed = String::from(tr(locale, MessageId::PlanRevisionSeed));
        if !comments.is_empty() {
            seed.push('\n');
            seed.push_str(tr(locale, MessageId::PlanRevisionCommentsHeader));
            for comment in comments {
                seed.push('\n');
                let start = comment.start_line.to_string();
                let end = comment.end_line.to_string();
                let line = if comment.start_line == comment.end_line {
                    tr_format(
                        locale,
                        MessageId::PlanRevisionCommentLine,
                        &[("line", start.as_str()), ("text", comment.text.as_str())],
                    )
                } else {
                    tr_format(
                        locale,
                        MessageId::PlanRevisionCommentRange,
                        &[
                            ("start", start.as_str()),
                            ("end", end.as_str()),
                            ("text", comment.text.as_str()),
                        ],
                    )
                };
                seed.push_str(&line);
            }
        }
        let separator = if self.composer.text().is_empty() {
            ""
        } else {
            "\n\n"
        };
        let suffix = format!("{separator}{seed}");
        self.composer
            .insert_str_at(self.composer.text().len(), &suffix);
        self.composer.set_cursor(self.composer.text().len());
        self.composer_state = TextAreaState::default();
        self.media.reconcile(&self.composer);
        self.sync_context_completion();
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
        let mut plan_comment_saved = None;
        let fullscreen_changes = self.options.screen_mode == Some(ScreenMode::Fullscreen);
        let locale = self.ui_locale();
        match self.overlays.active_mut() {
            Some(Overlay::ProductMenu(menu)) => match key.code {
                KeyCode::Up | KeyCode::BackTab => menu.selected = menu.selected.saturating_sub(1),
                KeyCode::Down | KeyCode::Tab => {
                    menu.selected =
                        (menu.selected + 1).min(PRODUCT_MENU_ITEMS.len().saturating_sub(1));
                }
                KeyCode::Home => menu.selected = 0,
                KeyCode::End => menu.selected = PRODUCT_MENU_ITEMS.len().saturating_sub(1),
                KeyCode::Enter => deferred = product_menu_action(menu.selected),
                KeyCode::Esc => deferred = Some(Action::CloseOverlay),
                _ => {}
            },
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
                if review.commenting {
                    match key.code {
                        KeyCode::Char(character)
                            if !key.modifiers.intersects(
                                KeyModifiers::CONTROL | KeyModifiers::ALT | KeyModifiers::SUPER,
                            ) =>
                        {
                            review.push_comment_char(character);
                        }
                        KeyCode::Backspace if key.modifiers.is_empty() => {
                            review.backspace_comment();
                        }
                        KeyCode::Enter if key.modifiers.is_empty() => {
                            if review.commit_comment() {
                                review.error.clear();
                                plan_comment_saved = Some(review.comment_count());
                            } else {
                                review.error = tr(locale, MessageId::PlanCommentEmpty).to_owned();
                            }
                        }
                        KeyCode::Esc if key.modifiers.is_empty() => review.cancel_comment(),
                        KeyCode::Up => review.scroll_up(1),
                        KeyCode::Down => {
                            review.scroll_down(1, PLAN_REVIEW_PAGE_LINES);
                        }
                        KeyCode::PageUp => review.scroll_up(PLAN_REVIEW_PAGE_LINES),
                        KeyCode::PageDown => {
                            review.scroll_down(PLAN_REVIEW_PAGE_LINES, PLAN_REVIEW_PAGE_LINES)
                        }
                        KeyCode::Home => review.scroll_home(),
                        KeyCode::End => review.scroll_end(PLAN_REVIEW_PAGE_LINES),
                        _ => {}
                    }
                } else if key
                    .modifiers
                    .intersects(KeyModifiers::CONTROL | KeyModifiers::ALT | KeyModifiers::SUPER)
                {
                    // Modified printable keys remain available to global shortcuts.
                } else {
                    match key.code {
                        KeyCode::Up | KeyCode::Char('k' | 'K') => {
                            review.move_up(PLAN_REVIEW_PAGE_LINES)
                        }
                        KeyCode::Down | KeyCode::Char('j' | 'J') => {
                            review.move_down(PLAN_REVIEW_PAGE_LINES)
                        }
                        KeyCode::PageUp => review.page_up(PLAN_REVIEW_PAGE_LINES),
                        KeyCode::PageDown => review.page_down(PLAN_REVIEW_PAGE_LINES),
                        KeyCode::Home => review.home(PLAN_REVIEW_PAGE_LINES),
                        KeyCode::End => review.end(PLAN_REVIEW_PAGE_LINES),
                        KeyCode::Char('m' | 'M') => {
                            review.toggle_mark();
                        }
                        _ => deferred = plan_review_key_action(key),
                    }
                }
            }
            Some(Overlay::Settings(settings)) => match settings.page {
                SettingsPage::Root => match key.code {
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
                SettingsPage::Symbols => match key.code {
                    KeyCode::Up | KeyCode::Left | KeyCode::BackTab => {
                        let index = settings.symbol_selected.saturating_sub(1);
                        deferred =
                            Some(Action::PreviewGlyphPreference(GlyphPreference::ALL[index]));
                    }
                    KeyCode::Down | KeyCode::Right | KeyCode::Tab => {
                        let index = (settings.symbol_selected + 1)
                            .min(GlyphPreference::ALL.len().saturating_sub(1));
                        deferred =
                            Some(Action::PreviewGlyphPreference(GlyphPreference::ALL[index]));
                    }
                    KeyCode::Char(value @ '1'..='4') => {
                        let index = value.to_digit(10).unwrap_or(1) as usize - 1;
                        deferred =
                            Some(Action::PreviewGlyphPreference(GlyphPreference::ALL[index]));
                    }
                    KeyCode::Enter => deferred = Some(Action::ApplyGlyphPreference),
                    KeyCode::Esc => deferred = Some(Action::CancelGlyphPreview),
                    _ => {}
                },
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
            Some(Overlay::Goal(_)) | Some(Overlay::SideQuery(_)) => match key.code {
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
                    let count = agent_roster_entries(
                        &agents.projection,
                        &agents.load.session_id,
                    )
                    .len();
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
            Some(Overlay::Changes(changes)) => {
                if changes.confirm_rollback {
                    match key.code {
                        KeyCode::Esc => deferred = Some(Action::CancelPatchRollback),
                        KeyCode::Enter | KeyCode::Char('y') => {
                            deferred = Some(Action::ConfirmPatchRollback)
                        }
                        _ => {}
                    }
                } else if fullscreen_changes && !changes.projection.patches.is_empty() {
                    match key.code {
                        KeyCode::Tab | KeyCode::Right | KeyCode::Char('l') => {
                            changes.focus = ChangesFocus::Files
                        }
                        KeyCode::BackTab | KeyCode::Left | KeyCode::Char('h') => {
                            changes.focus = ChangesFocus::Transactions
                        }
                        KeyCode::Up | KeyCode::Char('k') => {
                            if changes.focus == ChangesFocus::Files {
                                changes.selected_file = changes.selected_file.saturating_sub(1);
                            } else {
                                changes.selected = changes.selected.saturating_sub(1);
                                changes.selected_file = 0;
                            }
                            changes.scroll = 0;
                        }
                        KeyCode::Down | KeyCode::Char('j') => {
                            if changes.focus == ChangesFocus::Files {
                                let count = changes
                                    .patch_reviews
                                    .get(changes.selected)
                                    .map_or(0, |review| review.files.len());
                                changes.selected_file =
                                    (changes.selected_file + 1).min(count.saturating_sub(1));
                            } else {
                                changes.selected = (changes.selected + 1)
                                    .min(changes.projection.patches.len().saturating_sub(1));
                                changes.selected_file = 0;
                            }
                            changes.scroll = 0;
                        }
                        KeyCode::Enter => changes.focus = ChangesFocus::Files,
                        KeyCode::PageDown => changes.scroll = changes.scroll.saturating_add(12),
                        KeyCode::PageUp => changes.scroll = changes.scroll.saturating_sub(12),
                        KeyCode::Home => changes.scroll = 0,
                        KeyCode::Char('b') => deferred = Some(Action::BeginPatchRollback),
                        KeyCode::Char('r') => deferred = Some(Action::RefreshChanges),
                        KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::OpenStatus),
                        _ => {}
                    }
                } else {
                    match key.code {
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
                        KeyCode::Enter if !changes.projection.patches.is_empty() => {
                            deferred = Some(Action::BeginPatchRollback)
                        }
                        KeyCode::Char('r') => deferred = Some(Action::RefreshChanges),
                        KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::OpenStatus),
                        _ => {}
                    }
                }
            }
            Some(Overlay::FileViewer(viewer)) => {
                let visible_rows = self
                    .transcript_geometry
                    .viewport
                    .height
                    .saturating_sub(8)
                    .max(1) as usize;
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
            Some(Overlay::ToolOutput(output)) => match key.code {
                KeyCode::Up | KeyCode::Char('k') => output.scroll = output.scroll.saturating_sub(1),
                KeyCode::Down | KeyCode::Char('j') => {
                    output.scroll = output
                        .scroll
                        .saturating_add(1)
                        .min(self.tool_output_max_scroll)
                }
                KeyCode::PageUp => output.scroll = output.scroll.saturating_sub(12),
                KeyCode::PageDown => {
                    output.scroll = output
                        .scroll
                        .saturating_add(12)
                        .min(self.tool_output_max_scroll)
                }
                KeyCode::Home => output.scroll = 0,
                KeyCode::End => output.scroll = self.tool_output_max_scroll,
                KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::CloseOverlay),
                _ => {}
            },
            Some(Overlay::Plugins(plugins)) => match key.code {
                KeyCode::Up | KeyCode::Char('k') => {
                    plugins.selected = plugins.selected.saturating_sub(1);
                }
                KeyCode::Down | KeyCode::Char('j') => {
                    if !plugins.inventory.plugins.is_empty() {
                        plugins.selected = (plugins.selected + 1)
                            .min(plugins.inventory.plugins.len().saturating_sub(1));
                    }
                }
                KeyCode::Esc | KeyCode::Char('q') => deferred = Some(Action::CloseOverlay),
                _ => {}
            },
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
        if let Some(count) = plan_comment_saved {
            self.notice =
                Notice::localized_with(MessageId::PlanCommentSaved, [("count", count.to_string())]);
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
                self.open_settings();
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
        self.active_run_presentation = ActiveRunPresentation::from_execution(&outcome.execution);
        self.execution_timer.start_new();
        self.execution_activity.clear();
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
            self.tool_artifact_refs = tool_artifact_refs_from_items(&items);
            self.tool_artifact_loads.clear();
            self.blocks = self.transcript_reducer.hydrate(items);
            self.scrollback.reset();
            self.transcript_stale = false;
            self.reset_transcript_viewport();
        } else {
            self.execution_lifecycle.clear();
            self.tool_disclosure_overrides.clear();
            self.tool_artifact_refs.clear();
            self.tool_artifact_loads.clear();
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
        if matches!(
            self.overlays.active(),
            Some(Overlay::Settings(SettingsOverlay {
                page: SettingsPage::Symbols,
                ..
            }))
        ) {
            self.cancel_glyph_preview();
        }
        let Some(overlay) = self.overlays.active() else {
            return false;
        };
        if overlay.is_governance() {
            return false;
        }
        self.overlays.resolve_active();
        true
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
        if self.has_retained_run() {
            self.cancel_history_selection();
            self.notice = Notice::localized(MessageId::HistoryBusy);
            return;
        }
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
        let HistoryBranchOutcome {
            session,
            items,
            active_run,
            prompt_history,
            prompt_history_unavailable,
        } = outcome;
        self.execution_lifecycle.clear();
        self.tool_disclosure_overrides.clear();
        self.tool_artifact_refs = tool_artifact_refs_from_items(&items);
        self.tool_artifact_loads.clear();
        self.blocks = self.transcript_reducer.hydrate(items);
        self.scrollback.reset();
        self.persisted_prompt_history = prompt_history;
        self.persisted_prompt_history_unavailable = prompt_history_unavailable;
        self.transcript_stale = false;
        self.reset_transcript_viewport();
        self.active_run_id = execution_status_is_interactive(&session.execution_status)
            .then(|| session.latest_run_id.clone())
            .filter(|run_id| !run_id.is_empty());
        self.active_run_presentation = active_run
            .as_ref()
            .filter(|run| {
                execution_status_retains_run_truth(&session.execution_status)
                    && run.run_id == session.latest_run_id
            })
            .map(ActiveRunPresentation::from_execution)
            .unwrap_or_default();
        self.execution_timer.reset();
        self.execution_activity.clear();
        self.execution_status = if session.execution_status.is_empty() {
            "ready".into()
        } else {
            session.execution_status.clone()
        };
        self.seed_execution_lifecycle(&session.latest_run_id, &session.execution_status);
        let retained_run = self.has_retained_run();
        self.remember_session(session);
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
        if conversation_ready && !retained_run {
            self.notice = Notice::localized(MessageId::HistoryBranched);
        }
    }

    fn capture_reading_envelope(&self) -> Option<reading_state::ReadingStateEnvelopeV1> {
        let session_id = self.active_session.as_ref()?.session_id.clone();
        Some(reading_state::capture_reading_state(
            &session_id,
            &self.blocks,
            self.history_selected,
            &self.tool_disclosure_overrides,
            self.transcript_follow_bottom,
            self.transcript_anchor
                .as_ref()
                .map(TranscriptScrollAnchor::to_logical),
            self.scrollback.committed_snapshot(),
        ))
    }

    fn apply_reading_envelope(
        &mut self,
        envelope: &reading_state::ReadingStateEnvelopeV1,
        session_id: &str,
    ) -> Result<(), reading_state::ReadingStateError> {
        let restored = reading_state::restore_reading_state(
            envelope,
            session_id,
            &self.blocks,
            &mut self.scrollback,
        )?;
        self.tool_disclosure_overrides = restored.disclosure_overrides.into_iter().collect();
        self.history_selected = restored.selected_index;
        self.transcript_follow_bottom = restored.follow_bottom;
        self.transcript_anchor = restored.top_visible.map(|anchor| {
            let index = self
                .blocks
                .iter()
                .position(|block| block.id == anchor.block_id)
                .unwrap_or(0);
            TranscriptScrollAnchor::from_logical(anchor, index)
        });
        if restored.follow_bottom {
            self.transcript_scroll = 0;
        }
        Ok(())
    }

    fn reset_transcript_viewport(&mut self) {
        self.failure_action_focus = None;
        self.transcript_height_cache.clear();
        self.transcript_render_cache.clear();
        self.transcript_geometry = TranscriptGeometry::default();
        self.transcript_scrollbar = TranscriptScrollbar::default();
        self.transcript_scrollbar_interaction.release();
        self.transcript_scroll = 0;
        self.transcript_max_scroll = 0;
        self.transcript_follow_bottom = true;
        self.transcript_anchor = None;
    }

    fn follow_transcript_bottom(&mut self) {
        self.transcript_follow_bottom = true;
        self.transcript_scroll = self.transcript_max_scroll;
        self.transcript_anchor = None;
    }

    fn transcript_page_size(&self) -> usize {
        self.transcript_geometry.content.height.max(1) as usize
    }

    fn scroll_transcript(&mut self, delta: isize) {
        self.transcript_anchor = None;
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

    fn set_transcript_scroll(&mut self, offset: usize) {
        self.transcript_anchor = None;
        self.transcript_scroll = offset.min(self.transcript_max_scroll);
        self.transcript_follow_bottom = self.transcript_scroll >= self.transcript_max_scroll;
    }

    fn handle_mouse(&mut self, mouse: MouseEvent) {
        let position = Position::new(mouse.column, mouse.row);
        if self.overlays.active().is_some() {
            self.transcript_scrollbar_interaction.release();
        }
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
                self.composer_pointer_captured = false;
                if self.overlays.active().is_none() && self.phase == Phase::Conversation {
                    let (handled, next_scroll) = self
                        .transcript_scrollbar
                        .pointer_down(position, &mut self.transcript_scrollbar_interaction);
                    if handled {
                        self.media.set_hovered(None);
                        if let Some(next_scroll) = next_scroll {
                            self.set_transcript_scroll(next_scroll);
                        }
                        self.dirty = true;
                        return;
                    }
                }
                self.transcript_scrollbar_interaction.release();
                let action = self.interactions.action_at(position);
                if action == Some(Action::FocusComposer)
                    && self.overlays.active().is_none()
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
                    self.composer_pointer_captured = true;
                } else if let Some(action) = action {
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
                    self.composer_pointer_captured = true;
                } else {
                    self.media.set_hovered(None);
                }
                self.dirty = true;
            }
            MouseEventKind::Drag(MouseButton::Left)
                if self.overlays.active().is_none()
                    && self.phase == Phase::Conversation
                    && self.transcript_scrollbar_interaction.is_dragging() =>
            {
                if let Some(next_scroll) = self
                    .transcript_scrollbar
                    .pointer_drag(position.y, self.transcript_scrollbar_interaction)
                {
                    self.set_transcript_scroll(next_scroll);
                    self.dirty = true;
                }
            }
            MouseEventKind::Drag(MouseButton::Left)
                if self.overlays.active().is_none()
                    && self.phase == Phase::Conversation
                    && self.composer_pointer_captured =>
            {
                let _ = self
                    .composer
                    .handle_mouse(mouse, self.composer_area, self.composer_state);
                self.dirty = true;
            }
            MouseEventKind::Up(MouseButton::Left) => {
                if self.transcript_scrollbar_interaction.release() {
                    self.composer_pointer_captured = false;
                    self.dirty = true;
                } else if self.overlays.active().is_none()
                    && self.phase == Phase::Conversation
                    && self.composer_pointer_captured
                {
                    let _ =
                        self.composer
                            .handle_mouse(mouse, self.composer_area, self.composer_state);
                    self.composer_pointer_captured = false;
                    self.dirty = true;
                }
            }
            MouseEventKind::ScrollUp
                if matches!(self.overlays.active(), Some(Overlay::PlanReview(_))) =>
            {
                if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
                    if review.commenting {
                        review.scroll_up(3);
                    } else {
                        review.page_up(3);
                    }
                }
                self.dirty = true;
            }
            MouseEventKind::ScrollDown
                if matches!(self.overlays.active(), Some(Overlay::PlanReview(_))) =>
            {
                if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
                    if review.commenting {
                        review.scroll_down(3, PLAN_REVIEW_PAGE_LINES);
                    } else {
                        review.page_down(3);
                    }
                }
                self.dirty = true;
            }
            MouseEventKind::ScrollUp
                if matches!(self.overlays.active(), Some(Overlay::ToolOutput(_))) =>
            {
                if let Some(Overlay::ToolOutput(output)) = self.overlays.active_mut() {
                    output.scroll = output.scroll.saturating_sub(3);
                }
                self.dirty = true;
            }
            MouseEventKind::ScrollDown
                if matches!(self.overlays.active(), Some(Overlay::ToolOutput(_))) =>
            {
                if let Some(Overlay::ToolOutput(output)) = self.overlays.active_mut() {
                    output.scroll = output
                        .scroll
                        .saturating_add(3)
                        .min(self.tool_output_max_scroll);
                }
                self.dirty = true;
            }
            MouseEventKind::ScrollUp
                if self.overlays.active().is_none()
                    && self.transcript_geometry.content.contains(position) =>
            {
                self.scroll_transcript(-3);
                self.dirty = true;
            }
            MouseEventKind::ScrollDown
                if self.overlays.active().is_none()
                    && self.transcript_geometry.content.contains(position) =>
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
                    patch_reviews: Vec::new(),
                    selected: 0,
                    selected_file: 0,
                    focus: ChangesFocus::Transactions,
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
            .map(|projection| {
                let patch_reviews = project_patch_reviews(&projection.patches);
                Box::new(ChangesLoadOutcome {
                    projection,
                    patch_reviews,
                })
            });
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
            Some(Overlay::Agents(agents)) => agent_roster_entries(
                &agents.projection,
                &agents.load.session_id,
            )
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
            Action::CreateSession => {
                if self.phase == Phase::Conversation {
                    self.close_top_non_governance();
                    self.session_browser
                        .open(&self.sessions, &self.options.workspace);
                    self.phase = Phase::Session;
                    self.focus = Focus::Scene;
                }
                self.create_session_from_browser();
            }
            Action::OpenConversationImport => self.begin_conversation_import(),
            Action::SelectConversationImport(index) => self
                .session_browser
                .conversation_import_mut()
                .toggle_candidate(Some(index)),
            Action::ToggleConversationImportAll => {
                self.session_browser.conversation_import_mut().toggle_all()
            }
            Action::CycleConversationImportSource => {
                let generation = self
                    .session_browser
                    .conversation_import_mut()
                    .cycle_source();
                self.request_conversation_import_discovery(generation);
            }
            Action::ToggleConversationImportScope => {
                let generation = self
                    .session_browser
                    .conversation_import_mut()
                    .toggle_workspace_scope();
                self.request_conversation_import_discovery(generation);
            }
            Action::ReviewConversationImport => {
                if !self
                    .session_browser
                    .conversation_import_mut()
                    .begin_confirmation()
                {
                    self.notice = Notice::localized(MessageId::ConversationImportSelectRequired);
                }
            }
            Action::ConfirmConversationImport => self.confirm_conversation_import(),
            Action::CancelConversationImport => {
                let import = self.session_browser.conversation_import_mut();
                if import.stage() == ConversationImportStage::Confirming {
                    import.cancel_confirmation();
                } else {
                    import.close();
                }
            }
            Action::RetryConversationImport => {
                let generation = self
                    .session_browser
                    .conversation_import_mut()
                    .begin_discovery();
                self.request_conversation_import_discovery(generation);
            }
            Action::SelectConversationImportResult(index) => self
                .session_browser
                .conversation_import_mut()
                .select_result(index),
            Action::OpenConversationImportResult => self.open_conversation_import_result(),
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
            Action::OpenToolOutput(block_id) => {
                if let Some(block) = self.blocks.iter().find(|block| {
                    block.id == block_id
                        && block.kind == crate::transcript::BlockKind::Tool
                        && (!block.body.is_empty()
                            || block
                                .tool_members
                                .iter()
                                .any(|member| !member.body.is_empty())
                            || tool_component_call_ids(block)
                                .into_iter()
                                .any(|call_id| self.tool_artifact_refs.contains_key(call_id)))
                }) {
                    let references = tool_component_call_ids(block)
                        .into_iter()
                        .filter(|call_id| {
                            !self
                                .tool_artifact_loads
                                .get(*call_id)
                                .is_some_and(|load| load.loading)
                        })
                        .filter_map(|call_id| self.tool_artifact_refs.get(call_id).cloned())
                        .collect::<Vec<_>>();
                    self.overlays
                        .replace(Overlay::ToolOutput(ToolOutputOverlay {
                            block_id,
                            scroll: 0,
                        }));
                    for reference in references {
                        self.request_tool_artifact(reference);
                    }
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
            Action::RetryExecution { run_id, routing } => {
                self.retry_failed_execution(&run_id, routing)
            }
            Action::CopyFailureId(id) => self.copy_failure_id(&id),
            Action::ToggleProductMenu => self.toggle_product_menu(),
            Action::OpenSessions => {
                self.close_top_non_governance();
                self.open_session_browser();
            }
            Action::OpenModels => {
                self.close_top_non_governance();
                self.open_models();
            }
            Action::OpenSettings => self.open_settings(),
            Action::OpenGlyphPreview => self.open_glyph_preview(),
            Action::PreviewGlyphPreference(preference) => self.preview_glyph_preference(preference),
            Action::ApplyGlyphPreference => self.commit_glyph_preference(),
            Action::CancelGlyphPreview => self.cancel_glyph_preview(),
            Action::ToggleDensity => self.toggle_density(),
            Action::OpenQueue => self.open_queue_overlay(),
            Action::OpenPlugins => self.open_plugins_overlay(),
            Action::OpenStatus => {
                if matches!(self.overlays.active(), Some(Overlay::ProductMenu(_))) {
                    self.close_top_non_governance();
                }
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
            Action::OpenHelp => self.open_help_overlay(),
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
                        .map(|patch| {
                            (
                                patch.session_id.clone(),
                                patch.patch_id.clone(),
                                patch.transaction_id.clone(),
                            )
                        }),
                    _ => None,
                };
                if let Some((session_id, patch_id, transaction_id)) = target {
                    let locale = self.ui_locale();
                    let preview = self
                        .rpc
                        .preview_workspace_patch_rollback(&session_id, &patch_id);
                    if let Some(Overlay::Changes(changes)) = self.overlays.active_mut() {
                        match preview {
                            Ok(preview)
                                if rollback_preview_matches(
                                    &preview,
                                    &patch_id,
                                    &transaction_id,
                                ) =>
                            {
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
                    Some(Overlay::Changes(changes)) if changes.confirm_rollback => {
                        rollback_confirmation_target(changes)
                    }
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
                    Some(Overlay::Agents(agents)) => agent_roster_entries(
                        &agents.projection,
                        &agents.load.session_id,
                    )
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
                    && agent_roster_entries(&agents.projection, &agents.load.session_id)
                        .get(agents.selected)
                        .is_some_and(|agent| agent.category != "completed")
                {
                    agents.confirm_stop = true;
                }
            }
            Action::ConfirmStopAgent => {
                let task_id = match self.overlays.active() {
                    Some(Overlay::Agents(agents)) if agents.confirm_stop => {
                        agent_roster_entries(&agents.projection, &agents.load.session_id)
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
                if let Some(Overlay::Changes(changes)) = self.overlays.active_mut()
                    && !changes.confirm_rollback
                {
                    changes.selected = index;
                    changes.selected_file = 0;
                    changes.focus = ChangesFocus::Transactions;
                    changes.scroll = 0;
                }
            }
            Action::SelectPatchReviewFile(index) => {
                if let Some(Overlay::Changes(changes)) = self.overlays.active_mut()
                    && !changes.confirm_rollback
                {
                    changes.selected_file = index;
                    changes.focus = ChangesFocus::Files;
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
                    let visible_rows = self
                        .transcript_geometry
                        .viewport
                        .height
                        .saturating_sub(8)
                        .max(1) as usize;
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
                self.focus = Focus::Scene;
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
            Action::BeginPlanComment => {
                if let Some(Overlay::PlanReview(review)) = self.overlays.active_mut() {
                    review.begin_comment();
                    review.error.clear();
                }
            }
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

    fn sync_selection_from_session(&mut self, session: &Session) {
        if let Some(index) = self
            .models
            .iter()
            .position(|model| model.id == session.next_model)
        {
            self.model_index = index;
            self.selected_model = session.next_model.clone();
        }
        self.selected_reasoning_effort =
            self.resolve_reasoning_effort_for_selection(&session.next_reasoning_effort);
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
        let selected_provider = self.inventory.providers.get(self.provider_index).cloned();
        let provider_id = selected_provider
            .as_ref()
            .map(|provider| provider.id.clone());
        let grok_build_provider = selected_provider
            .as_ref()
            .is_some_and(|provider| provider.source_kind == "grok-build");

        let inventory = match if grok_build_provider {
            self.rpc.model_inventory_refresh()
        } else {
            self.rpc.model_inventory()
        } {
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
        self.inventory = inventory;
        if let Some(provider_id) = provider_id.as_deref() {
            let refreshed_provider_index = self
                .inventory
                .providers
                .iter()
                .position(|provider| provider.id == provider_id);
            if grok_build_provider && refreshed_provider_index.is_none() {
                self.provider_index = self
                    .provider_index
                    .min(self.inventory.providers.len().saturating_sub(1));
                self.phase = Phase::Provider;
                self.focus = Focus::Scene;
                self.notice = Notice::localized(MessageId::ProviderSelectionExpired);
                return;
            }
            self.provider_index = refreshed_provider_index.unwrap_or(self.provider_index);
        }
        if grok_build_provider {
            let Some(provider) = self.inventory.providers.get(self.provider_index) else {
                self.phase = Phase::Provider;
                self.focus = Focus::Scene;
                self.notice = Notice::localized(MessageId::ProviderSelectionExpired);
                return;
            };
            if !self.inventory.is_provider_runnable(provider) {
                self.phase = Phase::Provider;
                self.focus = Focus::Scene;
                self.notice = if !provider.available {
                    Notice::localized(grok_build_repair_message(provider.source_action.as_str()))
                } else {
                    Notice::localized_with(
                        MessageId::ProviderExecutionNotReady,
                        [("provider", provider.name.clone())],
                    )
                };
                return;
            }
        }
        if !self.inventory.has_runnable_provider() {
            self.phase = Phase::Model;
            self.focus = Focus::Scene;
            self.notice = Notice::localized(MessageId::ExecutionReadinessChanged);
            return;
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
            self.phase = if grok_build_provider {
                Phase::Provider
            } else {
                Phase::Model
            };
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
            let expected_revision = self
                .active_session
                .as_ref()
                .map(|session| session.model_preference_revision)
                .unwrap_or_default();
            self.rpc
                .set_session_model(
                    &session_id,
                    &model_id,
                    &self.selected_reasoning_effort,
                    expected_revision,
                )
                .map_err(anyhow::Error::new)
                .map(|selection| {
                    self.selected_model = selection.next_model.clone();
                    self.selected_reasoning_effort = selection.next_reasoning_effort.clone();
                    if let Some(session) = self.active_session.as_mut() {
                        session.next_model = selection.next_model;
                        session.next_reasoning_effort = selection.next_reasoning_effort;
                        session.model_preference_revision = selection.model_preference_revision;
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

fn agent_roster_entries<'a>(
    projection: &'a ProductProjection,
    parent_session: &str,
) -> Vec<&'a crate::rpc::AgentViewEntry> {
    projection.agents.roster_entries(parent_session)
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
    .then(|| {
        PlanReviewOverlay::new(
            session.latest_run_id.clone(),
            session.summary.trim().to_owned(),
        )
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
        SETTINGS_SYMBOLS_INDEX => Some(Action::OpenGlyphPreview),
        6 => Some(Action::OpenStatus),
        7 => Some(Action::OpenSessions),
        8 => Some(Action::ResumePausedExecutionRun),
        9 => Some(Action::CloseOverlay),
        _ => None,
    }
}

fn product_menu_action(index: usize) -> Option<Action> {
    PRODUCT_MENU_ITEMS
        .get(index)
        .map(|item| item.action.clone())
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

fn plan_review_key_action(key: KeyEvent) -> Option<Action> {
    let action_modifiers = key.modifiers.is_empty()
        || (matches!(key.code, KeyCode::Char(_)) && key.modifiers == KeyModifiers::SHIFT);
    if !action_modifiers {
        return None;
    }
    match key.code {
        KeyCode::Enter | KeyCode::Char('a' | 'A') => Some(Action::ApprovePlan),
        KeyCode::Char('s' | 'S' | 'r' | 'R') | KeyCode::Esc => Some(Action::RevisePlan),
        KeyCode::Char('c' | 'C') => Some(Action::BeginPlanComment),
        KeyCode::Char('q' | 'Q') => Some(Action::CancelPlan),
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
    let active_run = load_active_run(&mut rpc, &session);
    let (prompt_history, prompt_history_unavailable) =
        match rpc.prompt_history(&session.session_id, 200) {
            Ok(history) => (history.entries, false),
            Err(_) => (Vec::new(), true),
        };
    Ok(HistoryBranchOutcome {
        session,
        items,
        active_run,
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
    let active_run = load_active_run(&mut rpc, &session);
    let (prompt_history, prompt_history_unavailable) = match rpc.prompt_history(session_id, 200) {
        Ok(history) => (history.entries, false),
        Err(_) => (Vec::new(), true),
    };
    let (items, watermark, catch_up, live, boundary) =
        attach_reconnect_projection(&mut rpc, socket, session_id, &runtime)?;
    Ok(RuntimeReconnectOutcome {
        rpc,
        runtime,
        inventory,
        sessions,
        session,
        items,
        active_run,
        prompt_history,
        prompt_history_unavailable,
        security_context,
        watermark,
        catch_up,
        live,
        boundary,
    })
}

type ReconnectProjectionAttach = (
    Vec<SessionItemEvent>,
    usize,
    Vec<ReceivedEvent>,
    Option<std::sync::mpsc::Receiver<Result<ReceivedEvent, RpcError>>>,
    Option<ReplayBoundaryV1>,
);

fn attach_reconnect_projection(
    rpc: &mut Client,
    socket: &Path,
    session_id: &str,
    runtime: &RuntimeInitialize,
) -> Result<ReconnectProjectionAttach, String> {
    if runtime.capabilities.event_replay_tail_v1()
        && runtime.capabilities.session_items_watermark.version == 1
    {
        let snapshot = rpc
            .items_watermarked(session_id, runtime)
            .map_err(|error| error.to_string())?;
        let watermark = snapshot.durable_cursor;
        let attached = attach_replay_tail_v1(
            socket,
            &ReplayTailAttachRequest {
                session_id: session_id.to_owned(),
                since: watermark,
                runtime_id: runtime.runtime.runtime_id.clone(),
                runtime_epoch: runtime.runtime.epoch.clone(),
                runtime_process_epoch: runtime.runtime.process_epoch,
            },
        )
        .map_err(|error| error.to_string())?;
        validate_reconnect_boundary(session_id, &runtime.runtime, watermark, &attached.boundary)?;
        if !reconnect_stream_is_strictly_after_watermark(watermark, &attached.catch_up) {
            return Err("stream catch-up overlapped the items watermark".into());
        }
        return Ok((
            snapshot.items,
            watermark,
            attached.catch_up,
            Some(attached.live),
            Some(attached.boundary),
        ));
    }
    let items = rpc.items(session_id).map_err(|error| error.to_string())?;
    Ok((items, 0, Vec::new(), None, None))
}

fn validate_reconnect_boundary(
    session_id: &str,
    runtime: &crate::rpc::RuntimeIdentity,
    watermark: usize,
    boundary: &ReplayBoundaryV1,
) -> Result<(), String> {
    if boundary.session_id != session_id {
        return Err("replay_boundary session_id does not match".into());
    }
    if !runtime.runtime_id.is_empty() && boundary.runtime_id != runtime.runtime_id {
        return Err("replay_boundary runtime_id does not match".into());
    }
    if !runtime.epoch.is_empty() && boundary.runtime_epoch != runtime.epoch {
        return Err("replay_boundary runtime_epoch does not match".into());
    }
    let watermark = i64::try_from(watermark).unwrap_or(-1);
    if boundary.requested_since != watermark {
        return Err("requested_since does not match items watermark".into());
    }
    if boundary.durable_cursor < watermark {
        return Err("durable_cursor is below items watermark".into());
    }
    Ok(())
}

fn hydrate_reconnect_blocks(
    items: Vec<SessionItemEvent>,
    catch_up: Vec<ReceivedEvent>,
) -> (TranscriptReducer, Vec<TranscriptBlock>, usize) {
    let mut reducer = TranscriptReducer::default();
    let mut blocks = reducer.hydrate(items);
    let mut cursor = 0usize;
    for received in catch_up {
        if let Some(raw) = received.durable_raw_cursor() {
            cursor = cursor.max(raw);
        }
        reducer.reduce_event(&mut blocks, received.event);
    }
    (reducer, blocks, cursor)
}

fn reconnect_event_cursor(
    previous: usize,
    watermark: usize,
    catch_up_cursor: usize,
    has_v1_boundary: bool,
) -> usize {
    if has_v1_boundary {
        watermark.max(catch_up_cursor)
    } else {
        previous.max(watermark).max(catch_up_cursor)
    }
}

fn reconnect_stream_is_strictly_after_watermark(
    watermark: usize,
    catch_up: &[ReceivedEvent],
) -> bool {
    catch_up
        .iter()
        .all(|received| match received.durable_raw_cursor() {
            Some(cursor) => cursor > watermark,
            None => true,
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
            let speaker = if !block.title.is_empty() && block.title != "Carina" {
                block.title.as_str()
            } else if block.kind == crate::transcript::BlockKind::User {
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

fn fork_latest_and_load(
    socket: &Path,
    source_session_id: &str,
    client_fork_id: &str,
) -> Result<HistoryBranchOutcome, String> {
    let mut rpc = Client::connect(socket).map_err(|error| error.to_string())?;
    let session = rpc
        .fork_session_latest(source_session_id, client_fork_id)
        .map_err(|error| error.to_string())?;
    let items = rpc
        .items(&session.session_id)
        .map_err(|error| error.to_string())?;
    let active_run = load_active_run(&mut rpc, &session);
    let (prompt_history, prompt_history_unavailable) =
        match rpc.prompt_history(&session.session_id, 200) {
            Ok(history) => (history.entries, false),
            Err(_) => (Vec::new(), true),
        };
    Ok(HistoryBranchOutcome {
        session,
        items,
        active_run,
        prompt_history,
        prompt_history_unavailable,
    })
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
    let active_run = load_active_run(&mut rpc, &session);
    let (prompt_history, prompt_history_unavailable) =
        match rpc.prompt_history(&session.session_id, 200) {
            Ok(history) => (history.entries, false),
            Err(_) => (Vec::new(), true),
        };
    Ok(HistoryBranchOutcome {
        session,
        items,
        active_run,
        prompt_history,
        prompt_history_unavailable,
    })
}

fn load_active_run(rpc: &mut Client, session: &Session) -> Option<ExecutionRun> {
    execution_status_retains_run_truth(&session.execution_status)
        .then(|| session.latest_run_id.trim())
        .filter(|run_id| !run_id.is_empty())
        .and_then(|run_id| rpc.execution_status(run_id).ok())
        .filter(|run| run.run_id == session.latest_run_id && run.session_id == session.session_id)
}

fn execution_status_is_interactive(status: &str) -> bool {
    ExecutionLifecycle::from_status(status).is_some_and(ExecutionLifecycle::is_active)
}

fn execution_status_retains_run_truth(status: &str) -> bool {
    ExecutionLifecycle::from_status(status).is_some_and(|lifecycle| !lifecycle.is_terminal())
}

fn retained_execution_run_id<'a>(
    active_run_id: Option<&'a str>,
    presentation_run_id: &'a str,
    execution_status: &str,
) -> Option<&'a str> {
    active_run_id.or_else(|| {
        execution_status_retains_run_truth(execution_status)
            .then_some(presentation_run_id.trim())
            .filter(|run_id| !run_id.is_empty())
    })
}

fn execution_event_owns_projection(
    projected_run_id: Option<&str>,
    latest_session_run_id: Option<&str>,
    event_run_id: &str,
    lifecycle: Option<ExecutionLifecycle>,
) -> bool {
    match projected_run_id {
        Some(run_id) => run_id == event_run_id,
        None => latest_session_run_id
            .map_or(lifecycle == Some(ExecutionLifecycle::Queued), |run_id| {
                run_id == event_run_id
            }),
    }
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
    let probe = std::thread::Builder::new()
        .name("terminal-probe".into())
        .spawn(|| crate::terminal_probe::background(Duration::from_millis(80)))
        .ok();
    let mut app = App::bootstrap(options)?;
    let background = probe.and_then(|handle| handle.join().ok()).flatten();
    app.theme = Theme::detected(background);
    app.theme.glyphs = app.theme.glyphs.with_mode(app.glyph_resolution.mode);
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
        // Rendering may discover that retained pointer ownership changed after
        // the current frame rebuilt its hit geometry. Schedule that correction
        // before blocking for external input.
        if app.dirty {
            continue;
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
            transcript_anchor: app.transcript_anchor.clone(),
            reading_state: app.capture_reading_envelope(),
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
    command
        .arg("--glyphs")
        .arg(app.glyph_preference.as_config_value());
    if let Some(path) = app.options.glyphs_path.as_ref() {
        command.arg("--glyphs-path").arg(path);
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

fn grok_build_repair_message(action: &str) -> MessageId {
    match action {
        "login_cli" => MessageId::GrokBuildLoginDetail,
        "update_cli" => MessageId::GrokBuildUpdateDetail,
        "disabled" => MessageId::GrokBuildDisabledDetail,
        _ => MessageId::GrokBuildRetryDetail,
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

fn retry_dispatch_allowed(
    blocks: &[TranscriptBlock],
    active_run_id: Option<&str>,
    requested_run_id: &str,
) -> bool {
    active_run_id.is_none()
        && !requested_run_id.is_empty()
        && blocks.iter().any(|block| {
            block.failure.as_ref().is_some_and(|failure| {
                failure.run_id == requested_run_id
                    && matches!(
                        failure.action,
                        crate::transcript::FailureAction::Retry
                            | crate::transcript::FailureAction::RunAgain
                    )
            })
        })
}

fn artifact_target_is_current(
    generation: u64,
    current_generation: u64,
    active_session_id: Option<&str>,
    target_session_id: &str,
) -> bool {
    generation == current_generation && active_session_id == Some(target_session_id)
}

fn tool_artifact_refs_from_items(
    items: &[SessionItemEvent],
) -> HashMap<String, crate::rpc::ToolArtifactRef> {
    items
        .iter()
        .filter_map(|event| {
            let item = event.item.as_ref()?;
            if item.kind != "tool_call" {
                return None;
            }
            let call_id = item
                .details
                .get("call_id")
                .and_then(serde_json::Value::as_str)
                .unwrap_or(&item.id)
                .trim();
            let artifact_id = item
                .details
                .get("artifact_ids")
                .and_then(serde_json::Value::as_array)?
                .iter()
                .find_map(serde_json::Value::as_str)?
                .trim();
            if call_id.is_empty() || artifact_id.is_empty() || event.session_id.is_empty() {
                return None;
            }
            Some((
                call_id.to_owned(),
                crate::rpc::ToolArtifactRef {
                    session_id: event.session_id.clone(),
                    run_id: if item.run_id.is_empty() {
                        event.run_id.clone()
                    } else {
                        item.run_id.clone()
                    },
                    call_id: call_id.to_owned(),
                    artifact_id: artifact_id.to_owned(),
                },
            ))
        })
        .collect()
}

fn tool_component_call_ids(block: &TranscriptBlock) -> Vec<&str> {
    if block.tool_members.len() > 1 {
        return block
            .tool_members
            .iter()
            .filter_map(|member| member.id.strip_prefix("tool:"))
            .collect();
    }
    block.id.strip_prefix("tool:").into_iter().collect()
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

fn rollback_preview_matches(
    preview: &crate::rpc::PatchRollbackPreview,
    patch_id: &str,
    transaction_id: &str,
) -> bool {
    preview.can_rollback
        && preview.workspace_unchanged
        && !patch_id.is_empty()
        && !transaction_id.is_empty()
        && preview.patch_id == patch_id
        && preview.transaction_id == transaction_id
}

fn rollback_confirmation_target(changes: &ChangesOverlay) -> Option<(String, String)> {
    let preview = changes.rollback_preview.as_ref()?;
    if !preview.can_rollback
        || !preview.workspace_unchanged
        || preview.patch_id.is_empty()
        || preview.transaction_id.is_empty()
    {
        return None;
    }
    changes
        .projection
        .patches
        .iter()
        .find(|patch| {
            patch.patch_id == preview.patch_id
                && patch.transaction_id == preview.transaction_id
                && !patch.rollback_pointer.is_empty()
                && !matches!(patch.status.as_str(), "rolled_back" | "failed" | "proposed")
        })
        .map(|patch| (patch.session_id.clone(), patch.patch_id.clone()))
}

fn retain_patch_review_selection(
    patch_id: Option<&str>,
    file_path: Option<&str>,
    patches: &[crate::rpc::WorkspacePatch],
    reviews: &[PatchReview],
) -> (usize, usize) {
    let patch_index = patch_id
        .and_then(|patch_id| patches.iter().position(|patch| patch.patch_id == patch_id))
        .unwrap_or(0);
    let file_index = file_path
        .and_then(|path| {
            reviews
                .get(patch_index)
                .and_then(|review| review.files.iter().position(|file| file.path == path))
        })
        .unwrap_or(0);
    (patch_index, file_index)
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
        Ok(data) => {
            parse_unique_json_object(&data).with_context(|| format!("parse {}", path.display()))?
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => serde_json::Map::new(),
        Err(error) => return Err(error.into()),
    };
    root.insert(key.into(), serde_json::Value::String(value.into()));
    let parent = path
        .parent()
        .ok_or_else(|| anyhow!("TUI config has no parent"))?;
    fs::create_dir_all(parent)?;
    let permissions = match fs::metadata(path) {
        Ok(metadata) => Some(metadata.permissions()),
        Err(error) if error.kind() == io::ErrorKind::NotFound => None,
        Err(error) => return Err(error.into()),
    };
    let mut temp = tempfile::NamedTempFile::new_in(parent)?;
    if let Some(permissions) = permissions {
        temp.as_file().set_permissions(permissions)?;
    }
    serde_json::to_writer_pretty(temp.as_file_mut(), &root)?;
    temp.as_file_mut().write_all(b"\n")?;
    temp.as_file_mut().flush()?;
    temp.as_file().sync_all()?;
    temp.persist(path).map_err(|error| error.error)?;
    Ok(())
}

fn parse_unique_json_object(data: &[u8]) -> Result<serde_json::Map<String, serde_json::Value>> {
    let StrictJson(value) = serde_json::from_slice::<StrictJson>(data)?;
    value
        .as_object()
        .cloned()
        .ok_or_else(|| anyhow!("TUI config root must be an object"))
}

struct StrictJson(serde_json::Value);

impl<'de> serde::Deserialize<'de> for StrictJson {
    fn deserialize<D>(deserializer: D) -> std::result::Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        deserializer.deserialize_any(StrictJsonVisitor)
    }
}

struct StrictJsonVisitor;

impl<'de> Visitor<'de> for StrictJsonVisitor {
    type Value = StrictJson;

    fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("valid JSON without duplicate object keys")
    }

    fn visit_bool<E>(self, value: bool) -> std::result::Result<Self::Value, E> {
        Ok(StrictJson(value.into()))
    }

    fn visit_i64<E>(self, value: i64) -> std::result::Result<Self::Value, E> {
        Ok(StrictJson(value.into()))
    }

    fn visit_u64<E>(self, value: u64) -> std::result::Result<Self::Value, E> {
        Ok(StrictJson(value.into()))
    }

    fn visit_f64<E>(self, value: f64) -> std::result::Result<Self::Value, E>
    where
        E: de::Error,
    {
        serde_json::Number::from_f64(value)
            .map(serde_json::Value::Number)
            .map(StrictJson)
            .ok_or_else(|| E::custom("JSON number must be finite"))
    }

    fn visit_str<E>(self, value: &str) -> std::result::Result<Self::Value, E>
    where
        E: de::Error,
    {
        self.visit_string(value.to_owned())
    }

    fn visit_string<E>(self, value: String) -> std::result::Result<Self::Value, E> {
        Ok(StrictJson(value.into()))
    }

    fn visit_none<E>(self) -> std::result::Result<Self::Value, E> {
        Ok(StrictJson(serde_json::Value::Null))
    }

    fn visit_unit<E>(self) -> std::result::Result<Self::Value, E> {
        Ok(StrictJson(serde_json::Value::Null))
    }

    fn visit_seq<A>(self, mut values: A) -> std::result::Result<Self::Value, A::Error>
    where
        A: SeqAccess<'de>,
    {
        let mut array = Vec::new();
        while let Some(StrictJson(value)) = values.next_element::<StrictJson>()? {
            array.push(value);
        }
        Ok(StrictJson(serde_json::Value::Array(array)))
    }

    fn visit_map<A>(self, mut values: A) -> std::result::Result<Self::Value, A::Error>
    where
        A: MapAccess<'de>,
    {
        let mut object = serde_json::Map::new();
        while let Some(key) = values.next_key::<String>()? {
            if object.contains_key(&key) {
                return Err(de::Error::custom(format!(
                    "duplicate JSON key {key:?}; remove one entry before changing TUI settings"
                )));
            }
            let StrictJson(value) = values.next_value::<StrictJson>()?;
            object.insert(key, value);
        }
        Ok(StrictJson(serde_json::Value::Object(object)))
    }
}

fn persist_locale(path: &Path, locale: &str) -> Result<()> {
    persist_config_string(path, "tui_locale", locale)
}

fn persist_density(path: &Path, density: DensityMode) -> Result<()> {
    persist_config_string(path, "tui_density", density.as_config_value())
}

fn persist_glyph_preference(path: &Path, preference: GlyphPreference) -> Result<()> {
    persist_config_string(path, "tui_glyphs", preference.as_config_value())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn grok_model_confirmation_app(
        refreshed_inventory: serde_json::Value,
    ) -> (App, tempfile::TempDir, std::thread::JoinHandle<()>) {
        use std::io::{BufRead, BufReader, Write};

        let root = tempfile::tempdir().unwrap();
        let socket = root.path().join("daemon.sock");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            for (method, result) in [
                (
                    "runtime.initialize",
                    serde_json::json!({
                        "runtime_version": "test",
                        "protocol_version": "1.3.0",
                        "projection_version": "1.0.0",
                        "capabilities": {"rpc_methods": [
                            "execution.start", "execution.retry", "model.list", "session.create",
                            "session.events.stream", "session.list"
                        ]}
                    }),
                ),
                (
                    "model.list",
                    serde_json::json!({
                        "default_model": "grok-build/grok-4.6",
                        "reasoner": {"available": true},
                        "providers": [
                            {
                                "id": "grok-build",
                                "name": "Grok Build",
                                "registered": true,
                                "available": true,
                                "source_kind": "grok-build",
                                "source_action": "use_cli_session",
                                "models": [{
                                    "id": "grok-build/grok-4.6",
                                    "available": true
                                }]
                            },
                            {
                                "id": "xai",
                                "name": "xAI",
                                "registered": true,
                                "available": true,
                                "models": [{"id": "xai/grok-4.6", "available": true}]
                            }
                        ]
                    }),
                ),
                ("session.list", serde_json::json!([])),
            ] {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let request: serde_json::Value = serde_json::from_str(&line).unwrap();
                assert_eq!(request["method"], method);
                writeln!(
                    stream,
                    "{}",
                    serde_json::json!({
                        "jsonrpc": "2.0",
                        "id": request["id"],
                        "result": result
                    })
                )
                .unwrap();
                stream.flush().unwrap();
            }

            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            let request: serde_json::Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "model.list");
            assert_eq!(request["params"], serde_json::json!({"refresh": true}));
            writeln!(
                stream,
                "{}",
                serde_json::json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": refreshed_inventory
                })
            )
            .unwrap();
            stream.flush().unwrap();
        });

        let mut app = App::bootstrap(Options {
            socket,
            workspace: root.path().to_path_buf(),
            runtime_expectation: None,
            session_id: None,
            locale: Some(Locale::En.product_id().into()),
            locale_path: None,
            density: DensityMode::Compact,
            density_path: None,
            glyph_preference: GlyphPreference::Auto,
            glyphs_path: None,
            carina_bin: None,
            no_alt_screen: true,
            screen_mode: None,
            screen_handoff: None,
            alt_screen: AltScreenPolicy::Never,
            scrollback_wrap: ScrollbackWrap::PreWrap,
        })
        .unwrap();
        app.active_session = Some(
            serde_json::from_value(serde_json::json!({
                "session_id": "session-keep",
                "next_model": "grok-build/grok-4.6",
                "next_reasoning_effort": "high",
                "model_preference_revision": 7
            }))
            .unwrap(),
        );
        app.composer.set_text("unfinished Grok draft");
        app.phase = Phase::Model;
        app.focus = Focus::Scene;
        (app, root, server)
    }

    #[test]
    fn empty_or_divergent_session_model_needs_a_picker_write() {
        let empty: Session = serde_json::from_value(serde_json::json!({
            "session_id": "sess-empty",
            "next_model": "",
            "model_preference_revision": 0
        }))
        .unwrap();
        let matching: Session = serde_json::from_value(serde_json::json!({
            "session_id": "sess-match",
            "next_model": "grok-build/grok-4.6",
            "model_preference_revision": 1
        }))
        .unwrap();
        assert!(session_needs_picker_model_write(
            &empty,
            "grok-build/grok-4.6"
        ));
        assert!(!session_needs_picker_model_write(&empty, ""));
        assert!(!session_needs_picker_model_write(
            &matching,
            "grok-build/grok-4.6"
        ));
        assert!(session_needs_picker_model_write(
            &matching,
            "xai/grok-4.6"
        ));
    }

    #[test]
    fn submit_writes_picker_model_before_start_when_session_has_none() {
        use std::io::{BufRead, BufReader, Write};

        let root = tempfile::tempdir().unwrap();
        let socket = root.path().join("daemon.sock");
        let listener = std::os::unix::net::UnixListener::bind(&socket).unwrap();
        let server = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let inventory = serde_json::json!({
                "default_model": "grok-build/grok-4.6",
                "reasoner": {"available": true},
                "providers": [{
                    "id": "grok-build",
                    "registered": true,
                    "available": true,
                    "models": [{"id": "grok-build/grok-4.6", "available": true}]
                }]
            });
            for method in ["runtime.initialize", "model.list", "session.list"] {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let request: serde_json::Value = serde_json::from_str(&line).unwrap();
                assert_eq!(request["method"], method);
                let result = match method {
                    "runtime.initialize" => serde_json::json!({
                        "runtime_version": "test",
                        "protocol_version": "1.3.0",
                        "projection_version": "1.0.0",
                        "capabilities": {"rpc_methods": [
                            "execution.start", "execution.retry", "model.list", "session.create",
                            "session.model.set", "session.events.stream", "session.list"
                        ]}
                    }),
                    "model.list" => inventory.clone(),
                    _ => serde_json::json!([]),
                };
                writeln!(
                    stream,
                    "{}",
                    serde_json::json!({
                        "jsonrpc": "2.0",
                        "id": request["id"],
                        "result": result
                    })
                )
                .unwrap();
                stream.flush().unwrap();
            }

            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            let request: serde_json::Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "model.list");
            writeln!(
                stream,
                "{}",
                serde_json::json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": inventory
                })
            )
            .unwrap();
            stream.flush().unwrap();

            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            let request: serde_json::Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "session.model.set");
            assert_eq!(request["params"]["session_id"], "sess-empty-model");
            assert_eq!(request["params"]["model"], "grok-build/grok-4.6");
            assert_eq!(request["params"]["expected_model_preference_revision"], 0);
            writeln!(
                stream,
                "{}",
                serde_json::json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": {
                        "session_id": "sess-empty-model",
                        "next_model": "grok-build/grok-4.6",
                        "next_reasoning_effort": "high",
                        "model_preference_revision": 1
                    }
                })
            )
            .unwrap();
            stream.flush().unwrap();

            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            let request: serde_json::Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["method"], "execution.start");
            assert_eq!(request["params"]["model"], "grok-build/grok-4.6");
            assert_eq!(request["params"]["model_preference_revision"], 1);
            assert_eq!(request["params"]["prompt"], "你好");
            writeln!(
                stream,
                "{}",
                serde_json::json!({
                    "jsonrpc": "2.0",
                    "id": request["id"],
                    "result": {
                        "run_id": "run-1",
                        "session_id": "sess-empty-model",
                        "status": "queued"
                    }
                })
            )
            .unwrap();
            stream.flush().unwrap();
        });

        let mut app = App::bootstrap(Options {
            socket,
            workspace: root.path().to_path_buf(),
            runtime_expectation: None,
            session_id: None,
            locale: Some(Locale::ZhHans.product_id().into()),
            locale_path: None,
            density: DensityMode::Compact,
            density_path: None,
            glyph_preference: GlyphPreference::Auto,
            glyphs_path: None,
            carina_bin: None,
            no_alt_screen: true,
            screen_mode: None,
            screen_handoff: None,
            alt_screen: AltScreenPolicy::Never,
            scrollback_wrap: ScrollbackWrap::PreWrap,
        })
        .unwrap();
        app.active_session = Some(
            serde_json::from_value(serde_json::json!({
                "session_id": "sess-empty-model",
                "next_model": "",
                "model_preference_revision": 0
            }))
            .unwrap(),
        );
        app.selected_model = "grok-build/grok-4.6".into();
        app.selected_reasoning_effort = "high".into();
        app.phase = Phase::Conversation;
        app.focus = Focus::Composer;
        app.composer.set_text("你好");

        assert!(app.submit_new_prompt("你好".into(), Vec::new()).unwrap());
        assert_eq!(app.composer.text(), "");
        assert_eq!(app.active_run_id.as_deref(), Some("run-1"));
        assert_eq!(
            app.active_session
                .as_ref()
                .map(|session| session.model_preference_revision),
            Some(1)
        );

        server.join().unwrap();
    }

    #[test]
    fn retry_dispatch_revalidates_stale_pointer_actions_before_rpc() {
        let mut block = TranscriptBlock::local_steer(
            "failure:run-root".into(),
            "run-failed".into(),
            String::new(),
        );
        block.failure = Some(crate::transcript::FailurePresentation {
            kind: crate::transcript::FailureKind::Failed,
            action: crate::transcript::FailureAction::Retry,
            owner: "runtime".into(),
            reason: "failed".into(),
            source_event_id: "event-failed".into(),
            run_id: "run-failed".into(),
            model: "provider/model".into(),
            current_model: String::new(),
            retry_root_run_id: "run-root".into(),
            attempt_count: 1,
            focused_action: None,
        });

        assert!(retry_dispatch_allowed(&[block.clone()], None, "run-failed"));
        assert!(!retry_dispatch_allowed(
            &[block.clone()],
            Some("run-retry"),
            "run-failed"
        ));

        block.failure.as_mut().unwrap().action = crate::transcript::FailureAction::Recovering;
        assert!(!retry_dispatch_allowed(&[block], None, "run-failed"));
    }

    #[test]
    fn grok_build_repair_actions_never_map_to_credential_entry() {
        assert_eq!(
            grok_build_repair_message("login_cli"),
            MessageId::GrokBuildLoginDetail
        );
        assert_eq!(
            grok_build_repair_message("update_cli"),
            MessageId::GrokBuildUpdateDetail
        );
        assert_eq!(
            grok_build_repair_message("retry_probe"),
            MessageId::GrokBuildRetryDetail
        );
        assert_eq!(
            grok_build_repair_message("disabled"),
            MessageId::GrokBuildDisabledDetail
        );
    }

    #[test]
    fn grok_model_confirmation_refreshes_and_repairs_signed_out_provider() {
        let (mut app, root, server) = grok_model_confirmation_app(serde_json::json!({
            "default_model": "xai/grok-4.6",
            "reasoner": {"available": true},
            "providers": [
                {
                    "id": "grok-build",
                    "name": "Grok Build",
                    "registered": true,
                    "available": false,
                    "source_kind": "grok-build",
                    "source_action": "login_cli",
                    "models": []
                },
                {
                    "id": "xai",
                    "name": "xAI",
                    "registered": true,
                    "available": true,
                    "models": [{"id": "xai/grok-4.6", "available": true}]
                }
            ]
        }));

        app.select_model_and_continue(0);
        server.join().unwrap();

        assert_eq!(app.phase, Phase::Provider);
        assert_eq!(app.focus, Focus::Scene);
        assert_eq!(app.inventory.providers[app.provider_index].id, "grok-build");
        assert!(app.notice.is_localized(MessageId::GrokBuildLoginDetail));
        assert_eq!(app.composer.text(), "unfinished Grok draft");
        assert_eq!(
            app.active_session
                .as_ref()
                .map(|session| session.session_id.as_str()),
            Some("session-keep")
        );
        assert_eq!(app.selected_model, "grok-build/grok-4.6");
        drop(app);
        drop(root);
    }

    #[test]
    fn grok_model_confirmation_returns_to_provider_when_model_disappears() {
        let (mut app, root, server) = grok_model_confirmation_app(serde_json::json!({
            "default_model": "grok-build/grok-4.5",
            "reasoner": {"available": true},
            "providers": [
                {
                    "id": "grok-build",
                    "name": "Grok Build",
                    "registered": true,
                    "available": true,
                    "source_kind": "grok-build",
                    "source_action": "use_cli_session",
                    "models": [{
                        "id": "grok-build/grok-4.5",
                        "available": true
                    }]
                },
                {
                    "id": "xai",
                    "name": "xAI",
                    "registered": true,
                    "available": true,
                    "models": [{"id": "xai/grok-4.6", "available": true}]
                }
            ]
        }));

        app.select_model_and_continue(0);
        server.join().unwrap();

        assert_eq!(app.phase, Phase::Provider);
        assert_eq!(app.focus, Focus::Scene);
        assert!(app.notice.is_localized(MessageId::SelectedModelUnavailable));
        assert_eq!(app.composer.text(), "unfinished Grok draft");
        assert_eq!(
            app.active_session
                .as_ref()
                .map(|session| session.session_id.as_str()),
            Some("session-keep")
        );
        assert_eq!(app.selected_model, "grok-build/grok-4.6");
        assert_eq!(app.models[0].id, "grok-build/grok-4.5");
        drop(app);
        drop(root);
    }

    #[test]
    fn rollback_preview_requires_exact_identity_and_unchanged_workspace() {
        let preview = crate::rpc::PatchRollbackPreview {
            patch_id: "patch-a".into(),
            transaction_id: "tx-a".into(),
            can_rollback: true,
            workspace_unchanged: true,
            ..crate::rpc::PatchRollbackPreview::default()
        };
        assert!(rollback_preview_matches(&preview, "patch-a", "tx-a"));
        assert!(!rollback_preview_matches(&preview, "patch-b", "tx-a"));
        assert!(!rollback_preview_matches(&preview, "patch-a", "tx-b"));

        let mut changed = preview;
        changed.workspace_unchanged = false;
        assert!(!rollback_preview_matches(&changed, "patch-a", "tx-a"));
    }

    #[test]
    fn rollback_confirmation_keeps_the_previewed_transaction_when_selection_moves() {
        let patches = vec![
            crate::rpc::WorkspacePatch {
                patch_id: "patch-a".into(),
                transaction_id: "tx-a".into(),
                session_id: "session-a".into(),
                status: "verified".into(),
                rollback_pointer: "rollback-a".into(),
                ..crate::rpc::WorkspacePatch::default()
            },
            crate::rpc::WorkspacePatch {
                patch_id: "patch-b".into(),
                transaction_id: "tx-b".into(),
                session_id: "session-b".into(),
                status: "verified".into(),
                rollback_pointer: "rollback-b".into(),
                ..crate::rpc::WorkspacePatch::default()
            },
        ];
        let changes = ChangesOverlay {
            projection: ProductProjection {
                patches,
                ..ProductProjection::default()
            },
            patch_reviews: Vec::new(),
            selected: 1,
            selected_file: 0,
            focus: ChangesFocus::Transactions,
            scroll: 0,
            load: RetainedLoad::default(),
            confirm_rollback: true,
            rollback_preview: Some(crate::rpc::PatchRollbackPreview {
                patch_id: "patch-a".into(),
                transaction_id: "tx-a".into(),
                can_rollback: true,
                workspace_unchanged: true,
                ..crate::rpc::PatchRollbackPreview::default()
            }),
            rollback_error: String::new(),
        };
        assert_eq!(
            rollback_confirmation_target(&changes),
            Some(("session-a".into(), "patch-a".into()))
        );
    }

    #[test]
    fn changes_refresh_preserves_patch_and_file_identity_across_reordering() {
        let patches = vec![
            crate::rpc::WorkspacePatch {
                patch_id: "patch-b".into(),
                affected_files: vec!["other.rs".into(), "target.rs".into()],
                diff: concat!(
                    "diff --git a/other.rs b/other.rs\n",
                    "--- a/other.rs\n+++ b/other.rs\n@@ -1 +1 @@\n-old\n+new\n",
                    "diff --git a/target.rs b/target.rs\n",
                    "--- a/target.rs\n+++ b/target.rs\n@@ -1 +1 @@\n-before\n+after\n"
                )
                .into(),
                ..crate::rpc::WorkspacePatch::default()
            },
            crate::rpc::WorkspacePatch {
                patch_id: "patch-a".into(),
                affected_files: vec!["a.rs".into()],
                ..crate::rpc::WorkspacePatch::default()
            },
        ];
        let reviews = project_patch_reviews(&patches);
        assert_eq!(
            retain_patch_review_selection(Some("patch-b"), Some("target.rs"), &patches, &reviews),
            (0, 1)
        );
        assert_eq!(
            retain_patch_review_selection(Some("missing"), Some("missing.rs"), &patches, &reviews),
            (0, 0)
        );
    }

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
                transcript_anchor: None,
                reading_state: None,
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
    fn screen_handoff_accepts_legacy_payload_without_transcript_anchor() {
        let handoff: ScreenModeHandoff = serde_json::from_str(
            r#"{
                "session_id":"sess_1",
                "runtime_id":"runtime_1",
                "runtime_epoch":"epoch_1",
                "runtime_process_epoch":2,
                "runtime_pid":42,
                "draft":"keep",
                "queued_prompts":[],
                "committed_scrollback":[],
                "pending_governance":[],
                "selected_block_id":null,
                "transcript_scroll":17,
                "transcript_follow_bottom":false
            }"#,
        )
        .unwrap();

        assert_eq!(handoff.transcript_scroll, 17);
        assert!(!handoff.transcript_follow_bottom);
        assert_eq!(handoff.transcript_anchor, None);
        assert_eq!(handoff.reading_state, None);
    }

    #[test]
    fn screen_handoff_reading_envelope_is_authoritative_over_loose_fields() {
        let envelope = reading_state::ReadingStateEnvelopeV1 {
            version: 1,
            session_id: "sess_1".into(),
            selected_block_id: Some("assistant:keep".into()),
            disclosure_overrides: std::collections::BTreeMap::from([("tool:1".into(), true)]),
            follow_bottom: false,
            top_visible: Some(reading_state::LogicalTranscriptAnchorV1 {
                block_id: "assistant:keep".into(),
                logical_line: 2,
                wrapped_sub_row: 1,
                position_hint: 0,
                previous_block_id: None,
                next_block_id: None,
            }),
            committed_scrollback: Vec::new(),
        };
        let json = serde_json::to_string(&ScreenModeHandoff {
            session_id: "sess_1".into(),
            transcript_scroll: 99,
            transcript_follow_bottom: true,
            reading_state: Some(envelope.clone()),
            ..ScreenModeHandoff::default()
        })
        .unwrap();
        let handoff: ScreenModeHandoff = serde_json::from_str(&json).unwrap();
        assert_eq!(handoff.reading_state, Some(envelope));
        assert!(handoff.transcript_follow_bottom);
        assert_eq!(handoff.transcript_scroll, 99);
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
    fn glyph_update_preserves_unrelated_config_and_survives_reload() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("config.json");
        fs::write(
            &path,
            br#"{"max_concurrent_tasks":4,"provider":{"id":"openai"},"tui_locale":"zh-Hans"}"#,
        )
        .unwrap();

        persist_glyph_preference(&path, GlyphPreference::Nerd).unwrap();

        let value: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        assert_eq!(value["max_concurrent_tasks"], 4);
        assert_eq!(value["provider"]["id"], "openai");
        assert_eq!(value["tui_locale"], "zh-Hans");
        assert_eq!(value["tui_glyphs"], "nerd");
    }

    #[cfg(unix)]
    #[test]
    fn glyph_update_preserves_existing_config_permissions() {
        use std::os::unix::fs::PermissionsExt;

        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("config.json");
        fs::write(&path, br#"{"tui_locale":"en"}"#).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o640)).unwrap();

        persist_glyph_preference(&path, GlyphPreference::Ascii).unwrap();

        assert_eq!(
            fs::metadata(&path).unwrap().permissions().mode() & 0o777,
            0o640
        );
    }

    #[test]
    fn config_update_rejects_invalid_or_duplicate_json_without_replacing_it() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("config.json");
        for original in [
            br#"{"tui_glyphs":"auto""#.as_slice(),
            br#"{"tui_glyphs":"auto","tui_glyphs":"ascii"}"#.as_slice(),
            br#"{"provider":{"id":"one","id":"two"}}"#.as_slice(),
            br#"["not","an","object"]"#.as_slice(),
        ] {
            fs::write(&path, original).unwrap();
            assert!(
                persist_glyph_preference(&path, GlyphPreference::Unicode).is_err(),
                "invalid config must be rejected: {}",
                String::from_utf8_lossy(original)
            );
            assert_eq!(fs::read(&path).unwrap(), original);
        }
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
    fn runtime_truth_outlives_only_nonterminal_execution_states() {
        for status in [
            "queued",
            "running",
            "waiting_input",
            "waiting_approval",
            "paused",
            "interrupted",
        ] {
            assert!(
                execution_status_retains_run_truth(status),
                "status={status}"
            );
        }
        for status in ["", "completed", "failed", "degraded", "cancelled"] {
            assert!(
                !execution_status_retains_run_truth(status),
                "status={status}"
            );
        }
        for status in ["queued", "running", "waiting_input", "waiting_approval"] {
            assert!(execution_status_is_interactive(status), "status={status}");
        }
        for status in ["paused", "interrupted", "completed", "failed"] {
            assert!(!execution_status_is_interactive(status), "status={status}");
        }
    }

    #[test]
    fn paused_run_remains_cancelable_without_becoming_steerable() {
        assert_eq!(
            retained_execution_run_id(None, "run-paused", "paused"),
            Some("run-paused")
        );
        assert_eq!(
            retained_execution_run_id(None, "run-interrupted", "interrupted"),
            Some("run-interrupted")
        );
        assert_eq!(
            retained_execution_run_id(Some("run-active"), "run-stale", "running"),
            Some("run-active")
        );
        for status in ["completed", "failed", "cancelled"] {
            assert_eq!(retained_execution_run_id(None, "run-old", status), None);
        }
    }

    #[test]
    fn stale_run_events_cannot_replace_the_foreground_projection() {
        assert!(execution_event_owns_projection(
            Some("run-current"),
            Some("run-current"),
            "run-current",
            Some(ExecutionLifecycle::Completed),
        ));
        assert!(!execution_event_owns_projection(
            Some("run-current"),
            Some("run-current"),
            "run-old",
            Some(ExecutionLifecycle::Completed),
        ));
        assert!(!execution_event_owns_projection(
            Some("run-current"),
            Some("run-current"),
            "run-old",
            Some(ExecutionLifecycle::Queued),
        ));
        assert!(execution_event_owns_projection(
            None,
            Some("run-paused"),
            "run-paused",
            Some(ExecutionLifecycle::Paused),
        ));
        assert!(execution_event_owns_projection(
            None,
            None,
            "run-new",
            Some(ExecutionLifecycle::Queued),
        ));
        assert!(!execution_event_owns_projection(
            None,
            Some("run-latest"),
            "run-old",
            Some(ExecutionLifecycle::Completed),
        ));
        assert!(!execution_event_owns_projection(
            None,
            Some("run-latest"),
            "run-old",
            Some(ExecutionLifecycle::Queued),
        ));
    }

    fn reconnect_item(kind: &str, run_id: &str, text: &str) -> SessionItemEvent {
        serde_json::from_value(serde_json::json!({
            "type": "item.completed",
            "session_id": "sess-reconnect",
            "turn_id": run_id,
            "item": {
                "id": run_id,
                "type": if kind == "user" { "user" } else { "agent_message" },
                "status": "completed",
                "task_id": run_id,
                "details": { "text": text }
            }
        }))
        .unwrap()
    }

    fn reconnect_event(
        kind: &str,
        run_id: &str,
        cursor: usize,
        payload: serde_json::Value,
    ) -> ReceivedEvent {
        ReceivedEvent {
            event: WireEvent {
                session_id: "sess-reconnect".into(),
                run_id: run_id.into(),
                kind: kind.into(),
                raw_cursor: cursor,
                payload: payload
                    .as_object()
                    .cloned()
                    .unwrap_or_default()
                    .into_iter()
                    .collect(),
                ..WireEvent::default()
            },
            received_at: std::time::Instant::now(),
            replayed: cursor > 0,
            delivery: None,
        }
    }

    #[test]
    fn reconnect_hydrate_includes_snapshot_before_the_projection_is_swapped() {
        let items = vec![reconnect_item("user", "run-new", "hello")];
        let catch_up = vec![
            reconnect_event(
                "assistant.message.snapshot",
                "run-new",
                0,
                serde_json::json!({
                    "generation": 1,
                    "sequence": 4,
                    "phase": "final_answer",
                    "content": "draft",
                    "tail_revision": 8,
                    "state": "open"
                }),
            ),
            reconnect_event(
                "ToolCallStarted",
                "run-new",
                12,
                serde_json::json!({"call_id": "c1", "tool": "read"}),
            ),
        ];
        let (_reducer, blocks, cursor) = hydrate_reconnect_blocks(items, catch_up);
        assert!(
            blocks
                .iter()
                .any(|block| block.id == "assistant:run-new" && block.body == "draft"),
            "hydrated projection missing transient snapshot: {blocks:?}"
        );
        assert_eq!(cursor, 12);
    }

    #[test]
    fn reconnect_legacy_path_keeps_the_durable_cursor() {
        assert_eq!(reconnect_event_cursor(7, 0, 0, false), 7);
        assert_eq!(reconnect_event_cursor(7, 4, 0, false), 7);
        assert_eq!(reconnect_event_cursor(7, 9, 12, true), 12);
        assert_eq!(reconnect_event_cursor(7, 9, 8, true), 9);
    }

    #[test]
    fn reconnect_items_and_stream_partition_at_watermark() {
        let watermark = 10;
        let after = reconnect_event(
            "ToolCallCompleted",
            "run-new",
            11,
            serde_json::json!({"call_id": "c1"}),
        );
        let overlap = reconnect_event(
            "ToolCallStarted",
            "run-new",
            10,
            serde_json::json!({"call_id": "c1"}),
        );
        assert!(reconnect_stream_is_strictly_after_watermark(
            watermark,
            &[after]
        ));
        assert!(!reconnect_stream_is_strictly_after_watermark(
            watermark,
            &[overlap]
        ));
    }

    #[test]
    fn reconnect_rejects_identity_and_watermark_drift() {
        let runtime = crate::rpc::RuntimeIdentity {
            runtime_id: "rt".into(),
            epoch: "ep".into(),
            process_epoch: 3,
            ..crate::rpc::RuntimeIdentity::default()
        };
        let mut boundary = ReplayBoundaryV1 {
            version: 1,
            session_id: "sess".into(),
            runtime_id: "rt".into(),
            runtime_epoch: "ep".into(),
            runtime_process_epoch: 3,
            requested_since: 7,
            durable_cursor: 9,
            durable_replayed: 1,
            ..ReplayBoundaryV1::default()
        };
        assert!(validate_reconnect_boundary("sess", &runtime, 7, &boundary).is_ok());
        boundary.requested_since = 6;
        assert!(validate_reconnect_boundary("sess", &runtime, 7, &boundary).is_err());
        boundary.requested_since = 7;
        boundary.durable_cursor = 6;
        assert!(validate_reconnect_boundary("sess", &runtime, 7, &boundary).is_err());
        boundary.durable_cursor = 9;
        boundary.session_id = "other".into();
        assert!(validate_reconnect_boundary("sess", &runtime, 7, &boundary).is_err());
    }

    #[test]
    fn reconnect_cursor_zero_does_not_promote_older_completed_run() {
        assert!(!execution_event_owns_projection(
            None,
            Some("run-new"),
            "run-old",
            Some(ExecutionLifecycle::Queued),
        ));
        assert!(!execution_event_owns_projection(
            None,
            Some("run-new"),
            "run-old",
            Some(ExecutionLifecycle::Completed),
        ));
        assert!(execution_event_owns_projection(
            None,
            Some("run-new"),
            "run-new",
            Some(ExecutionLifecycle::Completed),
        ));
    }

    #[test]
    fn reconnect_legacy_cursor_does_not_promote_older_queued_run() {
        assert!(!execution_event_owns_projection(
            Some("run-new"),
            Some("run-new"),
            "run-old",
            Some(ExecutionLifecycle::Queued),
        ));
        assert!(!execution_event_owns_projection(
            Some("run-new"),
            Some("run-new"),
            "run-old",
            Some(ExecutionLifecycle::Completed),
        ));
    }

    #[test]
    fn reconnect_stale_generation_is_ignored() {
        assert!(!artifact_target_is_current(
            3,
            4,
            Some("sess-current"),
            "sess-current"
        ));
    }

    fn assistant_bodies(blocks: &[TranscriptBlock]) -> Vec<(&str, &str)> {
        blocks
            .iter()
            .filter(|block| block.kind == crate::transcript::BlockKind::Assistant)
            .map(|block| (block.id.as_str(), block.body.as_str()))
            .collect()
    }

    fn reading_block(id: &str, kind: crate::transcript::BlockKind, body: &str) -> TranscriptBlock {
        let mut block = TranscriptBlock::local_user(id.into(), body.into());
        block.id = id.into();
        block.kind = kind;
        block.body = body.into();
        block.collapsible = matches!(
            kind,
            crate::transcript::BlockKind::Tool
                | crate::transcript::BlockKind::Thinking
                | crate::transcript::BlockKind::Diagnostic
        );
        block
    }

    #[test]
    fn disconnect_after_delta_then_reconnect_seals_canonical_final_once() {
        use std::io::{BufRead, BufReader, Write};
        use std::os::unix::net::UnixListener;
        use std::time::Duration;

        let root = tempfile::tempdir().unwrap();
        let socket = root.path().join("daemon.sock");
        let listener = UnixListener::bind(&socket).unwrap();
        let (started, first_closed) = (
            std::sync::mpsc::channel::<()>(),
            std::sync::mpsc::channel::<()>(),
        );
        let server = std::thread::spawn(move || {
            let write_line = |stream: &mut std::os::unix::net::UnixStream,
                              value: serde_json::Value| {
                writeln!(stream, "{value}").unwrap();
                stream.flush().unwrap();
            };
            let (mut stream, _) = listener.accept().unwrap();
            let mut line = String::new();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            let request: serde_json::Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["params"]["replay_tail_version"], 1);
            assert_eq!(request["params"]["since"], 0);
            write_line(
                &mut stream,
                serde_json::json!({
                    "jsonrpc":"2.0","id":1,"result":{
                        "subscription_id":"sub_live",
                        "cursor":2,
                        "replayed":0,
                        "event_mode":"canonical",
                        "replay_boundary":{
                            "version":1,
                            "session_id":"sess-reconnect",
                            "runtime_id":"rt",
                            "runtime_epoch":"ep",
                            "runtime_process_epoch":3,
                            "requested_since":0,
                            "durable_cursor":2,
                            "durable_replayed":0,
                            "transient_tail_revision":0,
                            "transient_snapshots":0,
                            "buffered_live":0
                        }
                    }
                }),
            );
            for (kind, sequence, extra) in [
                ("reset", 1, serde_json::json!({})),
                ("delta", 2, serde_json::json!({"delta":"Hel"})),
                ("delta", 3, serde_json::json!({"delta":"lo"})),
            ] {
                let mut payload = extra;
                payload["generation"] = serde_json::json!(1);
                payload["sequence"] = serde_json::json!(sequence);
                payload["phase"] = serde_json::json!("final_answer");
                write_line(
                    &mut stream,
                    serde_json::json!({
                        "jsonrpc":"2.0","method":"event","params":{
                            "type":format!("assistant.message.{kind}"),
                            "session_id":"sess-reconnect",
                            "run_id":"run-live",
                            "payload":payload
                        }
                    }),
                );
            }
            let _ = started.0.send(());
            let _ = first_closed.1.recv_timeout(Duration::from_secs(2));
            drop(stream);

            let (mut stream, _) = listener.accept().unwrap();
            line.clear();
            BufReader::new(stream.try_clone().unwrap())
                .read_line(&mut line)
                .unwrap();
            let request: serde_json::Value = serde_json::from_str(&line).unwrap();
            assert_eq!(request["params"]["since"], 2);
            write_line(
                &mut stream,
                serde_json::json!({
                    "jsonrpc":"2.0","method":"event","params":{
                        "type":"assistant.message.snapshot",
                        "session_id":"sess-reconnect",
                        "run_id":"run-live",
                        "payload":{
                            "generation":1,"sequence":3,"phase":"final_answer",
                            "content":"Hello","tail_revision":4,"state":"open"
                        }
                    }
                }),
            );
            write_line(
                &mut stream,
                serde_json::json!({
                    "jsonrpc":"2.0","method":"event","params":{
                        "type":"ModelResponded",
                        "session_id":"sess-reconnect",
                        "run_id":"run-live",
                        "payload":{"text":"Hello, world"}
                    }
                }),
            );
            write_line(
                &mut stream,
                serde_json::json!({
                    "jsonrpc":"2.0","id":1,"result":{
                        "subscription_id":"sub_reconnect",
                        "cursor":2,
                        "replayed":0,
                        "event_mode":"canonical",
                        "replay_boundary":{
                            "version":1,
                            "session_id":"sess-reconnect",
                            "runtime_id":"rt",
                            "runtime_epoch":"ep",
                            "runtime_process_epoch":3,
                            "requested_since":2,
                            "durable_cursor":2,
                            "durable_replayed":0,
                            "transient_tail_revision":4,
                            "transient_snapshots":1,
                            "buffered_live":1
                        }
                    }
                }),
            );
        });

        let first = attach_replay_tail_v1(
            &socket,
            &ReplayTailAttachRequest {
                session_id: "sess-reconnect".into(),
                since: 0,
                runtime_id: "rt".into(),
                runtime_epoch: "ep".into(),
                runtime_process_epoch: 3,
            },
        )
        .unwrap();
        started.1.recv_timeout(Duration::from_secs(2)).unwrap();
        let mut live = Vec::new();
        for _ in 0..3 {
            live.push(
                first
                    .live
                    .recv_timeout(Duration::from_secs(1))
                    .unwrap()
                    .unwrap(),
            );
        }
        let _ = first_closed.0.send(());
        drop(first.live);
        let mut reducer = crate::transcript::TranscriptReducer::default();
        let mut live_blocks = Vec::new();
        for received in live {
            reducer.reduce_event(&mut live_blocks, received.event);
        }
        assert_eq!(
            assistant_bodies(&live_blocks),
            vec![("assistant:run-live", "Hello")]
        );

        let attached = attach_replay_tail_v1(
            &socket,
            &ReplayTailAttachRequest {
                session_id: "sess-reconnect".into(),
                since: 2,
                runtime_id: "rt".into(),
                runtime_epoch: "ep".into(),
                runtime_process_epoch: 3,
            },
        )
        .unwrap();
        assert_eq!(attached.catch_up.len(), 2);
        let items = vec![reconnect_item("user", "run-live", "prompt")];
        let (_reducer, blocks, _) = hydrate_reconnect_blocks(items, attached.catch_up);
        assert_eq!(
            assistant_bodies(&blocks),
            vec![("assistant:run-live", "Hello, world")],
            "canonical final must replace the transient tail exactly once: {blocks:?}"
        );
        server.join().unwrap();
    }

    #[test]
    fn reconnect_reading_survives_insert_remove_disclosure_and_follow_mode() {
        let tool = reading_block("tool:keep", crate::transcript::BlockKind::Tool, "tool body");
        let selected = reading_block(
            "assistant:keep",
            crate::transcript::BlockKind::Assistant,
            "line0\nline1\nline2",
        );
        let before = vec![
            reading_block("user:old", crate::transcript::BlockKind::User, "old prompt"),
            reading_block(
                "user:before",
                crate::transcript::BlockKind::User,
                "before selection",
            ),
            tool.clone(),
            selected.clone(),
        ];
        let mut disclosure = std::collections::HashMap::new();
        disclosure.insert("tool:keep".into(), true);
        let paused = reading_state::capture_reading_state(
            "sess-reconnect",
            &before,
            Some(3),
            &disclosure,
            false,
            Some(reading_state::LogicalTranscriptAnchorV1 {
                block_id: "assistant:keep".into(),
                logical_line: 1,
                wrapped_sub_row: 0,
                position_hint: 3,
                previous_block_id: Some("tool:keep".into()),
                next_block_id: None,
            }),
            Vec::new(),
        );
        let following = reading_state::capture_reading_state(
            "sess-reconnect",
            &before,
            Some(3),
            &disclosure,
            true,
            Some(reading_state::LogicalTranscriptAnchorV1 {
                block_id: "assistant:keep".into(),
                logical_line: 1,
                wrapped_sub_row: 0,
                position_hint: 3,
                previous_block_id: Some("tool:keep".into()),
                next_block_id: None,
            }),
            Vec::new(),
        );
        assert!(following.top_visible.is_none());

        let after = vec![
            reading_block(
                "user:inserted",
                crate::transcript::BlockKind::User,
                "inserted before selection",
            ),
            tool,
            selected,
            reading_block(
                "user:after",
                crate::transcript::BlockKind::User,
                "appended after",
            ),
        ];
        let mut ledger = crate::native_scrollback::ScrollbackLedger::default();
        let restored =
            reading_state::restore_reading_state(&paused, "sess-reconnect", &after, &mut ledger)
                .unwrap();
        assert_eq!(
            after[restored.selected_index.unwrap()].id,
            "assistant:keep",
            "selection must follow the stable block ID across insert/remove"
        );
        assert_eq!(restored.selected_index, Some(2));
        assert_eq!(restored.disclosure_overrides.len(), 1);
        assert!(restored.disclosure_overrides["tool:keep"]);
        assert!(!restored.follow_bottom);
        let anchor = restored.top_visible.expect("paused reader keeps an anchor");
        assert_eq!(anchor.block_id, "assistant:keep");
        assert_eq!(anchor.logical_line, 1);
        assert_eq!(anchor.wrapped_sub_row, 0);

        let restored_follow =
            reading_state::restore_reading_state(&following, "sess-reconnect", &after, &mut ledger)
                .unwrap();
        assert!(restored_follow.follow_bottom);
        assert!(restored_follow.top_visible.is_none());
        assert_eq!(
            after[restored_follow.selected_index.unwrap()].id,
            "assistant:keep"
        );
    }

    #[test]
    fn reconnect_cursor_zero_does_not_let_old_run_own_hydrated_foreground() {
        let items = vec![
            reconnect_item("user", "run-new", "hello"),
            reconnect_item("assistant", "run-new", "new final"),
        ];
        let catch_up = vec![
            reconnect_event(
                "ExecutionCompleted",
                "run-old",
                0,
                serde_json::json!({"summary": "stale old final"}),
            ),
            reconnect_event(
                "ModelResponded",
                "run-old",
                0,
                serde_json::json!({"text": "stale old final"}),
            ),
        ];
        let (_reducer, blocks, _) = hydrate_reconnect_blocks(items, catch_up);
        assert_eq!(
            assistant_bodies(&blocks)
                .into_iter()
                .filter(|(id, _)| *id == "assistant:run-new")
                .collect::<Vec<_>>(),
            vec![("assistant:run-new", "new final")]
        );
        assert!(!execution_event_owns_projection(
            None,
            Some("run-new"),
            "run-old",
            Some(ExecutionLifecycle::Completed),
        ));
        assert!(execution_event_owns_projection(
            None,
            Some("run-new"),
            "run-new",
            Some(ExecutionLifecycle::Completed),
        ));
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
                source_credential_owner: String::new(),
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
            model_preference_revision: 0,
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
        assert_eq!(SETTINGS_ITEM_COUNT, 10);
        assert_eq!(settings_action(1), Some(Action::OpenProvider));
        assert_eq!(settings_action(3), Some(Action::TogglePlanMode));
        assert_eq!(settings_action(4), Some(Action::ToggleDensity));
        assert_eq!(settings_action(5), Some(Action::OpenGlyphPreview));
        assert_eq!(settings_action(6), Some(Action::OpenStatus));
        assert_eq!(settings_action(9), Some(Action::CloseOverlay));
        assert_eq!(settings_action(10), None);
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
    fn hydrated_tool_artifact_refs_are_scoped_and_resolve_group_member_ids() {
        let items: Vec<SessionItemEvent> = serde_json::from_value(serde_json::json!([
            {
                "type": "item.completed",
                "session_id": "sess-current",
                "turn_id": "run-1",
                "item_id": "call-1",
                "item": {
                    "id": "call-1",
                    "type": "tool_call",
                    "status": "completed",
                    "task_id": "run-1",
                    "details": {
                        "tool": "extension.run",
                        "artifact_ids": ["artifact-1", "artifact-2"]
                    }
                }
            },
            {
                "type": "item.completed",
                "session_id": "sess-current",
                "turn_id": "run-1",
                "item_id": "call-without-artifact",
                "item": {
                    "id": "call-without-artifact",
                    "type": "tool_call",
                    "status": "completed",
                    "details": {"tool": "read"}
                }
            }
        ]))
        .unwrap();

        let references = tool_artifact_refs_from_items(&items);
        assert_eq!(references.len(), 1);
        assert_eq!(references["call-1"].session_id, "sess-current");
        assert_eq!(references["call-1"].run_id, "run-1");
        assert_eq!(references["call-1"].artifact_id, "artifact-1");

        let mut block = TranscriptBlock::local_user("tool:call-1".into(), String::new());
        block.kind = crate::transcript::BlockKind::Tool;
        assert_eq!(tool_component_call_ids(&block), vec!["call-1"]);
        block.tool_members = vec![crate::transcript::ToolGroupMember {
            id: "tool:call-2".into(),
            tool_name: "read".into(),
            title: String::new(),
            body: String::new(),
            body_kind: crate::transcript::BlockBodyKind::Plain,
            additions: 0,
            deletions: 0,
            status: String::new(),
            lifecycle: "completed".into(),
        }];
        block.tool_members.push(crate::transcript::ToolGroupMember {
            id: "tool:call-3".into(),
            tool_name: "read".into(),
            title: String::new(),
            body: String::new(),
            body_kind: crate::transcript::BlockBodyKind::Plain,
            additions: 0,
            deletions: 0,
            status: String::new(),
            lifecycle: "completed".into(),
        });
        assert_eq!(tool_component_call_ids(&block), vec!["call-2", "call-3"]);
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
            model_preference_revision: 0,
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
    fn plan_review_keys_keep_approval_revision_comment_and_close_distinct() {
        assert_eq!(
            plan_review_key_action(KeyEvent::new(KeyCode::Esc, KeyModifiers::NONE)),
            Some(Action::RevisePlan)
        );
        assert_eq!(
            plan_review_key_action(KeyEvent::new(KeyCode::Char('s'), KeyModifiers::NONE)),
            Some(Action::RevisePlan)
        );
        assert_eq!(
            plan_review_key_action(KeyEvent::new(KeyCode::Char('c'), KeyModifiers::NONE)),
            Some(Action::BeginPlanComment)
        );
        assert_eq!(
            plan_review_key_action(KeyEvent::new(KeyCode::Char('q'), KeyModifiers::NONE)),
            Some(Action::CancelPlan)
        );
        assert_eq!(
            plan_review_key_action(KeyEvent::new(KeyCode::Enter, KeyModifiers::NONE)),
            Some(Action::ApprovePlan)
        );
        for modifiers in [
            KeyModifiers::CONTROL,
            KeyModifiers::ALT,
            KeyModifiers::SUPER,
            KeyModifiers::SHIFT,
        ] {
            assert_eq!(
                plan_review_key_action(KeyEvent::new(KeyCode::Enter, modifiers)),
                None
            );
        }
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
