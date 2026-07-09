//! Keyword search: FTS5 BM25 and exact substring match over chunks — plus,
//! when the caller supplies a query embedding, brute-force cosine similarity
//! over stored vectors — fused with Reciprocal Rank Fusion (k = 60).
//!
//! `rrf_fuse` is a pure function: `score(d) = sum over lists of
//! 1 / (k + rank(d))` with 1-based ranks, sorted by score descending and by
//! ascending id on ties so results are deterministic.

use std::collections::HashMap;

use rusqlite::params;

use crate::extract::SymbolRecord;
use crate::lang::Lang;
use crate::store::{lang_to_str, Store};
use crate::symbols::symbol_by_id;
use crate::IndexError;

/// RRF constant from the design doc: score(d) = sum of 1 / (60 + rank).
const RRF_K: f64 = 60.0;

/// Snippets are chunk content truncated for transport.
const SNIPPET_MAX_BYTES: usize = 512;

pub struct SearchOptions {
    pub limit: usize,
    pub lang: Option<Lang>,
    pub path_prefix: Option<String>,
    /// Caller-supplied query embedding: set together with `model_id` or not
    /// at all; enables the third (cosine) RRF channel. Without it search is
    /// bit-identical V1 two-way.
    pub query_vector: Option<Vec<f32>>,
    /// Embedding model the vector (and the candidate rows) belong to.
    pub model_id: Option<String>,
}

