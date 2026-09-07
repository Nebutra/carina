#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use serde::{Deserialize, Serialize};
use std::ffi::OsString;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use tauri_plugin_dialog::DialogExt;

const MAX_BOOTSTRAP_OUTPUT_BYTES: usize = 64 * 1024;
const MAX_BOOTSTRAP_ERROR_BYTES: usize = 4 * 1024;

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "lowercase")]
enum GatewayRole {
    Observer,
    Operator,
}

impl GatewayRole {
    fn parse(value: &str) -> Result<Self, String> {
        match value {
            "observer" => Ok(Self::Observer),
            "operator" => Ok(Self::Operator),
            _ => Err("role must be observer or operator".to_string()),
        }
    }

    fn as_str(self) -> &'static str {
        match self {
            Self::Observer => "observer",
            Self::Operator => "operator",
        }
    }
}

#[derive(Debug, Deserialize, Eq, PartialEq, Serialize)]
struct BootstrapWorkspaceResult {
    workspace_root: String,
    gateway_url: String,
    token: String,
    role: GatewayRole,
    expires_at: String,
}

fn executable_name() -> &'static str {
    if cfg!(windows) {
        "carina.exe"
    } else {
        "carina"
    }
}

fn is_executable_file(path: &Path) -> bool {
    let Ok(metadata) = path.metadata() else {
        return false;
    };
    if !metadata.is_file() {
        return false;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        metadata.permissions().mode() & 0o111 != 0
    }
    #[cfg(not(unix))]
    {
        true
    }
}

fn resolve_carina_binary_from(
    explicit: Option<OsString>,
    path_value: Option<OsString>,
    fallback_candidates: &[PathBuf],
) -> Result<PathBuf, String> {
    if let Some(value) = explicit.filter(|value| !value.is_empty()) {
        let path = PathBuf::from(value);
        if !path.is_absolute() {
            return Err("CARINA_CLI_BIN must be an absolute path".to_string());
        }
        if !is_executable_file(&path) {
            return Err("CARINA_CLI_BIN does not point to an executable file".to_string());
        }
        return path
            .canonicalize()
            .map_err(|error| format!("resolve CARINA_CLI_BIN: {error}"));
    }

    if let Some(path_value) = path_value {
        for directory in std::env::split_paths(&path_value) {
            let candidate = directory.join(executable_name());
            if is_executable_file(&candidate) {
                return candidate
                    .canonicalize()
                    .map_err(|error| format!("resolve installed Carina CLI: {error}"));
            }
        }
    }
    for candidate in fallback_candidates {
        if is_executable_file(candidate) {
            return candidate
                .canonicalize()
                .map_err(|error| format!("resolve installed Carina CLI: {error}"));
        }
    }
    Err("Carina CLI was not found. Install the matching Carina CLI/runtime or set CARINA_CLI_BIN to its absolute path.".to_string())
}

fn resolve_carina_binary() -> Result<PathBuf, String> {
    let mut fallback_candidates = Vec::new();
    if let Ok(current_exe) = std::env::current_exe() {
        if let Some(directory) = current_exe.parent() {
            fallback_candidates.push(directory.join(executable_name()));
        }
    }
    fallback_candidates.extend([
        PathBuf::from("/opt/homebrew/bin").join(executable_name()),
        PathBuf::from("/usr/local/bin").join(executable_name()),
    ]);
    resolve_carina_binary_from(
        std::env::var_os("CARINA_CLI_BIN"),
        std::env::var_os("PATH"),
        &fallback_candidates,
    )
}

fn is_loopback_host(host: &str) -> bool {
    matches!(host, "localhost" | "tauri.localhost")
        || host
            .parse::<IpAddr>()
            .is_ok_and(|address| address.is_loopback())
}

fn validate_workspace(value: &str) -> Result<PathBuf, String> {
    let path = PathBuf::from(value);
    if !path.is_absolute() {
        return Err("workspace must be an absolute path".to_string());
    }
    let canonical = path
        .canonicalize()
        .map_err(|error| format!("workspace is unavailable: {error}"))?;
    if !canonical.is_dir() {
        return Err("workspace must be a directory".to_string());
    }
    Ok(canonical)
}

fn validate_origin(value: &str) -> Result<String, String> {
    let parsed = url::Url::parse(value).map_err(|_| "origin must be a valid URL origin")?;
    if parsed.username() != ""
        || parsed.password().is_some()
        || parsed.query().is_some()
        || parsed.fragment().is_some()
        || parsed.path() != "/"
    {
        return Err("origin must not contain credentials, a path, query, or fragment".to_string());
    }
    if !is_loopback_host(parsed.host_str().unwrap_or_default())
        || !matches!(parsed.scheme(), "http" | "https" | "tauri")
    {
        return Err("origin must be a local Tauri or loopback origin".to_string());
    }
    Ok(value.trim_end_matches('/').to_string())
}

