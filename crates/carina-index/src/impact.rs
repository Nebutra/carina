//! Impact analysis (docs/plans/code-intelligence.md, V3): bounded,
//! deterministic transitive *dependents* of a symbol name.
//!
//! Edges point `referencer -> definition`, so dependents follow incoming
//! edges (`e.dst_id = current`, surfacing `e.src_id`) via a recursive CTE.
//! Confidence decays multiplicatively with each edge's `1/n` fan-out
//! confidence; the walk is bounded twice (depth clamp plus a hard row cap on
//! the recursive member) and every list in the report — seeds included — is
//! bounded by the clamped limit, because bounded output is a governance
//! requirement. A walk that fills the row cap flags the report `truncated`
//! (degrade paths stay observable), and every ordering is fully specified so
//! identical inputs produce identical reports. `source` labels each
//! dependent honestly (V4): the walk carries a per-path flag that stays set
//! only while **every** hop is an LSP-sourced edge, and a dependent is
//! labeled `"lsp"` iff the strongest all-lsp path ties the strongest path
//! overall (deterministic tie-break toward the precise label); any
//! tree-sitter hop keeps the label `"tree-sitter"`.

use rusqlite::params;

use crate::extract::SymbolRecord;
use crate::store::Store;
use crate::symbols::{record_from_row, SYMBOL_COLUMNS, SYMBOL_JOIN};
use crate::IndexError;

/// Hard resource bound on the recursive member of the walk: a dense
/// name-approximate graph must not balloon the kernel process.
pub(crate) const IMPACT_WALK_ROW_CAP: i64 = 10_000;

#[derive(Debug, Clone)]
pub struct ImpactOptions {
    /// Hops from the seed definitions; clamped to 1..=5.
    pub max_depth: usize,
    /// Maximum dependents returned; clamped to 1..=200.
    pub limit: usize,
}

impl Default for ImpactOptions {
    fn default() -> Self {
        ImpactOptions {
            max_depth: 3,
            limit: 50,
        }
    }
}

