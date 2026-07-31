use crate::i18n::MessageId;
use crate::rpc::{PromptCommand, PromptCommandArgument};

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CommandId {
    Settings,
    Status,
    Changes,
    Provider,
    Model,
    Plan,
    Build,
    Sessions,
    Resume,
    Cancel,
    Doctor,
    Help,
    Quit,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct CommandSpec {
    pub id: CommandId,
    pub name: &'static str,
    pub description: MessageId,
    pub availability: AvailabilityRule,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AvailabilityRule {
    Always,
    ActiveExecution,
}

pub const COMMANDS: &[CommandSpec] = &[
    command(
        CommandId::Settings,
        "/settings",
        MessageId::CommandSettings,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Status,
        "/status",
        MessageId::CommandStatus,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Changes,
        "/changes",
        MessageId::Changes,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Provider,
        "/provider",
        MessageId::CommandProvider,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Model,
        "/model",
        MessageId::CommandModel,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Plan,
        "/plan",
        MessageId::CommandPlan,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Build,
        "/build",
        MessageId::CommandBuild,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Sessions,
        "/sessions",
        MessageId::CommandSessions,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Resume,
        "/resume",
        MessageId::CommandResume,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Cancel,
        "/cancel",
        MessageId::CommandCancel,
        AvailabilityRule::ActiveExecution,
    ),
    command(
        CommandId::Doctor,
        "/doctor",
        MessageId::CommandDoctor,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Help,
        "/help",
        MessageId::CommandHelp,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Quit,
        "/quit",
        MessageId::CommandQuit,
        AvailabilityRule::Always,
    ),
];

const fn command(
    id: CommandId,
    name: &'static str,
    description: MessageId,
    availability: AvailabilityRule,
) -> CommandSpec {
    CommandSpec {
        id,
        name,
        description,
        availability,
    }
}

pub fn matching(input: &str, has_active_execution: bool) -> Vec<&'static CommandSpec> {
    let input = input.trim();
    if !input.starts_with('/') || input.contains(char::is_whitespace) {
        return Vec::new();
    }
    COMMANDS
        .iter()
        .filter(|command| command_unavailable_reason(command, has_active_execution).is_none())
        .filter(|command| command.name.starts_with(input) && command.name != input)
        .collect()
}

pub fn resolve(input: &str, has_active_execution: bool) -> Option<&'static CommandSpec> {
    let input = match input.trim() {
        "/exit" => "/quit",
        other => other,
    };
    lookup(input)
        .filter(|command| command_unavailable_reason(command, has_active_execution).is_none())
}

pub fn lookup(input: &str) -> Option<&'static CommandSpec> {
    let input = match input.trim() {
        "/exit" => "/quit",
        other => other,
    };
    COMMANDS.iter().find(|command| command.name == input)
}

