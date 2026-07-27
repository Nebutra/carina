mod render;

use std::collections::VecDeque;
use std::fs;
use std::io::{self, Write};
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
use ratatui::{TerminalOptions, Viewport};
use xai_ratatui_inline::Terminal;
use xai_ratatui_textarea::{TextArea, TextAreaState};

use crate::component::{Action, InteractionMap};
use crate::overlay::{
    ApprovalScope, CheckpointOverlay, CheckpointStep, Overlay, OverlayStack, SettingsOverlay,
};
use crate::rpc::{
    Checkpoint, CheckpointPreview, CheckpointRestoreResult, Client, GovernanceId, Model,
    ModelInventory, RpcError, RuntimeInitialize, Session, SessionItemEvent, Task, WireEvent,
    spawn_event_stream,
};
use crate::theme::Theme;
use crate::transcript::TranscriptBlock;

const LOCALES: &[(&str, &str)] = &[
    ("en", "English"),
    ("zh-Hans", "简体中文"),
    ("zh-Hant", "繁體中文"),
    ("ja", "日本語"),
    ("ko", "한국어"),
    ("es", "Español"),
    ("fr", "Français"),
];
const REWIND_PRIME_WINDOW: Duration = Duration::from_millis(500);

#[derive(Debug, Clone)]
pub struct Options {
    pub socket: PathBuf,
    pub workspace: PathBuf,
    pub session_id: Option<String>,
    pub locale: Option<String>,
    pub locale_path: Option<PathBuf>,
    pub carina_bin: Option<PathBuf>,
    pub no_alt_screen: bool,
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

enum AsyncMessage {
    CredentialStored {
        generation: u64,
        provider: String,
        result: Result<(), String>,
    },
    Event {
        generation: u64,
        value: Box<Result<WireEvent, RpcError>>,
    },
    Reconnect {
        generation: u64,
    },
    CheckpointList {
        generation: u64,
        session_id: String,
        result: Result<Vec<Checkpoint>, String>,
    },
    CheckpointPreview {
        generation: u64,
        session_id: String,
        checkpoint_id: String,
        result: Result<CheckpointPreview, String>,
    },
    CheckpointRestore {
        generation: u64,
        session_id: String,
        checkpoint_id: String,
        result: Result<CheckpointRestoreOutcome, String>,
    },
    CheckpointResume {
        generation: u64,
        session_id: String,
        task_id: String,
        result: Result<CheckpointResumeOutcome, String>,
    },
}

struct CheckpointRestoreOutcome {
    restore: CheckpointRestoreResult,
    session: Option<Session>,
    items: Option<Vec<SessionItemEvent>>,
    refresh_error: Option<String>,
}

struct CheckpointResumeOutcome {
    task: Task,
    session: Option<Session>,
    items: Option<Vec<SessionItemEvent>>,
    refresh_error: Option<String>,
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
    model_index: usize,
    session_index: usize,
    selected_model: String,
    active_session: Option<Session>,
    blocks: Vec<TranscriptBlock>,
    composer: TextArea,
    composer_state: TextAreaState,
    composer_area: Rect,
    transcript_area: Rect,
    transcript_scroll: usize,
    history_selected: Option<usize>,
    history_stashed_draft: Option<String>,
    history_original_scroll: Option<usize>,
    rewind_primed_at: Option<Instant>,
    credential: String,
    credential_generation: u64,
    credential_pending: bool,
    credential_child: Arc<Mutex<Option<Child>>>,
    notice: String,
    interactions: InteractionMap,
    overlays: OverlayStack,
    active_task_id: Option<String>,
    task_status: String,
    queued_prompts: VecDeque<String>,
    event_generation: u64,
    event_cursor: usize,
    checkpoint_generation: u64,
    checkpoint_resume_pending: bool,
    theme: Theme,
    async_tx: Sender<AsyncMessage>,
    async_rx: Receiver<AsyncMessage>,
    quit: bool,
    outcome: Outcome,
    dirty: bool,
}

impl App {
    fn bootstrap(options: Options) -> Result<Self> {
        let mut rpc = Client::connect(&options.socket)
            .with_context(|| format!("connect {}", options.socket.display()))?;
        let runtime = rpc.initialize().context("initialize runtime protocol")?;
        let inventory = rpc
            .model_inventory()
            .context("load provider/model inventory")?;
        let mut sessions = rpc.sessions().context("load sessions")?;
        sessions.sort_by(|left, right| right.created_at.cmp(&left.created_at));
        let models = inventory.available_models();
        let locale_index = options
            .locale
            .as_deref()
            .and_then(|locale| LOCALES.iter().position(|(id, _)| *id == locale))
            .unwrap_or(0);
        let phase = if options.locale.is_none() {
            Phase::Locale
        } else if !inventory.has_runnable_provider() {
            Phase::Provider
        } else {
            Phase::Model
        };
        let model_index = models
            .iter()
            .position(|model| model.id == inventory.default_model)
            .unwrap_or(0);
        let selected_model = models
            .get(model_index)
            .map(|model| model.id.clone())
            .unwrap_or_default();
        let mut composer = TextArea::new();
        composer.show_scrollbar = false;
        composer.set_tab_width(4);
        let (async_tx, async_rx) = mpsc::channel();

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
            provider_index: 0,
            model_index,
            session_index: 0,
            selected_model,
            active_session: None,
            blocks: Vec::new(),
            composer,
            composer_state: TextAreaState::default(),
            composer_area: Rect::default(),
            transcript_area: Rect::default(),
            transcript_scroll: 0,
            history_selected: None,
            history_stashed_draft: None,
            history_original_scroll: None,
            rewind_primed_at: None,
            credential: String::new(),
            credential_generation: 0,
            credential_pending: false,
            credential_child: Arc::new(Mutex::new(None)),
            notice: String::new(),
            interactions: InteractionMap::default(),
            overlays: OverlayStack::default(),
            active_task_id: None,
            task_status: "ready".into(),
            queued_prompts: VecDeque::new(),
            event_generation: 0,
            event_cursor: 0,
            checkpoint_generation: 0,
            checkpoint_resume_pending: false,
            theme: Theme::carina(std::env::var_os("NO_COLOR").is_some()),
            async_tx,
            async_rx,
            quit: false,
            outcome: Outcome::Ok,
            dirty: true,
        };
        app.resume_requested_session()?;
        Ok(app)
    }

    fn resume_requested_session(&mut self) -> Result<()> {
        let Some(session_id) = self.options.session_id.clone() else {
            return Ok(());
        };
        if self.phase == Phase::Locale || self.phase == Phase::Provider {
            return Ok(());
        }
        let session = self
            .rpc
            .resume_session(&session_id)
            .with_context(|| format!("resume session {session_id}"))?;
        if !session.next_model.is_empty() {
            self.selected_model = session.next_model.clone();
        }
        self.open_session(session)?;
        Ok(())
    }

