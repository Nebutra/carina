# Contributing

Carina is currently source-first alpha software. Contributions should preserve
the core runtime invariants: policy before side effects, auditability, rollback,
and clear user-facing behavior.

## Development Setup

Requirements:

- Go 1.25+
- Rust 1.85+
- Zig 0.15.x
- macOS or Linux

Build everything:

```bash
make all
```

To install the built binaries onto `PATH` (mirrors the release `bin/` layout),
use `make install`; it defaults to `~/.local/bin` and honors
`PREFIX=/usr/local`. `make uninstall` removes them.

## Test Matrix

Run focused tests while developing, then the release gate before larger changes:

```bash
go test ./go/... ./apps/...
cargo test
go test -race ./go/daemon ./go/config ./apps/carina-daemon
make release-check
```

### TUI rendering changes

Golden terminal frames live under `crates/carina-tui/tests/snapshots/`. A
rendering change is reviewed through the resulting `.snap` diff, including
cell styles, rather than inferred from the Rust diff alone:

```bash
cargo install cargo-insta
cargo insta test -p carina-tui
cargo insta review
```

Do not accept snapshots only to make CI green. Freeze animation/time inputs,
inspect every changed frame at its declared terminal size, and include the
accepted `.snap` files in the pull request.

For changes touching Rust kernel behavior, rebuild the release kernel service
before Go integration tests:

```bash
cargo build --release -p carina-kernel --bin carina-kernel-service
```

## Contribution Guidelines

- Keep user-facing naming consistently `carina` / `Nebutra Carina`.
- Do not expose historical aliases in CLI help or product docs.
- Prefer objective documentation over promotional claims.
- Do not claim competitor limitations without a current official source.
- Keep policy/audit/patch behavior covered by tests.
- Avoid unrelated formatting churn.

## Pull Request Checklist

- Explain the user-facing behavior change.
- List tests run.
- Update README/docs when commands or workflows change.
- Update `docs/release.md` when release artifacts or gates change.
- Keep secrets out of logs, tests, and examples.
