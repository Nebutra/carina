# Nebutra Carina — Brand Brief

**Status:** Approved identity integrated into the repository. Canonical assets and machine-readable tokens live under `docs/brand/`.
**Production pipeline:** `generate-brand-vi`; repository consumption is governed by `docs/brand/AGENTS.md` and `asset-manifest.json`.
**Historical visual reference:** a hydrogen-alpha-dominant rendering of the Carina Nebula informed the early temperament and palette research. It is not a repository dependency or an identity master.
**Canonical naming:** Product lockup is **Nebutra Carina**; short form **Carina**; descriptor **the Carina agent runtime** (lowercase descriptor, never a second proper name). "Agent OS" is internal-PRD ambition language and does not appear in brand surfaces. Absorbed upstreams (Hermes, Headroom, OpenClaw) are external projects, not Nebutra siblings; no constellation codename system exists yet, and none should be implied.

---

## 1. Brand philosophy

### The one-sentence brand idea (buyer test)

> **Carina is the local runtime that lets coding agents work at full power on your machine, because every action passes policy, lands on a hash-chained audit record, and can be rolled back.**

A buyer who hears only this sentence knows what Carina is (a runtime, local, for coding agents), why it exists (full power without losing control), and what makes the claim credible (policy per action, tamper-evident audit, transactional rollback). The sentence is abstracted from the business scenario, not from the nebula — the nebula is the name-source and cultural reference, never the pitch.

### The temperament

Look at the reference image before reading anything about it. It is mostly darkness. One color family — rose deepening to crimson, thinning to mauve — carries the whole frame. The light does not perform: no flares, no rays, no center of attention. The stars are few, small, and matter more for it. Enormous scale, rendered still.

That is the brand, fully stated. Not the astronomy — the temperament: **quiet, exact, warm-dark.** Depth earned through restraint rather than spectacle. The sensibility sits closer to color-field painting and long-exposure photography than to science-fiction illustration: color as atmosphere rather than signal, stillness as the visible evidence of something vast taking its time.

There is one honest resonance between the image and the product, and it is a posture, not a plot. Astronomy is the discipline of regarding overwhelming power calmly — through instruments, patiently, keeping records. Carina builds that kind of instrument for a different sky. A good instrument is quiet, exact, and never inserts itself between the observer and the thing observed. That is the standard the brand holds every surface to; the product's own vocabulary (policy, audit, rollback) needs no cosmic costume to be dignified.

**The rule that governs all use of the nebula: it is a palette and a temperament, not an allegory.** No feature of the product is "because of" any feature of the sky. No keyhole-equals-policy, no dust-equals-audit, no star-equals-agent. If a design decision needs an astronomy footnote to justify itself, the decision is wrong. The name is an inheritance, worn lightly — the way a ship is named for a constellation without pretending to sail there.

### How literally may the nebula appear?

- **Marks (logo, icon, favicon):** abstract geometry only. Aperture, carved edge, held light — flat, hard-edged, two to three palette tones. Never a nebula photo, never a gradient orb.
- **Scene assets (hero, social, backgrounds):** the nebula appears as *material* — matte, smoky, organic emission texture in the brief's palette, evoking the physics (emission, dust, aperture) without tracing or counterfeiting Hubble/Webb frames.
- **Copy:** the metaphor stays in brand/docs-marketing surfaces. Product docs and CLI output speak in governance vocabulary (policy, audit, rollback, capability, boundary, attenuation, hash-chained, local authority), not astronomy.

---

## 2. Palette

### Decision: separate identity color from product signals

The accepted symbol uses `brand-rose`; product interaction uses `ion-cyan`. Treating one accent as both mark identity and UI state created drift, so the current system keeps those roles separate. The original electric-blue/teal/mint/violet README palette is retired.

### Token table (dark-first, terminal-native)