impl Default for SearchOptions {
    fn default() -> Self {
        SearchOptions {
            limit: 20,
            lang: None,
            path_prefix: None,
            query_vector: None,
            model_id: None,
        }
    }
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct SearchHit {
    pub path: String,
    pub start_line: u32,
    pub end_line: u32,
    /// Chunk content, truncated for transport.
    pub snippet: String,
    /// RRF-fused score.
    pub score: f64,
    /// Subset of ["bm25", "exact", "vector"].
    pub sources: Vec<String>,
    pub symbol: Option<SymbolRecord>,
    /// Provenance: hash of the file content this hit was indexed from, so
    /// callers can detect results that no longer match the workspace.
    pub content_hash: String,
    /// Provenance: when the file version was ingested (RFC 3339).
    pub indexed_at: String,
}

/// Reciprocal Rank Fusion: score(d) = sum over lists of 1 / (k + rank(d)),
/// k = 60, rank 1-based. Pure function, unit-testable.
pub(crate) fn rrf_fuse(ranked_lists: &[Vec<i64>], k: f64) -> Vec<(i64, f64)> {
    // Accumulate in first-seen order so identical inputs fold floats in the
    // same order and produce bit-identical scores.
    let mut fused: Vec<(i64, f64)> = Vec::new();
    let mut slot_by_id: HashMap<i64, usize> = HashMap::new();
    for list in ranked_lists {
        for (rank0, id) in list.iter().enumerate() {
            let contribution = 1.0 / (k + (rank0 + 1) as f64);
            match slot_by_id.get(id) {
                Some(&slot) => fused[slot].1 += contribution,
                None => {
                    slot_by_id.insert(*id, fused.len());
                    fused.push((*id, contribution));
                }
            }
        }
    }
    fused.sort_by(|a, b| {
        b.1.partial_cmp(&a.1)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(a.0.cmp(&b.0))
    });
    fused
}

/// Cosine similarity ranking over candidate `(chunk_id, vector)` rows:
/// `dot(a, b) / (|a||b|)`. Zero-norm vectors are skipped; rows whose dims
/// differ from the query are skipped (a model change mid-flight must not
/// error queries). Sorted by score descending, then ascending chunk_id —
/// pure function, deterministic.
pub(crate) fn cosine_rank(
    candidates: &[(i64, Vec<f32>)],
    query: &[f32],
    top: usize,
) -> Vec<i64> {
    let query_norm = query
        .iter()
        .map(|v| f64::from(*v) * f64::from(*v))
        .sum::<f64>()
        .sqrt();
    if query_norm == 0.0 || top == 0 {
        return Vec::new(); // zero-norm query: undefined cosine, rank nothing
    }
    let mut scored: Vec<(i64, f64)> = Vec::new();
    for (chunk_id, vector) in candidates {
        if vector.len() != query.len() {
            continue; // model change mid-flight: skip, never error
        }
        let mut dot = 0.0f64;
        let mut norm_sq = 0.0f64;
        for (a, b) in vector.iter().zip(query) {
            dot += f64::from(*a) * f64::from(*b);
            norm_sq += f64::from(*a) * f64::from(*a);
        }
        let norm = norm_sq.sqrt();
        if norm == 0.0 {
            continue; // zero-norm row: undefined cosine
        }
        scored.push((*chunk_id, dot / (norm * query_norm)));
    }
    scored.sort_by(|a, b| {
        b.1.partial_cmp(&a.1)
            .unwrap_or(std::cmp::Ordering::Equal)
            .then(a.0.cmp(&b.0))
    });
    scored.truncate(top);
    scored.into_iter().map(|(chunk_id, _)| chunk_id).collect()
}

/// Runs keyword search over the store: BM25 and exact-substring candidate
/// lists fused with RRF, then hydrated into transport-ready hits.
pub(crate) fn run(
    store: &Store,
    query: &str,
    opts: &SearchOptions,
) -> Result<Vec<SearchHit>, IndexError> {
    let query = query.trim();
    if query.is_empty() || opts.limit == 0 {
        return Ok(Vec::new());
    }
    let lang = opts.lang.map(lang_to_str);
    let path_prefix = opts.path_prefix.as_deref();
    // Fetch deeper than the final limit so fusion can promote documents that
    // one list ranks low but the other ranks high.
    let fetch = opts.limit.saturating_mul(5).clamp(20, 200) as i64;

    let bm25_ids = bm25_candidates(store, query, lang, path_prefix, fetch)?;
    let exact_ids = exact_candidates(store, query, lang, path_prefix, fetch)?;
    // The cosine channel exists only when the caller supplied a query vector
    // (with its model_id); otherwise search stays bit-identical V1 two-way.
    let vector_ids = match (&opts.query_vector, &opts.model_id) {
        (Some(query_vector), Some(model_id)) => vector_candidates(
            store,
            query_vector,
            model_id,
            lang,
            path_prefix,
            fetch,
        )?,
        _ => Vec::new(),
    };

    let mut hits = Vec::new();
    for (chunk_id, score) in rrf_fuse(
        &[bm25_ids.clone(), exact_ids.clone(), vector_ids.clone()],
        RRF_K,
    ) {
        if hits.len() == opts.limit {
            break;
        }
        let mut sources = Vec::new();
        if bm25_ids.contains(&chunk_id) {
            sources.push("bm25".to_string());
        }
        if exact_ids.contains(&chunk_id) {
            sources.push("exact".to_string());
        }
        if vector_ids.contains(&chunk_id) {
            sources.push("vector".to_string());
        }
        hits.push(hydrate_hit(store, chunk_id, score, sources)?);
    }
    Ok(hits)
}

/// Chunk ids ranked by cosine similarity to `query_vector`. Candidate rows
/// are fetched in ascending `chunk_id` order; the join on
/// `embeddings.content_hash = files.content_hash` makes a stale vector
/// structurally incapable of surfacing, even if the FK cascade were bypassed.
fn vector_candidates(
    store: &Store,
    query_vector: &[f32],
    model_id: &str,
    lang: Option<&str>,
    path_prefix: Option<&str>,
    fetch: i64,
) -> Result<Vec<i64>, IndexError> {
    let mut stmt = store.conn.prepare(
        "SELECT e.chunk_id, e.vector FROM embeddings e
         JOIN chunks c ON c.id = e.chunk_id
         JOIN files f ON f.path = c.path AND f.content_hash = e.content_hash
         WHERE e.model_id = ?1
           AND (?2 IS NULL OR f.lang = ?2)
           AND (?3 IS NULL OR instr(c.path, ?3) = 1)
         ORDER BY e.chunk_id",
    )?;
    let candidates: Vec<(i64, Vec<f32>)> = stmt
        .query_map(params![model_id, lang, path_prefix], |row| {
            let chunk_id: i64 = row.get(0)?;
            let blob: Vec<u8> = row.get(1)?;
            Ok((chunk_id, crate::embed::f32_from_le_blob(&blob)))
        })?
        .collect::<Result<_, _>>()?;
    Ok(cosine_rank(&candidates, query_vector, fetch as usize))
}

/// Chunk ids by ascending BM25 rank (best first) for a sanitized FTS query.
fn bm25_candidates(
    store: &Store,
    query: &str,
    lang: Option<&str>,
    path_prefix: Option<&str>,
    fetch: i64,
) -> Result<Vec<i64>, IndexError> {
    let Some(match_expr) = fts_match_expr(query) else {
        return Ok(Vec::new());
    };
    let mut stmt = store.conn.prepare(
        "SELECT c.id FROM chunks_fts
         JOIN chunks c ON c.id = chunks_fts.rowid
         JOIN files f ON f.path = c.path
         WHERE chunks_fts MATCH ?1
           AND (?2 IS NULL OR f.lang = ?2)
           AND (?3 IS NULL OR instr(c.path, ?3) = 1)
         ORDER BY bm25(chunks_fts), c.id
         LIMIT ?4",
    )?;
    let ids = stmt
        .query_map(params![match_expr, lang, path_prefix, fetch], |row| {
            row.get(0)
        })?
        .collect::<Result<_, _>>()?;
    Ok(ids)
}

/// Chunk ids containing the query as a literal substring, in stable
/// (path, start_line) order.
fn exact_candidates(
    store: &Store,
    query: &str,
    lang: Option<&str>,
    path_prefix: Option<&str>,
    fetch: i64,
) -> Result<Vec<i64>, IndexError> {
    let mut stmt = store.conn.prepare(
        "SELECT c.id FROM chunks c
         JOIN files f ON f.path = c.path
         WHERE instr(c.content, ?1) > 0
           AND (?2 IS NULL OR f.lang = ?2)
           AND (?3 IS NULL OR instr(c.path, ?3) = 1)
         ORDER BY c.path, c.start_line, c.id
         LIMIT ?4",
    )?;
    let ids = stmt
        .query_map(params![query, lang, path_prefix, fetch], |row| row.get(0))?
        .collect::<Result<_, _>>()?;
    Ok(ids)
}

/// Quotes each whitespace-separated term so user input is never parsed as
/// FTS5 query syntax (`AND`, `*`, `-`, …). Terms are implicitly ANDed.
fn fts_match_expr(query: &str) -> Option<String> {
    let terms: Vec<String> = query
        .split_whitespace()
        .map(|term| format!("\"{}\"", term.replace('"', "\"\"")))
        .collect();
    if terms.is_empty() {
        None
    } else {
        Some(terms.join(" "))
    }
}

fn hydrate_hit(
    store: &Store,
    chunk_id: i64,
    score: f64,
    sources: Vec<String>,
) -> Result<SearchHit, IndexError> {
    type HitRow = (String, u32, u32, String, Option<i64>, String, String);
    let (path, start_line, end_line, content, symbol_id, content_hash, indexed_at): HitRow =
        store.conn.query_row(
            "SELECT c.path, c.start_line, c.end_line, c.content, c.symbol_id,
                    f.content_hash, f.indexed_at
             FROM chunks c JOIN files f ON f.path = c.path
             WHERE c.id = ?1",
            [chunk_id],
            |row| {
                Ok((
                    row.get(0)?,
                    row.get(1)?,
                    row.get(2)?,
                    row.get(3)?,
                    row.get(4)?,
                    row.get(5)?,
                    row.get(6)?,
                ))
            },
        )?;
    let symbol = match symbol_id {
        Some(id) => symbol_by_id(store, id)?,
        None => None,
    };
    Ok(SearchHit {
        path,
        start_line,
        end_line,
        snippet: truncate_snippet(&content),
        score,
        sources,
        symbol,
        content_hash,
        indexed_at,
    })
}

fn truncate_snippet(content: &str) -> String {
    if content.len() <= SNIPPET_MAX_BYTES {
        return content.to_string();
    }
    let mut cut = SNIPPET_MAX_BYTES;
    while !content.is_char_boundary(cut) {
        cut -= 1;
    }
    format!("{}…", &content[..cut])
}

#[cfg(test)]
mod tests {
    use super::*;

