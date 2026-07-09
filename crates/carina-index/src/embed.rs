//! Embedding storage facade (docs/plans/code-intelligence.md, V2).
//!
//! Vectors are caller-supplied derived data: the crate never computes or
//! fetches embeddings (kernel zero-network invariant). `pending_chunks`
//! surfaces chunk text lacking a vector for a `model_id`; `embed_store`
//! upserts vectors keyed `(chunk_id, model_id)` and verifies each item's
//! `content_hash` against the current file row so a vector can never attach
//! to content it was not computed from. Embeddings die with their chunks
//! (FK CASCADE), so invalidation is structural.

use rusqlite::{params, OptionalExtension};

use crate::{CodeIndex, IndexError};

/// Encodes a vector as the documented BLOB layout: f32 little-endian,
/// `dims * 4` bytes.
pub(crate) fn f32_to_le_blob(values: &[f32]) -> Vec<u8> {
    values.iter().flat_map(|v| v.to_le_bytes()).collect()
}

/// Decodes an embedding BLOB back into f32 values. Trailing bytes that do
/// not form a whole f32 are ignored (a malformed row must not error queries;
/// its dims will simply mismatch and the cosine channel skips it).
pub(crate) fn f32_from_le_blob(blob: &[u8]) -> Vec<f32> {
    blob.chunks_exact(4)
        .map(|bytes| f32::from_le_bytes([bytes[0], bytes[1], bytes[2], bytes[3]]))
        .collect()
}

/// One chunk lacking an embedding for a model. `content` is full chunk text —
/// content-equivalent data, only ever released through policy-gated RPCs.
#[derive(Debug, Clone, serde::Serialize)]
pub struct PendingChunk {
    pub chunk_id: i64,
    pub path: String,
    pub start_line: u32,
    pub end_line: u32,
    pub content: String,
    /// files.content_hash the chunk text belongs to; echoed back to
    /// `embed_store` to close the ingest-during-embed race.
    pub content_hash: String,
}

/// One vector to store, echoing the content_hash from `pending_chunks`.
#[derive(Debug, Clone)]
pub struct ChunkEmbedding {
    pub chunk_id: i64,
    pub content_hash: String,
    pub vector: Vec<f32>,
}

/// Semantic-channel liveness counters (V3 observable degrade): what the
/// cosine channel could actually rank for a query, so callers can say
/// "semantic:on" only when it is true.
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
pub struct EmbeddingStats {
    /// Vectors for the model attached to currently indexed content (the
    /// same liveness join as the cosine channel), any dims.
    pub stored: usize,
    /// The subset of `stored` whose dims match the query vector — the rows
    /// cosine similarity can rank. `stored > 0 && live == 0` means every
    /// stored vector was skipped (a model dims change).
    pub live: usize,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct EmbedStoreReport {
    pub stored: usize,
    /// Chunk ids rejected: unknown id (chunk replaced/deleted since
    /// pending_chunks) or content_hash mismatch. Staleness is expected under
    /// concurrent edits — success, not error; the caller just re-syncs.
    pub stale: Vec<i64>,
}

impl CodeIndex {
    /// Chunks lacking an embedding for `model_id`, ascending `chunk_id`, at
    /// most `limit`. Second tuple element: total pending count.
    pub fn pending_chunks(
        &self,
        model_id: &str,
        limit: usize,
    ) -> Result<(Vec<PendingChunk>, usize), IndexError> {
        // Pending means "no vector for this model computed from the file
        // version currently indexed": a hash-mismatched row (impossible under
        // cascade, but belt and braces) counts as pending too.
        const PENDING_WHERE: &str = "NOT EXISTS (
                 SELECT 1 FROM embeddings e
                 WHERE e.chunk_id = c.id
                   AND e.model_id = ?1
                   AND e.content_hash = f.content_hash
               )";
        let conn = &self.store.conn;
        let total: i64 = conn.query_row(
            &format!(
                "SELECT COUNT(*) FROM chunks c
                 JOIN files f ON f.path = c.path
                 WHERE {PENDING_WHERE}"
            ),
            params![model_id],
            |row| row.get(0),
        )?;
        let mut stmt = conn.prepare(&format!(
            "SELECT c.id, c.path, c.start_line, c.end_line, c.content, f.content_hash
             FROM chunks c
             JOIN files f ON f.path = c.path
             WHERE {PENDING_WHERE}
             ORDER BY c.id
             LIMIT ?2"
        ))?;
        let chunks = stmt
            .query_map(params![model_id, limit as i64], |row| {
                Ok(PendingChunk {
                    chunk_id: row.get(0)?,
                    path: row.get(1)?,
                    start_line: row.get(2)?,
                    end_line: row.get(3)?,
                    content: row.get(4)?,
                    content_hash: row.get(5)?,
                })
            })?
            .collect::<Result<_, _>>()?;
        Ok((chunks, total as usize))
    }