| Token | Hex | Role | ANSI-256 fallback |
|---|---|---|---|
| **Void** | `#0d1214` | Base background | 233 (`#121212`) |
| **Surface** | `#141b1d` | Primary panel surface | 234 (`#1c1c1c`) |
| **Border** | `#344144` | Boundaries and dividers | 237 (`#3a3a3a`) |
| **Starlight** | `#f3f0e8` | Primary text on dark | 255 (`#eeeeee`) |
| **Dust** | `#b0b7b3` | Secondary and muted text | 249 (`#b2b2b2`) |
| **Brand Rose** | `#8e4053` | Canonical symbol and brand mark only | 95 (`#875f5f`) |
| **Ion Cyan** | `#8edbd2` | Primary interaction and focus | 116 (`#87d7d7`) |
| **Oxygen Blue** | `#78bff2` | Information, links, retrieval | 111 (`#87afff`) |
| **Dust Violet** | `#c6a6ea` | Agents and delegated execution | 182 (`#d7afd7`) |
| **Copper Amber** | `#e8a85f` | Warnings and tool activity | 179 (`#d7af5f`) |
| **Spectral Green** | `#68d2a3` | Healthy and completed states | 79 (`#5fd7af`) |
| **Event Red** | `#ff7c78` | Error, destructive, and failed states | 210 (`#ff8787`) |

Truecolor terminals use the exact design tokens. ANSI-256 uses the fallbacks above. `NO_COLOR`, dumb terminals, and non-TTY output remain unstyled.

### 60 / 30 / 10

- **60% — surfaces:** Void, Surface, and raised neutral steps.
- **30% — structure:** borders, text hierarchy, and quiet neutral mass.
- **10% — signal:** one dominant semantic color at a time. Brand Rose is reserved for identity, not general UI emphasis.

### WCAG 2.2 pairings (computed, relative-luminance method)

| Foreground on background | Ratio | Grade |
|---|---|---|
| Starlight on Void | **16.56:1** | AAA — body text default |
| Dust on Void | **9.22:1** | AAA — secondary text |
| Ion Cyan on Void | **11.86:1** | AAA — interaction and focus |
| Oxygen Blue on Void | **9.46:1** | AAA — information and links |
| Spectral Green on Void | **10.18:1** | AAA — success |
| Copper Amber on Void | **9.16:1** | AAA — warning |
| Event Red on Void | **7.55:1** | AAA — error |
| Brand Rose on Void | **2.70:1** | Decorative mark only; never normal text |
| Brand Rose on Starlight | **6.13:1** | AA normal text on light surfaces |

`design-system/design-tokens.json` is authoritative for machine consumers.

---

## 3. Typography & mark direction

### Typography

Four registers, matching how the product already speaks:

- **Brand display:** **Carina Display Alpha**, derived from the accepted CARINA wordmark. Use for the product name and composed identity lockups only — not general headings or body.
- **Editorial titles:** **Newsreader** (variable preferred). Use for documentation and marketing **primary titles** (page H1, hero title). Never use Newsreader to typeset or replace the CARINA wordmark; never use it for body, nav, or controls.
- **UI and body:** **Geist Sans** or the system sans stack. Controls, navigation, tables, metrics, section headings (H2+), and long-form documentation body.
- **Mono / the register of truth:** **Geist Mono** or the system mono stack. Anything the product attests — audit lines, hashes, policy names, file paths, version strings — renders in mono.
- **Repo READMEs** may stay GitHub-default type; the **docs site** (`apps/docs`) follows this four-register system end-to-end.
- **Design-system source:** `design-system/DESIGN.md` and `design-system/design-tokens.json` are checked in for VS Code, the docs site, and future clients.

### Mark direction — governed action with a return path

The earlier recommendation to use an aperture as the core mark is withdrawn.
An aperture expresses the brand's visual temperament, but it does not provide
product-specific evidence: photography, security, optics, and generic gateway
software can make the same claim. Aperture remains scene and layout grammar only.

The core mark starts from Carina's implemented execution contract: an agent action
crosses local authority only after a capability decision, writes durable evidence,
and retains a transaction path back. Exploration uses three formal families:

