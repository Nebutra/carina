//! Governed code intelligence (docs/plans/code-intelligence.md).
//!
//! The index is a *derived artifact of files the caller is allowed to read*:
//! ingestion takes an explicit allowlist of `(path, content)` pairs and this
//! crate never opens files or walks the filesystem itself. Invalidation is
//! caller-driven (patch apply / rollback outcomes), keyed by `content_hash`.
//!
//! Query surfaces: keyword search (FTS5 BM25 + exact match fused with RRF),
//! symbol lookup (tree-sitter approximate def/refs), and an Aider-style
//! PageRank repo map rendered within a token budget.

mod chunker;
mod embed;
mod extract;
mod impact;
mod lang;
mod repomap;
mod search;
mod store;
mod symbols;

pub use embed::{ChunkEmbedding, EmbedStoreReport, EmbeddingStats, PendingChunk};
pub use extract::{SymbolKind, SymbolRecord};
pub use impact::{ImpactOptions, ImpactReport, ImpactedSymbol};
pub use lang::Lang;
pub use repomap::{RankedSymbol, RepoMap, RepoMapOptions};
pub use search::{SearchHit, SearchOptions};
pub use store::{EdgeSpec, EdgesStoreReport, SkippedEdge};
pub use symbols::{ReferenceSite, SymbolOptions, SymbolReport};

use std::path::Path;

use sha2::{Digest, Sha256};
use time::format_description::well_known::Rfc3339;
use time::OffsetDateTime;

/// Symbol chunks larger than this are split (each piece keeps its symbol link).
const MAX_CHUNK_LINES: usize = 120;