    const EPS: f64 = 1e-9;

    #[test]
    fn rrf_matches_hand_computed_scores() {
        let fused = rrf_fuse(&[vec![1, 2, 3], vec![3, 1]], 60.0);
        // doc 1: 1/61 + 1/62; doc 3: 1/63 + 1/61; doc 2: 1/62.
        let expected = [
            (1, 1.0 / 61.0 + 1.0 / 62.0),
            (3, 1.0 / 63.0 + 1.0 / 61.0),
            (2, 1.0 / 62.0),
        ];
        assert_eq!(fused.len(), 3);
        for ((id, score), (want_id, want_score)) in fused.iter().zip(expected.iter()) {
            assert_eq!(id, want_id);
            assert!(
                (score - want_score).abs() < EPS,
                "doc {id}: got {score}, want {want_score}"
            );
        }
    }

    #[test]
    fn rrf_handles_doc_in_single_list() {
        let fused = rrf_fuse(&[vec![7]], 60.0);
        assert_eq!(fused.len(), 1);
        assert_eq!(fused[0].0, 7);
        assert!((fused[0].1 - 1.0 / 61.0).abs() < EPS);
    }

    #[test]
    fn rrf_empty_input_is_empty() {
        assert!(rrf_fuse(&[], 60.0).is_empty());
        assert!(rrf_fuse(&[vec![], vec![]], 60.0).is_empty());
    }