1. **Cross / Register / Return** — one boundary crossing creates its own indexed
   counterpart and visible return path.
2. **Attenuated Passage** — a form exits a decision plane with narrower authority,
   leaving a fixed registration at the crossing.
3. **Transaction Pair** — two states share one footprint and one reversible,
   precisely indexed displacement.

All families are flat, constructed geometry with one controlled event, legible at
16 px and explainable without an astronomy metaphor. They must avoid arrows, undo
icons, funnels, filters, portals, shields, locks, chains, initials, or diagrams.

**Candidate B — The Carved Edge.**
- *Silhouette:* a diagonal sculpted boundary dividing the canvas — emission (Ionized Rose → Carina Crimson) above, Void below — with an irregular but geometric cliff line.
- *Signal:* a single Starlight point just above the edge.
- *Rationale:* a horizon line with weight — one diagonal division gives every composition depth and a place for type to sit; strong as a background/texture system and social-card composition.
- *Banned readings:* mountain-outdoor logo, audio waveform, swoosh, stock-chart line. *Risk:* weak at favicon sizes; better as a scene motif than a mark.

**Candidate C — Held Light.**
- *Silhouette:* a soft enclosing outline (Dust Mauve or Ionized Rose) around a single bright point.
- *Signal:* one Starlight star-point, enclosed but not caged — the outline breathes, it does not seal.
- *Rationale:* a formal study in figure and ground for docs illustrations; the interest is the tension between the point and the boundary, nothing more.
- *Banned readings:* peanut, infinity symbol, atom/electron orbitals, eye, hourglass, padlock. *Risk:* enclosure forms drift toward cliché at small sizes; treat as a secondary illustration motif only.

**Final decision:** the symbol and CARINA wordmark are approved and frozen at
`assets/logo/carina-symbol.svg` and `assets/logo/carina-wordmark.svg`. Earlier
candidate language remains design history, not permission to redraw the mark.

---

## 4. Voice & tone

### Docs voice (already good — codify it)

Keep the existing register, verbatim as rules:

1. Calm, engineering-first, declarative present tense. Zero hype, zero emoji, zero exclamation marks.
2. Define by negation and boundary ("It is not an editor, a chat app, or a hosted sandbox"). Non-Goals sections are a brand feature.
3. Radical honesty about maturity — say what is not done, in the first screen.
4. Mechanism over marketing — every claim ties to a verifiable artifact: a test name, a file path, a config key, a PRD section.
5. Structured and tabular: Need→Answer tables, short noun-phrase headings.
6. Governance vocabulary as identity: policy, audit, rollback, capability, boundary, attenuation, hash-chained, local authority.
7. Six-locale parity: en, zh-CN, ja, ko, es, and fr are peer product languages. Each is rewritten in the same register, never assembled from machine-translated fragments. Fix the internal drift: the Chinese PRD's "Agent OS" register stays internal; public positioning uses the locale-native equivalent of "agent runtime" everywhere.

### CLI microcopy voice (rules for the rules+LLM microcopy engine)

Two voice registers, implemented as three isolated domains: Ambient uses the
field register; Governed and Degrade use the sober register. The switch is
hard and test-enforced.

