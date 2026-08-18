use std::fs;
use std::path::{Path, PathBuf};

use crate::i18n::MessageId;
use crate::rpc::{PromptCommand, PromptCommandArgument};

pub const COMMAND_MRU_LIMIT: usize = 20;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CommandId {
    Settings,
    Density,
    Symbols,
    Status,
    Context,
    Changes,
    Provider,
    Model,
    Plan,
    ViewPlan,
    ApprovePlan,
    Build,
    AlwaysApprove,
    DontAsk,
    AcceptEdits,
    ApprovalMode,
    Sessions,
    Import,
    Resume,
    New,
    Fork,
    Compact,
    Goal,
    Btw,
    Cancel,
    Minimal,
    Fullscreen,
    Inline,
    Queue,
    Agents,
    Plugins,
    Doctor,
    Keymap,
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
        CommandId::Density,
        "/density",
        MessageId::CommandDensity,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Symbols,
        "/symbols",
        MessageId::CommandSymbols,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Status,
        "/status",
        MessageId::CommandStatus,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Context,
        "/context",
        MessageId::CommandContext,
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
        CommandId::ViewPlan,
        "/view-plan",
        MessageId::CommandViewPlan,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::ApprovePlan,
        "/approve-plan",
        MessageId::CommandApprovePlan,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Build,
        "/build",
        MessageId::CommandBuild,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::AlwaysApprove,
        "/always-approve",
        MessageId::CommandAlwaysApprove,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::DontAsk,
        "/dont-ask",
        MessageId::CommandDontAsk,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::AcceptEdits,
        "/accept-edits",
        MessageId::CommandAcceptEdits,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::ApprovalMode,
        "/approval-mode",
        MessageId::CommandApprovalMode,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Sessions,
        "/sessions",
        MessageId::CommandSessions,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Import,
        "/import",
        MessageId::CommandImportConversations,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Resume,
        "/resume",
        MessageId::CommandResume,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::New,
        "/new",
        MessageId::CommandNew,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Fork,
        "/fork",
        MessageId::CommandFork,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Compact,
        "/compact",
        MessageId::CommandCompact,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Goal,
        "/goal",
        MessageId::CommandGoal,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Btw,
        "/btw",
        MessageId::CommandBtw,
        AvailabilityRule::ActiveExecution,
    ),
    command(
        CommandId::Cancel,
        "/cancel",
        MessageId::CommandCancel,
        AvailabilityRule::ActiveExecution,
    ),
    command(
        CommandId::Minimal,
        "/minimal",
        MessageId::CommandMinimal,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Fullscreen,
        "/fullscreen",
        MessageId::CommandFullscreen,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Inline,
        "/inline",
        MessageId::CommandInline,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Queue,
        "/queue",
        MessageId::CommandQueue,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Agents,
        "/agents",
        MessageId::CommandAgents,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Plugins,
        "/plugins",
        MessageId::CommandPlugins,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Doctor,
        "/doctor",
        MessageId::CommandDoctor,
        AvailabilityRule::Always,
    ),
    command(
        CommandId::Keymap,
        "/keymap",
        MessageId::CommandKeymap,
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
    if let Some(exact) = COMMANDS.iter().find(|command| command.name == input) {
        return Some(exact);
    }
    unique_typo_operator(input)
}

pub fn resolve_operator_input(input: &str) -> Option<(&'static CommandSpec, Option<&str>)> {
    let (head, tail) = split_prompt_command(input);
    let command = lookup(head)?;
    Some((command, tail.filter(|value| !value.is_empty())))
}

pub fn accepts_arguments(id: CommandId) -> bool {
    matches!(
        id,
        CommandId::Goal | CommandId::ApprovalMode | CommandId::Btw
    )
}

/// Names that docs once advertised and that we now refuse in place.
/// They stay out of `COMMANDS` so `/help` does not list a dead surface.
pub fn withdrawn_operator(input: &str) -> Option<MessageId> {
    let (head, _) = split_prompt_command(input);
    match head {
        "/side" | "/side-close" => Some(MessageId::SideWithdrawn),
        _ => None,
    }
}

