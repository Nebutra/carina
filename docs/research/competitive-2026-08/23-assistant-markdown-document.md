# Carina TUI — 长文答案排版审计（v0.8.34）

> Date: 2026-08-20  
> Carina: **v0.8.34**。Chrome + A010–A013 **已发**。本文不重开空状态/composer。  
> Evidence: 操作者截图「分析这个 repo」长文（GFM 表 + 中文小标题 + 目录子弹 + 产品边界段）。  
> Law: `styles.md`、`DESIGN.md`、`theme.rs`。`18` 仍是对话文档 SSOT。  
> **Verdict: 长文答案 FAIL。** 框设计过了。模型一写出 README，页就塌成一张无等级的说明书。功能能渲染 markdown ≠ 好看。

Steal jobs, not skins. 禁止新 token set、GrokNight、OMP 四边盒上主壳、Buddy、terracotta 当身份。

---

## Phase 0 — 标尺（markdown 页，不是 chrome）

17/18 已提取五家颜色/盒子/状态行。这里只补 **长回答** 这一面。参考项目几乎都不在对话栏里画电子表格。

| Job | Grok | OMP | Codex | Claude | Jcode |
|-----|------|-----|-------|--------|-------|
| 答案 | 不装箱，正文是页 | 成功 recede；正文消息向 | history **cells**，composer 无框 | 散文优先；工具另框 | 角色色；负空间给 leftover |
| 标题 | 少；不跟 accent 抢 live 工具 | 语义 token | 默认 fg + 少量 cyan tips | 克制；品牌色不进正文墙 | 标题不是第三套皮肤 |
| 表 | 对话里少见；结构用列表 | 不把 chat 当 grid | 不对齐的表宁可变定义列表 | 罕见 | — |
| 列表标记 | 不是每行一根饱和色 | dim 完成态 | 2-col gutter `›`/`•` | `·` 思考在答案外 | intent 行，不是子弹墙 |
| 呼吸 | 段间空白是设计 | 完成框变 dim | 单元格之间有树缝 | 极低密度 | notification ≠ status |

**好看的长回答（角色，不是 hex）：**

1. **Lede 是一句人话。** 不是产品简介，不是五层架构表。  
2. **标题是声音变小一档，不是换一种霓虹。** 对话栏里最多一级用 accent。  
3. **列表标记是标点，不是状态灯。** muted/dim。  
4. **表不是盒子。** 对话栏用对齐列或「标签：值」。`┌┬┐` 只给 `/changes` 那种 workbench。  
5. **一段一个意思。** 段间空白 > 行间。标题上方多一拍。  
6. **NO_COLOR 仍能扫：** 空白、粗体、缩进，不靠颜色。

Chrome 标尺（开口栏、空状态、live `┃`）以 `18` 为准，这里不改分。

---

## Phase 1 — 这张截图长什么样

操作者会话：`Search ^module` → `Read Cargo.toml` / `go.mod` → 一篇《Carina 说明书》。

### 看见的东西（不是「功能有 markdown」）

- 第一段：产品定位 + **「不是编辑器，也不是托管云 Agent」**（稻草人，BRIEF 回声）。  
- `分层架构` 然后一张 **三线框表**（层 / 语言 / 职责），单元格 CJK 折行（`TS / Python / Go / Rust` 撕开）。  
- `仓库怎么长` + 一长串 `•` 目录。  
- `产品边界` / `成熟度与缺口` / `开发注意` 同等字重段落。  
- 左 `•` 安静；表是 **尖角** `┌┬┐`；composer 是 **开口栏**。同一屏三种几何。  
- 工具行已塌缩在答案上面。A012/A011 的「收据 recede」成立。失败在 **答案自己**。

代码路径：

| 表面 | 路径 |
|------|------|
| 助手 markdown | `app/render.rs` `SemanticCellKind::Assistant` → `render_markdown_prefixed` |
| 标题色 | `theme.rs` `markdown_heading[0..=1]` = **accent + BOLD**（H1 还 UNDERLINE） |
| 列表标记 | `markdown.rs` `Tag::Item` → `self.theme.accent` |
| accent 从哪来 | FinalAnswer 的 `label_style` = `transcript_assistant()` = **`accent` + BOLD** |
| 表 | `markdown.rs` `render_table` → `glyphs.table()` 尖角 + `muted` 线 |
| 段间距 | `finish_paragraph`：有内容就再空 **一行**，标题没有额外拍 |

