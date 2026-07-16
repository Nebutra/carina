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
