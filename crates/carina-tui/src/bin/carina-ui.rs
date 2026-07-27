use std::path::PathBuf;

use anyhow::{Context, Result};
use carina_tui::{Options, choose_runtime_mode, run};
use clap::Parser;

#[derive(Debug, Parser)]
#[command(name = "carina-ui", version, about = "Internal Carina Ratatui surface")]
struct Args {
    #[arg(long)]
    socket: Option<PathBuf>,
    #[arg(long)]
    workspace: Option<PathBuf>,
    #[arg(long)]
    session: Option<String>,
    #[arg(long)]
    locale: Option<String>,
    #[arg(long)]
    locale_path: Option<PathBuf>,
    #[arg(long)]
    carina_bin: Option<PathBuf>,
    #[arg(long)]
    no_alt_screen: bool,
    #[arg(long, hide = true)]
    runtime_mode_setup: bool,
    #[arg(long, hide = true)]
    home: Option<PathBuf>,
}

fn main() {
    let exit = match try_main() {
        Ok(code) => code,
        Err(error) => {
            eprintln!("carina: {error:#}");
            1
        }
    };
    std::process::exit(exit);
}

fn try_main() -> Result<i32> {
    let args = Args::parse();
    if args.runtime_mode_setup {
        let carina_bin = args
            .carina_bin
            .context("runtime mode setup requires --carina-bin")?;
        let _home = args.home.context("runtime mode setup requires --home")?;
        let choice = choose_runtime_mode(args.no_alt_screen)?;
        let Some(choice) = choice else {
            return Ok(2);
        };
        let status = std::process::Command::new(&carina_bin)
            .args(["runtime", "mode", choice.as_str()])
            .status()
            .context("persist runtime mode")?;
        if !status.success() {
            return Ok(status.code().unwrap_or(1));
        }
        let status = std::process::Command::new(&carina_bin)
            .status()
            .context("restart Carina after runtime mode selection")?;
        return Ok(status.code().unwrap_or(1));
    }
    let workspace = args
        .workspace
        .map(Ok)
        .unwrap_or_else(std::env::current_dir)
        .context("resolve workspace")?;
    let socket = args
        .socket
        .or_else(|| std::env::var_os("CARINA_SOCKET").map(PathBuf::from))
        .or_else(default_socket)
        .context("resolve daemon socket; pass --socket")?;
    let carina_bin = args
        .carina_bin
        .or_else(|| std::env::var_os("CARINA_BIN").map(PathBuf::from));
    let outcome = run(Options {
        socket,
        workspace,
        session_id: args.session,
        locale: args.locale,
        locale_path: args.locale_path,
        carina_bin,
        no_alt_screen: args.no_alt_screen,
    })?;
    Ok(outcome.exit_code())
}

fn default_socket() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .map(|home| home.join(".carina/daemon.sock"))
}