pub fn is_btw_fork_request(tail: &str) -> bool {
    matches!(
        tail.split_whitespace().next().unwrap_or(""),
        "--fork" | "-fork"
    )
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

pub fn registry_state_notice(state: &str) -> Option<MessageId> {
    match state {
        "probing" => Some(MessageId::CommandRegistryProbing),
        "offline" => Some(MessageId::CommandRegistryStale),
        _ => None,
    }
}

pub fn is_mcp_slash(input: &str) -> bool {
    let (head, _) = split_prompt_command(input.trim());
    head.trim_start_matches('/')
        .to_ascii_lowercase()
        .starts_with("mcp.")
}

pub fn mcp_slash_unready(input: &str, registry_state: &str) -> Option<MessageId> {
    if !is_mcp_slash(input) {
        return None;
    }
    match registry_state {
        "probing" => Some(MessageId::CommandMcpProbingDraftKept),
        "offline" => Some(MessageId::CommandRegistryStale),
        _ => None,
    }
}

pub fn apply_registry_state(suggestions: &mut [CommandSuggestion], registry_state: &str) {
    let reason = match registry_state {
        "probing" => Some(MessageId::CommandRegistryProbing),
        "offline" => Some(MessageId::CommandRegistryStale),
        _ => None,
    };
    let Some(reason) = reason else {
        return;
    };
    for suggestion in suggestions {
        if suggestion.source == "mcp" {
            suggestion.unavailable_reason = Some(reason);
        }
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
    if let Some(exact) = prompt_commands.iter().find(|command| command.name == name) {
        return Some(exact.id.clone());
    }
    unique_typo_prompt(name, prompt_commands)
}

/// Persisted operator MRU lives in `~/.carina/slash-mru.json`. Tests only
/// touch the file when `CARINA_SLASH_MRU` is set so `cargo test` cannot
/// rewrite the operator's real list.
pub fn command_mru_file() -> Option<PathBuf> {
    if let Some(path) = std::env::var_os("CARINA_SLASH_MRU") {
        let path = PathBuf::from(path);
        if !path.as_os_str().is_empty() {
            return Some(path);
        }
    }
    #[cfg(test)]
    {
        None
    }
    #[cfg(not(test))]
    {
        std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".carina/slash-mru.json"))
    }
}

pub fn load_command_mru() -> Vec<String> {
    command_mru_file()
        .map(|path| load_command_mru_from(&path))
        .unwrap_or_default()
}

pub fn load_command_mru_from(path: &Path) -> Vec<String> {
    let Ok(data) = fs::read(path) else {
        return Vec::new();
    };
    let Ok(ids) = serde_json::from_slice::<Vec<String>>(&data) else {
        return Vec::new();
    };
    sanitize_command_mru(ids)
}

pub fn persist_command_mru(ids: &[String]) {
    let Some(path) = command_mru_file() else {
        return;
    };
    let _ = persist_command_mru_to(&path, ids);
}

pub fn persist_command_mru_to(path: &Path, ids: &[String]) -> std::io::Result<()> {
    let ids = sanitize_command_mru(ids.to_vec());
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    let payload = serde_json::to_vec_pretty(&ids)
        .map_err(|error| std::io::Error::new(std::io::ErrorKind::InvalidData, error))?;
    let tmp = path.with_extension("json.tmp");
    fs::write(&tmp, payload)?;
    fs::rename(&tmp, path)?;
    Ok(())
}

pub fn retain_operator_mru(mru: &mut Vec<String>) {
    mru.retain(|id| id.starts_with("operator:"));
}

fn sanitize_command_mru(ids: Vec<String>) -> Vec<String> {
    let mut out = Vec::new();
    for id in ids {
        let id = id.trim();
        if id.is_empty() || id.chars().any(char::is_control) {
            continue;
        }
        if out.iter().any(|existing| existing == id) {
            continue;
        }
        out.push(id.to_owned());
        if out.len() == COMMAND_MRU_LIMIT {
            break;
        }
    }
    out
}