fn parse_bootstrap_output(
    data: &[u8],
    workspace: &Path,
    requested_role: GatewayRole,
) -> Result<BootstrapWorkspaceResult, String> {
    if data.is_empty() || data.len() > MAX_BOOTSTRAP_OUTPUT_BYTES {
        return Err("Carina CLI returned an empty or oversized bootstrap response".to_string());
    }
    let result: BootstrapWorkspaceResult = serde_json::from_slice(data)
        .map_err(|error| format!("Carina CLI returned invalid bootstrap JSON: {error}"))?;
    if result.token.is_empty() {
        return Err("Carina CLI returned an empty bootstrap token".to_string());
    }
    if result.expires_at.is_empty() {
        return Err("Carina CLI returned an empty bootstrap expiry".to_string());
    }
    if result.role != requested_role {
        return Err("Carina CLI returned a different Gateway role than requested".to_string());
    }
    let returned_workspace = validate_workspace(&result.workspace_root)?;
    if returned_workspace != workspace {
        return Err("Carina CLI returned a different workspace than requested".to_string());
    }
    let gateway = url::Url::parse(&result.gateway_url)
        .map_err(|_| "Carina CLI returned an invalid Gateway URL")?;
    if !matches!(gateway.scheme(), "ws" | "wss")
        || gateway.username() != ""
        || gateway.password().is_some()
        || gateway.query().is_some()
        || gateway.fragment().is_some()
        || gateway.path() != "/gateway"
        || !is_loopback_host(gateway.host_str().unwrap_or_default())
    {
        return Err("Carina CLI returned an invalid Gateway URL".to_string());
    }
    Ok(result)
}

fn bootstrap_workspace_blocking(
    workspace: String,
    role: String,
    origin: String,
) -> Result<BootstrapWorkspaceResult, String> {
    let binary = resolve_carina_binary()?;
    let workspace = validate_workspace(&workspace)?;
    let role = GatewayRole::parse(&role)?;
    let origin = validate_origin(&origin)?;
    let output = Command::new(binary)
        .arg("harness")
        .arg("bootstrap")
        .arg("--workspace")
        .arg(&workspace)
        .arg("--role")
        .arg(role.as_str())
        .arg("--origin")
        .arg(origin)
        .arg("--json")
        .stdin(Stdio::null())
        .output()
        .map_err(|error| format!("start Carina CLI bootstrap: {error}"))?;
    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        let detail = stderr
            .trim()
            .chars()
            .take(MAX_BOOTSTRAP_ERROR_BYTES)
            .collect::<String>();
        return Err(if detail.is_empty() {
            format!("Carina CLI bootstrap failed with {}", output.status)
        } else {
            format!("Carina CLI bootstrap failed: {detail}")
        });
    }
    parse_bootstrap_output(&output.stdout, &workspace, role)
}

#[tauri::command]
async fn pick_workspace(app: tauri::AppHandle) -> Option<String> {
    tauri::async_runtime::spawn_blocking(move || {
        app.dialog()
            .file()
            .set_title("Choose Carina workspace")
            .blocking_pick_folder()
            .and_then(|folder| folder.into_path().ok())
            .map(|path| path.to_string_lossy().into_owned())
    })
    .await
    .ok()
    .flatten()
}

