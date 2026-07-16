<div align="center">

<img src="docs/brand/assets/hero/carina-readme-hero.webp" alt="暗い鉱物質の表面に置かれた Nebutra Carina シンボル" width="100%" />

# Nebutra Carina

**ポリシー、監査、ロールバックの境界内で、実リポジトリ上のコーディングエージェントを動かす。**

[![status](https://img.shields.io/badge/status-alpha-8E4053)](#current-status)
[![build](https://img.shields.io/badge/build-source%20first-176F70)](#quickstart-from-source)
[![runtime](https://img.shields.io/badge/runtime-local--first-087C58)](#why-carina)
[![audit](https://img.shields.io/badge/audit-hash--chained-8C5A15)](#review-and-audit)
[![license](https://img.shields.io/badge/license-MIT-182023)](LICENSE)

[English](README.md) · [简体中文](README.zh-CN.md) · **日本語**

</div>

Carina は、AI コーディングエージェントのためのローカルファーストなランタイム層です。エディタ、チャットアプリ、ホステッドサンドボックスではありません。エージェントとマシンの間に入り、ファイル読み取り、編集、コマンド、ネットワークアクセス、プラグイン、secret を明示的なポリシーの後ろに置きます。

このリポジトリは、ソースビルド、ローカル実験、自社の Agent 実行基盤を設計するチームに向いています。まだ alpha です。macOS パッケージは Nebutra Homebrew tap から利用できます。Apple signing/notarization の自動化は実装済みですが、release credential を待っています。Linux archive/package、npm、Windows worker、container、package 済み VS Code/Web Operator client は release pipeline に入り、残作業は外部 registry、publisher、credential、hosting の有効化です。

## Why Carina

難しいのは、モデルにコードを書かせることだけではありません。モデルが行動を決めた後、何を許可するかを制御することです。

Carina が提供するもの：

- **アクションごとの権限判定**：ファイル、コマンド、ネットワーク、secret、patch apply、プラグイン、remote work。
- **監査可能な実行**：append-only の hash chain が permission decision と許可された side effect を記録。
- **トランザクショナルなファイル変更**：patch を propose、inspect、apply、rollback できる。
- **Daemon-backed session**：CLI 終了後も session や background task を保持できる。
- **BYOK model access**：provider catalog discovery、ローカル API key 優先、設定時は Nebutra OAuth fallback。
- **MCP、plugin、sub-agent、workflow、egress control** を同じ capability boundary の内側で扱う。

## Good Fits

向いている用途：

- ローカルリポジトリで Agent task を実行しつつ、raw machine access を与えたくない。
- エージェントが何を読み、何を変更し、何を実行し、なぜ許可されたかを残したい。
- IDE extension、CI integration、internal Agent platform、workflow runner の実行基盤が必要。
- sub-agent、plugin、remote worker に親タスクより狭い権限だけを与えたい。
- rollback と audit が重要な環境で Agent output を評価したい。

向いていない用途：

- 必要なのはエディタ内 assistant だけ。
- hosted managed agent service が欲しい。
- 今日すぐ安定した packaged release が必要。

## Current Status

現在実装されているもの：

| Area | Today |
|---|---|
| Sessions and tasks | daemon session、background run、event stream、attach/replay、task steering |
| Agent loop | ReAct loop、structured action、dual-threshold/token-triggered prompt compaction（verbatim-user preservation 付き）、structured compaction summary、canonical-signature loop detection、consecutive-failure circuit breaker、opt-in best-of-N patch generation、success check、verifier、risk review |
| Memory | local governed memory store with `memory` / `user` targets, frozen per-run prompt snapshot, native `memory` tool, local `memory.*` RPC, and kernel-gated `MemoryWrite` audit |
| Permissions | built-in profile、approval mode、justification 付き approval overlay、workspace trust、org-locked config keys、per-agent tool allow-list と kernel-gated spawn capability を持つ宣言的 sub-agent manifest |
| Audit | hash-chained event log、audit export、verify、normalized `session.items`、turn net diff |
| File changes | transactional patch propose/apply/rollback、post-edit diagnostics |
| Commands | risk classification、approval gate、command output event、optional OS sandbox backend |
| Network and secrets | deny-by-default egress proxy、allowlist、daemon-side credential injection、explicit per-host HTTPS MITM opt-in |
| Models | BYOK auth chain、provider catalog、OpenAI/Anthropic/Gemini/OpenRouter-style adapter、catalog-gated image input（raw bytes は artifact store のみ、transcript/audit には不出）|
| Context engine | native context-engine boundary、bundled/configured Headroom discovery、private managed MCP transport、`carina context` diagnostics |
| Integration | MCP client/server（`mcp_find` tool search 付き）、WASM plugin boundary（org/user/project tighten-only enable merge）、worker、workflow DAG |
| Nebutra boundary | ローカル runtime が action authority を維持し、identity と multi-endpoint sync は Nebutra Cloud（`nebutra.com`）の境界に置く |

外部 activation が必要なもの：

- Apple に受理される credentialed signing/notarization public release；
  fail-closed automation は実装済みですが、Apple credential は未設定；
- Linux/npm/container の public registry と trusted publisher 設定；
- package 済み VS Code/Web Operator client の Marketplace/hosting 公開；
- tap 未追加の `brew install carina` に必要な Homebrew Core upstream review；
- real provider credential と代表的 terminal hardware を使う CJK/reconnect 検証；
- Nebutra Cloud API、tenant、identity、retention contract；local sync は off；
- Windows は remote worker package のみを support し、desktop daemon/CLI は非対応。

## Homebrew Install

Apple Silicon と Intel macOS 向け package を Nebutra 公式 tap からインストールできます：

```bash
brew install Nebutra/tap/carina
```

この fully-qualified command は tap を追加して Carina Formula を trust します。
初回インストール後は `brew install carina` でも同じ Formula を解決できます。

Upgrade は Homebrew の標準フローを使います：

```bash
brew update
brew upgrade carina
```

`brew update carina` は有効な Homebrew command ではありません。
インストール後に daemon は自動起動しません。

## Quickstart From Source

Requirements:

- Go 1.25+
- Rust 1.85+
- Zig 0.15.x
- macOS or Linux

Build and install:

```bash
git clone https://github.com/Nebutra/carina
cd carina
make install
```

`make install` はすべてをビルドし、`carina*` バイナリを `~/.local/bin` に
インストールします（`PREFIX=/usr/local` で変更可能）。そのディレクトリが
`PATH` 上にあることを確認してください。インストールせずにビルドだけする
場合は `make all` の後に `./bin/carina` を直接使います。Homebrew で
インストールした場合はすでに `PATH` 上にあります。

Start daemon:

```bash
carina-daemon &
```

モデル credential を daemon process に渡します。BYOK API key が優先です。設定されていれば Nebutra OAuth fallback も利用できます。

```bash
export ANTHROPIC_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

現在の repository で task を実行：

```bash
carina run "fix the failing tests and show the patch"
```

Submit 後、CLI は continuation hint を表示します：

```bash
To continue this session, run:
  carina resume <session_id>
```

結果を確認：

```bash
carina sessions
carina resume <session_id> "continue the previous task"
carina items <session_id>
carina audit verify <session_id>
carina patch list <session_id>
carina patch show <session_id> <patch_id>
```

Applied patch を rollback：

```bash
carina patch rollback <session_id> <patch_id>
```

## Common Workflows

### Local Repository Work

通常の開発では default の `safe-edit` session を使います。Agent は workspace を読み、patch を提案し、allowlist された build/test command を実行できます。危険な command、network access、secret、plugin は profile に応じて拒否または承認待ちになります。

### Review And Audit

`carina items <session_id>` は thread/turn/item の normalized view を返し、turn-level patch summary も含みます。raw event chain と tamper-evidence が必要な場合は `carina audit <session_id>` または `carina audit verify <session_id>` を使います。

### Governed Memory

Carina keeps local long-term memory under the daemon state directory. Agent/project notes use `target=memory`; user profile facts use `target=user`. Memory enters each agent run as a frozen prompt snapshot, so writes during the run persist but do not rewrite that run's stable prompt prefix. Use local `memory.*` RPC methods or the native `memory` tool for add/replace/remove/batch. Writes go through the default approval-gated `MemoryWrite` capability, are bounded and content-scanned, and are audited by target/scope/action/content hash instead of raw memory text.

External semantic memory providers and Nebutra Cloud memory sync are not enabled in the source-first alpha.

### Native Context Engine

Release packages include a pinned Headroom executable as `bin/headroom`.
`context_engine=auto` only enables bundled or explicitly configured Headroom; a
global `headroom` found on `PATH` is reported but not used as the built-in
engine.

```bash
carina context status
carina context doctor
carina context stats
```

The managed Headroom MCP server is private to Carina's context adapter and is
not listed as a public agent MCP tool.

### BYOK Providers

Credential を保存し、provider catalog を確認：

```bash
carina auth login anthropic - < ~/.secrets/anthropic-key
carina auth login openai - < ~/.secrets/openai-key
carina auth list
carina providers list --refresh
```

必要に応じて model を指定：

```bash
CARINA_REASONER_MODEL=openai/gpt-5 carina-daemon &
carina run --model openrouter/anthropic/claude-sonnet-4-5 "inspect this migration"
```

### Agent Modes And Slash Commands

Reusable agent と command を確認：

```bash
carina agents list
carina commands list
carina run --agent plan "inspect the release risk"
carina run "/review main"
```

Built-in agent には `build`、`plan`、`general`、`explore` があります。User/project override は `~/.carina/agents`、`<repo>/.carina/agents`、`~/.carina/commands`、`<repo>/.carina/commands` に置きます。

### Embedding

Carina を別の UI の背後に置く場合は、JSON-RPC、SDK、MCP server mode を使います。IDE extension、web console、CI workflow、internal Agent platform などに向いています。

## How It Compares

これは勝敗表ではありません。各プロジェクトは別の仕事に最適化されており、機能も変化します。最新の機能詳細は各公式ドキュメントを確認してください。

| If you primarily need... | Common choices | Where Carina fits |
|---|---|---|
| In-editor coding assistance | Cursor、Windsurf、Cline、IDE extensions | Carina can back an editor, but is not an editor product. |
| Terminal-first pair programming | Claude Code、Codex CLI、Aider、OpenCode | Carina focuses less on chat UX and more on runtime boundary, audit, rollback, workers, and embeddability. |
| Cloud-hosted agent tasks | OpenAI Codex cloud tasks and managed agent services | Carina is local-first. Cloud identity and multi-endpoint sync live behind Nebutra Cloud boundaries. |
| Disposable cloud sandboxes | E2B and other sandbox runtimes | Carina can use sandboxing, but the core unit is policy-gated action on a repository, not a hosted VM product. |
| Internal agent infrastructure | Custom stacks、CI systems、internal platforms | Carina is designed as a control-plane/runtime component. |

## Architecture

Carina は責務で分かれています：

| Layer | Responsibility |
|---|---|
| Agent surface | agent loop、transcript、approval、sub-agent、workflow |
| Control plane | session、scheduler、JSON-RPC、worker、event streaming、egress |
| Capability kernel | permission decision、policy、transactional patch、audit chain、plugin |
| Native toolchain | scan、grep、diff、patch、process execution、pty |
| Client surfaces | CLI、TUI、SDK、MCP client/server |

重要なのは言語分割ではなく境界です。Agent は action を要求し、runtime がそれを許可するか決め、結果を記録します。

## Security Model

Default posture:

1. Least privilege by default.
2. No access outside workspace unless explicitly granted.
3. Secrets unreadable by default.
4. Network access restricted by default.
5. Destructive commands denied by default.
6. File changes go through patch transactions.
7. Plugins start with no implicit permissions.
8. Persistent memory writes are capability-gated, scoped, bounded, and audited.

Alpha limitations:

- Carina itself is not a VM or complete container isolation system.
- OS sandbox backends exist, but production profiles need deployment review.
- Policy correctness depends on routing commands through the Carina daemon and toolchain.
- Release archive には checksum と GitHub build provenance があります。
  Apple code signing と notarization の automation は実装済みですが、
  credentialed かつ Apple-accepted な public release はまだ完了していません。

See [SECURITY.md](SECURITY.md) and [docs/security-model.md](docs/security-model.md).

## Development

Build and test:

```bash
make all
go test ./go/... ./apps/...
cargo test
go test -race ./go/daemon ./go/config ./apps/carina-daemon
```

Run local release gate:

```bash
make release-check
```

Build a local release candidate archive:

```bash
make release-package
```

More docs:

- [Product positioning](docs/product.md)
- [Nebutra Cloud boundary](docs/nebutra-cloud-boundary.md)
- [Roadmap](docs/roadmap.md)
- [Release process](docs/release.md)
- [Architecture](docs/architecture.md)
- [RPC API](docs/rpc-api.md)
- [Plugin model](docs/plugin-model.md)
- [Research status](docs/research/absorption-status.md)

## License

MIT License. See [LICENSE](LICENSE).