    fn route_after_locale(&mut self) {
        self.phase = if self.inventory.has_runnable_provider() {
            Phase::Model
        } else {
            Phase::Provider
        };
        self.focus = Focus::Scene;
        self.notice.clear();
    }

    fn open_session(&mut self, session: Session) -> Result<()> {
        self.invalidate_checkpoint_requests();
        let session = self.hydrate_session_snapshot(session);
        let items = self
            .rpc
            .items(&session.session_id)
            .with_context(|| format!("load session {}", session.session_id))?;
        self.blocks = items.into_iter().map(TranscriptBlock::from_item).collect();
        self.transcript_scroll = self.blocks.len().saturating_sub(1);
        self.active_task_id = matches!(
            session.task_status.as_str(),
            "queued" | "running" | "waiting_input" | "waiting_approval"
        )
        .then(|| session.latest_task_id.clone())
        .filter(|task_id| !task_id.is_empty());
        self.task_status = if session.task_status.is_empty() {
            "ready".into()
        } else {
            session.task_status.clone()
        };
        if session.task_status == "paused" && !session.latest_task_id.is_empty() {
            self.notice = format!(
                "Task {} is paused; resume it or inspect checkpoints",
                session.latest_task_id
            );
        }
        self.remember_session(session);
        self.overlays = OverlayStack::default();
        self.phase = Phase::Conversation;
        self.focus = Focus::Composer;
        self.event_cursor = 0;
        self.start_event_stream();
        Ok(())
    }

    fn hydrate_session_snapshot(&self, mut session: Session) -> Session {
        let Some(snapshot) = self
            .sessions
            .iter()
            .find(|candidate| candidate.session_id == session.session_id)
        else {
            return session;
        };
        session.latest_task_id = snapshot.latest_task_id.clone();
        session.task_status = snapshot.task_status.clone();
        session.summary = snapshot.summary.clone();
        session.continuity = snapshot.continuity.clone();
        session
    }

    fn remember_session(&mut self, session: Session) {
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
    }

    fn update_active_task_snapshot(&mut self, task_id: &str, status: &str) {
        let Some(mut session) = self.active_session.as_ref().cloned() else {
            return;
        };
        session.latest_task_id = task_id.to_owned();
        session.task_status = status.to_owned();
        self.remember_session(session);
    }