    #[test]
    fn rrf_breaks_ties_deterministically() {
        // Both docs score 1/61 + 1/62: ties order by ascending id.
        let fused = rrf_fuse(&[vec![1, 2], vec![2, 1]], 60.0);
        assert_eq!(fused.len(), 2);
        assert_eq!(fused[0].0, 1);
        assert_eq!(fused[1].0, 2);
        assert!((fused[0].1 - fused[1].1).abs() < EPS);
    }

    #[test]
    fn search_options_default() {
        let opts = SearchOptions::default();
        assert_eq!(opts.limit, 20);
        assert!(opts.lang.is_none());
        assert!(opts.path_prefix.is_none());
        assert!(opts.query_vector.is_none(), "no vector channel by default");
        assert!(opts.model_id.is_none());
    }

    // ---- V2: cosine channel + three-way fusion ---------------------------

    use crate::{ChunkEmbedding, CodeIndex, IngestFile};

    const MODEL: &str = "openai/text-embedding-3-small";

    fn fixture_index(files: &[(&str, &str)]) -> CodeIndex {
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

    /// Embeds every pending chunk with a per-path vector from `by_path`
    /// (chunks of unlisted paths get the zero vector, which cosine skips).
    fn embed_by_path(idx: &mut CodeIndex, by_path: &[(&str, Vec<f32>)]) {
        let dims = by_path[0].1.len();
        let (pending, _) = idx.pending_chunks(MODEL, 1000).expect("pending_chunks");
        let items: Vec<ChunkEmbedding> = pending
            .iter()
            .map(|p| ChunkEmbedding {
                chunk_id: p.chunk_id,
                content_hash: p.content_hash.clone(),
                vector: by_path
                    .iter()
                    .find(|(path, _)| *path == p.path)
                    .map(|(_, v)| v.clone())
                    .unwrap_or_else(|| vec![0.0; dims]),
            })
            .collect();
        let report = idx.embed_store(MODEL, &items).expect("embed_store");
        assert!(report.stale.is_empty(), "fixture chunks must store: {report:?}");
    }

    fn vector_opts(vector: Vec<f32>) -> SearchOptions {
        SearchOptions {
            query_vector: Some(vector),
            model_id: Some(MODEL.to_string()),
            ..SearchOptions::default()
        }
    }

    #[test]
    fn cosine_rank_matches_hand_computed_similarities() {
        // query [1, 0]: doc 1 cos=1.0; doc 2 ([0.6, 0.8]) cos=0.6; doc 3 cos=0.0.
        let candidates = vec![
            (3, vec![0.0f32, 1.0]),
            (1, vec![1.0f32, 0.0]),
            (2, vec![0.6f32, 0.8]),
        ];
        assert_eq!(cosine_rank(&candidates, &[1.0, 0.0], 3), vec![1, 2, 3]);
        assert_eq!(
            cosine_rank(&candidates, &[1.0, 0.0], 2),
            vec![1, 2],
            "top must cap the ranked list"
        );
    }

    #[test]
    fn cosine_rank_skips_zero_norm_and_dims_mismatched_rows() {
        let candidates = vec![
            (1, vec![1.0f32, 0.0]),
            (4, vec![0.0f32, 0.0]),       // zero norm: undefined cosine, skip
            (5, vec![1.0f32, 0.0, 0.0]),  // dims mismatch: model change, skip
        ];
        assert_eq!(cosine_rank(&candidates, &[1.0, 0.0], 10), vec![1]);
        // A zero-norm QUERY ranks nothing (never NaN-panics).
        assert!(cosine_rank(&candidates, &[0.0, 0.0], 10).is_empty());
    }

    #[test]
    fn cosine_rank_breaks_score_ties_by_ascending_chunk_id() {
        let candidates = vec![
            (9, vec![2.0f32, 0.0]), // same direction as id 2: identical cosine
            (2, vec![1.0f32, 0.0]),
        ];
        assert_eq!(cosine_rank(&candidates, &[1.0, 0.0], 10), vec![2, 9]);
    }

    #[test]
    fn search_with_query_vector_adds_vector_source_and_provenance() {
        let content_a = "pub fn zz_vec_alpha() {}\n";
        let mut idx = fixture_index(&[("a.rs", content_a), ("b.rs", "pub fn zz_vec_beta() {}\n")]);
        embed_by_path(&mut idx, &[("a.rs", vec![1.0, 0.0]), ("b.rs", vec![0.0, 1.0])]);

        // The text query matches a.rs; the vector agrees.
        let hits = idx
            .search("zz_vec_alpha", &vector_opts(vec![1.0, 0.0]))
            .expect("vector search");
        assert!(!hits.is_empty(), "expected hits");
        let hit = hits.iter().find(|h| h.path == "a.rs").expect("a.rs hit");
        assert!(
            hit.sources.iter().any(|s| s == "vector"),
            "cosine channel must contribute a 'vector' source: {hit:?}"
        );
        // Provenance is unchanged from V1 hydration.
        assert_eq!(hit.content_hash, crate::sha256_hex(content_a));
        assert!(!hit.indexed_at.is_empty());
    }

    #[test]
    fn vector_only_match_surfaces_without_keyword_overlap() {
        // The text query only matches a.rs, but the vector points at b.rs:
        // b.rs must surface through the cosine channel alone.
        let mut idx = fixture_index(&[
            ("a.rs", "pub fn zz_kw_target() {}\n"),
            ("b.rs", "pub fn zz_semantic_neighbor() {}\n"),
        ]);
        embed_by_path(&mut idx, &[("a.rs", vec![1.0, 0.0]), ("b.rs", vec![0.0, 1.0])]);
        let hits = idx
            .search("zz_kw_target", &vector_opts(vec![0.0, 1.0]))
            .expect("vector search");
        let b_hit = hits
            .iter()
            .find(|h| h.path == "b.rs")
            .expect("vector-only neighbor must be in the fused results");
        assert_eq!(b_hit.sources, vec!["vector".to_string()], "got {b_hit:?}");
        assert!(!b_hit.content_hash.is_empty());
        assert!(!b_hit.indexed_at.is_empty());
    }

    #[test]
    fn search_without_query_vector_stays_two_way_even_with_stored_vectors() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_two_way_fn() {}\n")]);
        embed_by_path(&mut idx, &[("a.rs", vec![1.0, 0.0])]);
        let hits = idx
            .search("zz_two_way_fn", &SearchOptions::default())
            .expect("plain search");
        assert!(!hits.is_empty());
        for hit in &hits {
            assert!(
                hit.sources.iter().all(|s| s == "bm25" || s == "exact"),
                "no vector source without a query_vector: {hit:?}"
            );
        }
    }

