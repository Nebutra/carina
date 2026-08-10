use std::path::PathBuf;

use anyhow::{Context, Result};
use carina_tui::app::{ScreenMode, read_screen_handoff};
use carina_tui::density::DensityMode;
use carina_tui::i18n::Locale;
use carina_tui::{
    Options, RuntimeDiagnosticOptions, RuntimeDiagnosticOutcome, choose_runtime_mode, run,
    run_runtime_diagnostic,
};
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
    #[arg(long, value_enum, default_value_t = DensityArg::Compact)]
    density: DensityArg,
    #[arg(long)]
    density_path: Option<PathBuf>,
    #[arg(long)]
    carina_bin: Option<PathBuf>,
    #[arg(long)]
    no_alt_screen: bool,
    #[arg(long, value_enum)]
    screen_mode: Option<ScreenModeArg>,
    #[arg(long, hide = true)]
    screen_handoff: Option<PathBuf>,
    #[arg(long, value_enum, default_value_t = AltScreenArg::Auto)]
    alt_screen: AltScreenArg,
    #[arg(long, hide = true, value_enum, default_value_t = ScrollbackWrapArg::PreWrap)]
    scrollback_wrap: ScrollbackWrapArg,
    #[arg(long, hide = true)]
    runtime_mode_setup: bool,
    #[arg(long, hide = true)]
    home: Option<PathBuf>,
    #[arg(long, hide = true)]
    runtime_diagnostic: bool,
    #[arg(long, hide = true)]
    runtime_id: Option<String>,
    #[arg(long, hide = true)]
    runtime_log: Option<PathBuf>,
    #[arg(long, hide = true)]
    missing_method: Vec<String>,
    #[arg(long, hide = true)]
    obligation: Vec<String>,
}

#[derive(Debug, Clone, Copy, clap::ValueEnum)]
enum AltScreenArg {
    Auto,
    Always,
    Never,
}

#[derive(Debug, Clone, Copy, clap::ValueEnum)]
enum DensityArg {
    Compact,
    Comfortable,
}

#[derive(Debug, Clone, Copy, clap::ValueEnum)]
enum ScreenModeArg {
    Minimal,
    Fullscreen,
    Inline,
}

#[derive(Debug, Clone, Copy, clap::ValueEnum)]
enum ScrollbackWrapArg {
    PreWrap,
    Terminal,
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
    if args.runtime_diagnostic {
        let workspace = args
            .workspace
            .context("runtime diagnostic requires --workspace")?;
        let carina_bin = args
            .carina_bin
            .context("runtime diagnostic requires --carina-bin")?;
        let locale = args
            .locale
            .as_deref()
            .and_then(Locale::from_product_id)
            .unwrap_or(Locale::En);
        let outcome = run_runtime_diagnostic(RuntimeDiagnosticOptions {
            workspace,
            runtime_id: args.runtime_id.unwrap_or_default(),
            log_path: args.runtime_log.unwrap_or_default(),
            missing_methods: args.missing_method,
            obligations: args.obligation,
            locale,
            carina_bin: carina_bin.clone(),
            no_alt_screen: args.no_alt_screen,
        })?;
        if outcome == RuntimeDiagnosticOutcome::Restart {
            let status = std::process::Command::new(carina_bin)
                .status()
                .context("restart Carina after runtime replacement")?;
            return Ok(status.code().unwrap_or(1));
        }
        return Ok(2);
    }
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
        density: match args.density {
            DensityArg::Compact => DensityMode::Compact,
            DensityArg::Comfortable => DensityMode::Comfortable,
        },
        density_path: args.density_path,
        carina_bin,
        no_alt_screen: args.no_alt_screen,
        screen_mode: args.screen_mode.map(|mode| match mode {
            ScreenModeArg::Minimal => ScreenMode::Minimal,
            ScreenModeArg::Fullscreen => ScreenMode::Fullscreen,
            ScreenModeArg::Inline => ScreenMode::Inline,
        }),
        screen_handoff: args
            .screen_handoff
            .as_deref()
            .map(read_screen_handoff)
            .transpose()?,
        alt_screen: match args.alt_screen {
            AltScreenArg::Auto => carina_tui::AltScreenPolicy::Auto,
            AltScreenArg::Always => carina_tui::AltScreenPolicy::Always,
            AltScreenArg::Never => carina_tui::AltScreenPolicy::Never,
        },
        scrollback_wrap: match args.scrollback_wrap {
            ScrollbackWrapArg::PreWrap => carina_tui::ScrollbackWrap::PreWrap,
            ScrollbackWrapArg::Terminal => carina_tui::ScrollbackWrap::Terminal,
        },
    })?;
    Ok(outcome.exit_code())
}

fn default_socket() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .map(|home| home.join(".carina/daemon.sock"))
}
