<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina" width="100%" />

# Nebutra Carina

**ポリシー、監査、ロールバックの境界内で、実リポジトリ上のコーディングエージェントを動かす。**

[![status](https://img.shields.io/badge/status-alpha-0033FE)](#current-status)
[![build](https://img.shields.io/badge/build-source%20first-0B7285)](#quickstart-from-source)
[![runtime](https://img.shields.io/badge/runtime-local--first-0BF1C3)](#why-carina)
[![audit](https://img.shields.io/badge/audit-hash--chained-6D28D9)](#review-and-audit)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](LICENSE)

[English](README.md) · [简体中文](README.zh-CN.md) · **日本語**

</div>

Carina は、AI コーディングエージェントのためのローカルファーストなランタイム層です。エディタ、チャットアプリ、ホステッドサンドボックスではありません。エージェントとマシンの間に入り、ファイル読み取り、編集、コマンド、ネットワークアクセス、プラグイン、secret を明示的なポリシーの後ろに置きます。

このリポジトリは、ソースビルド、ローカル実験、自社の Agent 実行基盤を設計するチームに向いています。まだ alpha です。安定したパッケージ、公開インストーラー、磨き込まれた dashboard は未完成です。

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
| Agent loop | ReAct loop、structured action、prompt compaction、success check、verifier、risk review |
| Permissions | built-in profile、approval mode、justification 付き approval overlay、workspace trust、sub-agent attenuation |
| Audit | hash-chained event log、audit export、verify、normalized `session.items`、turn net diff |
| File changes | transactional patch propose/apply/rollback、post-edit diagnostics |
| Commands | risk classification、approval gate、command output event、optional OS sandbox backend |
| Network and secrets | deny-by-default egress proxy、allowlist、daemon-side credential injection、explicit per-host HTTPS MITM opt-in |
| Models | BYOK auth chain、provider catalog、OpenAI/Anthropic/Gemini/OpenRouter-style adapter |
| Integration | MCP client/server、WASM plugin boundary、worker、workflow DAG |

まだ product-complete ではないもの：

- signed public release、Homebrew tap、npm のインストールチャネル；
- contributor/security process の完成；
- polished TUI/dashboard；
- Windows support；
- TypeScript、Python、Go SDK の parity；
- remote-worker fleet の production guide。

## Quickstart From Source

Requirements:

- Go 1.25+
- Rust 1.85+
- Zig 0.15.x
- macOS or Linux

Build:

```bash
git clone https://github.com/Nebutra/carina
cd carina
make all
```

Start daemon:

```bash
./bin/carina-daemon &
```

モデル credential を daemon process に渡します。BYOK API key が優先です。設定されていれば Nebutra OAuth fallback も利用できます。

```bash
export ANTHROPIC_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

現在の repository で task を実行：

```bash
./bin/carina run "fix the failing tests and show the patch"
```

結果を確認：

```bash
./bin/carina sessions
./bin/carina items <session_id>
./bin/carina audit verify <session_id>
./bin/carina patch list <session_id>
./bin/carina patch show <session_id> <patch_id>
```

Applied patch を rollback：

```bash
./bin/carina patch rollback <session_id> <patch_id>
```

## Common Workflows

### Local Repository Work

通常の開発では default の `safe-edit` session を使います。Agent は workspace を読み、patch を提案し、allowlist された build/test command を実行できます。危険な command、network access、secret、plugin は profile に応じて拒否または承認待ちになります。

### Review And Audit

`carina items <session_id>` は thread/turn/item の normalized view を返し、turn-level patch summary も含みます。raw event chain と tamper-evidence が必要な場合は `carina audit <session_id>` または `carina audit verify <session_id>` を使います。

### BYOK Providers

Credential を保存し、provider catalog を確認：

```bash
./bin/carina auth login anthropic - < ~/.secrets/anthropic-key
./bin/carina auth login openai - < ~/.secrets/openai-key
./bin/carina auth list
./bin/carina providers list --refresh
```

必要に応じて model を指定：

```bash
CARINA_REASONER_MODEL=openai/gpt-5 ./bin/carina-daemon &
./bin/carina run --model openrouter/anthropic/claude-sonnet-4-5 "inspect this migration"
```

### Agent Modes And Slash Commands

Reusable agent と command を確認：

```bash
./bin/carina agents list
./bin/carina commands list
./bin/carina run --agent plan "inspect the release risk"
./bin/carina run "/review main"
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
| Cloud-hosted agent tasks | OpenAI Codex cloud tasks and managed agent services | Carina is local-first. Cloud identity and multi-endpoint sync should live behind Nebutra boundaries. |
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

Alpha limitations:

- Carina itself is not a VM or complete container isolation system.
- OS sandbox backends exist, but production profiles need deployment review.
- Policy correctness depends on routing commands through the Carina daemon and toolchain.
- Public release signing and supply-chain provenance are not complete yet.

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

More docs:

- [Product positioning](docs/product.md)
- [Roadmap](docs/roadmap.md)
- [Release process](docs/release.md)
- [Architecture](docs/architecture.md)
- [RPC API](docs/rpc-api.md)
- [Plugin model](docs/plugin-model.md)
- [Research status](docs/research/absorption-status.md)

## License

Apache-2.0. See [LICENSE](LICENSE).