/// One transitive dependent of the seed symbol(s).
#[derive(Debug, Clone, serde::Serialize)]
pub struct ImpactedSymbol {
    /// Carries `content_hash` / `indexed_at` provenance like every result.
    pub symbol: SymbolRecord,
    /// Shortest hop count from a seed; 1 = direct dependent.
    pub depth: u32,
    /// Product of edge confidences along the strongest path.
    pub confidence: f64,
    /// "lsp" when an all-lsp path carries the reported confidence,
    /// "tree-sitter" otherwise (any approximate hop keeps the honest label).
    pub source: String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct ImpactReport {
    /// Definitions of the queried name (the walk origins), bounded by the
    /// clamped limit like every list in the report — the walk itself still
    /// starts from every definition of the name.
    pub seeds: Vec<SymbolRecord>,
    /// Ordered by depth ASC, confidence DESC, path ASC, symbol id ASC.
    pub dependents: Vec<ImpactedSymbol>,
    /// True when the report is incomplete: the (clamped) limit cut the
    /// dependent list, or the walk itself filled `IMPACT_WALK_ROW_CAP` (a
    /// capped walk may have dropped reachable dependents — it must never be
    /// indistinguishable from a complete one).
    pub truncated: bool,
}

/// Walks the incoming-edge graph from every definition of `name`.
/// Unknown names yield an empty report (not an error); seeds never list
/// themselves as dependents, even when a cycle reaches them again.
pub(crate) fn run(
    store: &Store,
    name: &str,
    opts: &ImpactOptions,
) -> Result<ImpactReport, IndexError> {
    let max_depth = opts.max_depth.clamp(1, 5) as i64;
    let limit = opts.limit.clamp(1, 200);

    // Seeds are bounded output too (governance): a ubiquitous name must not
    // hydrate thousands of definition rows into the RPC response. The walk
    // below still originates from every definition (its name subquery is
    // independent of this LIMIT).
    let mut seed_stmt = store.conn.prepare(&format!(
        "SELECT {SYMBOL_COLUMNS} FROM {SYMBOL_JOIN}
         WHERE s.name = ?1
         ORDER BY s.path, s.start_line, s.id
         LIMIT ?2"
    ))?;
    let seeds: Vec<SymbolRecord> = seed_stmt
        .query_map(params![name, limit as i64], record_from_row)?
        .collect::<Result<_, _>>()?;
    if seeds.is_empty() {
        // Unknown names are an empty report, not an error.
        return Ok(ImpactReport {
            seeds,
            dependents: Vec::new(),
            truncated: false,
        });
    }

    // BFS over incoming edges. `ORDER BY 2, 1, 3 DESC` (depth, src id,
    // confidence) turns the recursive queue into a priority queue so the row
    // cap always cuts the deepest rows first — and which rows survive the
    // cap within a depth is fully specified, never SQLite queue order; the
    // depth clamp makes cycles terminate. Depth and confidence aggregate
    // independently per the documented semantics: shortest hop count,
    // strongest path product. Every symbol with the queried name is a seed,
    // so the name filter keeps seeds out of the dependents even when a cycle
    // walks back into them. The `lsp` column carries the per-path source:
    // it starts at (source = 'lsp') and stays 1 only while every further hop
    // is an LSP edge — one tree-sitter hop zeroes it for the whole path.
    let walk_cte = format!(
        "WITH RECURSIVE walk(id, depth, confidence, lsp) AS (
             SELECT e.src_id, 1, e.confidence, e.source = 'lsp'
             FROM edges e
             WHERE e.dst_id IN (SELECT id FROM symbols WHERE name = ?1)
           UNION ALL
             SELECT e.src_id, w.depth + 1, w.confidence * e.confidence,
                    w.lsp AND e.source = 'lsp'
             FROM walk w JOIN edges e ON e.dst_id = w.id
             WHERE w.depth < ?2
           ORDER BY 2, 1, 3 DESC
           LIMIT {IMPACT_WALK_ROW_CAP}
         )"
    );
    // Row-cap observability: a walk that filled the cap may have dropped
    // reachable dependents, so the report must flag itself truncated — a
    // capped walk must never be indistinguishable from a complete one.
    let walk_rows: i64 = store.conn.query_row(
        &format!("{walk_cte} SELECT COUNT(*) FROM walk"),
        params![name, max_depth],
        |row| row.get(0),
    )?;
    let walk_capped = walk_rows >= IMPACT_WALK_ROW_CAP;

    // `lsp_confidence` is the strongest all-lsp path (NULL when none); the
    // dependent is labeled "lsp" iff it ties the strongest path overall —
    // both maxima range over the same walk rows, so a tie compares the very
    // same float and the tie-break lands deterministically on the precise
    // label.
    let mut stmt = store.conn.prepare(&format!(
        "{walk_cte}
         SELECT {SYMBOL_COLUMNS}, MIN(w.depth) AS depth, MAX(w.confidence) AS confidence,
                MAX(CASE WHEN w.lsp THEN w.confidence END) AS lsp_confidence
         FROM walk w
         JOIN symbols s ON s.id = w.id
         JOIN files f ON f.path = s.path
         WHERE s.name <> ?1
         GROUP BY s.id
         ORDER BY depth ASC, confidence DESC, s.path ASC, s.id ASC
         LIMIT ?3"
    ))?;
    // Fetch one row past the clamped limit so truncation is observable.
    let mut dependents: Vec<ImpactedSymbol> = stmt
        .query_map(params![name, max_depth, (limit + 1) as i64], |row| {
            let confidence: f64 = row.get(10)?;
            let lsp_confidence: Option<f64> = row.get(11)?;
            let source = if lsp_confidence == Some(confidence) {
                "lsp"
            } else {
                "tree-sitter"
            };
            Ok(ImpactedSymbol {
                symbol: record_from_row(row)?,
                depth: row.get::<_, i64>(9)? as u32,
                confidence,
                source: source.to_string(),
            })
        })?
        .collect::<Result<_, _>>()?;
    let truncated = dependents.len() > limit || walk_capped;
    dependents.truncate(limit);

