use anyhow::Result;
use xai_ratatui_textarea::TextAreaState;

use crate::command::{self, CommandId, CommandSuggestion, SuggestionExecution};
use crate::component::Action;
use crate::i18n::{MessageId, Notice};
use crate::overlay::Overlay;

use super::{App, Focus, Phase, ScreenMode};

impl App {
    pub(super) fn slash_suggestions(&self) -> Vec<CommandSuggestion> {
        let input = self.composer.text().trim();
        if self.slash_dismissed_input.as_deref() == Some(input) {
            return Vec::new();
        }
        let mut suggestions = command::palette_matching(
            input,
            self.has_retained_run(),
            &self.command_registry.commands,
            &self.command_registry.revision,
            &self.command_mru,
        );
        command::apply_registry_state(&mut suggestions, &self.command_registry.state);
        suggestions
    }

    pub(super) fn sync_slash_selection(&mut self) {
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

    pub(super) fn remember_command_use(&mut self, id: String) {
        self.command_mru.retain(|candidate| candidate != &id);
        self.command_mru.insert(0, id);
        self.command_mru.truncate(command::COMMAND_MRU_LIMIT);
        command::persist_command_mru(&self.command_mru);
    }

    pub(super) fn execute_slash_suggestion(
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

    pub(super) fn handle_slash_command(&mut self, prompt: &str) -> Option<bool> {
        if let Some(reason) = command::withdrawn_operator(prompt) {
            self.notice = Notice::localized(reason);
            return Some(false);
        }
        let (command, tail) = command::resolve_operator_input(prompt)?;
        if let Some(reason) = command::command_unavailable_reason(command, self.has_retained_run())
        {
            self.notice = Notice::localized(reason);
            return Some(false);
        }
        if tail.is_some() && !command::accepts_arguments(command.id) {
            self.notice = Notice::localized(MessageId::CommandArgumentsNotAccepted);
            return Some(false);
        }
        self.remember_command_use(command::operator_id(command.id));
        match command.id {
            CommandId::Settings => {
                self.open_settings();
            }
            CommandId::Density => self.apply_action(Action::ToggleDensity),
            CommandId::Theme => {
                if let Some(tail) = tail {
                    let Some(preference) = crate::theme::ThemePreference::parse(tail) else {
                        self.notice = Notice::localized(MessageId::ThemeUnknownChoice);
                        return Some(false);
                    };
                    self.apply_action(Action::OpenThemePreview);
                    self.apply_action(Action::PreviewThemePreference(preference));
                } else {
                    self.apply_action(Action::OpenThemePreview);
                }
            }
            CommandId::Symbols => self.apply_action(Action::OpenGlyphPreview),
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
            CommandId::ViewPlan => self.open_plan_review(),
            CommandId::ApprovePlan => self.approve_plan(),
            CommandId::AlwaysApprove => {
                return Some(self.set_product_approval_mode("always-approve"));
            }
            CommandId::DontAsk => return Some(self.set_product_approval_mode("dont-ask")),
            CommandId::AcceptEdits => {
                return Some(self.set_product_approval_mode("accept-edits"));
            }
            CommandId::ApprovalMode => {
                return Some(self.handle_approval_mode_command(tail.unwrap_or("")));
            }
            CommandId::Sessions => self.open_session_browser(),
            CommandId::Import => {
                self.open_session_browser();
                self.begin_conversation_import();
            }
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
            CommandId::New => self.start_new_conversation(),
            CommandId::Fork => self.fork_current_conversation(),
            CommandId::Compact => self.compact_current_checkpoint(),
            CommandId::Goal => self.handle_goal_command(tail.unwrap_or("")),
            CommandId::Btw => return Some(self.handle_btw_command(tail.unwrap_or(""))),
            CommandId::Cancel => {
                if let Some(run_id) = self.retained_run_id().map(str::to_owned) {
                    self.cancel_execution(&run_id);
                } else {
                    self.notice = Notice::localized(MessageId::NoActiveExecutionRun);
                }
            }
            CommandId::Minimal => self.request_screen_mode(ScreenMode::Minimal),
            CommandId::Fullscreen => self.request_screen_mode(ScreenMode::Fullscreen),
            CommandId::Inline => self.request_screen_mode(ScreenMode::Inline),
            CommandId::Queue => self.apply_action(Action::OpenQueue),
            CommandId::Agents => self.apply_action(Action::OpenAgents),
            CommandId::Plugins => self.apply_action(Action::OpenPlugins),
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
}