fn unique_typo_operator(input: &str) -> Option<&'static CommandSpec> {
    let query = input.trim().trim_start_matches('/').to_ascii_lowercase();
    if query.is_empty() {
        return None;
    }
    let mut best: Option<&'static CommandSpec> = None;
    let mut best_distance = usize::MAX;
    let mut ties = 0usize;
    for command in COMMANDS {
        let distance = operator_name_candidates(command.id)
            .chain(std::iter::once(
                command.name.trim_start_matches('/').to_ascii_lowercase(),
            ))
            .filter_map(|candidate| {
                let distance = damerau_levenshtein(&candidate, &query);
                (distance > 0 && distance <= typo_allowance(&query, &candidate)).then_some(distance)
            })
            .min()
            .unwrap_or(usize::MAX);
        if distance == usize::MAX {
            continue;
        }
        if distance < best_distance {
            best_distance = distance;
            best = Some(command);
            ties = 1;
        } else if distance == best_distance && best.is_some_and(|current| current.id != command.id)
        {
            ties += 1;
        }
    }
    (ties == 1).then_some(best).flatten()
}

fn unique_typo_prompt(name: &str, prompt_commands: &[PromptCommand]) -> Option<String> {
    let query = name.to_ascii_lowercase();
    if query.is_empty() {
        return None;
    }
    let mut best: Option<String> = None;
    let mut best_distance = usize::MAX;
    let mut ties = 0usize;
    for command in prompt_commands {
        let candidate = command.name.to_ascii_lowercase();
        let distance = damerau_levenshtein(&candidate, &query);
        if distance == 0 || distance > typo_allowance(&query, &candidate) {
            continue;
        }
        if distance < best_distance {
            best_distance = distance;
            best = Some(command.id.clone());
            ties = 1;
        } else if distance == best_distance {
            ties += 1;
        }
    }
    (ties == 1).then_some(best).flatten()
}

fn operator_name_candidates(id: CommandId) -> impl Iterator<Item = String> {
    operator_aliases(id)
        .iter()
        .map(|alias| alias.trim_start_matches('/').to_ascii_lowercase())
}

fn typo_allowance(query: &str, candidate: &str) -> usize {
    if query.chars().count() >= 5 && candidate.chars().count() >= 5 {
        2
    } else {
        1
    }
}

fn damerau_levenshtein(left: &str, right: &str) -> usize {
    let left: Vec<char> = left.chars().collect();
    let right: Vec<char> = right.chars().collect();
    let n = left.len();
    let m = right.len();
    if n.abs_diff(m) > 2 {
        return n.abs_diff(m);
    }
    if n == 0 {
        return m;
    }
    if m == 0 {
        return n;
    }
    let mut dp = vec![vec![0usize; m + 1]; n + 1];
    for (i, row) in dp.iter_mut().enumerate().take(n + 1) {
        row[0] = i;
    }
    for (j, cell) in dp[0].iter_mut().enumerate().take(m + 1) {
        *cell = j;
    }
    for i in 1..=n {
        for j in 1..=m {
            let cost = usize::from(left[i - 1] != right[j - 1]);
            dp[i][j] = (dp[i - 1][j] + 1)
                .min(dp[i][j - 1] + 1)
                .min(dp[i - 1][j - 1] + cost);
            if i > 1 && j > 1 && left[i - 1] == right[j - 2] && left[i - 2] == right[j - 1] {
                dp[i][j] = dp[i][j].min(dp[i - 2][j - 2] + 1);
            }
        }
    }
    dp[n][m]
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
    let subsequence = subsequence_score(candidate, query);
    let typo = typo_score(candidate, query);
    match (subsequence, typo) {
        (Some(left), Some(right)) => Some(left.max(right)),
        (Some(score), None) | (None, Some(score)) => Some(score),
        (None, None) => None,
    }
}

fn subsequence_score(candidate: &str, query: &str) -> Option<i64> {
    let mut score = 0i64;
    let mut offset = 0usize;
    for needle in query.chars() {
        let found = candidate[offset..].find(needle)?;
        score += 100 - found as i64;
        offset += found + needle.len_utf8();
    }
    Some(score - candidate.len() as i64)
}

