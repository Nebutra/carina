use crate::i18n::MessageId;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CommandId {
    Settings,
    Status,
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
    pub requires_active_execution: bool,
}

pub const COMMANDS: &[CommandSpec] = &[
    command(
        CommandId::Settings,
        "/settings",
        MessageId::CommandSettings,
        false,
    ),
    command(
        CommandId::Status,
        "/status",
        MessageId::CommandStatus,
        false,
    ),
    command(
        CommandId::Provider,
        "/provider",
        MessageId::CommandProvider,
        false,
    ),
    command(CommandId::Model, "/model", MessageId::CommandModel, false),
    command(CommandId::Plan, "/plan", MessageId::CommandPlan, false),
    command(CommandId::Build, "/build", MessageId::CommandBuild, false),
    command(
        CommandId::Sessions,
        "/sessions",
        MessageId::CommandSessions,
        false,
    ),
    command(
        CommandId::Resume,
        "/resume",
        MessageId::CommandResume,
        false,
    ),
    command(CommandId::Cancel, "/cancel", MessageId::CommandCancel, true),
    command(CommandId::Doctor, "/doctor", MessageId::CommandDoctor, false),
    command(CommandId::Help, "/help", MessageId::CommandHelp, false),
    command(CommandId::Quit, "/quit", MessageId::CommandQuit, false),
];

const fn command(
    id: CommandId,
    name: &'static str,
    description: MessageId,
    requires_active_execution: bool,
) -> CommandSpec {
    CommandSpec {
        id,
        name,
        description,
        requires_active_execution,
    }
}

pub fn matching(input: &str, has_active_execution: bool) -> Vec<&'static CommandSpec> {
    let input = input.trim();
    if !input.starts_with('/') || input.contains(char::is_whitespace) {
        return Vec::new();
    }
    COMMANDS
        .iter()
        .filter(|command| !command.requires_active_execution || has_active_execution)
        .filter(|command| command.name.starts_with(input) && command.name != input)
        .collect()
}

pub fn resolve(input: &str, has_active_execution: bool) -> Option<&'static CommandSpec> {
    let input = match input.trim() {
        "/exit" => "/quit",
        other => other,
    };
    COMMANDS.iter().find(|command| {
        command.name == input && (!command.requires_active_execution || has_active_execution)
    })
}

#[cfg(test)]
mod tests {
    use super::*;

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
    fn internal_checkpoints_are_not_a_user_command() {
        assert!(
            COMMANDS
                .iter()
                .all(|command| command.name != "/checkpoints")
        );
        assert!(resolve("/checkpoints", false).is_none());
        assert!(resolve("/checkpoint", false).is_none());
    }
}
