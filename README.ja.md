<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina — エージェントが走るための、安全なキール（竜骨）" width="100%" />

# Nebutra Carina

**Go・Rust・Zig で書かれた、セキュアなエージェントランタイム（agent runtime）。**

*エージェントが走るための、安全なキール（竜骨） — あらゆる副作用（side effect）をゲートし、監査し、元に戻せる。*

[![build](https://img.shields.io/badge/build-passing-0033FE)](#)
[![release](https://img.shields.io/badge/release-v0.1.0--alpha-0BF1C3)](#)
[![stack](https://img.shields.io/badge/Go%20%C2%B7%20Rust%20%C2%B7%20Zig-polyglot-8b5cf6)](#)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](#)
[![signed releases](https://img.shields.io/badge/releases-signed-0033FE)](#)
[![no telemetry](https://img.shields.io/badge/telemetry-none-0BF1C3)](#)
[![powered by Nebutra](https://img.shields.io/badge/powered%20by-Nebutra-0033FE)](#)

`curl -fsSL https://get.nebutra.com/carina | sh`

[English](README.md) · [简体中文](README.zh-CN.md) · **日本語**

</div>

---

## Carina とは

**Carina は、AI コーディングエージェントを _実行する_ ためのセキュアな基盤（substrate）です** — OS でもフレームワークでもなく、ランタイムです。エージェントにタスクを指し示すと、Carina はそのエージェントの ReAct ループを実行します。その間、**ケイパビリティカーネル（capability kernel）が境界であらゆる副作用をゲート**し、そのひとつひとつを**改ざん検知可能な、ハッシュチェーンでつながれた監査ログ（audit log）**に記録し、ファイル変更を**ロールバック（rollback）可能なトランザクショナルなパッチ（patch）**として適用します。

他の誰もが売っているのは、_行動する_ エージェントです。Carina が動かすのは、行動し、**しかも信頼でき、取り消せる**エージェントです。

Carina は、実マシンへの本物のアクセス — ファイルの書き込み、コマンドの実行、ツールの呼び出し — をエージェントに与えたい、しかし鍵を丸ごと渡して祈るような真似はしたくない、そんな人のためのものです。*「エージェントは正確に何をしたのか、そしてそれを取り消せるのか？」* に暗号的な「イエス」で答えたいと思ったことがあるなら、Carina はあなたが CLI とサンドボックスと監査シムを手作業で継ぎ接ぎして作ろうとしてきた、まさにそのランタイムです。

ひとつのバイナリが、その寄せ集めを置き換えます。必要なのは `carina` だけ。

```
Go makes it run.  Rust makes it safe.  Zig makes it sharp.  LLM makes it useful.
```

---

## なぜ三つの言語（と、ひとつの LLM）なのか

これがすべての核心です。それぞれの言語には**ただひとつの仕事**があり、その言語だけが図抜けて得意とすることに合わせて選ばれています。履歴書を飾るための設計は一切なし — すべての依存関係は、その価値を勝ち取ったものだけです。

| レイヤー | 言語 | 担う唯一の仕事 | 具体的な仕組み | 測定可能な効果 |
|---|---|---|---|---|
| **コントロールプレーン** | **Go** | *動かす* | daemon、スケジューラ、セッションストア、JSON-RPC サーフェス、モデルルーター。セッションごとに goroutine で並行処理し、長命なプロセスをひとつ動かす。 | 多数のエージェントセッションを、リモートからスケジュール可能なかたちで、監視下の単一 daemon 上で実行。CLI は**クライアント**にすぎない — 落としてもセッションは生き続ける。 |
| **ケイパビリティカーネル** | **Rust** | *安全にする* | ポリシーエンジン + ロールバック可能なパッチエンジン + ハッシュチェーンされた追記専用（append-only）監査ログ + プラグインランタイム。あらゆる副作用は型付けされたケイパビリティ境界を越える。 | **副作用の 100% をゲート＋監査。** すべてのパッチは**アトミックかつ可逆**。ログは**改ざん検知可能** — エントリをひとつでも書き換えれば、チェーンが壊れる。 |
| **ネイティブツールチェーン** | **Zig** | *切れ味を出す* | `scan`、`grep`、`diff`、`patch`、`pty` を極小のネイティブバイナリとして提供。GC なし、ランタイムなし、構造化された JSON 出力。 | 数 ms で起動する、高速でアロケーションの少ないプリミティブ群。エージェントが何千回も叩くホットパスは、言語ランタイムのコストを払わない。 |
| **エージェントサーフェス** | **LLM** | *役立たせる* | 型付きトランスクリプトを持つ ReAct ループ、コンパクション＋ループガード、**ケイパビリティ減衰（capability attenuation、子 ⊆ 親）** を伴うサブエージェント、Codex 流のゴール／成功基準＋承認モード。 | エージェントは *考えて行動* できる — そして生成されたサブエージェントは、**親の権限を決して超えられない**。権限昇格なき委譲。 |

### 各仕組みを、平易な言葉で

- **ケイパビリティゲーティング。** エージェントは OS に直接触れることはありません。ファイルの読み込み、パッチの書き込み、プロセスの生成、ネットワークへのアクセス — そのいずれもが、カーネルが付与しなければならない**ケイパビリティ**です。付与されていない副作用は ⇒ 決して起きません。これが、「モデルが `rm -rf` しないと約束した」と「モデルにそもそもそのケイパビリティが渡されていない」の違いです。
- **改ざん検知可能な監査ログ。** 付与されたすべての副作用は、各エントリが直前のエントリのハッシュを埋め込むログに追記されます（`hash(N) = H(entry_N ‖ hash(N-1))`）。チェーンは端から端まで検証可能です。事後に何も挿入・削除・編集されていないことを証明できます。「信じてくれ」ではなく、「計算を確かめてくれ」です。
- **トランザクショナルでロールバック可能なパッチ。** ファイルの変更は、うまくいくことを願いながらディスクへ流し込む、といったやり方はしません。パッチエンジンは変更をトランザクションとしてステージし、プレビューさせ、アトミックに適用し、**きれいに取り消す**のに十分な情報を保持します。まずいエージェントの編集も、`carina rollback` ひとつで元通りです。
- **サブエージェント減衰。** エージェントが委譲するとき、子は親のケイパビリティの**部分集合**を継承します — 決して上位集合ではありません。読み取りアクセスしか必要としなかったリサーチ用サブエージェントが、突然書き込めるようになることはありません。最小権限を、構造的に、ツリーの末端まで徹底します。
- **ネイティブ Zig ツール。** エージェントが絶えず頼るプリミティブ（リポジトリの grep、変更の diff、pty の駆動）はネイティブかつ構造化されています — 人間向けに整形された標準出力からスクレイピングするのではなく、機械から呼ばれ、JSON を吐くように設計されています。

> **ミッション。** これからの十年、ソフトウェアはエージェントをループに組み込みながら書かれていきます。Carina は、「本物のアクセスを持つエージェント」と「制御下にあること」はトレードオフではない — 安全性は、正しく形にすれば、ただの優れたインフラである、という賭けです。

---

## アーキテクチャ

```
┌───────────────────────────────────────────────────────────┐
│                      Agent Surface  (LLM)                  │
│   CLI · TUI · SDK · ReAct loop · sub-agents · approval     │
└───────────────────────────────┬───────────────────────────┘
                                │ JSON-RPC
┌───────────────────────────────▼───────────────────────────┐
│                   Control Plane  (Go)                      │
│   daemon · scheduler · session store · model router        │
│   "make it run"                                            │
└───────────────────────────────┬───────────────────────────┘
                                │ Capability API  (every effect crosses here)
┌───────────────────────────────▼───────────────────────────┐
│                 Capability Kernel  (Rust)                  │
│   policy engine · transactional patch · hash-chained audit │
│   plugin runtime · "make it safe"                          │
└───────────────────────────────┬───────────────────────────┘
                                │ Native tool calls
┌───────────────────────────────▼───────────────────────────┐
│                 Native Toolchain  (Zig)                    │
│   scan · grep · diff · patch · pty · "make it sharp"       │
└───────────────────────────────────────────────────────────┘
```

**コア不変条件（invariants）**

1. エージェントはシステムリソースに直接触れない。
2. あらゆる副作用はケイパビリティカーネルを通過する。
3. 付与されたすべての副作用はハッシュチェーンされた監査ログに追記される。
4. すべてのパッチはプレビュー可能・検証可能・ロールバック可能である。
5. すべてのツールとプラグインは、自らのケイパビリティを明示的に宣言する。
6. 既定ではローカルファースト。リモート実行は拡張であって、要件ではない。
7. CLI はクライアントであり、daemon がランタイムである。

詳細は [`docs/architecture.md`](docs/architecture.md) と [`docs/security-model.md`](docs/security-model.md) に。この README が開示するのはモデルであって、内部実装ではありません。

---

## インストール

> **必要環境：** macOS または Linux（x86-64 / arm64）。Windows は[ロードマップ](#roadmap)にあります。

**ワンコマンド（推奨）：**

```bash
curl -fsSL https://get.nebutra.com/carina | sh
```

**Homebrew：**

```bash
brew install nebutra/tap/carina
```

**ソースから** — Go ≥ 1.25、Rust ≥ 1.85、Zig 0.15.x が必要です：

```bash
git clone https://github.com/Nebutra/carina && cd carina
make all        # builds Go control plane, Rust kernel crates, Zig tools
```

確認：

```bash
carina --version   # carina 0.1.0-alpha
```

---

## クイックスタート — はじめてのエージェント実行

```bash
# 1. Start the runtime (control-plane daemon; sessions outlive your shell)
carina daemon &
#   ⇒ carina daemon listening on ~/.carina/daemon.sock

# 2. Point an API key at it (never hardcoded — env only)
export ANTHROPIC_API_KEY=sk-...

# 3. Run your first agent against a task
carina run "add a --json flag to the status command and update its test"
#   ⇒ session f3a9c1  created
#   ⇒ [react] plan → grep → edit → test
#   ⇒ [gate]  write  cmd/status.go              approved (capability: fs.write)
#   ⇒ [patch] staged 1 file · atomic · rollbackable  →  cr patch show f3a9c1
#   ⇒ [audit] entry 0007 chained  sha256:9b1e…  (prev 3c7a…)
#   ⇒ done · success criteria met · 1 patch applied

# 4. Inspect exactly what happened — and verify the chain
carina audit f3a9c1 --verify
#   ⇒ 7 entries · chain intact · no tampering detected ✓

# 5. Don't like it? Take it back, atomically.
carina rollback f3a9c1
#   ⇒ reverted 1 patch · workspace clean
```

すべての動詞に対して、短いエイリアス `cr` が使えます（`cr run`、`cr audit`）。スクリプトやドキュメントでは、フル表記の `carina` を推奨します。

---

## 主な機能

- 🔒 **ケイパビリティゲーティング** — あらゆる副作用は明示的な付与を必要とする。付与されなければ ⇒ 決して起きない。
- 🧾 **ハッシュチェーン監査ログ** — 追記専用、改ざん検知可能、端から端まで検証可能。
- ↩️ **トランザクショナルなロールバック** — パッチはアトミックで、きれいに可逆。
- 🧬 **サブエージェント減衰** — 子のケイパビリティは親 ⊆、構造的に強制される。
- 🔁 **ReAct ループ + ループガード** — 型付きトランスクリプト、コンパクション、暴走検知。
- ✋ **承認モード** — Codex 流のゴール／成功基準。選んだ副作用クラスに人間の承認を要求できる。
- ⚡ **ネイティブ Zig ツールチェーン** — scan / grep / diff / patch / pty、構造化 JSON、GC なし。
- 🛰️ **daemon ファースト** — セッションはリモートからスケジュール可能で、CLI より長生きする。
- 🔌 **サンドボックス化されたプラグインランタイム** — サードパーティ製ツールも、同じケイパビリティ契約のもとで動く。
- 🙈 **テレメトリなし** — 何もホームへ電話しない。ログはあなたのもの。

---

## セキュリティと監査可能性

エージェントランタイムにとって、**セキュリティこそがプロダクトです。** Carina の保証は、それぞれが検証可能な、三つの性質の連鎖です：

1. **ゲートされている（Gated）** — ケイパビリティカーネルは、副作用に至る *唯一* の経路です。モデルが `exec` を直接呼ぶような裏口はありません。モデルはカーネルに要求し、カーネルがポリシーに照らして判断します。承認モードを使えば、任意の副作用クラス（書き込み、ネットワーク、プロセス生成）に人間を割り込ませられます。
2. **監査されている（Audited）** — 付与されたすべての副作用は追記専用ログのエントリとなり、各エントリは直前のものへハッシュチェーンされます。`carina audit <session> --verify` はチェーンを再計算し、改ざんを報告します。連結が暗号的であるため、履歴を書き換えようとする攻撃者は、後続のハッシュをすべて破らねばならず — そしてそれはできません。
3. **可逆である（Reversible）** — パッチエンジンは変更をトランザクションとして扱います。事前にプレビューし、事後にロールバック、しかもアトミックに。「エージェントがリポジトリを壊した」は、もはや大惨事ではなく、`rollback` のひとことになります。

サブエージェントは**減衰した**ケイパビリティ集合（子 ⊆ 親）を継承するため、委譲によって権限が昇格することは決してありません。プラグインは、ファーストパーティ製ツールと同じサンドボックス内で、同じ明示的ケイパビリティ契約のもとに動作します。

脅威モデル、ログフォーマット、検証プロトコルは：[`docs/security-model.md`](docs/security-model.md)。

**脆弱性の報告：** **security@nebutra.com** までメールしてください（[`SECURITY.md`](SECURITY.md) 参照）。セキュリティ報告について公開 issue を立てるのはお控えください。

---

## 先行研究と系譜

Carina は、新規性それ自体のためではなく、実績のある技術の上に立っています：**ReAct** ループ（推論 + 行動）、**Codex 流**のゴール／成功基準と承認モード、減衰（子 ⊆ 親）を伴う**ケイパビリティベースのセキュリティ**、そして改ざん検知可能な**ハッシュチェーンログ**。Carina の貢献は、これらをひとつのランタイムと、ひとつの強制境界の下にまとめ上げたことにあります。

---

## 他ツールとの比較

| | Carina | Aider / Cline | Cursor / Windsurf | E2B / sandboxes |
|---|---|---|---|---|
| 監視下の daemon 上でエージェントを実行 | ✅ | ❌（CLI 依存） | ❌（エディタ依存） | 一部対応 |
| あらゆる副作用をケイパビリティゲート | ✅ | ❌ | ❌ | ✅（隔離であって、副作用単位ではない） |
| 改ざん検知可能な監査ログ | ✅ | ❌ | ❌ | ❌ |
| 編集のトランザクショナルなロールバック | ✅ | git のみ | git のみ | ❌ |
| サブエージェントのケイパビリティ減衰 | ✅ | ❌ | ❌ | ❌ |

Carina はエディタ**ではなく**、ホスティング型プロダクト**でもありません**。他ツールがその上で動きうる、基盤（substrate）です。

---

## ロードマップ

包み隠さぬ現状です。Carina は**アルファ版** — 強制の中核は本物ですが、その周りのエコシステムはまだ初期段階です。

**リリース済み**
- [x] Go コントロールプレーン：daemon、スケジューラ、セッションストア、JSON-RPC
- [x] Rust ケイパビリティカーネル：ポリシーエンジン + ケイパビリティゲーティング
- [x] 改ざん検知可能なハッシュチェーン監査ログ + `--verify`
- [x] トランザクショナルでロールバック可能なパッチエンジン
- [x] Zig ネイティブツールチェーン：scan / grep / diff / patch / pty
- [x] 型付きトランスクリプト、コンパクション、ループガードを備えた ReAct エージェントループ
- [x] サブエージェントのケイパビリティ減衰（子 ⊆ 親）
- [x] `carina` CLI（エイリアス `cr`）
- [x] ワークフローオーケストレーションエンジン — 宣言的なサブエージェント DAG、並列 + 再開可能
- [x] 永続的で再開可能なバックグラウンド実行 — ターンごとのチェックポイント + 再起動後の再開、実行レジストリ（`task.list`/`task.result`）、並行数上限、パニック隔離

**計画中**
- [ ] 初期ルーターセットを超える、追加のモデルプロバイダー
- [ ] サンドボックスプロファイル（プロジェクトごとのケイパビリティプリセット＆テンプレート）
- [ ] プラグインマーケットプレイス + 署名付きプラグイン配布
- [ ] ライブセッション + 監査ストリーム検査のための TUI ダッシュボード
- [ ] ワーカーノードをまたぐリモート／クラスタ実行
- [ ] Windows サポート
- [ ] TypeScript / Python / Go 間での SDK 機能パリティ
- [ ] すべてのリリース成果物への SLSA ビルドプロヴェナンス
- [ ] `get.nebutra.com` にホストするワンライナーインストーラー + Homebrew tap + 公開 `SECURITY.md`（security@nebutra.com）
- [ ] バックグラウンドエージェント UX：attach/tail（リプレイカーソル）、完了 webhook、git worktree 分離、リモート/サンドボックス実行

追跡は [GitHub Issues](https://github.com/Nebutra/carina/issues) で — 埋まっていない部分は、不意打ちではなく、コントリビューションの機会です。

---

## コントリビューション

OS ごとの開発ビルドガイドと、アーキテクチャ案内は [`CONTRIBUTING.md`](CONTRIBUTING.md) にあります。ビルドは `make go` / `make rust` / `make zig` / `make all`。ケイパビリティを追加する PR は、その監査ログのカバレッジも必ず追加すること — それがこの家のルールです。

## コミュニティ

- ディスカッション：[GitHub Discussions](https://github.com/Nebutra/carina/discussions)
- チャット：[Discord](https://discord.gg/nebutra)
- アップデート：[@nebutra](https://x.com/nebutra)

## ライセンス

Apache-2.0 — [`LICENSE`](LICENSE) を参照してください。

<div align="center">

**Nebutra Carina** — エージェントが走るための、安全なキール（竜骨）。
Powered by [Nebutra](https://nebutra.com) · sibling to [Sailor](https://github.com/Nebutra/create-sailor).

</div>