`styles.md` 原文：*Assistant body stays `Reset`; the quiet bullet is not ion-cyan.*  
实现：`transcript_assistant()` 是 ion-cyan，并灌进 `MarkdownTheme.accent`，于是 **每一个 `-` 列表项的子弹都是交互色**。法律与代码打架。这不是「主题还没做」，是 **已经写过的纪律没执行**。

### 属于哪种丑

| # | Pattern | 这张图 |
|---|---------|--------|
| 1 | 墙式文本 | **FAIL** — README 墙 |
| 4 | 边框混乱 | **FAIL** — 开口栏圆角纪律 vs 尖角表 |
| 6 | 无呼吸 | **FAIL** — 标题/段/表同一拍 |
| 12 | 无层级 | **FAIL** — 小标题、表、子弹、免责声明同等吵；子弹还抢 accent |
| 13 | 对齐破碎 | **FAIL** — 三列表 + CJK 折行 |

2 硬编码色、7 闪烁、10 无签名输入、9 错误刺眼：这张图 **不是**。Composer 仍过关。

参考怎么处理同类：Grok/Claude **不把架构矩阵画进 chat**。Codex 用 cell + gutter，不用 `┌┼┐`。OMP 完成态 dim，不给每颗子弹一根饱和色。

---

## Phase 2 — Ugly checklist（本面）

长文答案：**5 FAIL**（1, 4, 6, 12, 13）。Chrome 那些 PASS 救不了这一页。

---

## Phase 3 — 分数

参考 8+。日用长回答加权最高。

| 表面 | 参考 | Carina 0.8.34 这张图 | 分 | Ugly | 优先级 |
|------|------|---------------------|:--:|------|--------|
| **长文 markdown 答案** | Claude 散文；Codex cells；表不当 chat 控件 | 尖角表 + 氰子弹 + 说明书墙 | **3** | 1,4,6,12,13 | **P0** |
| 短对话（你好） | 18 的 pill/unboxed | A010–A013 已发 | **7** | — | 保持 |
| 工具行 | Grok live rail | 截图里 Search/Read 已 recede | **7** | — | 保持 |
| Composer | Claude 开口 | 开口栏仍在 | **7** | A107 chips | P1（本图未见 chip） |
| 空状态 / 主题 / slash | 18 | 本图未出现 | — | 18 P1 | 不重开 |

**加权：长回答 3，短聊 7 → 打开仓库问一句分析，产品仍 FAIL。**  
一句话：框是设计过的。**说明书不是。**

---

## Phase 4 — 交付

### 4.1 DESIGN.md 补丁（不要第二套皮肤）

在 `DESIGN.md` / `styles.md` 增加 **「对话栏是文档，不是 CommonMark 预览器」**：

1. `transcript_assistant()` **不得**再是 accent。助手身份与正文是 `Reset`。accent 只给：焦点 composer、live 工具、链接、**至多一级**标题。  
2. 列表标记 / quote 前缀 = `muted` 或 `gray_dim`，永不 `transcript_assistant()`。  
3. 对话栏 GFM 表：**无箱**。表头 muted+bold，下一行 `─` hairline，单元格用空格对齐。窄宽走已有 `render_stacked_table`（`标签: 值`）。`┌┬┐` 仅 `/changes`、plan、overlay。  
4. 标题：H1/H2 最多一个用 accent+bold（不要 underline，终端下划线像链接）。H3+ `Reset+bold`，再下 muted。  
5. 标题前多一空行（文档拍）。段间保持一行。  
6. NO_COLOR：靠空行、粗体、缩进，不靠色。

Token 表、开口栏、3-hue 预算、A114 rose **不动**。

### 4.2 P0（立刻让长回答不丑）

#### P0-A015 — 子弹不是状态灯