    fn open_checkpoints(&mut self) {
        if self.active_task_id.is_some() {
            self.notice =
                "Finish or cancel the active task before opening checkpoint recovery".into();
            return;
        }
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            self.notice = "Open a conversation before browsing checkpoints".into();
            return;
        };
        let generation = self.next_checkpoint_generation();
        self.overlays
            .replace(Overlay::Checkpoint(CheckpointOverlay::loading(
                session_id.clone(),
            )));
        self.notice = "Loading checkpoints...".into();
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.checkpoints(&session_id))
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::CheckpointList {
                generation,
                session_id,
                result,
            });
        });
    }

    fn next_checkpoint_generation(&mut self) -> u64 {
        self.checkpoint_generation = self.checkpoint_generation.saturating_add(1);
        self.checkpoint_generation
    }

    fn invalidate_checkpoint_requests(&mut self) {
        self.checkpoint_generation = self.checkpoint_generation.saturating_add(1);
        self.checkpoint_resume_pending = false;
    }

    fn checkpoint_target_is_current(&self, generation: u64, session_id: &str) -> bool {
        generation == self.checkpoint_generation
            && self
                .active_session
                .as_ref()
                .is_some_and(|session| session.session_id == session_id)
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
                self.route_after_locale();
            }
            None => self.notice = "Could not persist the language choice".into(),
        }
    }

    fn begin_credential(&mut self) {
        if self.inventory.providers.is_empty() {
            self.phase = Phase::Diagnostic;
            self.notice = "No provider definitions are available".into();
            return;
        }
        self.credential.clear();
        self.credential_pending = false;
        self.phase = Phase::Credential;
        self.notice.clear();
    }

    fn submit_credential(&mut self) {
        if self.credential_pending || self.credential.trim().is_empty() {
            return;
        }
        let Some(carina_bin) = self.options.carina_bin.clone() else {
            self.notice = "Internal carina command path is unavailable".into();
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
        self.notice = "Validating credential...".into();
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
        while let Ok(message) = self.async_rx.try_recv() {
            match message {
                AsyncMessage::CredentialStored {
                    generation,
                    provider,
                    result,
                } => {
                    if generation != self.credential_generation {
                        continue;
                    }
                    self.credential_pending = false;
                    match result {
                        Ok(()) => match self.rpc.model_inventory() {
                            Ok(inventory) => {
                                self.inventory = inventory;
                                self.models = self.inventory.available_models();
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
                                if self.inventory.has_runnable_provider() {
                                    self.phase = Phase::Model;
                                    self.notice = format!("{provider} is ready");
                                } else {
                                    self.phase = Phase::Provider;
                                    self.notice =
                                        format!("{provider} was stored but is not runnable");
                                }
                            }
                            Err(_) => {
                                self.phase = Phase::Provider;
                                self.notice =
                                    "Credential stored; provider inventory refresh failed".into();
                            }
                        },
                        Err(message) => self.notice = message,
                    }
                }
                AsyncMessage::Event { generation, value } => {
                    if generation != self.event_generation {
                        continue;
                    }
                    match *value {
                        Ok(event) => {
                            self.event_cursor = self.event_cursor.max(event.raw_cursor);
                            let governance_resolution = event.governance_resolution();
                            let governance_changed = self.overlays.reconcile_event(&event);
                            if governance_changed {
                                match governance_resolution {
                                    Some(GovernanceId::Approval(id)) => {
                                        self.notice = format!("Approval {id} durably resolved")
                                    }
                                    Some(GovernanceId::Question(id)) => {
                                        self.notice = format!("Question {id} durably resolved")
                                    }
                                    None => {}
                                }
                            }
                            let projected_status = event.projected_status().to_owned();
                            if let Some(status) = event.task_activity_status() {
                                self.active_task_id = Some(event.task_id.clone());
                                self.task_status = status.to_owned();
                                self.update_active_task_snapshot(&event.task_id, status);
                            }
                            if event.clears_active_task() {
                                if self.active_task_id.as_deref() == Some(event.task_id.as_str()) {
                                    self.active_task_id = None;
                                }
                                self.task_status = if projected_status.is_empty() {
                                    "completed".into()
                                } else {
                                    projected_status
                                };
                                let task_status = self.task_status.clone();
                                self.update_active_task_snapshot(&event.task_id, &task_status);
                            }
                            let block = TranscriptBlock::from_event(event);
                            if let Some(existing) =
                                self.blocks.iter_mut().find(|item| item.id == block.id)
                            {
                                *existing = block;
                            } else {
                                self.blocks.push(block);
                            }
                            self.transcript_scroll = self.blocks.len().saturating_sub(1);
                            if self.active_task_id.is_none()
                                && let Some(prompt) = self.queued_prompts.pop_front()
                                && let Err(error) = self.submit_new_prompt(prompt)
                            {
                                self.notice = format!("Queued prompt failed: {error}");
                            }
                        }
                        Err(error) => {
                            self.notice =
                                format!("Event stream interrupted: {error}; reconnecting...");
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
                        self.start_event_stream();
                        self.notice = "Reconnecting event stream...".into();
                    }
                }
                AsyncMessage::CheckpointList {
                    generation,
                    session_id,
                    result,
                } => {
                    if !self.checkpoint_target_is_current(generation, &session_id) {
                        continue;
                    }
                    match result {
                        Ok(checkpoints) => {
                            let count = checkpoints.len();
                            self.overlays
                                .replace(Overlay::Checkpoint(CheckpointOverlay::loaded(
                                    session_id,
                                    checkpoints,
                                )));
                            self.sync_checkpoint_selection();
                            self.notice = if count == 0 {
                                "No checkpoints are available for this session".into()
                            } else {
                                format!(
                                    "{count} checkpoint{} available",
                                    if count == 1 { "" } else { "s" }
                                )
                            };
                        }
                        Err(error) => {
                            if let Some(Overlay::Checkpoint(checkpoint)) =
                                self.overlays.active_mut()
                            {
                                checkpoint.step = CheckpointStep::List;
                                checkpoint.error = format!("Checkpoint list failed: {error}");
                            }
                        }
                    }
                }
                AsyncMessage::CheckpointPreview {
                    generation,
                    session_id,
                    checkpoint_id,
                    result,
                } => {
                    if !self.checkpoint_target_is_current(generation, &session_id) {
                        continue;
                    }
                    let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut() else {
                        continue;
                    };
                    if checkpoint.session_id != session_id
                        || !matches!(
                            &checkpoint.step,
                            CheckpointStep::Previewing { checkpoint_id: target }
                                if target == &checkpoint_id
                        )
                    {
                        continue;
                    }
                    match result {
                        Ok(preview) => {
                            checkpoint.error.clear();
                            checkpoint.step = CheckpointStep::Preview(preview);
                        }
                        Err(error) => {
                            checkpoint.step = CheckpointStep::List;
                            checkpoint.error = format!("Preview failed: {error}");
                        }
                    }
                }
                AsyncMessage::CheckpointRestore {
                    generation,
                    session_id,
                    checkpoint_id,
                    result,
                } => self.apply_checkpoint_restore(generation, &session_id, &checkpoint_id, result),
                AsyncMessage::CheckpointResume {
                    generation,
                    session_id,
                    task_id,
                    result,
                } => self.apply_checkpoint_resume(generation, &session_id, &task_id, result),
            }
            self.dirty = true;
        }
        if self
            .rewind_primed_at
            .is_some_and(|primed| primed.elapsed() > REWIND_PRIME_WINDOW)
        {
            self.rewind_primed_at = None;
            if self.notice == "Press Esc again to edit an earlier prompt" {
                self.notice.clear();
            }
            self.dirty = true;
        }
    }

    fn handle_event(&mut self, event: Event) -> Result<()> {
        match event {
            Event::Key(key) if key.kind == KeyEventKind::Press => self.handle_key(key)?,
            Event::Mouse(mouse) => self.handle_mouse(mouse),
            Event::Paste(value) if self.phase == Phase::Conversation => {
                self.composer.insert_str(&value.replace('\r', ""));
                self.focus = Focus::Composer;
            }
            Event::Resize(_, _) | Event::FocusGained | Event::FocusLost => self.dirty = true,
            _ => {}
        }
        Ok(())
    }

    fn handle_key(&mut self, key: KeyEvent) -> Result<()> {
        if key.code == KeyCode::Char('c') && key.modifiers.contains(KeyModifiers::CONTROL) {
            if self.close_top_non_governance() {
                self.dirty = true;
                return Ok(());
            }
            if let Some(task_id) = self.active_task_id.clone() {
                self.cancel_task(&task_id);
            } else {
                self.quit = true;
            }
            return Ok(());
        }
        if self.overlays.active().is_some() {
            self.handle_overlay_key(key);
            self.dirty = true;
            return Ok(());
        }
        match self.phase {
            Phase::Locale => match key.code {
                KeyCode::Up => self.locale_index = self.locale_index.saturating_sub(1),
                KeyCode::Down => self.locale_index = (self.locale_index + 1).min(LOCALES.len() - 1),
                KeyCode::Enter => self.select_locale(),
                KeyCode::Esc => {
                    self.outcome = Outcome::Usage;
                    self.quit = true;
                }
                _ => {}
            },
            Phase::Provider => match key.code {
                KeyCode::Up => self.provider_index = self.provider_index.saturating_sub(1),
                KeyCode::Down => {
                    self.provider_index = (self.provider_index + 1)
                        .min(self.inventory.providers.len().saturating_sub(1));
                }
                KeyCode::Enter => self.begin_credential(),
                KeyCode::Char('d') => self.phase = Phase::Diagnostic,
                KeyCode::Esc => {
                    self.outcome = Outcome::Degraded;
                    self.quit = true;
                }
                _ => {}
            },
            Phase::Credential => self.handle_credential_key(key),
            Phase::Model => match key.code {
                KeyCode::Up => self.model_index = self.model_index.saturating_sub(1),
                KeyCode::Down => {
                    self.model_index =
                        (self.model_index + 1).min(self.models.len().saturating_sub(1));
                }
                KeyCode::Enter => {
                    if let Some(model) = self.models.get(self.model_index) {
                        self.selected_model = model.id.clone();
                        self.phase = Phase::Session;
                        self.notice.clear();
                    }
                }
                KeyCode::Esc => self.phase = Phase::Provider,
                _ => {}
            },
            Phase::Session => match key.code {
                KeyCode::Up => self.session_index = self.session_index.saturating_sub(1),
                KeyCode::Down => {
                    self.session_index = (self.session_index + 1).min(self.sessions.len());
                }
                KeyCode::Enter => {
                    let session = if self.session_index == self.sessions.len() {
                        self.rpc
                            .create_session(&self.options.workspace.to_string_lossy())?
                    } else {
                        let id = self.sessions[self.session_index].session_id.clone();
                        self.rpc.resume_session(&id)?
                    };
                    self.open_session(session)?;
                }
                KeyCode::Esc => self.phase = Phase::Model,
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
                self.notice = "Credential validation cancelled".into();
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
        if self.history_selected.is_some() {
            return self.handle_history_key(key);
        }
        match key.code {
            KeyCode::Esc => {
                self.handle_rewind_escape();
            }
            KeyCode::Char(',') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                self.overlays
                    .replace(Overlay::Settings(SettingsOverlay { selected: 0 }));
            }
            KeyCode::Char('c') if key.modifiers.contains(KeyModifiers::CONTROL) => {
                if let Some(task_id) = self.active_task_id.clone() {
                    self.cancel_task(&task_id);
                }
            }
            KeyCode::PageUp => {
                self.transcript_scroll = self.transcript_scroll.saturating_sub(5);
            }
            KeyCode::PageDown => {
                self.transcript_scroll =
                    (self.transcript_scroll + 5).min(self.blocks.len().saturating_sub(1));
            }
            KeyCode::Enter if !key.modifiers.contains(KeyModifiers::SHIFT) => {
                self.submit_prompt()?;
            }
            KeyCode::Tab if self.active_task_id.is_some() => {
                let prompt = self.composer.text().trim().to_owned();
                if !prompt.is_empty() {
                    self.queued_prompts.push_back(prompt);
                    self.composer.set_text("");
                    self.composer_state = TextAreaState::default();
                    self.notice = format!(
                        "Queued {} follow-up{}",
                        self.queued_prompts.len(),
                        if self.queued_prompts.len() == 1 {
                            ""
                        } else {
                            "s"
                        }
                    );
                }
            }
            _ => {
                self.rewind_primed_at = None;
                self.composer.input(key)
            }
        }
        Ok(())
    }

    fn submit_prompt(&mut self) -> Result<()> {
        let prompt = self.composer.text().trim().to_owned();
        if prompt.is_empty() {
            return Ok(());
        }
        if self.handle_slash_command(&prompt) {
            self.composer.set_text("");
            self.composer_state = TextAreaState::default();
            return Ok(());
        }
        if let Some(task_id) = self.active_task_id.clone() {
            return self.submit_steer(task_id, prompt);
        }
        self.submit_new_prompt(prompt)
    }

    fn submit_new_prompt(&mut self, prompt: String) -> Result<()> {
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
            .ok_or_else(|| anyhow!("conversation has no active session"))?;
        let local_id = operation_id("local");
        let submission_id = operation_id("tui");
        self.blocks.push(TranscriptBlock::local_user(
            local_id.clone(),
            prompt.clone(),
        ));
        self.composer.set_text("");
        self.composer_state = TextAreaState::default();
        match self
            .rpc
            .submit(&session_id, &prompt, &self.selected_model, &submission_id)
        {
            Ok(task) => {
                if let Some(block) = self.blocks.iter_mut().find(|block| block.id == local_id) {
                    block.id = format!("user:{}", task.task_id);
                    block.task_id = task.task_id.clone();
                    block.branchable = true;
                }
                self.active_task_id = Some(task.task_id.clone());
                self.task_status = if task.status.is_empty() {
                    "queued".into()
                } else {
                    task.status.clone()
                };
                self.notice = format!("Task {} {}", task.task_id, self.task_status);
            }
            Err(error) => self.notice = format!("Submit failed: {error}"),
        }
        self.transcript_scroll = self.blocks.len().saturating_sub(1);
        Ok(())
    }

    fn submit_steer(&mut self, task_id: String, prompt: String) -> Result<()> {
        if prompt.is_empty() {
            return Ok(());
        }
        let steer_id = operation_id("steer");
        self.rpc.steer(&task_id, &prompt, &steer_id)?;
        self.blocks.push(TranscriptBlock::local_steer(
            format!("steer:{steer_id}"),
            task_id,
            prompt,
        ));
        self.composer.set_text("");
        self.composer_state = TextAreaState::default();
        self.notice = "Steering message queued for the active task".into();
        self.transcript_scroll = self.blocks.len().saturating_sub(1);
        Ok(())
    }

    fn handle_slash_command(&mut self, prompt: &str) -> bool {
        match prompt.trim() {
            "/settings" => {
                self.overlays
                    .replace(Overlay::Settings(SettingsOverlay { selected: 0 }));
            }
            "/model" => self.phase = Phase::Model,
            "/sessions" => self.phase = Phase::Session,
            "/resume" => {
                let paused = self
                    .active_session
                    .as_ref()
                    .is_some_and(|session| session.task_status == "paused");
                if paused {
                    self.resume_paused_task();
                } else {
                    self.phase = Phase::Session;
                }
            }
            "/checkpoints" | "/checkpoint" => self.open_checkpoints(),
            "/cancel" => {
                if let Some(task_id) = self.active_task_id.clone() {
                    self.cancel_task(&task_id);
                } else {
                    self.notice = "No active task to cancel".into();
                }
            }
            "/quit" | "/exit" => self.quit = true,
            "/help" => {
                self.notice =
                    "/settings  /model  /sessions  /resume  /checkpoints  /cancel  /quit  double-Esc edit history"
                        .into();
            }
            _ => return false,
        }
        true
    }

    fn cancel_task(&mut self, task_id: &str) {
        match self.rpc.cancel_task(task_id) {
            Ok(task) => {
                self.active_task_id = None;
                self.task_status = if task.status.is_empty() {
                    "cancelled".into()
                } else {
                    task.status
                };
                self.notice = format!("Task {task_id} cancellation requested");
            }
            Err(error) => self.notice = format!("Cancel failed: {error}"),
        }
    }

    fn handle_overlay_key(&mut self, key: KeyEvent) {
        let mut deferred = None;
        match self.overlays.active_mut() {
            Some(Overlay::Approval(approval)) => match key.code {
                KeyCode::Left => {
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
                    approval.error = "This task is waiting for a decision. Allow, deny, or Ctrl+C to cancel the task.".into();
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
                                "Answer the question or Ctrl+C to cancel the task.".into();
                        }
                        _ => {}
                    }
                } else {
                    match key.code {
                        KeyCode::Up | KeyCode::Left => {
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
                                "Choose an answer or Ctrl+C to cancel the task.".into();
                        }
                        _ => {}
                    }
                }
            }
            Some(Overlay::Checkpoint(checkpoint)) => match &mut checkpoint.step {
                CheckpointStep::LoadingList => {
                    if key.code == KeyCode::Esc {
                        deferred = Some(Action::CloseOverlay);
                    }
                }
                CheckpointStep::List => match key.code {
                    KeyCode::Up => {
                        deferred = Some(Action::SelectCheckpoint(
                            checkpoint.selected.saturating_sub(1),
                        ))
                    }
                    KeyCode::Down => {
                        deferred = Some(Action::SelectCheckpoint(
                            (checkpoint.selected + 1)
                                .min(checkpoint.checkpoints.len().saturating_sub(1)),
                        ));
                    }
                    KeyCode::Enter if !checkpoint.checkpoints.is_empty() => {
                        deferred = Some(Action::PreviewCheckpoint)
                    }
                    KeyCode::Esc => deferred = Some(Action::CloseOverlay),
                    _ => {}
                },
                CheckpointStep::Previewing { .. } => match key.code {
                    KeyCode::Esc | KeyCode::Backspace => deferred = Some(Action::CheckpointBack),
                    _ => {}
                },
                CheckpointStep::Preview(_) => match key.code {
                    KeyCode::Enter | KeyCode::Char('r') => {
                        deferred = Some(Action::BeginCheckpointRestore)
                    }
                    KeyCode::Esc | KeyCode::Backspace => deferred = Some(Action::CheckpointBack),
                    _ => {}
                },
                CheckpointStep::Confirm(_) => match key.code {
                    KeyCode::Enter | KeyCode::Char('y') => {
                        deferred = Some(Action::ConfirmCheckpointRestore)
                    }
                    KeyCode::Esc | KeyCode::Backspace | KeyCode::Char('n') => {
                        deferred = Some(Action::CheckpointBack)
                    }
                    _ => {}
                },
                CheckpointStep::Restoring(_) => {}
                CheckpointStep::Restored(_) => match key.code {
                    KeyCode::Enter | KeyCode::Char('r') => {
                        deferred = Some(Action::ResumeRestoredTask)
                    }
                    KeyCode::Esc | KeyCode::Char('d') => deferred = Some(Action::CloseOverlay),
                    _ => {}
                },
                CheckpointStep::Resuming(_) => {}
            },
            Some(Overlay::Settings(settings)) => match key.code {
                KeyCode::Up => settings.selected = settings.selected.saturating_sub(1),
                KeyCode::Down => settings.selected = (settings.selected + 1).min(5),
                KeyCode::Enter => match settings.selected {
                    0 => {
                        self.overlays.resolve_active();
                        self.phase = Phase::Locale;
                    }
                    1 => {
                        self.overlays.resolve_active();
                        self.phase = Phase::Model;
                    }
                    2 => {
                        self.overlays.resolve_active();
                        self.phase = Phase::Session;
                    }
                    3 => deferred = Some(Action::OpenCheckpoints),
                    4 => deferred = Some(Action::ResumePausedTask),
                    _ => self.overlays.resolve_active(),
                },
                KeyCode::Esc => self.overlays.resolve_active(),
                _ => {}
            },
            None => {}
        }
        if let Some(action) = deferred {
            match action {
                Action::QuestionOption(usize::MAX) => self.answer_active_question(None),
                other => self.apply_action(other),
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
                self.notice = format!(
                    "{} {} ({scope}); waiting for durable confirmation",
                    if approve { "Allowing" } else { "Denying" },
                    decision_id
                );
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
            if let Some(Overlay::Question(question)) = self.overlays.active_mut() {
                question.error = "An answer is required".into();
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
                self.notice = format!("Answered {question_id}; waiting for durable confirmation");
            }
            Err(error) => {
                if let Some(Overlay::Question(question)) = self.overlays.active_mut() {
                    question.resolving = false;
                    question.error = format!("Answer failed: {error}");
                }
            }
        }
    }

    fn preview_selected_checkpoint(&mut self) {
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        let checkpoint_id = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) => checkpoint
                .selected_checkpoint()
                .map(|checkpoint| checkpoint.checkpoint_id.clone()),
            _ => None,
        };
        let Some(checkpoint_id) = checkpoint_id else {
            return;
        };
        let generation = self.next_checkpoint_generation();
        if let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut() {
            checkpoint.error.clear();
            checkpoint.step = CheckpointStep::Previewing {
                checkpoint_id: checkpoint_id.clone(),
            };
        }
        self.notice = "Loading checkpoint impact...".into();
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = Client::connect(&socket)
                .and_then(|mut rpc| rpc.checkpoint_preview(&session_id, &checkpoint_id))
                .map_err(|error| error.to_string());
            let _ = tx.send(AsyncMessage::CheckpointPreview {
                generation,
                session_id,
                checkpoint_id,
                result,
            });
        });
    }

    fn begin_checkpoint_restore(&mut self) {
        if let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut()
            && let CheckpointStep::Preview(preview) = &checkpoint.step
        {
            checkpoint.step = CheckpointStep::Confirm(preview.clone());
            checkpoint.error.clear();
        }
    }

    fn confirm_checkpoint_restore(&mut self) {
        if self.active_task_id.is_some() {
            if let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut() {
                checkpoint.error =
                    "Finish or cancel the active task before restoring a checkpoint".into();
            }
            return;
        }
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        let checkpoint_id = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) => match &checkpoint.step {
                CheckpointStep::Confirm(preview) => Some(preview.checkpoint.checkpoint_id.clone()),
                _ => None,
            },
            _ => None,
        };
        let Some(checkpoint_id) = checkpoint_id else {
            return;
        };
        let preview = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) => match &checkpoint.step {
                CheckpointStep::Confirm(preview) => preview.clone(),
                _ => return,
            },
            _ => return,
        };
        let generation = self.next_checkpoint_generation();
        if let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut() {
            checkpoint.error.clear();
            checkpoint.step = CheckpointStep::Restoring(preview);
        }
        self.notice = "Restoring conversation and workspace...".into();
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = restore_checkpoint_and_refresh(&socket, &session_id, &checkpoint_id);
            let _ = tx.send(AsyncMessage::CheckpointRestore {
                generation,
                session_id,
                checkpoint_id,
                result,
            });
        });
    }

    fn resume_restored_task(&mut self) {
        let restored = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) => match &checkpoint.step {
                CheckpointStep::Restored(result) => Some(result.clone()),
                _ => None,
            },
            _ => None,
        };
        let Some(restored) = restored else {
            return;
        };
        if let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut() {
            checkpoint.error.clear();
            checkpoint.step = CheckpointStep::Resuming(restored.clone());
        }
        self.start_checkpoint_resume(restored.task_id);
    }

    fn resume_paused_task(&mut self) {
        if let Some(blocker) = self.paused_resume_blocker() {
            self.notice = blocker;
            return;
        }
        let task_id = self
            .active_session
            .as_ref()
            .filter(|session| session.task_status == "paused")
            .map(|session| session.latest_task_id.clone())
            .filter(|task_id| !task_id.is_empty());
        let Some(task_id) = task_id else {
            self.notice = "No paused task is available in this session".into();
            return;
        };
        if matches!(self.overlays.active(), Some(Overlay::Settings(_))) {
            self.overlays.resolve_active();
        }
        self.start_checkpoint_resume(task_id);
    }

    fn paused_resume_blocker(&self) -> Option<String> {
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
        Some(if failed.is_empty() {
            format!("Resume requires continuity review: {reason}")
        } else {
            format!(
                "Resume requires continuity review: {reason}; failed proofs: {}",
                failed.join(", ")
            )
        })
    }

    fn start_checkpoint_resume(&mut self, task_id: String) {
        if self.checkpoint_resume_pending {
            return;
        }
        let Some(session_id) = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
        else {
            return;
        };
        let generation = self.next_checkpoint_generation();
        self.checkpoint_resume_pending = true;
        self.notice = format!("Resuming task {task_id}...");
        let socket = self.options.socket.clone();
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = resume_checkpoint_task_and_refresh(&socket, &session_id, &task_id);
            let _ = tx.send(AsyncMessage::CheckpointResume {
                generation,
                session_id,
                task_id,
                result,
            });
        });
    }

    fn apply_checkpoint_restore(
        &mut self,
        generation: u64,
        session_id: &str,
        checkpoint_id: &str,
        result: Result<CheckpointRestoreOutcome, String>,
    ) {
        if !self.checkpoint_target_is_current(generation, session_id) {
            return;
        }
        let preview = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) if checkpoint.session_id == session_id => {
                match &checkpoint.step {
                    CheckpointStep::Restoring(preview)
                        if preview.checkpoint.checkpoint_id == checkpoint_id =>
                    {
                        Some(preview.clone())
                    }
                    _ => None,
                }
            }
            _ => None,
        };
        let Some(preview) = preview else {
            return;
        };
        let outcome = match result {
            Ok(outcome) => outcome,
            Err(error) => {
                if let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut() {
                    checkpoint.step = CheckpointStep::Confirm(preview);
                    checkpoint.error = format!("Restore failed: {error}");
                }
                self.notice = "Checkpoint restore failed; retry the same checkpoint".into();
                return;
            }
        };

        self.active_task_id = None;
        self.task_status = outcome.restore.status.clone();
        if let Some(mut session) = outcome
            .session
            .or_else(|| self.active_session.as_ref().cloned())
        {
            session.latest_task_id = outcome.restore.task_id.clone();
            session.task_status = outcome.restore.status.clone();
            self.remember_session(session);
        }
        if let Some(items) = outcome.items {
            self.blocks = items.into_iter().map(TranscriptBlock::from_item).collect();
            self.transcript_scroll = self.blocks.len().saturating_sub(1);
        }
        self.event_cursor = 0;
        self.start_event_stream();
        self.notice = if let Some(error) = outcome.refresh_error {
            format!(
                "Restored {} to turn {}; task is paused. Refresh warning: {error}",
                outcome.restore.checkpoint_id, outcome.restore.turn
            )
        } else {
            format!(
                "Restored {} to turn {}; task is paused",
                outcome.restore.checkpoint_id, outcome.restore.turn
            )
        };
        if let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut() {
            checkpoint.error.clear();
            checkpoint.step = CheckpointStep::Restored(outcome.restore);
        }
    }

    fn apply_checkpoint_resume(
        &mut self,
        generation: u64,
        session_id: &str,
        task_id: &str,
        result: Result<CheckpointResumeOutcome, String>,
    ) {
        if !self.checkpoint_target_is_current(generation, session_id) {
            return;
        }
        self.checkpoint_resume_pending = false;
        let restored = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) if checkpoint.session_id == session_id => {
                match &checkpoint.step {
                    CheckpointStep::Resuming(restored) if restored.task_id == task_id => {
                        Some(restored.clone())
                    }
                    _ => None,
                }
            }
            _ => None,
        };
        let outcome = match result {
            Ok(outcome) => outcome,
            Err(error) => {
                if let Some(restored) = restored
                    && let Some(Overlay::Checkpoint(checkpoint)) = self.overlays.active_mut()
                {
                    checkpoint.step = CheckpointStep::Restored(restored);
                    checkpoint.error = format!("Resume failed: {error}");
                }
                self.notice = format!("Resume failed: {error}");
                return;
            }
        };
        self.active_task_id = Some(outcome.task.task_id.clone());
        self.task_status = if outcome.task.status.is_empty() {
            "running".into()
        } else {
            outcome.task.status.clone()
        };
        if let Some(mut session) = outcome
            .session
            .or_else(|| self.active_session.as_ref().cloned())
        {
            session.latest_task_id = outcome.task.task_id.clone();
            session.task_status = self.task_status.clone();
            self.remember_session(session);
        }
        if let Some(items) = outcome.items {
            self.blocks = items.into_iter().map(TranscriptBlock::from_item).collect();
            self.transcript_scroll = self.blocks.len().saturating_sub(1);
        }
        self.event_cursor = 0;
        self.start_event_stream();
        self.notice = if let Some(error) = outcome.refresh_error {
            format!(
                "Resumed task {}; refresh warning: {error}",
                outcome.task.task_id
            )
        } else {
            format!("Resumed task {}", outcome.task.task_id)
        };
        if restored.is_some() {
            self.overlays.resolve_active();
        }
    }

    fn checkpoint_back(&mut self) {
        if self
            .overlays
            .active()
            .is_some_and(|overlay| matches!(overlay, Overlay::Checkpoint(checkpoint) if checkpoint.blocks_close()))
        {
            self.notice = "Checkpoint operation is already running and cannot be hidden".into();
            return;
        }
        self.invalidate_checkpoint_requests();
        let keep_open = match self.overlays.active_mut() {
            Some(Overlay::Checkpoint(checkpoint)) => checkpoint.back(),
            _ => false,
        };
        if !keep_open {
            self.overlays.resolve_active();
        }
    }

    fn select_checkpoint(&mut self, index: usize) {
        let task_id = match self.overlays.active_mut() {
            Some(Overlay::Checkpoint(checkpoint)) => {
                checkpoint.select(index);
                checkpoint
                    .selected_checkpoint()
                    .map(|item| item.task_id.clone())
            }
            _ => None,
        };
        if let Some(task_id) = task_id
            && let Some(anchor) = self
                .blocks
                .iter()
                .rposition(|block| block.task_id == task_id)
        {
            self.transcript_scroll = anchor;
        }
    }

    fn sync_checkpoint_selection(&mut self) {
        let selected = match self.overlays.active() {
            Some(Overlay::Checkpoint(checkpoint)) => checkpoint.selected,
            _ => return,
        };
        self.select_checkpoint(selected);
    }

    fn close_top_non_governance(&mut self) -> bool {
        let Some(overlay) = self.overlays.active() else {
            return false;
        };
        if overlay.is_governance() {
            return false;
        }
        if matches!(overlay, Overlay::Checkpoint(checkpoint) if checkpoint.blocks_close()) {
            self.notice = "Checkpoint operation is already running and cannot be hidden".into();
            return true;
        }
        if matches!(overlay, Overlay::Checkpoint(_)) {
            self.invalidate_checkpoint_requests();
        }
        self.overlays.resolve_active();
        true
    }

    fn handle_rewind_escape(&mut self) {
        if self.active_task_id.is_some() {
            self.notice = "Cancel or finish the active task before editing history".into();
            self.rewind_primed_at = None;
            return;
        }
        let eligible = self.eligible_history_indices();
        if eligible.is_empty() {
            self.notice = "No earlier prompt can be edited".into();
            self.rewind_primed_at = None;
            return;
        }
        let now = Instant::now();
        if self
            .rewind_primed_at
            .is_some_and(|primed| now.duration_since(primed) <= REWIND_PRIME_WINDOW)
        {
            self.history_stashed_draft = Some(self.composer.text().to_owned());
            self.history_original_scroll = Some(self.transcript_scroll);
            self.composer.set_text("");
            self.composer_state = TextAreaState::default();
            self.history_selected = eligible.last().copied();
            self.rewind_primed_at = None;
            self.sync_history_selection();
            self.notice =
                "Choose a prompt in the conversation, then press Enter to branch and edit".into();
        } else {
            self.rewind_primed_at = Some(now);
            self.notice = "Press Esc again to edit an earlier prompt".into();
        }
    }

    fn handle_history_key(&mut self, key: KeyEvent) -> Result<()> {
        match key.code {
            KeyCode::Esc => self.cancel_history_selection(),
            KeyCode::Up | KeyCode::Left => self.move_history_selection(-1),
            KeyCode::Down | KeyCode::Right => self.move_history_selection(1),
            KeyCode::Enter => {
                if let Err(error) = self.branch_from_history() {
                    self.notice = format!("Could not branch from this prompt: {error}");
                }
            }
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
                    && !block.task_id.is_empty()
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
        self.history_selected = Some(eligible[next]);
        self.sync_history_selection();
    }

    fn sync_history_selection(&mut self) {
        for (index, block) in self.blocks.iter_mut().enumerate() {
            block.selected = self.history_selected == Some(index);
        }
        if let Some(index) = self.history_selected {
            self.transcript_scroll = index;
        }
    }

    fn cancel_history_selection(&mut self) {
        self.history_selected = None;
        self.sync_history_selection();
        if let Some(draft) = self.history_stashed_draft.take() {
            self.composer.set_text(&draft);
            self.composer_state = TextAreaState::default();
        }
        if let Some(scroll) = self.history_original_scroll.take() {
            self.transcript_scroll = scroll;
        }
        self.notice.clear();
    }

    fn branch_from_history(&mut self) -> Result<()> {
        let Some(selected) = self.history_selected else {
            return Ok(());
        };
        let source_session = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone())
            .ok_or_else(|| anyhow!("history edit has no source session"))?;
        let eligible = self.eligible_history_indices();
        let position = eligible
            .iter()
            .position(|index| *index == selected)
            .ok_or_else(|| anyhow!("history selection is stale"))?;
        let selected_prompt = self.blocks[selected].source_prompt.clone();
        let stashed_draft = self.history_stashed_draft.clone().unwrap_or_default();
        let destination = if position == 0 {
            self.rpc
                .create_session(&self.options.workspace.to_string_lossy())?
        } else {
            let previous_task_id = self.blocks[eligible[position - 1]].task_id.clone();
            self.rpc.fork_session(&source_session, &previous_task_id)?
        };
        self.open_session(destination)?;
        let draft = if stashed_draft.trim().is_empty() {
            selected_prompt
        } else {
            format!("{selected_prompt}\n\n{stashed_draft}")
        };
        self.composer.set_text(&draft);
        self.composer_state = TextAreaState::default();
        self.history_selected = None;
        self.history_stashed_draft = None;
        self.history_original_scroll = None;
        self.notice = "Branched from the selected prompt. Edit and submit when ready.".into();
        Ok(())
    }

    fn handle_mouse(&mut self, mouse: MouseEvent) {
        let position = Position::new(mouse.column, mouse.row);
        match mouse.kind {
            MouseEventKind::Moved => {
                if self.interactions.update_hover(position) {
                    self.dirty = true;
                }
            }
            MouseEventKind::Down(MouseButton::Left) => {
                if let Some(action) = self.interactions.action_at(position) {
                    self.apply_action(action);
                } else if self.overlays.active().is_none()
                    && self.phase == Phase::Conversation
                    && self.composer_area.contains(position)
                {
                    let _ =
                        self.composer
                            .handle_mouse(mouse, self.composer_area, self.composer_state);
                    self.focus = Focus::Composer;
                }
                self.dirty = true;
            }
            MouseEventKind::ScrollUp if self.transcript_area.contains(position) => {
                self.transcript_scroll = self.transcript_scroll.saturating_sub(3);
                self.dirty = true;
            }
            MouseEventKind::ScrollDown if self.transcript_area.contains(position) => {
                self.transcript_scroll =
                    (self.transcript_scroll + 3).min(self.blocks.len().saturating_sub(1));
                self.dirty = true;
            }
            _ => {}
        }
    }

    fn apply_action(&mut self, action: Action) {
        match action {
            Action::SelectProvider(index) => {
                self.provider_index = index;
                self.begin_credential();
            }
            Action::SelectModel(index) => {
                self.model_index = index;
                if let Some(model) = self.models.get(index) {
                    self.selected_model = model.id.clone();
                    self.phase = Phase::Session;
                }
            }
            Action::SelectSession(index) => {
                self.session_index = index;
                let result = if index == self.sessions.len() {
                    self.rpc
                        .create_session(&self.options.workspace.to_string_lossy())
                } else {
                    let id = self.sessions[index].session_id.clone();
                    self.rpc.resume_session(&id)
                };
                match result.and_then(|session| {
                    self.open_session(session)
                        .map_err(|error| RpcError::Protocol(error.to_string()))
                }) {
                    Ok(()) => {}
                    Err(error) => self.notice = format!("Session failed: {error}"),
                }
            }
            Action::ToggleBlock(index) => {
                if let Some(block) = self.blocks.get_mut(index) {
                    block.expanded = !block.expanded;
                }
            }
            Action::SelectHistory(index) => {
                if self.eligible_history_indices().contains(&index) {
                    self.history_selected = Some(index);
                    self.sync_history_selection();
                }
            }
            Action::FocusComposer => self.focus = Focus::Composer,
            Action::OpenSessions => {
                self.close_top_non_governance();
                self.phase = Phase::Session;
            }
            Action::OpenModels => {
                self.close_top_non_governance();
                self.phase = Phase::Model;
            }
            Action::OpenCheckpoints => self.open_checkpoints(),
            Action::OpenSettings => self
                .overlays
                .replace(Overlay::Settings(SettingsOverlay { selected: 0 })),
            Action::OpenLocale => {
                self.close_top_non_governance();
                self.phase = Phase::Locale;
            }
            Action::OpenProvider => {
                self.close_top_non_governance();
                self.phase = Phase::Provider;
            }
            Action::ApprovalAllow => self.resolve_active_approval(true),
            Action::ApprovalDeny => self.resolve_active_approval(false),
            Action::QuestionOption(index) => self.answer_active_question(Some(index)),
            Action::SelectCheckpoint(index) => self.select_checkpoint(index),
            Action::PreviewCheckpoint => self.preview_selected_checkpoint(),
            Action::BeginCheckpointRestore => self.begin_checkpoint_restore(),
            Action::ConfirmCheckpointRestore => self.confirm_checkpoint_restore(),
            Action::ResumeRestoredTask => self.resume_restored_task(),
            Action::ResumePausedTask => self.resume_paused_task(),
            Action::CheckpointBack => self.checkpoint_back(),
            Action::CloseOverlay => {
                self.close_top_non_governance();
            }
        }
    }
}