- **Field register (default):** dry, brief, occasionally playful. Wit is allowed when nothing is at stake — idle states, progress, success, housekeeping. Jokes are structural (understatement, precision-as-humor), never emoji, never exclamation marks, never at the expense of trust. If a line could make a user doubt that the tool is serious about their machine, cut the joke.
- **Sober register (mandatory):** any moment touching **permission, policy denial, audit, destructive action, rollback, secrets, or data leaving the machine** switches to sober. Sober lines state: what happened, what changed, how to inspect or undo. Exact nouns, no metaphor, no personality. The register switch is itself a trust signal — users learn that when Carina goes quiet and precise, it matters.
- **Locale parity:** en, zh-CN/zh-Hans, ja, ko, es, and fr are peers. The runtime key `zh` means Simplified Chinese; zh-Hant/zh-TW/zh-HK are not claimed until they have an authored catalog. Ambient copy is authored for the locale rather than translating English jokes: Chinese favors concise understatement; Japanese favors calm service language; Korean favors direct, respectful status language; Spanish and French favor natural sentence rhythm over English syntax. Personality remains conservative in every locale.
- **Governed parity:** permission, policy, audit, rollback, destructive action, secret, and egress copy carries the same facts and certainty in all six languages. It is never humorous, hedged, or generated at runtime.
- **Degrade parity:** every degraded state names the fact, its user-visible effect, and a concrete inspection or repair step. Commands, paths, hashes, IDs, provider names, and policy names remain byte-for-byte verbatim.
- **Grammar safety:** do not build sentences by concatenating translated fragments. Use complete locale templates with named placeholders so Japanese and Korean particles, Spanish agreement, French spacing, and CJK punctuation remain authored and reviewable.
- **Mechanical rules:** lowercase-first fragments allowed in field register; sober register uses full sentences. Hashes, paths, policy names always verbatim in mono. Never anthropomorphize the agent being governed; Carina speaks as the runtime, about the agent, in third person.

### Calibration lines

**Field register — en (10):**

1. `carina is watching. the agent works, the ledger fills.`
2. `nothing to roll back. today was uneventful, which is the point.`
3. `47 actions, 47 audit entries. arithmetic checks out.`
4. `workspace warm, policies loaded. ready when you are.`
5. `agent idle for 12m. no rush on this end.`
6. `cache hit. we have, in fact, been here before.`
7. `patch applied cleanly. no drama, as designed.`
8. `session resumed. everything is where you left it, and we can prove it.`
9. `update available (0.4.2). changelog is shorter than this sentence is long.`
10. `done. exit 0, ledger sealed, kettle's yours.`

**Field register — zh (10, native wit, not translations):**

1. `carina 在场。代理干活，账本记账。`
2. `无事可回滚。今天很平淡——平淡是设计目标。`
3. `47 个动作，47 条审计。一个不多，一个不少。`
4. `工作区已热身，策略已装载。万事俱备，不欠东风。`
5. `代理已闲置 12 分钟。这边不催。`
6. `缓存命中。此路我们走过。`
7. `补丁干净落地。无惊无险，本该如此。`
8. `会话已恢复。原样奉还，且有据可查。`
9. `有更新（0.4.2）。更新日志比这句话还短。`
10. `完成。exit 0，账本封存，慢用。`

**Sober register — en (10):**

1. `permission required: agent requests write access to ~/.ssh. no default. [allow once] [deny] [inspect request]`
2. `denied by policy net.outbound-allowlist: connection to 203.0.113.7:443 was not attempted. audit entry a8f2c1 written.`
3. `destructive action: rm -rf ./build (214 files). a restore point will be created first. confirm to proceed.`
4. `rollback complete. 12 file changes reverted to checkpoint 7f3d9e. nothing outside the transaction was touched.`
5. `audit chain verified: 1,204 entries, head 9c4b…e21a, no gaps, no rewrites.`
6. `secret detected in agent output. display, transcript, and audit record contain only the redacted value. original not retained.`
7. `policy bundle updated: 2 rules tightened, 0 loosened. loosening requires explicit operator approval.`
8. `partial result for transaction 41. applied steps: 3/5. remaining steps did not complete. inspect the audit trail for details.`
9. `this action sends file contents to an external model endpoint (api.example.com). proceed?`
10. `checkpoint created before migration. preview with: carina checkpoint preview sess-42 8a11f0. after review, restore with: carina checkpoint restore sess-42 8a11f0 --yes.`

**Sober register — zh (10):**