fn typo_score(candidate: &str, query: &str) -> Option<i64> {
    let distance = damerau_levenshtein(candidate, query);
    if distance == 0 || distance > typo_allowance(query, candidate) {
        return None;
    }
    Some(5_000 - (distance as i64 * 1_000) - candidate.len() as i64)
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
        assert!(!matching("/c", false)
            .iter()
            .any(|item| item.id == CommandId::Cancel));
        assert!(resolve("/cancel", false).is_none());
        assert!(matching("/c", true)
            .iter()
            .any(|item| item.id == CommandId::Cancel));
        assert_eq!(
            resolve("/cancel", true).map(|item| item.id),
            Some(CommandId::Cancel)
        );
    }

    #[test]
    fn status_is_discoverable_and_resolves_while_idle() {
        assert!(matching("/st", false)
            .iter()
            .any(|item| item.id == CommandId::Status));
        assert_eq!(
            resolve("/status", false).map(|item| item.id),
            Some(CommandId::Status)
        );
    }

    #[test]
    fn plugins_is_a_builtin_command_available_while_idle() {
        assert!(matching("/pl", false)
            .iter()
            .any(|item| item.id == CommandId::Plugins));
        assert_eq!(
            resolve("/plugins", false).map(|item| item.id),
            Some(CommandId::Plugins)
        );
        assert_eq!(
            lookup("/plugins").map(|item| item.description),
            Some(MessageId::CommandPlugins)
        );
        assert!(!accepts_arguments(CommandId::Plugins));
    }

    #[test]
    fn plugins_palette_sources_never_call_plugin_run() {
        let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
        for relative in [
            "command.rs",
            "help.rs",
            "app/mod.rs",
            "app/render.rs",
            "app/slash_dispatch.rs",
            "rpc/mod.rs",
            "overlay.rs",
        ] {
            let source = std::fs::read_to_string(root.join(relative)).unwrap();
            assert!(
                !source.contains("\"plugin.run\""),
                "TUI source {relative} must not invoke plugin execution RPC"
            );
        }
    }

    #[test]
    fn agents_is_a_builtin_command_available_while_idle() {
        assert!(matching("/ag", false)
            .iter()
            .any(|item| item.id == CommandId::Agents));
        assert_eq!(
            resolve("/agents", false).map(|item| item.id),
            Some(CommandId::Agents)
        );
        assert_eq!(
            lookup("/agents").map(|item| item.description),
            Some(MessageId::CommandAgents)
        );
        assert!(!accepts_arguments(CommandId::Agents));
    }

    #[test]
    fn density_is_discoverable_and_resolves_while_idle() {
        assert!(matching("/den", false)
            .iter()
            .any(|item| item.id == CommandId::Density));
        assert_eq!(
            resolve("/density", false).map(|item| item.id),
            Some(CommandId::Density)
        );
    }

    #[test]
    fn symbols_is_a_builtin_command_available_in_every_execution_state() {
        for has_active_execution in [false, true] {
            assert!(matching("/sym", has_active_execution)
                .iter()
                .any(|item| item.id == CommandId::Symbols));
            assert_eq!(
                resolve("/symbols", has_active_execution).map(|item| item.id),
                Some(CommandId::Symbols)
            );
        }
        assert_eq!(
            lookup("/symbols").map(|item| item.description),
            Some(MessageId::CommandSymbols)
        );
    }

    #[test]
    fn view_plan_is_a_builtin_command_available_while_idle() {
        assert!(matching("/view", false)
            .iter()
            .any(|item| item.id == CommandId::ViewPlan));
        assert_eq!(
            resolve("/view-plan", false).map(|item| item.id),
            Some(CommandId::ViewPlan)
        );
        assert_eq!(
            resolve("/view-plan", true).map(|item| item.id),
            Some(CommandId::ViewPlan)
        );
        assert_eq!(
            lookup("/view-plan").map(|item| item.description),
            Some(MessageId::CommandViewPlan)
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
        assert!(COMMANDS
            .iter()
            .all(|command| command.name != "/checkpoints"));
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
        let mut matches = palette_matching("/mcp.g", false, &namespaced, "revision", &[]);
        assert_eq!(matches[0].namespace.as_deref(), Some("mcp.github"));
        apply_registry_state(&mut matches, "probing");
        assert_eq!(
            matches[0].unavailable_reason,
            Some(MessageId::CommandRegistryProbing)
        );
        assert_eq!(
            mcp_slash_unready("/mcp.mock.review parser", "probing"),
            Some(MessageId::CommandMcpProbingDraftKept)
        );
        assert!(mcp_slash_unready("/review", "probing").is_none());

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

    #[test]
    fn operator_conversation_verbs_are_discoverable_and_resolve() {
        for name in ["/new", "/fork", "/compact", "/goal"] {
            assert!(
                COMMANDS.iter().any(|command| command.name == name),
                "{name} must be a typed operator"
            );
            assert_eq!(resolve(name, false).map(|command| command.name), Some(name));
        }
        let discovered = palette_matching("/", false, &[], "", &[]);
        for name in ["/new", "/fork", "/compact", "/goal"] {
            assert!(
                discovered.iter().any(|command| command.name == name),
                "{name} must appear in /help discovery"
            );
        }
    }

    #[test]
    fn goal_accepts_a_tail_and_other_new_verbs_do_not() {
        let (command, tail) = resolve_operator_input("/goal ship the review").unwrap();
        assert_eq!(command.id, CommandId::Goal);
        assert_eq!(tail, Some("ship the review"));
        assert!(accepts_arguments(CommandId::Goal));
        let (command, tail) = resolve_operator_input("/compact now").unwrap();
        assert_eq!(command.id, CommandId::Compact);
        assert_eq!(tail, Some("now"));
        assert!(!accepts_arguments(CommandId::Compact));
        assert!(!accepts_arguments(CommandId::New));
        assert!(!accepts_arguments(CommandId::Fork));
    }

    #[test]
    fn hitl_and_btw_verbs_are_discoverable_and_resolve() {
        for name in [
            "/approve-plan",
            "/always-approve",
            "/dont-ask",
            "/accept-edits",
            "/approval-mode",
        ] {
            assert!(
                COMMANDS.iter().any(|command| command.name == name),
                "{name} must be a typed operator"
            );
            assert_eq!(resolve(name, false).map(|command| command.name), Some(name));
        }
        assert!(resolve("/btw", false).is_none());
        assert_eq!(
            resolve("/btw", true).map(|command| command.id),
            Some(CommandId::Btw)
        );
        let idle = palette_matching("/", false, &[], "", &[]);
        for name in [
            "/approve-plan",
            "/always-approve",
            "/dont-ask",
            "/accept-edits",
            "/approval-mode",
        ] {
            assert!(
                idle.iter().any(|command| command.name == name),
                "{name} must appear in /help discovery"
            );
        }
        let live = palette_matching("/bt", true, &[], "", &[]);
        assert!(live.iter().any(|command| command.name == "/btw"));
        let idle_btw = palette_matching("/bt", false, &[], "", &[])
            .into_iter()
            .find(|command| command.name == "/btw")
            .unwrap();
        assert_eq!(
            idle_btw.unavailable_reason,
            Some(MessageId::CommandRequiresActiveExecution)
        );
    }

    #[test]
    fn approval_mode_and_btw_accept_tails() {
        let (command, tail) = resolve_operator_input("/approval-mode dont-ask").unwrap();
        assert_eq!(command.id, CommandId::ApprovalMode);
        assert_eq!(tail, Some("dont-ask"));
        assert!(accepts_arguments(CommandId::ApprovalMode));
        for preset in ["agent", "read-only", "accept-edits"] {
            let input = format!("/approval-mode {preset}");
            let (command, tail) = resolve_operator_input(&input).unwrap();
            assert_eq!(command.id, CommandId::ApprovalMode);
            assert_eq!(tail, Some(preset));
        }
        let (command, tail) = resolve_operator_input("/btw what is the plan").unwrap();
        assert_eq!(command.id, CommandId::Btw);
        assert_eq!(tail, Some("what is the plan"));
        assert!(accepts_arguments(CommandId::Btw));
        assert!(!accepts_arguments(CommandId::AlwaysApprove));
        assert!(!accepts_arguments(CommandId::ApprovePlan));
        assert!(is_btw_fork_request("--fork later"));
        assert!(is_btw_fork_request("-fork"));
        assert!(!is_btw_fork_request("what is the plan"));
    }

    #[test]
    fn side_dual_pane_is_withdrawn_not_registered() {
        assert!(COMMANDS.iter().all(|command| command.name != "/side"));
        assert!(COMMANDS.iter().all(|command| command.name != "/side-close"));
        assert!(resolve("/side", false).is_none());
        assert_eq!(
            withdrawn_operator("/side now"),
            Some(MessageId::SideWithdrawn)
        );
        assert_eq!(
            withdrawn_operator("/side-close"),
            Some(MessageId::SideWithdrawn)
        );
        assert!(withdrawn_operator("/btw --fork later").is_none());
    }

    #[test]
    fn unique_damerau_typo_resolves_and_ambiguous_distance_does_not() {
        assert_eq!(
            lookup("/stauts").map(|command| command.id),
            Some(CommandId::Status)
        );
        assert_eq!(
            resolve("/helpp", false).map(|command| command.id),
            Some(CommandId::Help)
        );
        assert_eq!(
            resolve("/quti", false).map(|command| command.id),
            Some(CommandId::Quit)
        );
        assert_eq!(
            lookup("/exitt").map(|command| command.id),
            Some(CommandId::Quit)
        );
        assert!(lookup("/xyz").is_none());
        assert!(lookup("/n").is_none());
    }

    #[test]
    fn longer_names_allow_distance_two_short_names_do_not() {
        assert_eq!(
            lookup("/statuuss").map(|command| command.id),
            Some(CommandId::Status)
        );
        assert!(lookup("/neeww").is_none());
    }

    #[test]
    fn palette_ranks_a_transposition_typo_above_unrelated_commands() {
        let matches = palette_matching("/stauts", false, &[], "", &[]);
        assert_eq!(matches[0].name, "/status");
        let matches = palette_matching(
            "/revieew",
            false,
            &[PromptCommand {
                id: "prompt:project:review".into(),
                name: "review".into(),
                source: "project".into(),
                ..PromptCommand::default()
            }],
            "revision",
            &[],
        );
        assert_eq!(matches[0].id, "prompt:project:review");
    }

    #[test]
    fn prompt_command_typo_resolves_only_when_unique() {
        let commands = vec![
            PromptCommand {
                id: "prompt:project:review".into(),
                name: "review".into(),
                source: "project".into(),
                ..PromptCommand::default()
            },
            PromptCommand {
                id: "prompt:project:revise".into(),
                name: "revise".into(),
                source: "project".into(),
                ..PromptCommand::default()
            },
        ];
        assert_eq!(
            prompt_command_id("/revieew", &commands).as_deref(),
            Some("prompt:project:review")
        );
        assert_eq!(
            prompt_command_id("/revie", &commands),
            None,
            "review and revise are both one edit from /revie"
        );
    }

    #[test]
    fn command_mru_round_trips_and_sanitizes() {
        let directory = tempfile::tempdir().unwrap();
        let path = directory.path().join("slash-mru.json");
        persist_command_mru_to(
            &path,
            &[
                "operator:status".into(),
                String::new(),
                "operator:status".into(),
                "prompt:project:review".into(),
                "bad\u{0007}id".into(),
            ],
        )
        .unwrap();
        assert_eq!(
            load_command_mru_from(&path),
            vec![
                "operator:status".to_owned(),
                "prompt:project:review".to_owned()
            ]
        );
        let mut mru = load_command_mru_from(&path);
        retain_operator_mru(&mut mru);
        assert_eq!(mru, vec!["operator:status".to_owned()]);
        assert!(load_command_mru_from(&directory.path().join("missing.json")).is_empty());
    }
}
