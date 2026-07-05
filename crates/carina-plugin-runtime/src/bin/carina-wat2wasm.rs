//! carina-wat2wasm — compile a WebAssembly text (.wat) file to a binary
//! (.wasm) module. A small convenience so plugin authors can build example
//! plugins without installing the wabt toolchain.
//!
//! Usage: carina-wat2wasm <input.wat> <output.wasm>

use std::process::exit;

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args.len() != 3 {
        eprintln!("usage: carina-wat2wasm <input.wat> <output.wasm>");
        exit(2);
    }
    let source = match std::fs::read_to_string(&args[1]) {
        Ok(s) => s,
        Err(e) => {
            eprintln!("carina-wat2wasm: read {}: {e}", args[1]);
            exit(1);
        }
    };
    let wasm = match wat::parse_str(&source) {
        Ok(bytes) => bytes,
        Err(e) => {
            eprintln!("carina-wat2wasm: compile error: {e}");
            exit(1);
        }
    };
    if let Err(e) = std::fs::write(&args[2], &wasm) {
        eprintln!("carina-wat2wasm: write {}: {e}", args[2]);
        exit(1);
    }
    println!("wrote {} ({} bytes)", args[2], wasm.len());
}