pub fn command_unavailable_reason(
    command: &CommandSpec,
    has_active_execution: bool,
) -> Option<MessageId> {
    match command.availability {
        AvailabilityRule::Always => None,
        AvailabilityRule::ActiveExecution if has_active_execution => None,
        AvailabilityRule::ActiveExecution => Some(MessageId::CommandRequiresActiveExecution),
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SuggestionDescription {
    Localized(MessageId),
    Plain(String),
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum SuggestionExecution {
    Operator(CommandId),
    PromptTemplate { id: String },
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct CommandSuggestion {
    pub id: String,
    pub name: String,
    pub source: String,
    pub namespace: Option<String>,
    pub description: SuggestionDescription,
    pub hints: Vec<String>,
    pub arguments: Vec<PromptCommandArgument>,
    pub unavailable_reason: Option<MessageId>,
    pub registry_revision: Option<String>,
    pub execution: SuggestionExecution,
}

pub fn operator_id(id: CommandId) -> String {
    format!("operator:{id:?}").to_ascii_lowercase()
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum PromptArgumentError {
    Required(String),
    TooMany,
    NotAccepted,
}

pub fn selected_index(
    suggestions: &[CommandSuggestion],
    selected_id: Option<&str>,
    fallback_index: usize,
) -> usize {
    selected_id
        .and_then(|id| suggestions.iter().position(|command| command.id == id))
        .unwrap_or_else(|| fallback_index.min(suggestions.len().saturating_sub(1)))
}

pub fn palette_matching(
    input: &str,
    has_active_execution: bool,
    prompt_commands: &[PromptCommand],
    registry_revision: &str,
    mru: &[String],
) -> Vec<CommandSuggestion> {
    let input = input.trim();
    if !input.starts_with('/') {
        return Vec::new();
    }
    let (head, tail) = split_prompt_command(input);
    let query = head.trim_start_matches('/').to_ascii_lowercase();
    let mut scored = Vec::new();
    for command in COMMANDS.iter().filter(|_| tail.is_none()) {
        let candidate = command.name.trim_start_matches('/').to_ascii_lowercase();
        if command.name == input {
            continue;
        }
        let score = std::iter::once(candidate.as_str())
            .chain(
                operator_aliases(command.id)
                    .iter()
                    .map(|alias| alias.trim_start_matches('/')),
            )
            .filter_map(|candidate| command_match_score(candidate, &query))
            .max();
        if let Some(score) = score {
            let suggestion = CommandSuggestion {
                id: operator_id(command.id),
                name: command.name.to_owned(),
                source: "carina".to_owned(),
                namespace: None,
                description: SuggestionDescription::Localized(command.description),
                hints: Vec::new(),
                arguments: Vec::new(),
                unavailable_reason: command_unavailable_reason(command, has_active_execution),
                registry_revision: None,
                execution: SuggestionExecution::Operator(command.id),
            };
            scored.push((score, mru_score(&suggestion.id, mru), suggestion));
        }
    }
    for command in prompt_commands {
        let candidate = command.name.to_ascii_lowercase();
        let name = format!("/{}", command.name);
        if (name == input && tail.is_none()) || COMMANDS.iter().any(|local| local.name == name) {
            continue;
        }
        if let Some(score) = command_match_score(&candidate, &query) {
            let suggestion = CommandSuggestion {
                id: command.id.clone(),
                name,
                source: command.source.clone(),
                namespace: command
                    .name
                    .rsplit_once('.')
                    .map(|(namespace, _)| namespace.to_owned()),
                description: SuggestionDescription::Plain(command.description.clone()),
                hints: command.hints.clone(),
                arguments: command.arguments.clone(),
                unavailable_reason: None,
                registry_revision: Some(registry_revision.to_owned()),
                execution: SuggestionExecution::PromptTemplate {
                    id: command.id.clone(),
                },
            };
            scored.push((score, mru_score(&suggestion.id, mru), suggestion));
        }
    }
    scored.sort_by(
        |(left_score, left_mru, left), (right_score, right_mru, right)| {
            right_score
                .cmp(left_score)
                .then_with(|| right_mru.cmp(left_mru))
                .then_with(|| left.name.cmp(&right.name))
                .then_with(|| left.source.cmp(&right.source))
                .then_with(|| left.id.cmp(&right.id))
        },
    );
    scored.into_iter().map(|(_, _, command)| command).collect()
}

fn operator_aliases(id: CommandId) -> &'static [&'static str] {
    match id {
        CommandId::Quit => &["/exit"],
        _ => &[],
    }
}

fn mru_score(id: &str, mru: &[String]) -> usize {
    mru.iter()
        .position(|candidate| candidate == id)
        .map_or(0, |index| mru.len().saturating_sub(index))
}

pub fn prompt_command_id(input: &str, prompt_commands: &[PromptCommand]) -> Option<String> {
    let (head, _) = split_prompt_command(input);
    let name = head.strip_prefix('/')?;
    prompt_commands
        .iter()
        .find(|command| command.name == name)
        .map(|command| command.id.clone())
}

pub fn split_prompt_command(input: &str) -> (&str, Option<&str>) {
    let trimmed = input.trim();
    match trimmed.find(char::is_whitespace) {
        Some(index) => (&trimmed[..index], Some(trimmed[index..].trim())),
        None => (trimmed, None),
    }
}

pub fn complete_prompt_token(input: &str, command_name: &str) -> String {
    let (_, tail) = split_prompt_command(input);
    match tail.filter(|tail| !tail.is_empty()) {
        Some(tail) => format!("{command_name} {tail}"),
        None => command_name.to_owned(),
    }
}

pub fn validate_prompt_arguments(
    input: &str,
    prompt_commands: &[PromptCommand],
) -> Option<Result<(), PromptArgumentError>> {
    let (head, tail) = split_prompt_command(input);
    let name = head.strip_prefix('/')?;
    let command = prompt_commands
        .iter()
        .find(|command| command.name == name)?;
    if command.source != "mcp" {
        return Some(Ok(()));
    }
    let tail = tail.unwrap_or_default();
    if command.arguments.is_empty() {
        return Some(if tail.is_empty() {
            Ok(())
        } else {
            Err(PromptArgumentError::NotAccepted)
        });
    }
    let fields = tail.split_whitespace().collect::<Vec<_>>();
    let mut position = 0usize;
    let mut catch_all = false;
    for argument in &command.arguments {
        let value_present = if argument.name.eq_ignore_ascii_case("ARGUMENTS") {
            catch_all = true;
            !tail.is_empty()
        } else if command.arguments.len() == 1 {
            !tail.is_empty()
        } else if position < fields.len() {
            position += 1;
            true
        } else {
            false
        };
        if argument.required && !value_present {
            return Some(Err(PromptArgumentError::Required(argument.name.clone())));
        }
    }
    if position < fields.len() && !catch_all && command.arguments.len() > 1 {
        return Some(Err(PromptArgumentError::TooMany));
    }
    Some(Ok(()))
}

pub fn argument_hint(command: &CommandSuggestion) -> String {
    if command.arguments.is_empty() {
        command
            .hints
            .iter()
            .map(|hint| format!("<{hint}>"))
            .collect::<Vec<_>>()
            .join(" ")
    } else {
        command
            .arguments
            .iter()
            .map(|argument| {
                if argument.required {
                    format!("<{}>", argument.name)
                } else {
                    format!("[{}]", argument.name)
                }
            })
            .collect::<Vec<_>>()
            .join(" ")
    }
}

fn command_match_score(candidate: &str, query: &str) -> Option<i64> {
    if query.is_empty() {
        return Some(0);
    }
    if candidate.starts_with(query) {
        return Some(10_000 - candidate.len() as i64);
    }
    let mut score = 0i64;
    let mut offset = 0usize;
    for needle in query.chars() {
        let found = candidate[offset..].find(needle)?;
        score += 100 - found as i64;
        offset += found + needle.len_utf8();
    }
    Some(score - candidate.len() as i64)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn palette_sources_never_dispatch_through_shell_command_exec() {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        for relative in ["command.rs", "help.rs", "app/mod.rs", "app/render.rs"] {
            let source = std::fs::read_to_string(root.join(relative)).unwrap();
            assert!(
                !source.contains("\"command.exec\""),
                "palette source {relative} must not invoke shell command.exec"
            );
        }
    }

    #[test]
    fn discovery_and_execution_share_capability_gates() {
        assert!(
            !matching("/c", false)
                .iter()
                .any(|item| item.id == CommandId::Cancel)
        );
        assert!(resolve("/cancel", false).is_none());
        assert!(
            matching("/c", true)
                .iter()
                .any(|item| item.id == CommandId::Cancel)
        );
        assert_eq!(
            resolve("/cancel", true).map(|item| item.id),
            Some(CommandId::Cancel)
        );
    }

    #[test]
    fn status_is_discoverable_and_resolves_while_idle() {
        assert!(
            matching("/st", false)
                .iter()
                .any(|item| item.id == CommandId::Status)
        );
        assert_eq!(
            resolve("/status", false).map(|item| item.id),
            Some(CommandId::Status)
        );
    }

    #[test]
    fn palette_explains_unavailable_operator_without_making_it_executable() {
        let matches = palette_matching("/can", false, &[], "", &[]);
        let cancel = matches
            .iter()
            .find(|command| command.id == operator_id(CommandId::Cancel))
            .unwrap();
        assert_eq!(
            cancel.unavailable_reason,
            Some(MessageId::CommandRequiresActiveExecution)
        );
        assert!(resolve("/cancel", false).is_none());
    }

    #[test]
    fn internal_checkpoints_are_not_a_user_command() {
        assert!(
            COMMANDS
                .iter()
                .all(|command| command.name != "/checkpoints")
        );
        assert!(resolve("/checkpoints", false).is_none());
        assert!(resolve("/checkpoint", false).is_none());
    }

    #[test]
    fn operator_alias_completes_to_the_canonical_command() {
        let matches = palette_matching("/ex", false, &[], "", &[]);
        assert_eq!(matches[0].id, operator_id(CommandId::Quit));
        assert_eq!(matches[0].name, "/quit");
        assert_eq!(
            resolve("/exit", false).map(|command| command.id),
            Some(CommandId::Quit)
        );
    }

    #[test]
    fn palette_merges_prompt_commands_without_shadowing_operators() {
        let remote = vec![PromptCommand {
            id: "prompt:project:review".into(),
            kind: "prompt_template".into(),
            name: "review".into(),
            description: "Review changes".into(),
            source: "project".into(),
            ..PromptCommand::default()
        }];
        let matches = palette_matching("/rev", false, &remote, "revision", &[]);
        assert_eq!(matches.len(), 1);
        assert_eq!(matches[0].name, "/review");
        assert_eq!(matches[0].registry_revision.as_deref(), Some("revision"));
        assert!(matches!(
            matches[0].execution,
            SuggestionExecution::PromptTemplate { .. }
        ));

        let namespaced = vec![PromptCommand {
            id: "prompt:mcp:mcp.github.review".into(),
            name: "mcp.github.review".into(),
            source: "mcp".into(),
            ..PromptCommand::default()
        }];
        let matches = palette_matching("/mcp.g", false, &namespaced, "revision", &[]);
        assert_eq!(matches[0].namespace.as_deref(), Some("mcp.github"));

        let shadow = vec![PromptCommand {
            id: "prompt:project:status".into(),
            name: "status".into(),
            source: "project".into(),
            ..PromptCommand::default()
        }];
        let matches = palette_matching("/sta", false, &shadow, "revision", &[]);
        assert_eq!(matches.len(), 1);
        assert!(matches!(
            matches[0].execution,
            SuggestionExecution::Operator(CommandId::Status)
        ));
    }

    #[test]
    fn selection_follows_stable_id_across_registry_reordering() {
        let before = palette_matching(
            "/",
            false,
            &[PromptCommand {
                id: "prompt:project:zeta".into(),
                name: "zeta".into(),
                source: "project".into(),
                ..PromptCommand::default()
            }],
            "revision-1",
            &[],
        );
        let selected_id = before.last().unwrap().id.clone();
        let after = palette_matching(
            "/",
            false,
            &[
                PromptCommand {
                    id: "prompt:project:alpha".into(),
                    name: "alpha".into(),
                    source: "project".into(),
                    ..PromptCommand::default()
                },
                PromptCommand {
                    id: selected_id.clone(),
                    name: "zeta".into(),
                    source: "project".into(),
                    ..PromptCommand::default()
                },
            ],
            "revision-2",
            &[],
        );
        let index = selected_index(&after, Some(&selected_id), 0);
        assert_eq!(after[index].id, selected_id);
    }

    #[test]
    fn mcp_arguments_follow_daemon_positional_contract() {
        let commands = vec![PromptCommand {
            id: "prompt:mcp:deploy".into(),
            name: "deploy".into(),
            source: "mcp".into(),
            arguments: vec![
                PromptCommandArgument {
                    name: "target".into(),
                    required: true,
                    ..PromptCommandArgument::default()
                },
                PromptCommandArgument {
                    name: "region".into(),
                    ..PromptCommandArgument::default()
                },
            ],
            ..PromptCommand::default()
        }];
        assert_eq!(
            validate_prompt_arguments("/deploy", &commands),
            Some(Err(PromptArgumentError::Required("target".into())))
        );
        assert_eq!(
            validate_prompt_arguments("/deploy api us", &commands),
            Some(Ok(()))
        );
        assert_eq!(
            validate_prompt_arguments("/deploy api us extra", &commands),
            Some(Err(PromptArgumentError::TooMany))
        );
    }

    #[test]
    fn completion_preserves_existing_inline_arguments() {
        assert_eq!(
            complete_prompt_token("/dep api us", "/deploy"),
            "/deploy api us"
        );
    }

    #[test]
    fn inline_arguments_do_not_mix_operator_fuzzy_matches_into_prompt_hints() {
        let commands = vec![PromptCommand {
            id: "prompt:mcp:review".into(),
            name: "review".into(),
            source: "mcp".into(),
            hints: vec!["target".into()],
            ..PromptCommand::default()
        }];
        let matches = palette_matching("/review parser", false, &commands, "revision", &[]);
        assert_eq!(matches.len(), 1);
        assert_eq!(matches[0].id, "prompt:mcp:review");
    }

    #[test]
    fn session_mru_precedes_alphabetical_order_for_equal_matches() {
        let commands = vec![
            PromptCommand {
                id: "prompt:project:alpha".into(),
                name: "alpha".into(),
                source: "project".into(),
                ..PromptCommand::default()
            },
            PromptCommand {
                id: "prompt:project:zeta".into(),
                name: "zeta".into(),
                source: "project".into(),
                ..PromptCommand::default()
            },
        ];
        let matches = palette_matching(
            "/",
            false,
            &commands,
            "revision",
            &["prompt:project:zeta".into()],
        );
        assert_eq!(matches[0].id, "prompt:project:zeta");
    }
}