- **问题：** `markdown.rs` Item 前缀用 `theme.accent`；生产里 accent = 氰 bold。  
- **参考：** Claude `·`；Codex gutter dim；`styles.md`「quiet bullet is not ion-cyan」。  
- **改：** Item/task marker → `muted`。`transcript_assistant()` 改回 `Reset`（或 gray_bright，**不要** accent）。MarkdownTheme.accent 只给链接。  
- **文件：** `theme.rs`、`markdown.rs`、`app/render.rs` 助手 MarkdownTheme 组装。  
- **验收：** 「分析 repo」夹具：列表 `•` 的 fg ≠ `theme.accent`。NO_COLOR 仍有 `•`。金帧 `visual_density_*` 里助手列表不引入第四饱和色。

#### P0-A016 — 对话里的表不装箱

- **问题：** `render_table` 尖角箱。与开口栏、unboxed 答案打架；CJK 三列必折。  
- **参考：** 对话里用定义列表/对齐列；grid 留给 workbench。  
- **改：** 助手（及一切 transcript markdown）走 **unboxed**：header / rule / rows。宽度不够继续 stacked。`glyphs.table()` 保留给非对话表面。  
- **文件：** `markdown.rs` `render_table`；可选 `MarkdownTheme { boxed_tables: bool }` 默认 false。  
- **验收：** 三列中文架构表在 80 列：无 `┌┬┐`；列对齐或退化为 `层: …`；每行 ≤ 内容宽。`/changes` 尖角不变。

#### P0-A017 — 标题是层级，不是第二条 neon

- **问题：** H1 underline+accent、H2 accent、列表也 accent → 满页交互色。  
- **改：** 对话栏 H1/H2：`Reset+BOLD`（或仅 H1 accent 且无 underline）。H3+ muted/bold。`finish_paragraph` 在 Heading start 前保证 **两个** 空行（或 heading_gap=2）。  
- **文件：** `theme.rs` `markdown_heading` 或助手路径覆盖；`markdown.rs`。  
- **验收：** 截图同类夹具：小标题比正文粗，但不比 live 工具更氰。标题上有明显空白。

### 4.3 P1 / P2

| ID | 项 | 何时 |
|----|----|------|
| A107/A106/A108 | chip / slash / `/context` 黑话 | 18 仍开；本图不是主犯 |
| A103 | `/changes` 量度 | 与 A016 分开：workbench 可以装箱 |
| 提示 | 「分析这个 repo」不要倒 FEATURE_MAP + 稻草人边界 | Intent-Meta；重启 0.8.34 daemon；BRIEF 不准回活 |
| A102 shine | 空状态扫光 | 仍可选；**不要**在长回答上 shimmer |
| A114 | brand hex | 对比度阻塞则保持 elevated |

模型侧：审美改渲染 **不能** 让 80 行说明书变好看。Lede 应是「这是 Go/Rust/Zig 的本地 runtime，入口 `go/daemon` + `crates/carina-kernel`」，而不是许可证+五层表+缺口清单。那是 S8/意图，另刀。

### 4.4 反模式 — 绝对不要

1. 不要为了「表好看」给答案加圆角卡。对话不装箱。  
2. 不要抄 Claude dashed box / OMP boxRound 进 transcript。  
3. 不要用 brand-rose 画标题。  
4. 不要 body shimmer。  
5. 不要把 `transcript_assistant()` 设回 accent「为了品牌」。  
6. 不要用功能金帧（gallery 里本来就有表）当审美 pass。夹具必须是 **一篇中文长回答**。  
7. 不要重开 A001–A013。  
8. 不要为排版开 MiniLM / pager / 新皮肤。

---

## 验收（审美）

80×24 或 120×32，夹具 = 短问「分析这个 repo」的 **渲染**（可 fixture 固定 markdown，不靠活模型）：

1. 三秒能指出 lede（第一段），不是先看见一张表。  
2. 表（若有）不是独立窗口；或已是 `键: 值`。  
3. 子弹不比正文更饱和。  
4. 标题只靠字重/空白扫视，NO_COLOR 仍成立。  
5. 开口栏、用户 pill、工具 recede **保持**。

功能正确、说明书仍墙 = **FAIL**。