#[derive(Debug, thiserror::Error)]
pub enum IndexError {
    #[error("sqlite: {0}")]
    Sqlite(#[from] rusqlite::Error),
    #[error("parse error in {path}: {message}")]
    Parse { path: String, message: String },
    #[error("invalid input: {0}")]
    InvalidInput(String),
}

/// One file the caller has ALREADY read under FileRead policy. The crate
/// never touches the filesystem: paths are workspace-relative identifiers.
#[derive(Debug, Clone)]
pub struct IngestFile {
    pub path: String,
    pub content: String,
}

/// A caller-reported change (patch apply / rollback outcome).
#[derive(Debug, Clone)]
pub enum FileChange {
    Upsert { path: String, content: String },
    Delete { path: String },
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct IngestReport {
    /// Files (re)ingested.
    pub indexed: usize,
    /// Files whose `content_hash` matched; skipped as a no-op.
    pub unchanged: usize,
    /// Unsupported language / parse failure.
    pub skipped: Vec<SkippedFile>,
    pub symbols: usize,
    pub edges: usize,
    pub chunks: usize,
}

impl IngestReport {
    fn empty() -> IngestReport {
        IngestReport {
            indexed: 0,
            unchanged: 0,
            skipped: Vec::new(),
            symbols: 0,
            edges: 0,
            chunks: 0,
        }
    }
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct SkippedFile {
    pub path: String,
    pub reason: String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct IndexStats {
    pub files: usize,
    pub symbols: usize,
    pub edges: usize,
    pub chunks: usize,
}

pub struct CodeIndex {
    store: store::Store,
}

impl CodeIndex {
    /// Opens (or creates + migrates) the index database at `db_path`.
    pub fn open(db_path: &Path) -> Result<Self, IndexError> {
        Ok(CodeIndex {
            store: store::Store::open(db_path)?,
        })
    }

    /// In-memory index for tests.
    pub fn in_memory() -> Result<Self, IndexError> {
        Ok(CodeIndex {
            store: store::Store::in_memory()?,
        })
    }

    /// Ingests exactly the given files (the policy-scoped allowlist).
    /// Files whose `content_hash` is unchanged are skipped.
    pub fn ingest(&mut self, files: &[IngestFile]) -> Result<IngestReport, IndexError> {
        // Reject the whole batch before touching any rows: an allowlist with
        // an escaping path is a caller bug, not a per-file skip.
        for file in files {
            validate_path(&file.path)?;
        }
        let mut report = IngestReport::empty();
        for file in files {
            self.ingest_one(&file.path, &file.content, &mut report)?;
        }
        Ok(report)
    }

    /// Applies caller-reported changes: drop + re-ingest upserts, drop deletes.
    pub fn update(&mut self, changes: &[FileChange]) -> Result<IngestReport, IndexError> {
        for change in changes {
            let (FileChange::Upsert { path, .. } | FileChange::Delete { path }) = change;
            validate_path(path)?;
        }
        let mut report = IngestReport::empty();
        for change in changes {
            match change {
                FileChange::Upsert { path, content } => {
                    self.ingest_one(path, content, &mut report)?;
                }
                FileChange::Delete { path } => {
                    self.store.delete_file(path)?;
                }
            }
        }
        Ok(report)
    }

    /// Drops and re-ingests one file unless its `content_hash` is unchanged.
    fn ingest_one(
        &mut self,
        path: &str,
        content: &str,
        report: &mut IngestReport,
    ) -> Result<(), IndexError> {
        let Some(lang) = Lang::from_path(path) else {
            report.skipped.push(SkippedFile {
                path: path.to_string(),
                reason: "unsupported language".to_string(),
            });
            return Ok(());
        };
        let content_hash = sha256_hex(content);
        if self.store.file_hash(path)?.as_deref() == Some(content_hash.as_str()) {
            report.unchanged += 1;
            return Ok(());
        }
        let extraction = match extract::extract(lang, path, content) {
            Ok(extraction) => extraction,
            Err(IndexError::Parse { path, message }) => {
                report.skipped.push(SkippedFile {
                    path,
                    reason: message,
                });
                return Ok(());
            }
            Err(err) => return Err(err),
        };
        let chunks = chunker::chunk(content, &extraction.symbols, MAX_CHUNK_LINES);
        let counts = self.store.replace_file(
            &store::FileRow {
                path: path.to_string(),
                content_hash,
                lang,
                indexed_at: now_rfc3339(),
            },
            &extraction.symbols,
            &extraction.references,
            &chunks,
        )?;
        report.indexed += 1;
        report.symbols += counts.symbols;
        report.edges += counts.edges;
        report.chunks += counts.chunks;
        Ok(())
    }

    /// Keyword search: FTS5 BM25 + exact substring match, fused with RRF (k=60).
    pub fn search(&self, query: &str, opts: &SearchOptions) -> Result<Vec<SearchHit>, IndexError> {
        search::run(&self.store, query, opts)
    }

    /// Definitions and approximate references for a symbol name.
    pub fn symbol_lookup(&self, name: &str, opts: &SymbolOptions) -> Result<SymbolReport, IndexError> {
        symbols::lookup(&self.store, name, opts)
    }

    /// Bounded, deterministic transitive dependents of a symbol name
    /// (docs/plans/code-intelligence.md V3 impact analysis).
    pub fn impact(&self, name: &str, opts: &ImpactOptions) -> Result<ImpactReport, IndexError> {
        impact::run(&self.store, name, opts)
    }

    /// Persists LSP-sourced precise edges (docs/plans/code-intelligence.md
    /// V4 §B): each endpoint resolves to the smallest enclosing indexed
    /// symbol; resolved pairs upsert with confidence 1.0, `source = 'lsp'`
    /// (the storage-level dedup prefers 'lsp' per (src, dst, edge_type)).
    /// Unresolvable endpoints and self edges are skipped, never an error.
    pub fn edges_store(&mut self, edges: &[EdgeSpec]) -> Result<EdgesStoreReport, IndexError> {
        self.store.edges_store(edges)
    }

    /// Aider-style repo map: PageRank over edges, rendered within a token budget.
    pub fn repo_map(&self, opts: &RepoMapOptions) -> Result<RepoMap, IndexError> {
        repomap::build(&self.store, opts)
    }

    /// Row counts for diagnostics / build results.
    pub fn stats(&self) -> Result<IndexStats, IndexError> {
        self.store.stats()
    }

    /// Every indexed file path, sorted — callers reconcile a full build
    /// against it so vanished/denied paths do not stay queryable.
    pub fn indexed_paths(&self) -> Result<Vec<String>, IndexError> {
        self.store.indexed_paths()
    }
}

/// Paths are workspace-relative identifiers: reject anything that could name
/// a file outside the caller's policy-scoped allowlist.
fn validate_path(path: &str) -> Result<(), IndexError> {
    if path.is_empty() {
        return Err(IndexError::InvalidInput("empty path".to_string()));
    }
    if Path::new(path).is_absolute() || path.starts_with('/') {
        return Err(IndexError::InvalidInput(format!(
            "absolute path not allowed: {path}"
        )));
    }
    if path.split('/').any(|component| component == "..") {
        return Err(IndexError::InvalidInput(format!(
            "parent traversal not allowed: {path}"
        )));
    }
    Ok(())
}

pub(crate) fn sha256_hex(content: &str) -> String {
    let mut hasher = Sha256::new();
    hasher.update(content.as_bytes());
    format!("{:x}", hasher.finalize())
}

fn now_rfc3339() -> String {
    OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .unwrap_or_else(|_| String::new())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ingest_files(idx: &mut CodeIndex, files: &[(&str, &str)]) -> IngestReport {
        let batch: Vec<IngestFile> = files
            .iter()
            .map(|(path, content)| IngestFile {
                path: (*path).to_string(),
                content: (*content).to_string(),
            })
            .collect();
        idx.ingest(&batch).expect("ingest")
    }

    fn fixture_index(files: &[(&str, &str)]) -> CodeIndex {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        ingest_files(&mut idx, files);
        idx
    }

    #[test]
    fn ingest_reports_indexed_counts() {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        let report = ingest_files(
            &mut idx,
            &[
                ("a.rs", "pub fn alpha_one() {}\n"),
                ("b.rs", "pub fn beta_one() {\n    alpha_one();\n}\n"),
            ],
        );
        assert_eq!(report.indexed, 2);
        assert_eq!(report.unchanged, 0);
        assert!(report.skipped.is_empty());
        assert_eq!(report.symbols, 2);
        assert!(report.chunks >= 2);
        assert!(report.edges >= 1, "beta_one references alpha_one");
    }

    #[test]
    fn ingest_resolves_references_regardless_of_order() {
        // The referencing file arrives before its definition: the edge must
        // still exist once the definition lands (ingest order is caller
        // controlled — scanner order must not decide graph quality).
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        ingest_files(
            &mut idx,
            &[
                ("b.rs", "pub fn beta_one() {\n    alpha_one();\n}\n"),
                ("a.rs", "pub fn alpha_one() {}\n"),
            ],
        );
        assert_eq!(
            idx.stats().expect("stats").edges,
            1,
            "reference ingested before its definition must still become an edge"
        );
    }

    #[test]
    fn update_preserves_incoming_edges_from_unchanged_files() {
        let mut idx = fixture_index(&[
            ("a.rs", "pub fn central() {}\n"),
            ("b.rs", "pub fn caller_one() {\n    central();\n}\n"),
            ("c.rs", "pub fn caller_two() {\n    central();\n}\n"),
        ]);
        assert_eq!(idx.stats().expect("stats").edges, 2);
        // A comment-only change to the definition file must not orphan the
        // references from the unchanged callers.
        idx.update(&[FileChange::Upsert {
            path: "a.rs".into(),
            content: "// hot file\npub fn central() {}\n".into(),
        }])
        .expect("update");
        assert_eq!(
            idx.stats().expect("stats").edges,
            2,
            "incoming cross-file edges must survive invalidation"
        );
        let map = idx
            .repo_map(&RepoMapOptions {
                token_budget: 10_000,
                ..RepoMapOptions::default()
            })
            .expect("repo map");
        assert!(
            map.ranked[0].qualified_name.contains("central"),
            "centrality must survive a re-ingest of the definition file: {:?}",
            map.ranked[0]
        );
    }

    #[test]
    fn reingest_unchanged_content_is_noop() {
        let files = vec![IngestFile {
            path: "a.rs".into(),
            content: "pub fn alpha_one() {}\n".into(),
        }];
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        let first = idx.ingest(&files).expect("first ingest");
        assert_eq!(first.indexed, 1);
        let second = idx.ingest(&files).expect("second ingest");
        assert_eq!(second.indexed, 0);
        assert_eq!(second.unchanged, 1);
        let stats = idx.stats().expect("stats");
        assert_eq!(stats.files, 1);
        assert_eq!(stats.symbols, 1);
    }

    #[test]
    fn reingest_changed_content_reindexes() {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        ingest_files(&mut idx, &[("a.rs", "pub fn old_name_fn() {}\n")]);
        let report = ingest_files(&mut idx, &[("a.rs", "pub fn new_name_fn() {}\n")]);
        assert_eq!(report.indexed, 1);
        assert_eq!(report.unchanged, 0);
        let old = idx
            .symbol_lookup("old_name_fn", &SymbolOptions::default())
            .expect("lookup old");
        assert!(old.definitions.is_empty(), "stale symbol must be dropped");
        let new = idx
            .symbol_lookup("new_name_fn", &SymbolOptions::default())
            .expect("lookup new");
        assert_eq!(new.definitions.len(), 1);
    }

    #[test]
    fn ingest_skips_unsupported_languages() {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        let report = ingest_files(&mut idx, &[("README.md", "# hello\n")]);
        assert_eq!(report.indexed, 0);
        assert_eq!(report.skipped.len(), 1);
        assert_eq!(report.skipped[0].path, "README.md");
        assert!(!report.skipped[0].reason.is_empty());
        assert_eq!(idx.stats().expect("stats").files, 0);
    }

    #[test]
    fn update_upsert_replaces_only_changed_paths() {
        let mut idx = fixture_index(&[
            ("a.rs", "pub fn alpha_one() {}\n"),
            ("b.rs", "pub fn beta_one() {}\n"),
        ]);
        let report = idx
            .update(&[FileChange::Upsert {
                path: "a.rs".into(),
                content: "pub fn alpha_two() {}\n".into(),
            }])
            .expect("update");
        assert_eq!(report.indexed, 1);
        let gone = idx
            .symbol_lookup("alpha_one", &SymbolOptions::default())
            .expect("lookup");
        assert!(gone.definitions.is_empty());
        let replaced = idx
            .symbol_lookup("alpha_two", &SymbolOptions::default())
            .expect("lookup");
        assert_eq!(replaced.definitions.len(), 1);
        let untouched = idx
            .symbol_lookup("beta_one", &SymbolOptions::default())
            .expect("lookup");
        assert_eq!(untouched.definitions.len(), 1, "b.rs must be untouched");
    }

    #[test]
    fn update_delete_drops_file_rows() {
        let mut idx = fixture_index(&[
            ("a.rs", "pub fn alpha_one() {}\n"),
            ("b.rs", "pub fn beta_one() {}\n"),
        ]);
        idx.update(&[FileChange::Delete { path: "b.rs".into() }])
            .expect("update");
        let stats = idx.stats().expect("stats");
        assert_eq!(stats.files, 1);
        let report = idx
            .symbol_lookup("beta_one", &SymbolOptions::default())
            .expect("lookup");
        assert!(report.definitions.is_empty());
        let hits = idx
            .search("beta_one", &SearchOptions::default())
            .expect("search");
        assert!(hits.is_empty(), "deleted file must leave no search hits");
    }

    #[test]
    fn ingest_rejects_parent_traversal_paths() {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        let err = idx
            .ingest(&[IngestFile {
                path: "../outside/evil.rs".into(),
                content: "pub fn evil() {}\n".into(),
            }])
            .expect_err("traversal path must be rejected");
        assert!(matches!(err, IndexError::InvalidInput(_)), "got {err:?}");
    }

    #[test]
    fn ingest_rejects_absolute_paths() {
        let mut idx = CodeIndex::in_memory().expect("in-memory index");
        let err = idx
            .ingest(&[IngestFile {
                path: "/etc/evil.rs".into(),
                content: "pub fn evil() {}\n".into(),
            }])
            .expect_err("absolute path must be rejected");
        assert!(matches!(err, IndexError::InvalidInput(_)), "got {err:?}");
    }

    #[test]
    fn ingest_uses_only_caller_supplied_content() {
        // "src/lib.rs" exists on disk with entirely different content; the
        // crate must index only what the caller hands it, never the file.
        let idx = fixture_index(&[("src/lib.rs", "pub fn phantom_marker_zz() {}\n")]);
        let hits = idx
            .search("phantom_marker_zz", &SearchOptions::default())
            .expect("search");
        assert!(!hits.is_empty(), "caller-supplied content must be indexed");
        let leaked = idx
            .search("IndexError", &SearchOptions::default())
            .expect("search");
        assert!(
            leaked.is_empty(),
            "on-disk content must never leak into the index"
        );
        assert_eq!(idx.stats().expect("stats").files, 1);
    }

    #[test]
    fn search_finds_exact_identifier() {
        let idx = fixture_index(&[("a.rs", "pub fn zz_unique_marker() {}\n")]);
        let hits = idx
            .search("zz_unique_marker", &SearchOptions::default())
            .expect("search");
        assert!(!hits.is_empty());
        let hit = &hits[0];
        assert_eq!(hit.path, "a.rs");
        assert!(hit.snippet.contains("zz_unique_marker"));
        assert!(hit.score > 0.0);
        assert!(!hit.sources.is_empty());
        for source in &hit.sources {
            assert!(source == "bm25" || source == "exact", "got {source}");
        }
    }

    #[test]
    fn search_respects_limit() {
        let idx = fixture_index(&[
            ("a.rs", "pub fn shared_token_zz_a() { shared_token_zz(); }\n"),
            ("b.rs", "pub fn shared_token_zz_b() { shared_token_zz(); }\n"),
            ("c.rs", "pub fn shared_token_zz_c() { shared_token_zz(); }\n"),
        ]);
        let opts = SearchOptions {
            limit: 2,
            ..SearchOptions::default()
        };
        let hits = idx.search("shared_token_zz", &opts).expect("search");
        assert!(hits.len() <= 2);
        assert!(!hits.is_empty());
    }

    #[test]
    fn search_filters_by_lang() {
        let idx = fixture_index(&[
            ("a.rs", "pub fn shared_token_zz() {}\n"),
            ("b.py", "def shared_token_zz():\n    pass\n"),
        ]);
        let opts = SearchOptions {
            lang: Some(Lang::Rust),
            ..SearchOptions::default()
        };
        let hits = idx.search("shared_token_zz", &opts).expect("search");
        assert!(!hits.is_empty());
        assert!(hits.iter().all(|h| h.path == "a.rs"), "got {hits:?}");
    }

    #[test]
    fn search_filters_by_path_prefix() {
        let idx = fixture_index(&[
            ("src/a.rs", "pub fn shared_token_zz() {}\n"),
            ("tests/b.rs", "pub fn shared_token_zz_test() { shared_token_zz(); }\n"),
        ]);
        let opts = SearchOptions {
            path_prefix: Some("src/".into()),
            ..SearchOptions::default()
        };
        let hits = idx.search("shared_token_zz", &opts).expect("search");
        assert!(!hits.is_empty());
        assert!(hits.iter().all(|h| h.path.starts_with("src/")), "got {hits:?}");
    }

    #[test]
    fn search_hits_carry_symbol_context() {
        let idx = fixture_index(&[("a.rs", "pub fn zz_symbol_ctx() {\n    let x = 1;\n}\n")]);
        let hits = idx
            .search("zz_symbol_ctx", &SearchOptions::default())
            .expect("search");
        assert!(!hits.is_empty());
        let symbol = hits[0].symbol.as_ref().expect("hit should carry its symbol");
        assert_eq!(symbol.name, "zz_symbol_ctx");
        assert_eq!(symbol.kind, SymbolKind::Function);
        assert_eq!(symbol.path, "a.rs");
    }

    #[test]
    fn results_carry_content_hash_and_indexed_at_provenance() {
        // Callers must be able to tell which file version a result reflects
        // (stale-result detection): every hit/definition carries the indexed
        // content_hash and indexed_at timestamp.
        let content = "pub fn zz_prov_fn() {}\n";
        let idx = fixture_index(&[("a.rs", content)]);
        let hits = idx
            .search("zz_prov_fn", &SearchOptions::default())
            .expect("search");
        assert!(!hits.is_empty());
        assert_eq!(hits[0].content_hash, sha256_hex(content));
        assert!(!hits[0].indexed_at.is_empty());
        let report = idx
            .symbol_lookup("zz_prov_fn", &SymbolOptions::default())
            .expect("lookup");
        assert_eq!(report.definitions[0].content_hash, sha256_hex(content));
        assert!(!report.definitions[0].indexed_at.is_empty());
    }

    #[test]
    fn stats_counts_all_tables() {
        let idx = fixture_index(&[(
            "a.rs",
            "pub fn helper_fn() {}\n\npub fn caller_fn() {\n    helper_fn();\n}\n",
        )]);
        let stats = idx.stats().expect("stats");
        assert_eq!(stats.files, 1);
        assert_eq!(stats.symbols, 2);
        assert!(stats.chunks >= 1);
        assert!(stats.edges >= 1);
    }

    #[test]
    fn open_persists_index_on_disk() {
        let db_path = std::env::temp_dir().join(format!(
            "carina-index-open-test-{}.sqlite",
            std::process::id()
        ));
        let _ = std::fs::remove_file(&db_path);
        {
            let mut idx = CodeIndex::open(&db_path).expect("open");
            ingest_files(&mut idx, &[("a.rs", "pub fn persisted_fn() {}\n")]);
        }
        {
            let idx = CodeIndex::open(&db_path).expect("reopen");
            let stats = idx.stats().expect("stats");
            assert_eq!(stats.files, 1);
            assert_eq!(stats.symbols, 1);
        }
        let _ = std::fs::remove_file(&db_path);
        let _ = std::fs::remove_file(db_path.with_extension("sqlite-wal"));
        let _ = std::fs::remove_file(db_path.with_extension("sqlite-shm"));
    }
}