    #[test]
    fn three_way_search_is_deterministic_across_runs() {
        let mut idx = fixture_index(&[
            ("a.rs", "pub fn zz_det_shared() {}\n"),
            ("b.rs", "pub fn zz_det_shared_b() { zz_det_shared(); }\n"),
            ("c.rs", "pub fn zz_det_shared_c() { zz_det_shared(); }\n"),
        ]);
        embed_by_path(
            &mut idx,
            &[
                ("a.rs", vec![0.9, 0.1]),
                ("b.rs", vec![0.7, 0.3]),
                ("c.rs", vec![0.7, 0.3]), // identical vectors: tie-break territory
            ],
        );
        let run = |idx: &CodeIndex| -> Vec<(String, u32, String)> {
            idx.search("zz_det_shared", &vector_opts(vec![1.0, 0.0]))
                .expect("search")
                .iter()
                .map(|h| (h.path.clone(), h.start_line, format!("{:.12}", h.score)))
                .collect()
        };
        let first = run(&idx);
        assert!(!first.is_empty());
        for _ in 0..5 {
            assert_eq!(run(&idx), first, "identical inputs must rank identically");
        }
    }

    #[test]
    fn stale_vector_cannot_surface_replaced_chunk() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_stale_vec_old() {}\n")]);
        embed_by_path(&mut idx, &[("a.rs", vec![1.0, 0.0])]);
        // The file changes; nobody re-embeds. The old vector must be dead.
        idx.ingest(&[IngestFile {
            path: "a.rs".into(),
            content: "pub fn zz_stale_vec_new() {}\n".into(),
        }])
        .expect("re-ingest");
        let hits = idx
            .search("zz_stale_vec_old", &vector_opts(vec![1.0, 0.0]))
            .expect("search");
        assert!(
            hits.is_empty(),
            "a stale vector must never surface replaced content: {hits:?}"
        );
        // Even a query that keyword-matches the NEW content must not carry a
        // vector source (the stored vector belongs to the old content_hash).
        let fresh = idx
            .search("zz_stale_vec_new", &vector_opts(vec![1.0, 0.0]))
            .expect("search new");
        for hit in &fresh {
            assert!(
                hit.sources.iter().all(|s| s != "vector"),
                "un-reembedded chunk must not rank by a stale vector: {hit:?}"
            );
        }
    }

    #[test]
    fn vector_channel_respects_lang_and_path_prefix_filters() {
        let mut idx = fixture_index(&[
            ("src/a.rs", "pub fn zz_filter_marker() {}\n"),
            ("scripts/b.py", "def zz_filter_marker():\n    pass\n"),
        ]);
        embed_by_path(
            &mut idx,
            &[("src/a.rs", vec![1.0, 0.0]), ("scripts/b.py", vec![1.0, 0.0])],
        );
        let mut opts = vector_opts(vec![1.0, 0.0]);
        opts.lang = Some(Lang::Rust);
        let hits = idx.search("zz_filter_marker", &opts).expect("lang-filtered");
        assert!(!hits.is_empty());
        assert!(hits.iter().all(|h| h.path == "src/a.rs"), "got {hits:?}");

        let mut opts = vector_opts(vec![1.0, 0.0]);
        opts.path_prefix = Some("scripts/".into());
        let hits = idx.search("zz_filter_marker", &opts).expect("prefix-filtered");
        assert!(!hits.is_empty());
        assert!(
            hits.iter().all(|h| h.path.starts_with("scripts/")),
            "got {hits:?}"
        );
    }
}