    /// Liveness counters for the cosine channel: how many stored vectors for
    /// `model_id` are attached to currently indexed content, and how many of
    /// those a `query_dims`-dimensional query could actually rank. Pure
    /// counting — releases no content; callers use it to keep degrade paths
    /// observable (V3).
    pub fn embedding_stats(
        &self,
        model_id: &str,
        query_dims: usize,
    ) -> Result<EmbeddingStats, IndexError> {
        let (stored, live): (i64, i64) = self.store.conn.query_row(
            "SELECT COUNT(*),
                    COALESCE(SUM(CASE WHEN e.dims = ?2 THEN 1 ELSE 0 END), 0)
             FROM embeddings e
             JOIN chunks c ON c.id = e.chunk_id
             JOIN files f ON f.path = c.path AND f.content_hash = e.content_hash
             WHERE e.model_id = ?1",
            params![model_id, query_dims as i64],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )?;
        Ok(EmbeddingStats {
            stored: stored as usize,
            live: live as usize,
        })
    }

    /// Upserts vectors (INSERT OR REPLACE) inside one transaction. All
    /// vectors must share one dims; a dims mismatch within the batch, an
    /// empty vector, or a non-finite (NaN/±Inf) component is InvalidInput.
    /// Unknown chunk_id / stale content_hash rows are skipped into `stale`.
    pub fn embed_store(
        &mut self,
        model_id: &str,
        items: &[ChunkEmbedding],
    ) -> Result<EmbedStoreReport, IndexError> {
        let Some(first) = items.first() else {
            return Ok(EmbedStoreReport {
                stored: 0,
                stale: Vec::new(),
            });
        };
        // Validate the whole batch before touching any rows: a rejected batch
        // must not poison the store.
        let dims = first.vector.len();
        if dims == 0 {
            return Err(IndexError::InvalidInput(
                "embedding vector must not be empty".to_string(),
            ));
        }
        for item in items {
            if item.vector.len() != dims {
                return Err(IndexError::InvalidInput(format!(
                    "mixed dims within one batch: {} vs {} (chunk {})",
                    item.vector.len(),
                    dims,
                    item.chunk_id
                )));
            }
            if let Some(bad) = item.vector.iter().find(|v| !v.is_finite()) {
                return Err(IndexError::InvalidInput(format!(
                    "non-finite embedding component {bad} (chunk {})",
                    item.chunk_id
                )));
            }
        }
        let tx = self.store.conn.transaction()?;
        let mut stored = 0usize;
        let mut stale: Vec<i64> = Vec::new();
        {
            let mut current_hash = tx.prepare(
                "SELECT f.content_hash FROM chunks c
                 JOIN files f ON f.path = c.path
                 WHERE c.id = ?1",
            )?;
            let mut upsert = tx.prepare(
                "INSERT OR REPLACE INTO embeddings (chunk_id, model_id, dims, vector, content_hash)
                 VALUES (?1, ?2, ?3, ?4, ?5)",
            )?;
            for item in items {
                let current: Option<String> = current_hash
                    .query_row([item.chunk_id], |row| row.get(0))
                    .optional()?;
                if current.as_deref() == Some(item.content_hash.as_str()) {
                    upsert.execute(params![
                        item.chunk_id,
                        model_id,
                        dims as i64,
                        f32_to_le_blob(&item.vector),
                        item.content_hash
                    ])?;
                    stored += 1;
                } else {
                    // Unknown chunk id or content changed since pending_chunks:
                    // expected staleness, never an error.
                    stale.push(item.chunk_id);
                }
            }
        }
        tx.commit()?;
        Ok(EmbedStoreReport { stored, stale })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{sha256_hex, IngestFile};

    const MODEL_A: &str = "openai/text-embedding-3-small";
    const MODEL_B: &str = "voyage/voyage-code-3";

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

    fn embed_all(idx: &mut CodeIndex, model_id: &str, vector: &[f32]) -> usize {
        let (pending, _total) = idx.pending_chunks(model_id, 1000).expect("pending_chunks");
        let items: Vec<ChunkEmbedding> = pending
            .iter()
            .map(|p| ChunkEmbedding {
                chunk_id: p.chunk_id,
                content_hash: p.content_hash.clone(),
                vector: vector.to_vec(),
            })
            .collect();
        let report = idx.embed_store(model_id, &items).expect("embed_store");
        assert!(report.stale.is_empty(), "fresh chunks must not be stale: {report:?}");
        report.stored
    }

    #[test]
    fn pending_chunks_lists_unembedded_chunks_ascending_with_content() {
        let content_a = "pub fn zz_pending_a() {}\n";
        let content_b = "pub fn zz_pending_b() {}\n";
        let idx = fixture_index(&[("a.rs", content_a), ("b.rs", content_b)]);
        let (pending, total) = idx.pending_chunks(MODEL_A, 100).expect("pending_chunks");
        assert!(pending.len() >= 2, "both files' chunks must be pending: {pending:?}");
        assert_eq!(total, pending.len(), "under the limit total == returned");
        let ids: Vec<i64> = pending.iter().map(|p| p.chunk_id).collect();
        let mut sorted = ids.clone();
        sorted.sort_unstable();
        assert_eq!(ids, sorted, "pending chunks must come in ascending chunk_id order");
        let a = pending
            .iter()
            .find(|p| p.path == "a.rs")
            .expect("a.rs chunk pending");
        assert!(a.content.contains("zz_pending_a"), "got {a:?}");
        assert_eq!(a.content_hash, sha256_hex(content_a));
        assert!(a.start_line >= 1);
        assert!(a.end_line >= a.start_line);
    }

    #[test]
    fn pending_chunks_respects_limit_and_reports_full_total() {
        let idx = fixture_index(&[
            ("a.rs", "pub fn zz_limit_a() {}\n"),
            ("b.rs", "pub fn zz_limit_b() {}\n"),
            ("c.rs", "pub fn zz_limit_c() {}\n"),
        ]);
        let (all, total_all) = idx.pending_chunks(MODEL_A, 1000).expect("pending all");
        assert!(total_all >= 3);
        let (limited, total_limited) = idx.pending_chunks(MODEL_A, 1).expect("pending limited");
        assert_eq!(limited.len(), 1, "limit must cap the returned batch");
        assert_eq!(
            total_limited, total_all,
            "total_pending reports the full backlog, not the batch"
        );
        assert_eq!(
            limited[0].chunk_id, all[0].chunk_id,
            "batches start at the lowest pending chunk_id"
        );
    }

    #[test]
    fn embed_store_then_pending_excludes_embedded_chunks() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_embedded_fn() {}\n")]);
        let stored = embed_all(&mut idx, MODEL_A, &[1.0, 0.0]);
        assert!(stored >= 1, "at least the one chunk must store");
        let (pending, total) = idx.pending_chunks(MODEL_A, 100).expect("pending after store");
        assert!(pending.is_empty(), "embedded chunks must leave pending: {pending:?}");
        assert_eq!(total, 0);
    }