    Ok(ImpactReport {
        seeds,
        dependents,
        truncated,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{sha256_hex, CodeIndex, EdgeSpec, FileChange, IngestFile};

    fn fixture(files: &[(&str, &str)]) -> CodeIndex {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        let batch: Vec<IngestFile> = files
            .iter()
            .map(|(path, content)| IngestFile {
                path: (*path).to_string(),
                content: (*content).to_string(),
            })
            .collect();
        idx.ingest(&batch).expect("ingest");
        idx
    }

    fn opts(max_depth: usize, limit: usize) -> ImpactOptions {
        ImpactOptions { max_depth, limit }
    }

    #[test]
    fn default_options_match_the_documented_bounds() {
        let defaults = ImpactOptions::default();
        assert_eq!(defaults.max_depth, 3);
        assert_eq!(defaults.limit, 50);
    }

    #[test]
    fn direct_dependents_carry_fanout_confidence() {
        // "zz_dup_target" has two definitions, so the caller's edge fans out
        // with confidence 1/2 to each — the dependent must report 0.5.
        let idx = fixture(&[
            ("t1.rs", "pub fn zz_dup_target() {}\n"),
            ("t2.rs", "pub fn zz_dup_target() {}\n"),
            ("c.rs", "pub fn zz_direct_caller() {\n    zz_dup_target();\n}\n"),
        ]);
        let report = idx
            .impact("zz_dup_target", &ImpactOptions::default())
            .expect("impact");
        assert_eq!(report.seeds.len(), 2, "both definitions are seeds");
        assert!(report.seeds.iter().all(|s| s.name == "zz_dup_target"));
        assert_eq!(report.dependents.len(), 1, "got {:?}", report.dependents);
        let dep = &report.dependents[0];
        assert_eq!(dep.symbol.name, "zz_direct_caller");
        assert_eq!(dep.depth, 1);
        assert!((dep.confidence - 0.5).abs() < 1e-9, "got {}", dep.confidence);
        assert_eq!(dep.source, "tree-sitter");
        assert!(!report.truncated);
    }

    #[test]
    fn transitive_walk_decays_confidence_multiplicatively() {
        // target (2 defs) <- zz_mid_caller (2 defs, each calls target)
        //                 <- zz_far_outer (calls zz_mid_caller).
        // outer's strongest path: 0.5 (outer->mid fan-out) * 0.5 (mid->target
        // fan-out) = 0.25.
        let idx = fixture(&[
            ("t1.rs", "pub fn zz_dup2_target() {}\n"),
            ("t2.rs", "pub fn zz_dup2_target() {}\n"),
            ("m1.rs", "pub fn zz_mid_caller() {\n    zz_dup2_target();\n}\n"),
            ("m2.rs", "pub fn zz_mid_caller() {\n    zz_dup2_target();\n}\n"),
            ("o.rs", "pub fn zz_far_outer() {\n    zz_mid_caller();\n}\n"),
        ]);
        let report = idx
            .impact("zz_dup2_target", &ImpactOptions::default())
            .expect("impact");
        let mids: Vec<&ImpactedSymbol> = report
            .dependents
            .iter()
            .filter(|d| d.symbol.name == "zz_mid_caller")
            .collect();
        assert_eq!(mids.len(), 2, "got {:?}", report.dependents);
        for mid in mids {
            assert_eq!(mid.depth, 1);
            assert!((mid.confidence - 0.5).abs() < 1e-9, "got {}", mid.confidence);
        }
        let outer = report
            .dependents
            .iter()
            .find(|d| d.symbol.name == "zz_far_outer")
            .expect("transitive dependent");
        assert_eq!(outer.depth, 2);
        assert!(
            (outer.confidence - 0.25).abs() < 1e-9,
            "confidence must decay multiplicatively, got {}",
            outer.confidence
        );
    }

    #[test]
    fn multiple_paths_dedup_to_shortest_depth_and_strongest_confidence() {
        // zz_both reaches the hub both directly (depth 1, conf 1.0) and via
        // zz_via (depth 2): one row, shortest depth, strongest confidence.
        let idx = fixture(&[
            ("h.rs", "pub fn zz_hub() {}\n"),
            ("v.rs", "pub fn zz_via() {\n    zz_hub();\n}\n"),
            ("b.rs", "pub fn zz_both() {\n    zz_hub();\n    zz_via();\n}\n"),
        ]);
        let report = idx.impact("zz_hub", &ImpactOptions::default()).expect("impact");
        assert_eq!(report.dependents.len(), 2, "got {:?}", report.dependents);
        let both = report
            .dependents
            .iter()
            .find(|d| d.symbol.name == "zz_both")
            .expect("zz_both dependent");
        assert_eq!(both.depth, 1, "shortest depth wins the dedup");
        assert!((both.confidence - 1.0).abs() < 1e-9, "got {}", both.confidence);
    }

    #[test]
    fn cycles_terminate_and_seeds_never_self_list() {
        let idx = fixture(&[
            ("a.rs", "pub fn zz_cycle_a() {\n    zz_cycle_b();\n}\n"),
            ("b.rs", "pub fn zz_cycle_b() {\n    zz_cycle_a();\n}\n"),
        ]);
        let report = idx.impact("zz_cycle_a", &opts(5, 50)).expect("impact");
        assert_eq!(report.seeds.len(), 1);
        assert!(
            report.dependents.iter().any(|d| d.symbol.name == "zz_cycle_b"),
            "the cyclic caller is a dependent: {:?}",
            report.dependents
        );
        assert!(
            report.dependents.iter().all(|d| d.symbol.name != "zz_cycle_a"),
            "seeds must never list themselves: {:?}",
            report.dependents
        );
    }

    /// zz_chain_{i+1} calls zz_chain_i, i in 0..=6: dependents of zz_chain_0
    /// sit at depths 1..=6, one per depth.
    fn chain_fixture() -> CodeIndex {
        let mut files: Vec<(String, String)> = vec![("f0.rs".into(), "pub fn zz_chain_0() {}\n".into())];
        for i in 1..=6 {
            files.push((
                format!("f{i}.rs"),
                format!("pub fn zz_chain_{i}() {{\n    zz_chain_{}();\n}}\n", i - 1),
            ));
        }
        let refs: Vec<(&str, &str)> = files
            .iter()
            .map(|(p, c)| (p.as_str(), c.as_str()))
            .collect();
        fixture(&refs)
    }

    #[test]
    fn max_depth_bounds_and_clamps_the_walk() {
        let idx = chain_fixture();
        let shallow = idx.impact("zz_chain_0", &opts(1, 50)).expect("impact");
        assert_eq!(shallow.dependents.len(), 1, "got {:?}", shallow.dependents);
        assert_eq!(shallow.dependents[0].symbol.name, "zz_chain_1");

        // 0 clamps up to 1 (never an unbounded or empty walk).
        let zero = idx.impact("zz_chain_0", &opts(0, 50)).expect("impact");
        assert_eq!(zero.dependents.len(), 1, "got {:?}", zero.dependents);

        // 99 clamps down to 5: depth 6 must never be reached.
        let deep = idx.impact("zz_chain_0", &opts(99, 50)).expect("impact");
        assert_eq!(deep.dependents.len(), 5, "got {:?}", deep.dependents);
        assert!(deep.dependents.iter().all(|d| d.depth <= 5));
        assert!(
            deep.dependents.iter().all(|d| d.symbol.name != "zz_chain_6"),
            "clamped depth must bound the walk: {:?}",
            deep.dependents
        );
    }

    #[test]
    fn limit_clamps_and_sets_truncated() {
        let idx = fixture(&[
            ("h.rs", "pub fn zz_lim_hub() {}\n"),
            ("a.rs", "pub fn zz_lim_a() {\n    zz_lim_hub();\n}\n"),
            ("b.rs", "pub fn zz_lim_b() {\n    zz_lim_hub();\n}\n"),
            ("c.rs", "pub fn zz_lim_c() {\n    zz_lim_hub();\n}\n"),
        ]);
        let cut = idx.impact("zz_lim_hub", &opts(3, 2)).expect("impact");
        assert_eq!(cut.dependents.len(), 2, "got {:?}", cut.dependents);
        assert!(cut.truncated, "hitting the limit must set truncated");

        // 0 clamps up to 1.
        let one = idx.impact("zz_lim_hub", &opts(3, 0)).expect("impact");
        assert_eq!(one.dependents.len(), 1, "got {:?}", one.dependents);
        assert!(one.truncated);

        // A huge limit clamps to 200 and returns everything (no truncation).
        let all = idx.impact("zz_lim_hub", &opts(3, 100_000)).expect("impact");
        assert_eq!(all.dependents.len(), 3, "got {:?}", all.dependents);
        assert!(!all.truncated);
    }

    #[test]
    fn seeds_are_bounded_by_the_clamped_limit() {
        // Bounded output is a governance requirement on every list in the
        // report: a ubiquitous name ("new", "init") must not serialize every
        // definition row into the RPC response.
        let idx = fixture(&[
            ("s1.rs", "pub fn zz_seed_bound() {}\n"),
            ("s2.rs", "pub fn zz_seed_bound() {}\n"),
            ("s3.rs", "pub fn zz_seed_bound() {}\n"),
        ]);
        let report = idx.impact("zz_seed_bound", &opts(3, 2)).expect("impact");
        assert_eq!(
            report.seeds.len(),
            2,
            "seeds must be bounded by the clamped limit: {:?}",
            report.seeds
        );
        // The bound is deterministic: path ASC keeps the same seeds surviving.
        assert_eq!(report.seeds[0].path, "s1.rs");
        assert_eq!(report.seeds[1].path, "s2.rs");
    }

    /// `hub_defs` definitions of zz_cap_hub, `callers` distinct direct
    /// callers (each caller's one reference fans out into `hub_defs` edges,
    /// so depth-1 walk rows = callers * hub_defs), plus one depth-2
    /// dependent zz_cap_deep that calls zz_cap_caller_0.
    fn cap_fixture(hub_defs: usize, callers: usize) -> CodeIndex {
        let mut files: Vec<(String, String)> = Vec::new();
        for i in 0..hub_defs {
            files.push((format!("d{i}.rs"), "pub fn zz_cap_hub() {}\n".to_string()));
        }
        for i in 0..callers {
            files.push((
                format!("c{i}.rs"),
                format!("pub fn zz_cap_caller_{i}() {{\n    zz_cap_hub();\n}}\n"),
            ));
        }
        files.push((
            "deep.rs".to_string(),
            "pub fn zz_cap_deep() {\n    zz_cap_caller_0();\n}\n".to_string(),
        ));
        let refs: Vec<(&str, &str)> = files
            .iter()
            .map(|(p, c)| (p.as_str(), c.as_str()))
            .collect();
        fixture(&refs)
    }

    #[test]
    fn walk_row_cap_is_observable_as_truncation() {
        // 100 defs x 100 callers = exactly IMPACT_WALK_ROW_CAP depth-1 rows:
        // the breadth-first queue cuts every depth-2 row, so zz_cap_deep is
        // dropped — the report must say truncated, never pretend the
        // transitive closure is complete.
        let capped = cap_fixture(100, 100)
            .impact("zz_cap_hub", &opts(3, 200))
            .expect("impact");
        assert!(
            capped.dependents.iter().all(|d| d.symbol.name != "zz_cap_deep"),
            "the row cap cuts the depth-2 dependent in this fixture"
        );
        assert!(
            capped.truncated,
            "a walk cut by IMPACT_WALK_ROW_CAP must be observable as truncated"
        );

        // 100 defs x 98 callers = 9,800 depth-1 + 100 depth-2 rows, under the
        // cap: the depth-2 dependent survives and the report is complete.
        let complete = cap_fixture(100, 98)
            .impact("zz_cap_hub", &opts(3, 200))
            .expect("impact");
        assert!(
            complete.dependents.iter().any(|d| d.symbol.name == "zz_cap_deep"),
            "under the cap the depth-2 dependent must be found: {:?}",
            complete.dependents.len()
        );
        assert!(!complete.truncated, "an un-capped, un-limited walk is complete");
    }

    #[test]
    fn results_are_deterministic_and_fully_ordered() {
        // Same depth, same confidence: path ASC breaks the tie, and two
        // identical calls return identical orderings.
        let idx = fixture(&[
            ("h.rs", "pub fn zz_ord_hub() {}\n"),
            ("z.rs", "pub fn zz_ord_z() {\n    zz_ord_hub();\n}\n"),
            ("a.rs", "pub fn zz_ord_a() {\n    zz_ord_hub();\n}\n"),
            ("m.rs", "pub fn zz_ord_m() {\n    zz_ord_hub();\n}\n"),
        ]);
        let first = idx.impact("zz_ord_hub", &ImpactOptions::default()).expect("impact");
        let paths: Vec<&str> = first.dependents.iter().map(|d| d.symbol.path.as_str()).collect();
        assert_eq!(paths, vec!["a.rs", "m.rs", "z.rs"], "path ASC tie-break");

        let second = idx.impact("zz_ord_hub", &ImpactOptions::default()).expect("impact");
        let first_keys: Vec<(String, i64, u32)> = first
            .dependents
            .iter()
            .map(|d| (d.symbol.path.clone(), d.symbol.id, d.depth))
            .collect();
        let second_keys: Vec<(String, i64, u32)> = second
            .dependents
            .iter()
            .map(|d| (d.symbol.path.clone(), d.symbol.id, d.depth))
            .collect();
        assert_eq!(first_keys, second_keys, "identical calls must return identical orderings");
    }

    #[test]
    fn unknown_name_returns_an_empty_report_not_an_error() {
        let idx = fixture(&[("a.rs", "pub fn zz_known_fn() {}\n")]);
        let report = idx
            .impact("zz_absent_name", &ImpactOptions::default())
            .expect("unknown name must not error");
        assert!(report.seeds.is_empty());
        assert!(report.dependents.is_empty());
        assert!(!report.truncated);
    }

    #[test]
    fn dependents_carry_content_hash_and_indexed_at_provenance() {
        let caller_content = "pub fn zz_prov_caller() {\n    zz_prov_hub();\n}\n";
        let idx = fixture(&[
            ("h.rs", "pub fn zz_prov_hub() {}\n"),
            ("c.rs", caller_content),
        ]);
        let report = idx.impact("zz_prov_hub", &ImpactOptions::default()).expect("impact");
        assert_eq!(report.dependents.len(), 1, "got {:?}", report.dependents);
        let dep = &report.dependents[0];
        assert_eq!(dep.symbol.content_hash, sha256_hex(caller_content));
        assert!(!dep.symbol.indexed_at.is_empty());
    }

    // ---- V4: source labeling over LSP-sourced edges ------------------------

    fn edge(src_path: &str, src_line: u32, dst_path: &str, dst_line: u32) -> EdgeSpec {
        EdgeSpec {
            src_path: src_path.into(),
            src_line,
            dst_path: dst_path.into(),
            dst_line,
        }
    }

    #[test]
    fn lsp_edges_label_dependents_source_lsp_with_full_confidence() {
        // Two definitions: the tree-sitter edge fans out at 0.5. The LSP
        // answer pins the caller to t1.rs precisely — the dependent must
        // report the deduped 1.0 confidence and source "lsp".
        let mut idx = fixture(&[
            ("t1.rs", "pub fn zz_lsp_target() {}\n"),
            ("t2.rs", "pub fn zz_lsp_target() {}\n"),
            ("c.rs", "pub fn zz_lsp_caller() {\n    zz_lsp_target();\n}\n"),
        ]);
        let report = idx
            .edges_store(&[edge("c.rs", 2, "t1.rs", 1)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "got {report:?}");

        let report = idx
            .impact("zz_lsp_target", &ImpactOptions::default())
            .expect("impact");
        let dep = report
            .dependents
            .iter()
            .find(|d| d.symbol.name == "zz_lsp_caller")
            .expect("caller dependent");
        assert!(
            (dep.confidence - 1.0).abs() < 1e-9,
            "the deduped lsp edge carries 1.0, got {}",
            dep.confidence
        );
        assert_eq!(dep.source, "lsp", "an all-lsp path must label the dependent lsp");
    }

    #[test]
    fn mixed_paths_keep_the_tree_sitter_label() {
        // hub <- mid (lsp edge) <- outer (tree-sitter edge): mid's only path
        // is all-lsp, outer's strongest path crosses a tree-sitter hop.
        let mut idx = fixture(&[
            ("h.rs", "pub fn zz_mix_hub() {}\n"),
            ("m.rs", "pub fn zz_mix_mid() {\n    zz_mix_hub();\n}\n"),
            ("o.rs", "pub fn zz_mix_outer() {\n    zz_mix_mid();\n}\n"),
        ]);
        let report = idx
            .edges_store(&[edge("m.rs", 2, "h.rs", 1)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "got {report:?}");

        let report = idx.impact("zz_mix_hub", &opts(3, 50)).expect("impact");
        let mid = report
            .dependents
            .iter()
            .find(|d| d.symbol.name == "zz_mix_mid")
            .expect("mid dependent");
        assert_eq!(mid.source, "lsp", "every hop lsp => lsp label");
        let outer = report
            .dependents
            .iter()
            .find(|d| d.symbol.name == "zz_mix_outer")
            .expect("outer dependent");
        assert_eq!(
            outer.source, "tree-sitter",
            "a path with any tree-sitter hop stays honestly tree-sitter"
        );
    }

    #[test]
    fn pagerank_reflects_the_lsp_upgraded_edge_weight() {
        // Same-shape hubs: each has two definitions, each is called once, so
        // both edges weigh 0.5 — until the LSP upsert dedups caller -> a1.rs
        // up to 1.0. PageRank consumes edge confidence, so the upgraded
        // definition must outrank its 0.5 counterpart (no repomap change:
        // the dedup itself carries the weight).
        let mut idx = fixture(&[
            ("a1.rs", "pub fn zz_pr_upgraded() {}\n"),
            ("a2.rs", "pub fn zz_pr_upgraded() {}\n"),
            ("b1.rs", "pub fn zz_pr_plain() {}\n"),
            ("b2.rs", "pub fn zz_pr_plain() {}\n"),
            (
                "c.rs",
                "pub fn zz_pr_caller() {\n    zz_pr_upgraded();\n    zz_pr_plain();\n}\n",
            ),
        ]);
        let report = idx
            .edges_store(&[edge("c.rs", 2, "a1.rs", 1)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "got {report:?}");

        let map = idx
            .repo_map(&crate::RepoMapOptions {
                token_budget: 10_000,
                ..crate::RepoMapOptions::default()
            })
            .expect("repo map");
        let rank_of = |path: &str| {
            map.ranked
                .iter()
                .find(|r| r.path == path)
                .unwrap_or_else(|| panic!("ranked symbol for {path}: {:?}", map.ranked))
                .rank
        };
        assert!(
            rank_of("a1.rs") > rank_of("b1.rs"),
            "the lsp-upgraded (1.0) definition must outrank the 0.5 one: a1={} b1={}",
            rank_of("a1.rs"),
            rank_of("b1.rs")
        );
    }

    #[test]
    fn impact_reflects_reresolved_edges_after_update() {
        // A re-ingested caller gets fresh symbol ids; the walk must follow
        // the re-resolved edges, not the dropped ones.
        let mut idx = fixture(&[
            ("h.rs", "pub fn zz_upd_hub() {}\n"),
            ("c.rs", "pub fn zz_upd_caller() {\n    zz_upd_hub();\n}\n"),
        ]);
        idx.update(&[FileChange::Upsert {
            path: "c.rs".into(),
            content: "// touched\npub fn zz_upd_caller() {\n    zz_upd_hub();\n}\n".into(),
        }])
        .expect("update");
        let report = idx.impact("zz_upd_hub", &ImpactOptions::default()).expect("impact");
        assert_eq!(
            report.dependents.len(),
            1,
            "a re-ingested caller stays a dependent: {:?}",
            report.dependents
        );
        assert_eq!(report.dependents[0].symbol.name, "zz_upd_caller");
        assert_eq!(report.dependents[0].depth, 1);
    }
}
