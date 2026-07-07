<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina" width="100%" />

# Nebutra Carina

**明示的な権限・監査・ロールバック境界の内側で
コーディングエージェントを実行するための、ローカルファーストな
Agent Runtime。**

[![status](https://img.shields.io/badge/status-alpha-0033FE)](#現在のリポジトリ状態)
[![build](https://img.shields.io/badge/build-source%20first-0B7285)](#ソースからのクイックスタート)
[![runtime](https://img.shields.io/badge/runtime-local--first-0BF1C3)](#carina-とは)
[![audit](https://img.shields.io/badge/audit-hash--chained-6D28D9)](#コア概念)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](LICENSE)

[English](README.md) · [简体中文](README.zh-CN.md) · **日本語**

</div>

状態：**alpha**。このリポジトリには中核となる実行・制御機構が実装されていますが、
パッケージング、公開リリース基盤、一部の UX はまだ初期段階です。CLI の詳細や設定形式は
今後変わる可能性があります。

---

## Carina とは

Carina はエディタ、チャット製品、ホステッドサンドボックスではありません。AI コーディングエージェントと実際のマシンの間に置かれるランタイム層です。

エージェントがファイルを読む、変更を提案する、コマンドを実行する、ネットワークへアクセスする、プラグインを呼ぶ、secret を使う、といった操作を必要とするとき、Carina はそれらをケイパビリティカーネルへ送ります。カーネルは現在のポリシーに基づき、その操作を許可、拒否、または承認待ちにします。許可された副作用はハッシュチェーン監査ログに記録され、ファイル変更は検査・ロールバック可能なトランザクショナル patch として適用されます。

目的は単純です。エージェントが実リポジトリで有用な作業をできるようにしつつ、すべての tool call が暗黙で追跡不能なマシンアクセスにならないようにすることです。

## いつ使うべきか

Carina は、コーディングエージェントに実行権限を与える必要があり、かつ prompt を送った後の制御と追跡を重視する場面向けです。

適している用途：

- ローカルリポジトリでエージェントタスクを実行しつつ、書き込み、コマンド、ネットワークアクセス、secret をポリシーの後ろに置きたい。
- 長時間またはバックグラウンドの agent session を、CLI 終了後も存続させたい。
- エージェントが何を、いつ、なぜ許可されて実行したのかを説明できるイベントストリームが必要。
- エージェントが生成したファイル変更を、場当たり的な Git cleanup だけに頼らずロールバックしたい。
- IDE、CI 連携、社内 Agent platform、workflow engine のための再利用可能な実行基盤が必要。
- サブエージェントやプラグインを、親タスクより狭い権限で動かしたい。

適していない用途：

- 必要なのはエディタ補助やチャット UI だけである。
- ホステッドで管理済みの Agent サービスが欲しい。
- 監査ログ、ポリシー境界、ロールバック、daemon-backed session が不要。
- 今日すぐ安定したパッケージ済みリリースが必要。本段階ではソースビルドが最も確実です。

## 現在のリポジトリ状態

現在のコードベースで実装済み：

- Go daemon と CLI client：session、task、scheduler、JSON-RPC、model routing、worker、event streaming。
- Rust capability kernel：permission decision、policy enforcement、transactional patch、audit log、plugin execution boundary。
- Zig native tools：scan、grep、diff、patch、command execution、pty primitives。
- `read-only`、`safe-edit`、`full-workspace`、`ci-runner`、enterprise 向け profile などの built-in permission profile。
- 検証可能な hash-chained append-only audit log。
- patch の propose、inspect、apply、rollback フロー。
- ReAct 風 agent loop：typed transcript、compaction、loop guard、completion verification、background run recovery。
- サブエージェントの capability attenuation：child session は parent permissions の subset を受け取れるが、superset は受け取れない。
- 宣言的 DAG による agent workflow orchestration。
- MCP client/server interop。同じ capability boundary を経由して実行される。
- deny-by-default egress proxy、network allowlist、daemon-side credential injection、明示的 per-host opt-in による HTTPS MITM credential injection。
- secret は brokered access で扱い、process environment の生 secret を agent command に直接渡さない。

未完了として扱うもの：

- 公開インストーラーと Homebrew tap。
- 公開 `SECURITY.md` と contributor guide。
- provenance 付きの安定した release artifact。
- 十分に磨かれた TUI/dashboard experience。
- Windows support。
- TypeScript、Python、Go SDK の機能 parity。
- remote worker / clustered deployment の production documentation。

## ソースからのクイックスタート

必要条件：

- Go 1.25 以上
- Rust 1.85 以上
- Zig 0.15.x
- macOS または Linux

ビルド：

```bash
git clone https://github.com/Nebutra/carina
cd carina
make all
```

daemon を起動：

```bash
./bin/carina-daemon &
```

モデル用 credential を daemon process environment に渡します。BYOK API key が優先され、設定されていれば Nebutra OAuth fallback も daemon 側で利用できます。

```bash
export ANTHROPIC_API_KEY=sk-...
# or
export OPENAI_API_KEY=sk-...
```

ローカルに BYOK credential を保存し、provider catalog を確認することもできます。

```bash
./bin/carina auth login anthropic - < ~/.secrets/anthropic-key
./bin/carina auth list
./bin/carina providers list --refresh
```

現在のリポジトリでタスクを実行：

```bash
./bin/carina run "fix the failing tests and show the patch"
```

session を確認：

```bash
./bin/carina sessions
./bin/carina audit <session_id>
./bin/carina audit verify <session_id>
./bin/carina patch list <session_id>
./bin/carina patch show <session_id> <patch_id>
```

適用済み patch を rollback：

```bash
./bin/carina patch rollback <session_id> <patch_id>
```

## よくあるワークフロー

### 個人リポジトリでの作業

通常のコーディングタスクでは `safe-edit` またはより厳しい profile を使います。エージェントは workspace を読み、patch を提案し、allowlist された test/build command を実行できます。危険なコマンド、ネットワークアクセス、secret は active profile に応じて拒否または承認待ちになります。

### チームまたはセキュリティレビュー

audit stream と audit export を使うと、どのファイルが読まれたか、どのコマンドが実行されたか、どの permission decision が下されたか、どの patch がファイルを変更したかを確認できます。hash chain により、記録された event history の改変検出ができます。

### バックグラウンドまたはリモート実行

CLI は client であり、daemon が runtime state を持ちます。session と background run は CLI 終了後も継続できます。worker interface は local、remote、CI、sandboxed execution pool を想定しています。

### 他製品への組み込み

Carina を別の product surface の背後に置く場合は、JSON-RPC server、SDK、MCP server mode を使えます。例：IDE extension、Web UI、CI workflow、社内 Agent platform。

## 主要概念

### Capability boundary

Carina は副作用を file read/write、command execution、network access、secret access、patch application、plugin loading、remote execution などの capability として扱います。session の permission profile が、request を許可、拒否、承認待ちのどれにするかを決めます。

### Audit log

すべての permission decision と許可された副作用は event として記録されます。event は hash chain に append されます。各 event は直前の event hash を含むため、verification pass で挿入、削除、編集された event を検出できます。

### Transactional patches

エージェントによるファイル変更は patch transaction として表現されます。patch は propose、inspect、apply、rollback できます。patch system は半適用状態を避け、各変更の provenance を残すことを目的としています。

### Daemon-backed sessions

daemon は session state を保存し、task を schedule し、event を stream し、worker を調整します。これにより、task は単一 terminal process の lifetime に依存しません。

### Sub-agent attenuation

task が sub-agent を spawn すると、child は attenuated permission set を受け取ります。child は parent より低権限にはなれますが、parent が持たない permission を得ることはできません。

### Egress と secrets

egress proxy が有効な場合、network access は deny-by-default です。host は policy で明示的に allow される必要があります。credential は daemon-side secret から egress boundary で injection できるため、command child が raw secret を environment に持つ必要はありません。HTTPS credential injection には明示的な per-host MITM opt-in が必要で、system trust を変更せず process-local trust bundle を使います。

## アーキテクチャ

Carina は技術スタックを見せるためではなく、責務ごとに分割されています。

| レイヤー | 主な責務 | 現在の実装 |
|---|---|---|
| Agent surface | ReAct loop、transcript、approval、sub-agent、workflow execution | Go daemon と model-router integration |
| Control plane | session、scheduler、JSON-RPC、worker、event streaming、egress proxy | Go |
| Capability kernel | permission decision、policy、transactional patch、audit chain、plugin boundary | Rust |
| Native toolchain | repository scan、grep、diff、patch、process execution、pty | Zig |
| Client surfaces | CLI、TUI、SDK、MCP server/client integration | Go と SDK packages |

この設計は、model-facing loop と side-effect boundary を分離します。エージェントは action を要求できますが、その action が実行されるかどうかを runtime が判断し、結果を記録します。

詳細：

- [Architecture](docs/architecture.md)
- [Security model](docs/security-model.md)
- [RPC API](docs/rpc-api.md)
- [Plugin model](docs/plugin-model.md)
- [Enterprise notes](docs/enterprise.md)

## セキュリティモデル

デフォルト姿勢：

1. デフォルトで least privilege。
2. 明示的に許可されない限り workspace 外へアクセスしない。
3. secret はデフォルトで読めない。
4. network access はデフォルトで制限される。
5. destructive command はデフォルトで拒否される。
6. ファイル変更は patch transaction を通る。
7. plugin には implicit permission がない。

Built-in profile はよくある policy bundle を定義します：

| Profile | 想定用途 |
|---|---|
| `read-only` | 書き込み、command、network、secret なしで workspace を検査する。 |
| `safe-edit` | workspace file を読み、patch 経由で書き込み、allowlist された test/build command を実行する。 |
| `full-workspace` | より広い workspace access。audit と approval awareness は維持する。 |
| `ci-runner` | test/build automation。任意 shell と secret access を制限する。 |
| `enterprise-restricted` | organization policy overlay と central approval rules。 |

セキュリティ境界は、制限も明記されている場合にだけ有用です。alpha の重要な制限：

- Carina それ自体は VM でも完全な container isolation system でもありません。
- 選択された backend には OS-level sandboxing が実装されていますが、production deployment profile は別途レビューが必要です。
- policy correctness は、command が Carina toolchain と daemon-controlled environment を通って実行されることに依存します。
- public packaging と supply-chain provenance は未完了です。

## 他ツールとの関係

これは勝敗表ではありません。この領域のツールは異なる目的に最適化されており、能力もすばやく変化します。正確な機能一覧は各プロジェクト自身のドキュメントを参照してください。

| 主に必要なもの | よく使われるツール | Carina の位置づけ |
|---|---|---|
| エディタ内のコード支援と対話 UX | Cursor、Windsurf、Cline、IDE extensions | Carina はより低レイヤーです。editor surface の背後には置けますが、editor replacement ではありません。 |
| CLI での pair-programming | Aider、Claude Code style CLI、Codex-style CLI | Carina は runtime boundary に重点を置きます：daemon sessions、policy、audit、rollback、workers。 |
| disposable hosted execution environment | E2B や他の sandbox providers | Carina は local-first runtime infrastructure です。sandboxing は使えますが、中心は per-action control と provenance です。 |
| 社内 Agent の再利用可能な execution substrate | custom agent stacks、CI systems、internal platforms | Carina は他の UI や workflow の背後に埋め込まれることを意図しています。 |

実用上の違いは、Carina が front-end polish よりも、agent execution を inspectable、policy-controlled、reversible にすることを重視する点です。

## ロードマップ

近い優先事項：

- installation path の公開：signed releases、hosted installer、Homebrew tap、supply-chain provenance。
- `SECURITY.md`、contributor documentation、release-process docs の追加。
- TUI と live audit inspection の改善。
- remote worker operation の hardening と production deployment pattern の文書化。
- TypeScript、Python、Go SDK の parity 改善。
- policy profile、sandbox backend、plugin signing の継続拡張。
- core Unix path が安定した後に Windows support を追加。

## 開発

すべてをビルド：

```bash
make all
```

Go tests：

```bash
make go-test
```

Rust tests：

```bash
make rust-test
```

Zig tools：

```bash
make zig
```

有用なドキュメント：

- [PRD](docs/PRD.md)
- [Agent model](docs/agent.md)
- [Architecture](docs/architecture.md)
- [Security model](docs/security-model.md)
- [Research status](docs/research/absorption-status.md)

## ライセンス

Apache-2.0。詳細は [LICENSE](LICENSE) を参照してください。