    #[test]
    fn pending_chunks_is_per_model_independent() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_per_model_fn() {}\n")]);
        embed_all(&mut idx, MODEL_A, &[1.0, 0.0]);
        let (pending_b, total_b) = idx.pending_chunks(MODEL_B, 100).expect("pending model B");
        assert!(
            !pending_b.is_empty(),
            "another model_id must still see every chunk pending"
        );
        assert_eq!(total_b, pending_b.len());
        let (pending_a, _) = idx.pending_chunks(MODEL_A, 100).expect("pending model A");
        assert!(pending_a.is_empty(), "model A stays fully embedded");
    }

    #[test]
    fn embed_store_reports_unknown_and_hash_mismatched_chunks_as_stale() {
        let content = "pub fn zz_stale_report_fn() {}\n";
        let mut idx = fixture_index(&[("a.rs", content)]);
        let (pending, _) = idx.pending_chunks(MODEL_A, 100).expect("pending_chunks");
        let good = pending[0].clone();
        let items = vec![
            ChunkEmbedding {
                chunk_id: good.chunk_id,
                content_hash: good.content_hash.clone(),
                vector: vec![1.0, 0.0],
            },
            ChunkEmbedding {
                chunk_id: 999_999, // never existed
                content_hash: good.content_hash.clone(),
                vector: vec![0.0, 1.0],
            },
            ChunkEmbedding {
                chunk_id: good.chunk_id,
                content_hash: "0000000000000000000000000000000000000000000000000000000000000000"
                    .to_string(), // wrong hash: vector computed from other content
                vector: vec![0.5, 0.5],
            },
        ];
        let report = idx.embed_store(MODEL_A, &items).expect("embed_store");
        assert_eq!(report.stored, 1, "only the hash-matching known chunk stores: {report:?}");
        assert!(report.stale.contains(&999_999), "unknown id is stale: {report:?}");
        // The mismatched-hash row for the good chunk id is stale too — but the
        // matching row already stored, so the id appears in stale exactly once
        // for the mismatched item.
        assert_eq!(report.stale.len(), 2, "got {report:?}");
    }

    #[test]
    fn embed_store_rejects_empty_vectors_and_mixed_dims() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_dims_fn() {}\n")]);
        let (pending, _) = idx.pending_chunks(MODEL_A, 100).expect("pending_chunks");
        let chunk = pending[0].clone();

        let empty = idx.embed_store(
            MODEL_A,
            &[ChunkEmbedding {
                chunk_id: chunk.chunk_id,
                content_hash: chunk.content_hash.clone(),
                vector: Vec::new(),
            }],
        );
        assert!(
            matches!(empty, Err(IndexError::InvalidInput(_))),
            "empty vector must be InvalidInput, got {empty:?}"
        );

        let mixed = idx.embed_store(
            MODEL_A,
            &[
                ChunkEmbedding {
                    chunk_id: chunk.chunk_id,
                    content_hash: chunk.content_hash.clone(),
                    vector: vec![1.0, 0.0],
                },
                ChunkEmbedding {
                    chunk_id: chunk.chunk_id,
                    content_hash: chunk.content_hash.clone(),
                    vector: vec![1.0, 0.0, 0.0],
                },
            ],
        );
        assert!(
            matches!(mixed, Err(IndexError::InvalidInput(_))),
            "mixed dims within one batch must be InvalidInput, got {mixed:?}"
        );

        // A well-formed batch on the same index still succeeds afterwards
        // (validation must reject before touching rows, not poison the store).
        let ok = idx
            .embed_store(
                MODEL_A,
                &[ChunkEmbedding {
                    chunk_id: chunk.chunk_id,
                    content_hash: chunk.content_hash.clone(),
                    vector: vec![1.0, 0.0],
                }],
            )
            .expect("valid batch after rejected batches");
        assert_eq!(ok.stored, 1);
    }

    #[test]
    fn embed_store_rejects_non_finite_components() {
        // V3 deferred minor D1: the store is the validation chokepoint —
        // NaN/±Inf components are a param error and must never be stored.
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_finite_fn() {}\n")]);
        let (pending, _) = idx.pending_chunks(MODEL_A, 100).expect("pending_chunks");
        let chunk = pending[0].clone();

        for bad in [f32::NAN, f32::INFINITY, f32::NEG_INFINITY] {
            let result = idx.embed_store(
                MODEL_A,
                &[
                    ChunkEmbedding {
                        chunk_id: chunk.chunk_id,
                        content_hash: chunk.content_hash.clone(),
                        vector: vec![1.0, 0.0],
                    },
                    ChunkEmbedding {
                        chunk_id: chunk.chunk_id,
                        content_hash: chunk.content_hash.clone(),
                        vector: vec![0.5, bad],
                    },
                ],
            );
            assert!(
                matches!(result, Err(IndexError::InvalidInput(_))),
                "non-finite component ({bad}) must be InvalidInput, got {result:?}"
            );
        }

        // Whole-batch validation happens before any row is touched: the
        // finite sibling of a rejected batch must not have stored either.
        let (pending_after, _) = idx.pending_chunks(MODEL_A, 100).expect("pending after rejects");
        assert!(
            pending_after.iter().any(|p| p.chunk_id == chunk.chunk_id),
            "the chunk must still be pending after rejected batches: {pending_after:?}"
        );

        // A well-formed batch on the same index still succeeds afterwards.
        let ok = idx
            .embed_store(
                MODEL_A,
                &[ChunkEmbedding {
                    chunk_id: chunk.chunk_id,
                    content_hash: chunk.content_hash.clone(),
                    vector: vec![1.0, 0.0],
                }],
            )
            .expect("valid batch after rejected batches");
        assert_eq!(ok.stored, 1);
    }

    #[test]
    fn embedding_stats_report_live_and_dims_mismatched_vectors() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_stats_fn() {}\n")]);
        // Nothing stored yet: both counters are zero for any dims.
        assert_eq!(
            idx.embedding_stats(MODEL_A, 2).expect("stats"),
            EmbeddingStats { stored: 0, live: 0 }
        );

        let stored = embed_all(&mut idx, MODEL_A, &[1.0, 0.0]);
        assert!(stored >= 1);
        let stats = idx.embedding_stats(MODEL_A, 2).expect("stats");
        assert_eq!(stats.stored, stored, "every stored vector is live content");
        assert_eq!(stats.live, stored, "matching dims: cosine can rank them all");

        // A 3-dim query cannot rank 2-dim vectors: stored stays, live drops
        // to zero — the observable dims-mismatch signal.
        let mismatched = idx.embedding_stats(MODEL_A, 3).expect("stats");
        assert_eq!(mismatched.stored, stored);
        assert_eq!(mismatched.live, 0);

        // Another model id sees nothing.
        assert_eq!(
            idx.embedding_stats(MODEL_B, 2).expect("stats"),
            EmbeddingStats { stored: 0, live: 0 }
        );
    }

    #[test]
    fn embedding_stats_exclude_stale_vectors_after_reingest() {
        // The liveness join matches the cosine channel: a vector whose
        // content_hash no longer matches the indexed file must not count.
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_stats_old() {}\n")]);
        embed_all(&mut idx, MODEL_A, &[1.0, 0.0]);
        assert!(idx.embedding_stats(MODEL_A, 2).expect("stats").live >= 1);
        idx.ingest(&[IngestFile {
            path: "a.rs".into(),
            content: "pub fn zz_stats_new() {}\n".into(),
        }])
        .expect("re-ingest");
        assert_eq!(
            idx.embedding_stats(MODEL_A, 2).expect("stats"),
            EmbeddingStats { stored: 0, live: 0 },
            "stale vectors are structurally dead for the channel and its stats"
        );
    }

    #[test]
    fn reingested_file_chunks_become_pending_again() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_reingest_old() {}\n")]);
        embed_all(&mut idx, MODEL_A, &[1.0, 0.0]);
        assert_eq!(idx.pending_chunks(MODEL_A, 100).expect("pending").1, 0);

        idx.ingest(&[IngestFile {
            path: "a.rs".into(),
            content: "pub fn zz_reingest_new() {}\n".into(),
        }])
        .expect("re-ingest changed content");

        let (pending, total) = idx.pending_chunks(MODEL_A, 100).expect("pending after change");
        assert!(
            total >= 1,
            "replaced chunks must be pending again for the same model"
        );
        assert!(
            pending.iter().any(|p| p.content.contains("zz_reingest_new")),
            "the new content is what needs embedding: {pending:?}"
        );
        assert!(
            pending.iter().all(|p| !p.content.contains("zz_reingest_old")),
            "old chunk text must be gone: {pending:?}"
        );
    }

    #[test]
    fn embed_store_after_reingest_reports_old_chunk_stale() {
        let mut idx = fixture_index(&[("a.rs", "pub fn zz_race_old() {}\n")]);
        let (pending, _) = idx.pending_chunks(MODEL_A, 100).expect("pending_chunks");
        let old = pending[0].clone();
        // The file changes between pending_chunks and embed_store (the
        // ingest-during-embed race): the echoed content_hash closes it.
        idx.ingest(&[IngestFile {
            path: "a.rs".into(),
            content: "pub fn zz_race_new() {}\n".into(),
        }])
        .expect("re-ingest");
        let report = idx
            .embed_store(
                MODEL_A,
                &[ChunkEmbedding {
                    chunk_id: old.chunk_id,
                    content_hash: old.content_hash,
                    vector: vec![1.0, 0.0],
                }],
            )
            .expect("embed_store");
        assert_eq!(report.stored, 0, "a raced vector must never store: {report:?}");
        assert_eq!(report.stale, vec![old.chunk_id]);
    }
}
