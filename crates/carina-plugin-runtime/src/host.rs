//! WASM host environment (PRD §8.7).
//!
//! Defines the ABI between a plugin and the runtime and enforces the
//! manifest permission boundary. The plugin's only imports are:
//!
//! - `env.carina_log(ptr, len)` — emit a UTF-8 log line.
//! - `env.carina_request_capability(cap_ptr, cap_len, res_ptr, res_len) -> i32`
//!   returns 1 if allowed, 0 if denied. A request for a capability not
//!   declared in the manifest is always denied and recorded.
//!
//! The plugin exports `carina_run() -> i32` (a result code).

use crate::{Manifest, PluginError};
use wasmi::{Caller, Engine, Extern, Linker, Memory, Module, Store};

/// The decision a plugin capability request resolved to.
#[derive(Debug, Clone)]
pub struct HostDecision {
    pub capability: String,
    pub resource: String,
    pub allowed: bool,
    pub reason: String,
}

/// Implemented by the kernel: the final say on whether a *declared*
/// capability is actually permitted by the session policy. This lets the
/// two gates compose — manifest first, then live policy.
pub trait CapabilityHost: Send + 'static {
    fn allow(&self, capability: &str, resource: &str) -> bool;
}

/// A host that permits everything the manifest already declared (used when
/// no session policy is attached, e.g. unit tests).
pub struct AllowDeclared;
impl CapabilityHost for AllowDeclared {
    fn allow(&self, _capability: &str, _resource: &str) -> bool {
        true
    }
}

/// The outcome of running a plugin.
#[derive(Debug, Clone)]
pub struct RunOutcome {
    pub result_code: i32,
    pub logs: Vec<String>,
    pub decisions: Vec<HostDecision>,
}

struct HostState {
    manifest: Manifest,
    host: Box<dyn CapabilityHost>,
    memory: Option<Memory>,
    logs: Vec<String>,
    decisions: Vec<HostDecision>,
}

/// Runs WASM plugins under the capability boundary.
pub struct PluginRuntime {
    engine: Engine,
}

impl Default for PluginRuntime {
    fn default() -> Self {
        Self::new()
    }
}

impl PluginRuntime {
    pub fn new() -> Self {
        Self {
            engine: Engine::default(),
        }
    }

    /// Loads and runs a WASM plugin's `carina_run` export. `wasm` is a module
    /// binary; `host` is the live policy gate.
    pub fn run(
        &self,
        manifest: &Manifest,
        wasm: &[u8],
        host: Box<dyn CapabilityHost>,
    ) -> Result<RunOutcome, PluginError> {
        let module =
            Module::new(&self.engine, wasm).map_err(|e| PluginError::Wasm(e.to_string()))?;

        let state = HostState {
            manifest: manifest.clone(),
            host,
            memory: None,
            logs: Vec::new(),
            decisions: Vec::new(),
        };
        let mut store = Store::new(&self.engine, state);
        let mut linker = <Linker<HostState>>::new(&self.engine);

        // env.carina_log(ptr, len)
        linker
            .func_wrap(
                "env",
                "carina_log",
                |mut caller: Caller<'_, HostState>, ptr: i32, len: i32| {
                    if let Some(text) = read_string(&mut caller, ptr, len) {
                        caller.data_mut().logs.push(text);
                    }
                },
            )
            .map_err(|e| PluginError::Wasm(e.to_string()))?;

        // env.carina_request_capability(cap_ptr, cap_len, res_ptr, res_len) -> i32
        linker
            .func_wrap(
                "env",
                "carina_request_capability",
                |mut caller: Caller<'_, HostState>,
                 cap_ptr: i32,
                 cap_len: i32,
                 res_ptr: i32,
                 res_len: i32|
                 -> i32 {
                    let capability = read_string(&mut caller, cap_ptr, cap_len).unwrap_or_default();
                    let resource = read_string(&mut caller, res_ptr, res_len).unwrap_or_default();

                    // Gate 1: manifest must declare it.
                    let declared = caller.data().manifest.declares(&capability, &resource);
                    // Gate 2: live policy (kernel) must permit it.
                    let allowed = declared && caller.data().host.allow(&capability, &resource);
                    let reason = if !declared {
                        "undeclared capability (not in manifest)".to_string()
                    } else if !allowed {
                        "denied by session policy".to_string()
                    } else {
                        "allowed".to_string()
                    };
                    caller.data_mut().decisions.push(HostDecision {
                        capability,
                        resource,
                        allowed,
                        reason,
                    });
                    if allowed {
                        1
                    } else {
                        0
                    }
                },
            )
            .map_err(|e| PluginError::Wasm(e.to_string()))?;

        let instance = linker
            .instantiate(&mut store, &module)
            .map_err(|e| PluginError::Wasm(e.to_string()))?
            .start(&mut store)
            .map_err(|e| PluginError::Wasm(e.to_string()))?;

        // Capture the plugin's exported memory for string reads.
        if let Some(Extern::Memory(mem)) = instance.get_export(&store, "memory") {
            store.data_mut().memory = Some(mem);
        }

        let run = instance
            .get_typed_func::<(), i32>(&store, "carina_run")
            .map_err(|e| PluginError::Wasm(format!("missing carina_run export: {e}")))?;
        let result_code = run
            .call(&mut store, ())
            .map_err(|e| PluginError::Wasm(e.to_string()))?;

        let data = store.into_data();
        Ok(RunOutcome {
            result_code,
            logs: data.logs,
            decisions: data.decisions,
        })
    }
}

fn read_string(caller: &mut Caller<'_, HostState>, ptr: i32, len: i32) -> Option<String> {
    let memory = caller.data().memory?;
    let mut buf = vec![0u8; len.max(0) as usize];
    memory.read(caller, ptr as usize, &mut buf).ok()?;
    String::from_utf8(buf).ok()
}
