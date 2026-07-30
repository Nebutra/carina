//! Issue #4: offscreen render benchmark smoke binary.
//!
//! Usage: `cargo run -p carina-tui --bin tui_bench -- --iterations 20`

use carina_tui::bench_harness::{format_result_jsonl, run_render_scenario};

fn main() {
    let iterations = std::env::args()
        .nth(1)
        .and_then(|value| value.parse().ok())
        .or_else(|| {
            std::env::args()
                .position(|arg| arg == "--iterations")
                .and_then(|index| std::env::args().nth(index + 1))
                .and_then(|value| value.parse().ok())
        })
        .unwrap_or(20);
    let result = run_render_scenario(iterations);
    println!("{}", format_result_jsonl(&result));
}
