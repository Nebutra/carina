# hello-plugin — Carina example WASM plugin

Demonstrates the plugin capability boundary (PRD §8.7): a plugin can only use
the capabilities it declares in its manifest, and each request is gated a
second time by the session's permission profile.

## Files

- `plugin.toml` — manifest. Declares `command_exec = ["go test ./..."]` and
  deliberately does **not** declare any `secret` permission.
- `hello.wat` — the plugin source (WebAssembly text).
- `hello.wasm` — compiled module (checked in so the example runs offline).

## What it does

`carina_run` makes two capability requests:

1. `command_exec` / `go test ./...` — **declared** → allowed (if the session
   profile also permits it).
2. `secret` / `API_KEY` — **undeclared** → refused, recorded as a
   `PolicyViolation` in the session audit log.

It returns `1` (only one request was allowed).

## Try it

```bash
# rebuild the wasm if you edit the .wat
cargo run --release -p carina-plugin-runtime --bin carina-wat2wasm -- \
  examples/plugins/hello/hello.wat examples/plugins/hello/hello.wasm

# with a running daemon:
carina plugin inspect examples/plugins/hello/plugin.toml
SID=$(carina run "demo" | grep -o 'sess_[a-f0-9]*' | head -1)
carina plugin run "$SID" examples/plugins/hello/plugin.toml examples/plugins/hello/hello.wasm
carina audit "$SID"   # see the PolicyViolation for the undeclared secret request
```
