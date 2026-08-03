# Carina TUI Visual Density Program

> **Date**: 2026-08-03  
> **Product**: Carina `0.8.1` line (post functional P0 catch-up)  
> **Parent Trellis**: `.trellis/tasks/08-03-carina-tui-visual-density/`  
> **DNA inputs**: `01-dna-{omp,jcode,grok-build,codex}.md` · `styles.md` · `VOICE.md` · `07-post-0.8-gap-refresh.md`  
> **Goal**: Systematic presentation upgrade so operators *feel* OMP/Jcode/Grok density without diluting policy / audit / transactional patch.

---

## 0. Problem statement

Carina 0.7–0.8 closed **capability** debt (readiness, lifecycle, patch trust, ScreenMode, steer).  
User-perceived complaint remains: **the shell still reads like an operations ledger**, not a coding-agent conversation product.

| Axis | Carina | Best practice target |
|------|--------|----------------------|
| Information architecture | Events compete at one visual level | OMP: group/collapse routine success; seal results |
| Tool row language | Path/command-first residual noise | Jcode: intent-primary row |
| Typed surfaces | One paragraph family too often | Grok: plan/permission/tool/hunk typed chrome |
| Density control | Mostly fixed gaps | Explicit compact/comfortable |
| Brand temperature | Correct, cold | OMP glyph tiers + GrokNight-level cohesion **without** brand copy |

**Non-goals (program-wide REJECTED)**  
Default yolo · 3D idle · Buddy/pets · snapcompact · wholesale fork Grok pager / OMP π branding · break 3-hue transcript budget · put full audit chain in transcript · prompt-as-policy.

---

## 1. Design laws (bind every ISSUE)

1. **One semantic tree, many projectors** — density never forks business state.  
2. **styles.md is law** — ≤3 saturated transcript hues; `theme.rs` only colors; Reset root.  
3. **VOICE.md is law** — outcome language; escape hatch on every truncate/degrade.  
4. **Fail/diff/approval never collapse-hidden**.  
5. **Live = replay** for any new cell identity.  
6. **Steal principles, not pixels** — cite DNA path, adapt to daemon lifecycle.

---

## 2. Cluster map

```text
                    ┌──────────────────────────────┐
                    │  V011 Golden matrix (P0 gate) │
                    └──────────────▲───────────────┘
                                   │
     Cluster A Transcript ─────────┼───────── Cluster B Review
     V001 tool row                 │          V005 fullscreen chrome
     V002 read collapse            │          V006 plan density
     V003 cell skeletons           │          V007 composer chrome
     V004 density modes            │
                                   │
                    Cluster C Brand/Motion
                    V008 glyph tiers
                    V009 motion system
                    V010 side panel (P2)

                    Cluster D Process
                    V012 sweep checklist (P2)
```

**Execution waves**

| Wave | Weeks | ISSUEs | Exit |
|------|-------|--------|------|
| **W1 Dialogue** | 1–2 | V001, V002, V003, V011(start) | 3s sweep test passes on fixture transcript |
| **W2 Control** | 2–3 | V004, V007, V009 | density preference + motion contract green |
| **W3 Review** | 3–5 | V005, V006 | fullscreen diff/plan feel “product” |
| **W4 Polish** | 5–7 | V008, V010, V012, V011(finish) | glyph preview + optional side panel + checklist |

---

## 3. ISSUE index (Trellis)

| ID | P | Effort | Trellis directory | Best-practice anchors |
|----|---|:------:|-------------------|------------------------|
| **V000** | P0 | XL | `08-03-carina-tui-visual-density` | Program parent |
| **V001** | P0 | M | `08-03-tui-vd-tool-row-grammar` | Jcode `ensure_intent_in_schema` / `ui_tools.rs` |
| **V002** | P0 | M | `08-03-tui-vd-read-collapse` | OMP `read-tool-group.ts` |
| **V003** | P0 | L | `08-03-tui-vd-semantic-cell-skeletons` | Grok typed blocks; OMP in-place seal |
| **V004** | P0 | M | `08-03-tui-vd-density-modes` | Grok compact density; layout_contract gaps |
| **V005** | P1 | L | `08-03-tui-vd-fullscreen-review-chrome` | Grok hunk tracker **principles** + carina-patch |
| **V006** | P1 | M | `08-03-tui-vd-plan-approval-density` | Grok plan_mode a/s/c/q; Codex hard plan |
| **V007** | P1 | M | `08-03-tui-vd-composer-chrome` | OMP status icon language; Carina mode/queue chips |
| **V008** | P1 | M | `08-03-tui-vd-glyph-tiers` | OMP glyph scene unicode/nerd/ascii |
| **V009** | P1 | M | `08-03-tui-vd-motion-system` | OMP status/activity fps; Grok reduced-motion |
| **V010** | P2 | M–L | `08-03-tui-vd-side-density-panel` | Jcode info widgets |
| **V011** | P0 | M | `08-03-tui-vd-visual-golden-matrix` | Carina golden_frames + style-discipline |
| **V012** | P2 | S–M | `08-03-tui-vd-sweep-comparison-checklist` | Operator checklist, not screenshot worship |

---

## 4. Dependency graph

```text
V011 ── (parallel from day 0; blocks merge of A/B)
V001 ──► V002 ──► V003 ──► V004
                └──► V007
V003 ──► V005 ──► V006
V004 ──► V009
V008 after V001 (glyph slots for tool states)
V010 after V007 (consumes chip model)
V012 after W1 (uses fixed fixtures)
```

---

## 5. Moat protection checklist (every PR)

- [ ] No default always-approve  
- [ ] No silent fuzzy patch  
- [ ] Audit events not dumped into transcript  
- [ ] 3-hue test still green  
- [ ] ScreenMode still projection-only  
- [ ] Soft interrupt ≠ hard cancel  

---

## 6. Success metrics

| Metric | Target |
|--------|--------|
| 3-second sweep (fixture: 30 tools + 1 patch + 1 approval) | Operator names intent, success count, open risk without scrolling twice |
| Transcript saturated hues | ≤3 (existing test) |
| Golden widths | 80 / 120 / 160 + CJK no reflow thrash |
| Density default | compact for power users; comfortable recoverable |
| User language | VOICE formula on all new truncation/degrade strings |

---

## 7. Relationship to prior competitive ISSUEs

| Prior | Relationship |
|-------|----------------|
| ISSUE-002 lifecycle | **Foundation** — V003 builds skeletons on lifecycle kinds |
| ISSUE-006 intent/read | **Deepen** — V001/V002 productize density language beyond minimum ship |
| ISSUE-003/015 patch/diff | **Surface** — V005 is chrome on transactional patch |
| ISSUE-005 ScreenMode | **Constraint** — V005 lives in Fullscreen; Minimal keeps native scrollback |
| ISSUE-007 queue | **Input** — V007 unifies queue chip with composer |

Do **not** reopen completed 001–018 unless regression; open V-series only.

---

## 8. Next actions

1. Review this program + child PRDs.  
2. `task.py start` on parent or V001 when ready to implement.  
3. Land W1 before any decorative work (V008/V010).
