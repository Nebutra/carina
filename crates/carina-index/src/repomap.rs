//! Aider-style repo map: confidence-weighted PageRank over the edges graph,
//! rendered as a compact per-file symbol listing cut to a token budget using
//! the daemon's `len/4 + 1` token estimate.
//!
//! `pagerank` is a pure function: damping 0.85, fixed iteration count,
//! dangling mass redistributed so scores stay a probability distribution.

use std::collections::{HashMap, HashSet};

use crate::extract::SymbolKind;
use crate::store::Store;
use crate::symbols::kind_from_str;
use crate::IndexError;

/// Multiplier applied to symbols living in `focus_paths` before the final
/// renormalization (Aider's "chat files" bias, kept outside the pure ranker).
const FOCUS_BOOST: f64 = 2.0;

pub struct RepoMapOptions {
    pub token_budget: usize,
    /// Bias rank toward these files (empty = none).
    pub focus_paths: Vec<String>,
    pub damping: f64,
    pub iterations: usize,
}

impl Default for RepoMapOptions {
    fn default() -> Self {
        RepoMapOptions {
            token_budget: 1024,
            focus_paths: Vec::new(),
            damping: 0.85,
            iterations: 20,
        }
    }
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct RankedSymbol {
    pub qualified_name: String,
    pub path: String,
    pub kind: SymbolKind,
    pub rank: f64,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct RepoMap {
    /// Rendered map, grouped by path.
    pub text: String,
    pub ranked: Vec<RankedSymbol>,
    pub token_estimate: usize,
    /// Total symbols found in the index before the token-budget cut.
    pub symbols_total: usize,
    /// Symbols actually rendered into `text` within the token budget.
    pub symbols_included: usize,
    /// Total distinct files with at least one symbol in the index.
    pub files_total: usize,
    /// Distinct files that got at least one symbol line rendered into `text`.
    pub files_included: usize,
}

/// Confidence-weighted PageRank over the symbol graph. Pure, unit-testable.
pub(crate) fn pagerank(
    nodes: usize,
    edges: &[(usize, usize, f64)],
    damping: f64,
    iterations: usize,
) -> Vec<f64> {
    if nodes == 0 {
        return Vec::new();
    }
    let n = nodes as f64;
    let mut out_weight = vec![0.0f64; nodes];
    for &(src, _, weight) in edges {
        out_weight[src] += weight;
    }
    let mut scores = vec![1.0 / n; nodes];
    for _ in 0..iterations {
        // Dangling nodes (no outgoing weight) spread their mass uniformly so
        // the scores stay a probability distribution.
        let dangling: f64 = scores
            .iter()
            .zip(&out_weight)
            .filter(|(_, out)| **out == 0.0)
            .map(|(score, _)| *score)
            .sum();
        let base = (1.0 - damping) / n + damping * dangling / n;
        let mut next = vec![base; nodes];
        for &(src, dst, weight) in edges {
            next[dst] += damping * scores[src] * (weight / out_weight[src]);
        }
        scores = next;
    }
    scores
}

/// Builds the repo map: PageRank over the stored symbol graph, focus-path
/// bias, then a per-file rendering cut to the token budget with the daemon's
/// `len/4 + 1` estimate.
pub(crate) fn build(store: &Store, opts: &RepoMapOptions) -> Result<RepoMap, IndexError> {
    let mut stmt = store.conn.prepare(
        "SELECT id, path, qualified_name, kind, start_line, end_line
         FROM symbols ORDER BY id",
    )?;
    struct Row {
        id: i64,
        path: String,
        qualified_name: String,
        kind: SymbolKind,
        start_line: u32,
        end_line: u32,
    }
    let rows: Vec<Row> = stmt
        .query_map([], |row| {
            let kind: String = row.get(3)?;
            Ok(Row {
                id: row.get(0)?,
                path: row.get(1)?,
                qualified_name: row.get(2)?,
                kind: kind_from_str(&kind).unwrap_or(SymbolKind::Function),
                start_line: row.get(4)?,
                end_line: row.get(5)?,
            })
        })?
        .collect::<Result<_, _>>()?;

    let node_by_id: HashMap<i64, usize> = rows
        .iter()
        .enumerate()
        .map(|(index, row)| (row.id, index))
        .collect();
    let mut edge_stmt = store
        .conn
        .prepare("SELECT src_id, dst_id, confidence FROM edges ORDER BY src_id, dst_id")?;
    let edges: Vec<(usize, usize, f64)> = edge_stmt
        .query_map([], |row| {
            Ok((row.get::<_, i64>(0)?, row.get::<_, i64>(1)?, row.get(2)?))
        })?
        .collect::<Result<Vec<(i64, i64, f64)>, _>>()?
        .into_iter()
        .filter_map(|(src, dst, confidence)| {
            Some((*node_by_id.get(&src)?, *node_by_id.get(&dst)?, confidence))
        })
        .collect();

    let mut ranks = pagerank(rows.len(), &edges, opts.damping, opts.iterations);
    if !opts.focus_paths.is_empty() {
        let focus: HashSet<&str> = opts.focus_paths.iter().map(String::as_str).collect();
        for (rank, row) in ranks.iter_mut().zip(&rows) {
            if focus.contains(row.path.as_str()) {
                *rank *= FOCUS_BOOST;
            }
        }
        let total: f64 = ranks.iter().sum();
        if total > 0.0 {
            for rank in &mut ranks {
                *rank /= total;
            }
        }
    }

    let mut ranked: Vec<RankedSymbol> = rows
        .iter()
        .zip(&ranks)
        .map(|(row, rank)| RankedSymbol {
            qualified_name: row.qualified_name.clone(),
            path: row.path.clone(),
            kind: row.kind,
            rank: *rank,
        })
        .collect();
    let mut order: Vec<usize> = (0..rows.len()).collect();
    order.sort_by(|&a, &b| {
        ranks[b]
            .partial_cmp(&ranks[a])
            .unwrap_or(std::cmp::Ordering::Equal)
            .then_with(|| rows[a].path.cmp(&rows[b].path))
            .then(rows[a].start_line.cmp(&rows[b].start_line))
            .then(rows[a].id.cmp(&rows[b].id))
    });
    ranked = order.iter().map(|&i| ranked[i].clone()).collect();

    // Group by file in first-appearance (best-rank) order; symbols within a
    // file keep rank order. Lines are added while they fit the budget; a
    // file's header is charged together with its first symbol line.
    let mut text = String::new();
    let mut by_file: Vec<(&str, Vec<usize>)> = Vec::new();
    for &index in &order {
        let path = rows[index].path.as_str();
        match by_file.iter_mut().find(|(p, _)| *p == path) {
            Some((_, members)) => members.push(index),
            None => by_file.push((path, vec![index])),
        }
    }
    let symbols_total = rows.len();
    let files_total = by_file.len();
    let mut symbols_included = 0usize;
    let mut files_included = 0usize;
    'render: for (path, members) in &by_file {
        let header = format!("{path}:\n");
        let mut header_pending = true;
        for &index in members {
            let row = &rows[index];
            let line = format!(
                "  {} {} ({}-{})\n",
                kind_label(row.kind),
                row.qualified_name,
                row.start_line,
                row.end_line
            );
            let added = line.len() + if header_pending { header.len() } else { 0 };
            if token_estimate(text.len() + added) > opts.token_budget {
                break 'render;
            }
            if header_pending {
                text.push_str(&header);
                header_pending = false;
                files_included += 1;
            }
            text.push_str(&line);
            symbols_included += 1;
        }
    }

    let token_estimate = token_estimate(text.len());
    Ok(RepoMap {
        text,
        ranked,
        token_estimate,
        symbols_total,
        symbols_included,
        files_total,
        files_included,
    })
}

/// The Go daemon's transport token estimate: `len/4 + 1`.
fn token_estimate(bytes: usize) -> usize {
    bytes / 4 + 1
}

/// Compact per-line kind label (doc example: `struct Kernel`, `fn …`).
fn kind_label(kind: SymbolKind) -> &'static str {
    match kind {
        SymbolKind::Function | SymbolKind::Method => "fn",
        SymbolKind::Struct => "struct",
        SymbolKind::Enum => "enum",
        SymbolKind::Trait => "trait",
        SymbolKind::Interface => "interface",
        SymbolKind::Class => "class",
        SymbolKind::Const => "const",
        SymbolKind::TypeAlias => "type",
        SymbolKind::Module => "mod",
        SymbolKind::Variable => "var",
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{CodeIndex, IngestFile};

    fn fixture_index() -> CodeIndex {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        let files = [
            ("a.rs", "pub fn central() {}\n"),
            ("b.rs", "pub fn caller_one() {\n    central();\n}\n"),
            ("c.rs", "pub fn caller_two() {\n    central();\n}\n"),
        ];
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

    fn rank_of(map: &RepoMap, needle: &str) -> f64 {
        map.ranked
            .iter()
            .find(|s| s.qualified_name.contains(needle))
            .unwrap_or_else(|| panic!("missing ranked symbol {needle}"))
            .rank
    }

    #[test]
    fn repo_map_options_default() {
        let opts = RepoMapOptions::default();
        assert_eq!(opts.token_budget, 1024);
        assert!(opts.focus_paths.is_empty());
        assert!((opts.damping - 0.85).abs() < 1e-12);
        assert_eq!(opts.iterations, 20);
    }

    #[test]
    fn pagerank_ranks_most_referenced_symbol_highest() {
        let scores = pagerank(4, &[(1, 0, 1.0), (2, 0, 1.0), (3, 0, 1.0)], 0.85, 20);
        assert_eq!(scores.len(), 4);
        for other in 1..4 {
            assert!(
                scores[0] > scores[other],
                "hub node must outrank node {other}: {scores:?}"
            );
        }
    }

    #[test]
    fn pagerank_is_deterministic() {
        let edges = [(0usize, 1usize, 1.0f64), (1, 2, 0.5), (2, 0, 1.0)];
        let a = pagerank(3, &edges, 0.85, 20);
        let b = pagerank(3, &edges, 0.85, 20);
        assert_eq!(a, b, "identical inputs must produce identical scores");
    }

    #[test]
    fn pagerank_scores_sum_to_one() {
        // Nodes 1 and 2 are dangling; their mass must be redistributed, not lost.
        let scores = pagerank(3, &[(0, 1, 1.0)], 0.85, 20);
        assert!(scores.iter().all(|s| s.is_finite() && *s >= 0.0), "{scores:?}");
        let sum: f64 = scores.iter().sum();
        assert!((sum - 1.0).abs() < 1e-6, "scores must sum to 1, got {sum}");
    }

    #[test]
    fn pagerank_weights_edges_by_confidence() {
        let scores = pagerank(3, &[(0, 1, 1.0), (0, 2, 0.25)], 0.85, 20);
        assert!(
            scores[1] > scores[2],
            "higher-confidence in-edge must outrank: {scores:?}"
        );
    }

    #[test]
    fn repo_map_respects_token_budget() {
        let idx = fixture_index();
        let opts = RepoMapOptions {
            token_budget: 8,
            ..RepoMapOptions::default()
        };
        let map = idx.repo_map(&opts).expect("repo_map");
        assert!(
            map.token_estimate <= 8,
            "estimate {} exceeds budget",
            map.token_estimate
        );
        assert_eq!(map.token_estimate, map.text.len() / 4 + 1);
    }

    #[test]
    fn repo_map_groups_symbols_by_file() {
        let idx = fixture_index();
        let map = idx
            .repo_map(&RepoMapOptions {
                token_budget: 10_000,
                ..RepoMapOptions::default()
            })
            .expect("repo_map");
        assert!(map.text.contains("a.rs:"), "map text:\n{}", map.text);
        assert!(map.text.contains("central"), "map text:\n{}", map.text);
        assert!(!map.ranked.is_empty());
        for pair in map.ranked.windows(2) {
            assert!(
                pair[0].rank >= pair[1].rank,
                "ranked must be sorted descending"
            );
        }
        assert!(
            map.ranked[0].qualified_name.contains("central"),
            "most-referenced symbol first: {:?}",
            map.ranked[0]
        );
    }

    #[test]
    fn repo_map_focus_paths_bias_ranking() {
        let idx = fixture_index();
        let unfocused = idx
            .repo_map(&RepoMapOptions {
                token_budget: 10_000,
                ..RepoMapOptions::default()
            })
            .expect("repo_map");
        let focused = idx
            .repo_map(&RepoMapOptions {
                token_budget: 10_000,
                focus_paths: vec!["b.rs".into()],
                ..RepoMapOptions::default()
            })
            .expect("repo_map focused");
        assert!(
            rank_of(&focused, "caller_one") > rank_of(&unfocused, "caller_one"),
            "focusing b.rs must raise caller_one's rank"
        );
    }

    #[test]
    fn repo_map_counts_totals_and_included_under_budget() {
        let idx = fixture_index();
        // A generous budget should include every symbol and every file.
        let map = idx
            .repo_map(&RepoMapOptions {
                token_budget: 10_000,
                ..RepoMapOptions::default()
            })
            .expect("repo_map");
        assert_eq!(map.symbols_total, 3, "fixture has 3 symbols");
        assert_eq!(map.files_total, 3, "fixture has 3 files");
        assert_eq!(map.symbols_included, map.symbols_total);
        assert_eq!(map.files_included, map.files_total);
    }

    #[test]
    fn repo_map_counts_dropped_items_under_tight_budget() {
        let idx = fixture_index();
        let map = idx
            .repo_map(&RepoMapOptions {
                token_budget: 1,
                ..RepoMapOptions::default()
            })
            .expect("repo_map");
        assert!(map.symbols_total >= map.symbols_included);
        assert!(map.files_total >= map.files_included);
        assert!(
            map.symbols_included < map.symbols_total,
            "a budget of 1 token must drop at least one symbol: {} included of {} total",
            map.symbols_included,
            map.symbols_total
        );
    }

    #[test]
    fn repo_map_counts_are_zero_on_empty_index() {
        let idx = CodeIndex::in_memory().expect("in-memory index");
        let map = idx.repo_map(&RepoMapOptions::default()).expect("repo_map");
        assert_eq!(map.symbols_total, 0);
        assert_eq!(map.symbols_included, 0);
        assert_eq!(map.files_total, 0);
        assert_eq!(map.files_included, 0);
        assert!(map.text.is_empty());
    }
}