#[tauri::command]
async fn bootstrap_workspace(
    workspace: String,
    role: String,
    origin: String,
) -> Result<BootstrapWorkspaceResult, String> {
    tauri::async_runtime::spawn_blocking(move || {
        bootstrap_workspace_blocking(workspace, role, origin)
    })
    .await
    .map_err(|error| format!("join Carina CLI bootstrap: {error}"))?
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_dialog::init())
        .invoke_handler(tauri::generate_handler![
            pick_workspace,
            bootstrap_workspace
        ])
        .run(tauri::generate_context!())
        .expect("error while running Carina Harness")
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn test_directory(name: &str) -> PathBuf {
        let directory = std::env::temp_dir().join(format!(
            "carina-tauri-{name}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("clock")
                .as_nanos()
        ));
        fs::create_dir_all(&directory).expect("create test directory");
        directory
    }

    fn executable(path: &Path) {
        fs::write(path, b"test").expect("write executable");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(path, fs::Permissions::from_mode(0o700)).expect("mark executable");
        }
    }

    #[test]
    fn role_validation_is_closed_to_known_gateway_roles() {
        assert_eq!(GatewayRole::parse("observer"), Ok(GatewayRole::Observer));
        assert_eq!(GatewayRole::parse("operator"), Ok(GatewayRole::Operator));
        assert!(GatewayRole::parse("admin").is_err());
        assert!(GatewayRole::parse("operator --help").is_err());
    }

    #[test]
    fn explicit_binary_must_be_absolute_and_executable() {
        assert!(resolve_carina_binary_from(Some(OsString::from("carina")), None, &[]).is_err());
        let directory = test_directory("explicit-bin");
        let binary = directory.join(executable_name());
        executable(&binary);
        assert_eq!(
            resolve_carina_binary_from(Some(binary.clone().into_os_string()), None, &[]).unwrap(),
            binary.canonicalize().unwrap()
        );
        fs::remove_dir_all(directory).expect("remove test directory");
    }

    #[test]
    fn installed_binary_is_resolved_from_path() {
        let directory = test_directory("path-bin");
        let binary = directory.join(executable_name());
        executable(&binary);
        assert_eq!(
            resolve_carina_binary_from(None, Some(directory.clone().into_os_string()), &[])
                .unwrap(),
            binary.canonicalize().unwrap()
        );
        fs::remove_dir_all(directory).expect("remove test directory");
    }

    #[test]
    fn installed_binary_is_resolved_from_trusted_fallback() {
        let directory = test_directory("fallback-bin");
        let binary = directory.join(executable_name());
        executable(&binary);
        assert_eq!(
            resolve_carina_binary_from(None, None, std::slice::from_ref(&binary)).unwrap(),
            binary.canonicalize().unwrap()
        );
        fs::remove_dir_all(directory).expect("remove test directory");
    }

    #[test]
    fn bootstrap_json_is_typed_and_bound_to_the_request() {
        let workspace = test_directory("bootstrap-json").canonicalize().unwrap();
        let encoded = serde_json::to_vec(&serde_json::json!({
            "workspace_root": workspace,
            "gateway_url": "ws://127.0.0.1:8765/gateway",
            "token": "short-lived-token",
            "role": "operator",
            "expires_at": "2026-09-04T12:00:00Z"
        }))
        .unwrap();
        let result = parse_bootstrap_output(&encoded, &workspace, GatewayRole::Operator).unwrap();
        assert_eq!(result.role, GatewayRole::Operator);
        assert_eq!(result.token, "short-lived-token");
        fs::remove_dir_all(workspace).expect("remove test directory");
    }

    #[test]
    fn bootstrap_json_rejects_mismatched_role_and_workspace_alias() {
        let workspace = test_directory("bootstrap-mismatch").canonicalize().unwrap();
        let wrong_role = serde_json::to_vec(&serde_json::json!({
            "workspace_root": workspace,
            "gateway_url": "ws://127.0.0.1:8765/gateway",
            "token": "short-lived-token",
            "role": "observer",
            "expires_at": "2026-09-04T12:00:00Z"
        }))
        .unwrap();
        assert!(parse_bootstrap_output(&wrong_role, &workspace, GatewayRole::Operator).is_err());
        let legacy_alias = serde_json::to_vec(&serde_json::json!({
            "workspace": workspace,
            "gateway_url": "ws://127.0.0.1:8765/gateway",
            "token": "short-lived-token",
            "role": "operator",
            "expires_at": "2026-09-04T12:00:00Z"
        }))
        .unwrap();
        assert!(parse_bootstrap_output(&legacy_alias, &workspace, GatewayRole::Operator).is_err());
        fs::remove_dir_all(workspace).expect("remove test directory");
    }

    #[test]
    fn bootstrap_json_rejects_remote_or_ambiguous_gateway_urls() {
        let workspace = test_directory("bootstrap-gateway-url")
            .canonicalize()
            .unwrap();
        for gateway_url in [
            "ws://example.com/gateway",
            "ws://127.0.0.1:8765/other",
            "ws://127.0.0.1:8765/gateway?token=leak",
            "ws://user@127.0.0.1:8765/gateway",
        ] {
            let encoded = serde_json::to_vec(&serde_json::json!({
                "workspace_root": workspace,
                "gateway_url": gateway_url,
                "token": "short-lived-token",
                "role": "operator",
                "expires_at": "2026-09-04T12:00:00Z"
            }))
            .unwrap();
            assert!(parse_bootstrap_output(&encoded, &workspace, GatewayRole::Operator).is_err());
        }
        fs::remove_dir_all(workspace).expect("remove test directory");
    }
}