1. `需要授权：代理请求写入 ~/.ssh。无默认选项。[允许一次] [拒绝] [查看请求详情]`
2. `已被策略 net.outbound-allowlist 拒绝：到 203.0.113.7:443 的连接未被发起。审计条目 a8f2c1 已写入。`
3. `破坏性操作：rm -rf ./build（214 个文件）。将先创建还原点。确认后执行。`
4. `回滚完成。12 处文件改动已还原至检查点 7f3d9e。事务之外未触碰任何内容。`
5. `审计链校验通过：1,204 条记录，链头 9c4b…e21a，无缺口，无改写。`
6. `在代理输出中检测到密钥。显示、转录和审计记录仅包含脱敏值；原文不会保留。`
7. `策略包已更新：收紧 2 条，放宽 0 条。放宽须操作者明确批准。`
8. `事务 41 部分完成。已应用步数：3/5。其余步骤未完成。请检查审计记录了解详情。`
9. `此操作将把文件内容发送至外部模型端点（api.example.com）。是否继续？`
10. `迁移前已创建检查点。先预览：carina checkpoint preview sess-42 8a11f0。审阅后恢复：carina checkpoint restore sess-42 8a11f0 --yes。`

---

## 5. Asset production record

Production followed a gated reference-image DAG: approved symbol and wordmark first,
dependent identity applications second, then font control glyphs and the remaining
alphabet. Only approved outputs were promoted into this repository.

Current stable consumers:

- **READMEs:** `assets/hero/carina-readme-hero.webp` and the approved badge palette.
- **VS Code:** `integrations/vscode/media/carina.svg`, derived byte-for-byte from the monochrome symbol master.
- **TUI:** semantic terminal tokens in `go/tui/theme`, checked against the DTCG source.
- **Future web/UI clients:** `design-system/variables.css`, `tailwind-v4.css`, `carina.ts`, and the WOFF2 display font.

Generation rounds, rejected candidates, logo-skin experiments, and mockups remain
outside the code repository. `asset-manifest.json` records the approved inventory.

---

## 6. Deliberate exclusions

Standing negative constraints — appended verbatim to every generation prompt and enforced in review:

1. **No purple-gradient AI slop.** No violet-to-blue gradients on black, no gradient orbs, no glassmorphism, no glowing translucent 3D loops (the old hero is the named anti-reference). Carina's rose/crimson/mauve is warmer and darker — never slide toward electric purple or neon violet. This resolves the gradient collision head-on: nebula color appears as **matte, smoky, organic texture** in scene assets and **flat duotone fills** in marks — never as a radial glow gradient.
2. **No teal-and-orange grading.** Oxygen-teal and Webb-amber belong to *other* renderings of Carina. The reference is Hα rose; do not import complementary-grade cinema color.
3. **No generic space clichés.** No rockets, astronauts, ringed planets, telescope silhouettes, constellation line-art, or "to the stars" copy.
4. **No starfield sprinkle.** Stars are sparse, small, mostly white with rare gold/blue — no glitter fields, no lens flares, no diffraction-spike crosses as decoration.
5. **No AI iconography.** No glowing brains, circuit neurons, sparkle emoji, robot mascots.
6. **No synthwave/cyberpunk.** No neon grids, cyan-magenta duotones, vector-neon. The nebula is organic, smoky, matte.
7. **No NASA counterfeits.** Never trace or composite actual Hubble/Webb frames; evoke the physics (emission, dust, aperture), don't counterfeit agency imagery. No photoreal nebula as a logo.
8. **No pure black, no pure white.** Product backgrounds use Void `#0d1214`; primary dark-mode text uses Starlight `#f3f0e8`.
9. **No security clichés in the mark.** No padlocks, shields, vaults, chains-as-links; the aperture is an *opening for light*, not a lock.
10. **No hype register.** No exclamation marks, no emoji, no superlatives in any brand surface — the docs voice governs marketing too.
11. **No generated text in rasters.** All composed type is set in real fonts at export time; image models never render words.
12. **No allegory.** The nebula supplies palette and temperament only (§1); no visual may encode a product-feature metaphor (aperture=policy, dust=audit, star=agent are all banned readings), and process metaphors from research docs (absorbed project names, internal codenames) never surface visually.