fn restore_checkpoint_and_refresh(
    socket: &Path,
    session_id: &str,
    checkpoint_id: &str,
) -> Result<CheckpointRestoreOutcome, String> {
    let mut rpc = Client::connect(socket).map_err(|error| error.to_string())?;
    let restore = rpc
        .restore_checkpoint(session_id, checkpoint_id)
        .map_err(|error| error.to_string())?;
    let session = refresh_session_projection(&mut rpc, session_id);
    let items = rpc.items(session_id);
    let mut refresh_errors = Vec::new();
    let session = match session {
        Ok(session) => Some(session),
        Err(error) => {
            refresh_errors.push(format!("session: {error}"));
            None
        }
    };
    let items = match items {
        Ok(items) => Some(items),
        Err(error) => {
            refresh_errors.push(format!("transcript: {error}"));
            None
        }
    };
    Ok(CheckpointRestoreOutcome {
        restore,
        session,
        items,
        refresh_error: (!refresh_errors.is_empty()).then(|| refresh_errors.join("; ")),
    })
}

fn resume_checkpoint_task_and_refresh(
    socket: &Path,
    session_id: &str,
    task_id: &str,
) -> Result<CheckpointResumeOutcome, String> {
    let mut rpc = Client::connect(socket).map_err(|error| error.to_string())?;
    let task = rpc
        .resume_task(task_id)
        .map_err(|error| error.to_string())?;
    let session = refresh_session_projection(&mut rpc, session_id);
    let items = rpc.items(session_id);
    let mut refresh_errors = Vec::new();
    let session = match session {
        Ok(session) => Some(session),
        Err(error) => {
            refresh_errors.push(format!("session: {error}"));
            None
        }
    };
    let items = match items {
        Ok(items) => Some(items),
        Err(error) => {
            refresh_errors.push(format!("transcript: {error}"));
            None
        }
    };
    Ok(CheckpointResumeOutcome {
        task,
        session,
        items,
        refresh_error: (!refresh_errors.is_empty()).then(|| refresh_errors.join("; ")),
    })
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
    let mut app = App::bootstrap(options)?;
    let mut terminal = TerminalHost::enter(app.options.no_alt_screen)?;
    while !app.quit {
        app.apply_async();
        if app.dirty {
            terminal.terminal.draw(|frame| app.render(frame))?;
            app.dirty = false;
        }
        if event::poll(Duration::from_millis(40))? {
            app.handle_event(event::read()?)?;
        }
    }
    Ok(app.outcome)
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
    use ratatui::widgets::{Block, Paragraph, Wrap};

    let mut terminal = TerminalHost::enter(no_alt_screen)?;
    let theme = Theme::carina(std::env::var_os("NO_COLOR").is_some());
    let mut selected = 0_usize;
    loop {
        terminal.terminal.draw(|frame| {
            let area = frame.area();
            frame.render_widget(
                Block::default().style(Style::default().bg(theme.background)),
                area,
            );
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
        if !event::poll(Duration::from_millis(100))? {
            continue;
        }
        match event::read()? {
            Event::Key(key) if key.kind == KeyEventKind::Press => match key.code {
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
    terminal: Terminal<CrosstermBackend<io::Stdout>>,
    alternate: bool,
}

impl TerminalHost {
    fn enter(no_alt_screen: bool) -> Result<Self> {
        enable_raw_mode()?;
        let mut stdout = io::stdout();
        if !no_alt_screen {
            execute!(stdout, EnterAlternateScreen)?;
        }
        execute!(
            stdout,
            EnableMouseCapture,
            EnableBracketedPaste,
            EnableFocusChange
        )?;
        let backend = CrosstermBackend::new(stdout);
        let terminal = if no_alt_screen {
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
            alternate: !no_alt_screen,
        })
    }
}

impl Drop for TerminalHost {
    fn drop(&mut self) {
        let _ = disable_raw_mode();
        let backend = self.terminal.backend_mut();
        let _ = execute!(
            backend,
            DisableFocusChange,
            DisableBracketedPaste,
            DisableMouseCapture
        );
        if self.alternate {
            let _ = execute!(backend, LeaveAlternateScreen);
        }
        let _ = self.terminal.show_cursor();
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

fn persist_locale(path: &Path, locale: &str) -> Result<()> {
    let mut root = match fs::read(path) {
        Ok(data) => serde_json::from_slice::<serde_json::Map<String, serde_json::Value>>(&data)
            .with_context(|| format!("parse {}", path.display()))?,
        Err(error) if error.kind() == io::ErrorKind::NotFound => serde_json::Map::new(),
        Err(error) => return Err(error.into()),
    };
    root.insert(
        "tui_locale".into(),
        serde_json::Value::String(locale.into()),
    );
    let parent = path
        .parent()
        .ok_or_else(|| anyhow!("locale config has no parent"))?;
    fs::create_dir_all(parent)?;
    let temp = parent.join(format!(".config.{}.tmp", std::process::id()));
    let data = serde_json::to_vec_pretty(&root)?;
    fs::write(&temp, data)?;
    fs::rename(&temp, path)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

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
}
