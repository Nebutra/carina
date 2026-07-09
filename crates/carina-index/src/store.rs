//! SQLite storage: open/migrate (WAL, foreign_keys ON, user_version = 3)
//! and transactional row writes.
//!
//! Tables: `files` (path PK), `symbols` / `refs` / `edges` / `chunks` (FK
//! CASCADE to files/symbols), `embeddings` (FK CASCADE to chunks, keyed
//! `(chunk_id, model_id)` — vectors die with their chunks), plus the
//! external-content FTS5 table `chunks_fts` kept in sync by AFTER
//! INSERT/DELETE triggers. Replacing a
//! changed file is DELETE (cascade drops symbols/refs/edges/chunks/FTS) +
//! re-insert inside one transaction. Raw name references are persisted in
//! `refs` so edges are *derived*: every name whose definition set is touched
//! by a replace/delete is re-resolved against all stored references with
//! confidence `1/n` fan-out — cross-file edges therefore never depend on
//! ingest order and survive invalidation of the defining file.

use std::collections::HashSet;
use std::path::Path;

use rusqlite::params;

use crate::chunker::Chunk;
use crate::extract::{PendingReference, PendingSymbol, SymbolKind};
use crate::lang::Lang;
use crate::{IndexError, IndexStats};

/// Schema version carried in `PRAGMA user_version`. The index is a derived
/// artifact: an unknown version is dropped and rebuilt, never migrated.
const SCHEMA_VERSION: i64 = 3;

const SCHEMA_DDL: &str = r#"
CREATE TABLE IF NOT EXISTS files (
  path         TEXT PRIMARY KEY,
  content_hash TEXT NOT NULL,
  lang         TEXT NOT NULL,
  indexed_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS symbols (
  id             INTEGER PRIMARY KEY,
  path           TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  qualified_name TEXT NOT NULL,
  kind           TEXT NOT NULL,
  start_line     INTEGER NOT NULL,
  end_line       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS symbols_by_name ON symbols(name);
CREATE INDEX IF NOT EXISTS symbols_by_path ON symbols(path);

CREATE TABLE IF NOT EXISTS refs (
  id     INTEGER PRIMARY KEY,
  src_id INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  name   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS refs_by_name ON refs(name);
CREATE INDEX IF NOT EXISTS refs_by_src ON refs(src_id);

CREATE TABLE IF NOT EXISTS edges (
  src_id     INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  dst_id     INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  edge_type  TEXT NOT NULL,
  confidence REAL NOT NULL,
  source     TEXT NOT NULL,
  PRIMARY KEY (src_id, dst_id, edge_type)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS chunks (
  id         INTEGER PRIMARY KEY,
  path       TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  symbol_id  INTEGER REFERENCES symbols(id) ON DELETE SET NULL,
  start_line INTEGER NOT NULL,
  end_line   INTEGER NOT NULL,
  content    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS chunks_by_path ON chunks(path);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
  content,
  path UNINDEXED,
  content = 'chunks',
  content_rowid = 'id',
  tokenize = "unicode61 tokenchars '_$'"
);
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
  INSERT INTO chunks_fts(rowid, content, path) VALUES (new.id, new.content, new.path);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
  INSERT INTO chunks_fts(chunks_fts, rowid, content, path)
  VALUES ('delete', old.id, old.content, old.path);
END;

CREATE TABLE IF NOT EXISTS embeddings (
  chunk_id     INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
  model_id     TEXT NOT NULL,
  dims         INTEGER NOT NULL,
  vector       BLOB NOT NULL,
  content_hash TEXT NOT NULL,
  PRIMARY KEY (chunk_id, model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS embeddings_by_model ON embeddings(model_id);
"#;

const DROP_DDL: &str = "
DROP TRIGGER IF EXISTS chunks_ad;
DROP TRIGGER IF EXISTS chunks_ai;
DROP TABLE IF EXISTS embeddings;
DROP TABLE IF EXISTS chunks_fts;
DROP TABLE IF EXISTS chunks;
DROP TABLE IF EXISTS edges;
DROP TABLE IF EXISTS refs;
DROP TABLE IF EXISTS symbols;
DROP TABLE IF EXISTS files;
";

/// DB encoding of `Lang` (matches the crate's serde lowercase form).
pub(crate) fn lang_to_str(lang: Lang) -> &'static str {
    match lang {
        Lang::Rust => "rust",
        Lang::Go => "go",
        Lang::TypeScript => "typescript",
        Lang::Python => "python",
    }
}

/// DB encoding of `SymbolKind` (matches the crate's serde snake_case form).
pub(crate) fn kind_to_str(kind: SymbolKind) -> &'static str {
    match kind {
        SymbolKind::Function => "function",
        SymbolKind::Method => "method",
        SymbolKind::Struct => "struct",
        SymbolKind::Enum => "enum",
        SymbolKind::Trait => "trait",
        SymbolKind::Interface => "interface",
        SymbolKind::Class => "class",
        SymbolKind::Const => "const",
        SymbolKind::TypeAlias => "type_alias",
        SymbolKind::Module => "module",
        SymbolKind::Variable => "variable",
    }
}

#[derive(Debug, Clone)]
pub(crate) struct FileRow {
    pub path: String,
    pub content_hash: String,
    pub lang: Lang,
    pub indexed_at: String,
}

#[derive(Debug, Clone, Copy)]
pub(crate) struct ReplaceCounts {
    pub symbols: usize,
    pub edges: usize,
    pub chunks: usize,
}

pub(crate) struct Store {
    pub(crate) conn: rusqlite::Connection,
}

impl Store {
    pub fn open(path: &Path) -> Result<Store, IndexError> {
        Store::init(rusqlite::Connection::open(path)?)
    }

    pub fn in_memory() -> Result<Store, IndexError> {
        Store::init(rusqlite::Connection::open_in_memory()?)
    }

    fn init(conn: rusqlite::Connection) -> Result<Store, IndexError> {
        // journal_mode returns the resulting mode as a row (":memory:"
        // databases stay on "memory"); read and discard it.
        conn.query_row("PRAGMA journal_mode = WAL", [], |_| Ok(()))?;
        conn.execute_batch(
            "PRAGMA foreign_keys = ON;
             PRAGMA recursive_triggers = ON;",
        )?;
        let version: i64 = conn.query_row("PRAGMA user_version", [], |row| row.get(0))?;
        if version != SCHEMA_VERSION {
            if version != 0 {
                // Unknown schema: the index is derived, drop and rebuild.
                conn.execute_batch(DROP_DDL)?;
            }
            conn.execute_batch(SCHEMA_DDL)?;
            conn.execute_batch(&format!("PRAGMA user_version = {SCHEMA_VERSION};"))?;
        }
        Ok(Store { conn })
    }

    pub fn file_hash(&self, path: &str) -> Result<Option<String>, IndexError> {
        use rusqlite::OptionalExtension;
        Ok(self
            .conn
            .query_row(
                "SELECT content_hash FROM files WHERE path = ?1",
                [path],
                |row| row.get(0),
            )
            .optional()?)
    }

    /// Replaces all rows for `file.path` in a single transaction and
    /// re-resolves the name-approximate edges the change can affect: the
    /// file's own outgoing references plus every stored reference to a name
    /// this file defined before or defines now (their `1/n` fan-out changes
    /// with the definition set).
    pub fn replace_file(
        &mut self,
        file: &FileRow,
        symbols: &[PendingSymbol],
        references: &[PendingReference],
        chunks: &[Chunk],
    ) -> Result<ReplaceCounts, IndexError> {
        let tx = self.conn.transaction()?;
        // Names whose definition set this replace touches (old + new defs);
        // their edges are rebuilt globally after the rows land.
        let mut names_to_resolve: HashSet<String> = {
            let mut stmt = tx.prepare("SELECT DISTINCT name FROM symbols WHERE path = ?1")?;
            let names = stmt
                .query_map([&file.path], |row| row.get(0))?
                .collect::<Result<_, _>>()?;
            names
        };
        names_to_resolve.extend(symbols.iter().map(|s| s.name.clone()));

        // The DELETE cascades symbols/refs/edges/chunks; triggers keep FTS in sync.
        tx.execute("DELETE FROM files WHERE path = ?1", [&file.path])?;
        tx.execute(
            "INSERT INTO files (path, content_hash, lang, indexed_at) VALUES (?1, ?2, ?3, ?4)",
            params![
                file.path,
                file.content_hash,
                lang_to_str(file.lang),
                file.indexed_at
            ],
        )?;

        let mut symbol_ids: Vec<i64> = Vec::with_capacity(symbols.len());
        {
            let mut insert = tx.prepare(
                "INSERT INTO symbols (path, name, qualified_name, kind, start_line, end_line)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
            )?;
            for symbol in symbols {
                insert.execute(params![
                    file.path,
                    symbol.name,
                    symbol.qualified_name,
                    kind_to_str(symbol.kind),
                    symbol.start_line,
                    symbol.end_line
                ])?;
                symbol_ids.push(tx.last_insert_rowid());
            }
        }

        // Persist raw references: edges are derived from them, so a reference
        // whose definition has not been ingested yet still resolves later.
        {
            let mut insert = tx.prepare("INSERT INTO refs (src_id, name) VALUES (?1, ?2)")?;
            for reference in references {
                let Some(index) = reference.symbol_index else {
                    continue; // file-level reference: no source symbol in V1
                };
                let src_id = *symbol_ids.get(index).ok_or_else(|| {
                    IndexError::InvalidInput(format!(
                        "reference symbol_index {index} out of range in {}",
                        file.path
                    ))
                })?;
                insert.execute(params![src_id, reference.name])?;
                names_to_resolve.insert(reference.name.clone());
            }
        }
        for name in &names_to_resolve {
            resolve_edges_for_name(&tx, name)?;
        }
        let edge_count: usize = tx.query_row(
            "SELECT COUNT(*) FROM edges
             WHERE src_id IN (SELECT id FROM symbols WHERE path = ?1)",
            [&file.path],
            |row| row.get::<_, i64>(0),
        )? as usize;

        let mut chunk_count = 0usize;
        {
            let mut insert = tx.prepare(
                "INSERT INTO chunks (path, symbol_id, start_line, end_line, content)
                 VALUES (?1, ?2, ?3, ?4, ?5)",
            )?;
            for chunk in chunks {
                let symbol_id = match chunk.symbol_index {
                    Some(index) => Some(*symbol_ids.get(index).ok_or_else(|| {
                        IndexError::InvalidInput(format!(
                            "chunk symbol_index {index} out of range in {}",
                            file.path
                        ))
                    })?),
                    None => None,
                };
                insert.execute(params![
                    file.path,
                    symbol_id,
                    chunk.start_line,
                    chunk.end_line,
                    chunk.content
                ])?;
                chunk_count += 1;
            }
        }

        tx.commit()?;
        Ok(ReplaceCounts {
            symbols: symbols.len(),
            edges: edge_count,
            chunks: chunk_count,
        })
    }

    /// Every indexed file path, sorted (deterministic reconciliation order).
    pub fn indexed_paths(&self) -> Result<Vec<String>, IndexError> {
        let mut stmt = self.conn.prepare("SELECT path FROM files ORDER BY path")?;
        let paths = stmt
            .query_map([], |row| row.get(0))?
            .collect::<Result<Vec<String>, _>>()?;
        Ok(paths)
    }

    pub fn delete_file(&mut self, path: &str) -> Result<(), IndexError> {
        let tx = self.conn.transaction()?;
        // Removing this file's definitions changes the `1/n` fan-out of every
        // surviving reference to those names: re-resolve them after the drop.
        let names: Vec<String> = {
            let mut stmt = tx.prepare("SELECT DISTINCT name FROM symbols WHERE path = ?1")?;
            let names = stmt
                .query_map([path], |row| row.get(0))?
                .collect::<Result<_, _>>()?;
            names
        };
        tx.execute("DELETE FROM files WHERE path = ?1", [path])?;
        for name in &names {
            resolve_edges_for_name(&tx, name)?;
        }
        tx.commit()?;
        Ok(())
    }

    /// Row counts for diagnostics / build results.
    pub fn stats(&self) -> Result<IndexStats, IndexError> {
        let count = |sql: &str| -> Result<usize, rusqlite::Error> {
            self.conn
                .query_row(sql, [], |row| row.get::<_, i64>(0))
                .map(|n| n as usize)
        };
        Ok(IndexStats {
            files: count("SELECT COUNT(*) FROM files")?,
            symbols: count("SELECT COUNT(*) FROM symbols")?,
            edges: count("SELECT COUNT(*) FROM edges")?,
            chunks: count("SELECT COUNT(*) FROM chunks")?,
        })
    }
}

/// One LSP-derived symbol→symbol relation to persist (docs/plans/
/// code-intelligence.md V4 §B). Endpoints are (path, 1-based line) pairs the
/// caller obtained from tool-triggered LSP queries; each must resolve to the
/// smallest enclosing indexed symbol on that path — the edges store never
/// ingests, so the index stays a derived artifact of ingested files.
#[derive(Debug, Clone)]
pub struct EdgeSpec {
    pub src_path: String,
    pub src_line: u32,
    pub dst_path: String,
    pub dst_line: u32,
}

/// One edge `edges_store` did not persist, with the reason (unresolvable
/// endpoint, self edge). Skips are success, not error — symbols churn under
/// concurrent edits and the caller simply re-persists on the next query.
#[derive(Debug, Clone, serde::Serialize)]
pub struct SkippedEdge {
    pub src_path: String,
    pub dst_path: String,
    pub reason: String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct EdgesStoreReport {
    pub stored: usize,
    pub skipped: Vec<SkippedEdge>,
}

impl Store {
    /// Persists LSP-sourced precise edges (V4 §B): resolves each endpoint to
    /// the smallest enclosing symbol on its path (deterministic
    /// `ORDER BY (end_line - start_line), id`), skips unresolvable endpoints
    /// and self edges, and upserts resolved pairs with `edge_type =
    /// 'references'` via `INSERT … ON CONFLICT(src_id, dst_id, edge_type) DO
    /// UPDATE SET confidence = 1.0, source = 'lsp'` — the storage-level dedup
    /// rule prefers `source = 'lsp'` for a pair.
    pub fn edges_store(&mut self, edges: &[EdgeSpec]) -> Result<EdgesStoreReport, IndexError> {
        use rusqlite::OptionalExtension;
        let tx = self.conn.transaction()?;
        let mut stored = 0usize;
        let mut skipped: Vec<SkippedEdge> = Vec::new();
        {
            let mut resolve = tx.prepare_cached(
                "SELECT id FROM symbols
                 WHERE path = ?1 AND start_line <= ?2 AND end_line >= ?2
                 ORDER BY (end_line - start_line), id
                 LIMIT 1",
            )?;
            let mut upsert = tx.prepare_cached(
                "INSERT INTO edges (src_id, dst_id, edge_type, confidence, source)
                 VALUES (?1, ?2, 'references', 1.0, 'lsp')
                 ON CONFLICT(src_id, dst_id, edge_type)
                 DO UPDATE SET confidence = 1.0, source = 'lsp'",
            )?;
            let mut skip = |edge: &EdgeSpec, reason: String| {
                skipped.push(SkippedEdge {
                    src_path: edge.src_path.clone(),
                    dst_path: edge.dst_path.clone(),
                    reason,
                });
            };
            for edge in edges {
                let src_id: Option<i64> = resolve
                    .query_row(params![edge.src_path, edge.src_line], |row| row.get(0))
                    .optional()?;
                let Some(src_id) = src_id else {
                    skip(edge, format!("no symbol at {}:{}", edge.src_path, edge.src_line));
                    continue;
                };
                let dst_id: Option<i64> = resolve
                    .query_row(params![edge.dst_path, edge.dst_line], |row| row.get(0))
                    .optional()?;
                let Some(dst_id) = dst_id else {
                    skip(edge, format!("no symbol at {}:{}", edge.dst_path, edge.dst_line));
                    continue;
                };
                if src_id == dst_id {
                    skip(edge, "self edge: both endpoints resolve to the same symbol".into());
                    continue;
                }
                upsert.execute(params![src_id, dst_id])?;
                stored += 1;
            }
        }
        tx.commit()?;
        Ok(EdgesStoreReport { stored, skipped })
    }
}

/// Rebuilds the name-approximate edges for one symbol name from the stored
/// raw references: drop every tree-sitter edge into the name's current
/// definitions, then fan each stored reference out to all `n` definitions
/// with confidence `1/n`. Called for every name a replace/delete touches,
/// inside its transaction, so edges stay consistent with the definition set.
/// The delete is scoped to `source = 'tree-sitter'` and the insert stays
/// `INSERT OR IGNORE`: an LSP edge whose endpoint symbols still exist
/// survives a same-name recompute (both its files are unchanged, so the
/// precise relation still holds) and is never downgraded or duplicated —
/// LSP edges die only with an endpoint file's cascade.
fn resolve_edges_for_name(tx: &rusqlite::Transaction<'_>, name: &str) -> Result<(), IndexError> {
    tx.execute(
        "DELETE FROM edges
         WHERE dst_id IN (SELECT id FROM symbols WHERE name = ?1)
           AND source = 'tree-sitter'",
        [name],
    )?;
    let dst_ids: Vec<i64> = {
        let mut stmt = tx.prepare_cached("SELECT id FROM symbols WHERE name = ?1")?;
        let ids = stmt
            .query_map([name], |row| row.get(0))?
            .collect::<Result<_, _>>()?;
        ids
    };
    if dst_ids.is_empty() {
        return Ok(());
    }
    let src_ids: Vec<i64> = {
        let mut stmt = tx.prepare_cached("SELECT src_id FROM refs WHERE name = ?1")?;
        let ids = stmt
            .query_map([name], |row| row.get(0))?
            .collect::<Result<_, _>>()?;
        ids
    };
    let confidence = 1.0 / dst_ids.len() as f64;
    let mut insert = tx.prepare_cached(
        "INSERT OR IGNORE INTO edges (src_id, dst_id, edge_type, confidence, source)
         VALUES (?1, ?2, 'references', ?3, 'tree-sitter')",
    )?;
    for src_id in src_ids {
        for dst_id in &dst_ids {
            insert.execute(params![src_id, dst_id, confidence])?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::extract::SymbolKind;

    fn file_row(path: &str, hash: &str) -> FileRow {
        FileRow {
            path: path.into(),
            content_hash: hash.into(),
            lang: Lang::Rust,
            indexed_at: "2026-01-01T00:00:00Z".into(),
        }
    }

    fn psym(name: &str, start: u32, end: u32) -> PendingSymbol {
        PendingSymbol {
            name: name.into(),
            qualified_name: name.into(),
            kind: SymbolKind::Function,
            start_line: start,
            end_line: end,
        }
    }

    fn pref(symbol_index: Option<usize>, name: &str, line: u32) -> PendingReference {
        PendingReference {
            symbol_index,
            name: name.into(),
            line,
            text: format!("{name}()"),
        }
    }

    fn chunk_row(start: u32, end: u32, content: &str, symbol_index: Option<usize>) -> Chunk {
        Chunk {
            start_line: start,
            end_line: end,
            content: content.into(),
            symbol_index,
        }
    }

    fn count(store: &Store, sql: &str) -> i64 {
        store
            .conn
            .query_row(sql, [], |row| row.get(0))
            .expect("count query")
    }

    #[test]
    fn open_migrates_schema_to_current_version() {
        let store = Store::in_memory().expect("in-memory store");
        let user_version: i64 = store
            .conn
            .query_row("PRAGMA user_version", [], |row| row.get(0))
            .expect("user_version");
        assert_eq!(user_version, SCHEMA_VERSION);
        let foreign_keys: i64 = store
            .conn
            .query_row("PRAGMA foreign_keys", [], |row| row.get(0))
            .expect("foreign_keys");
        assert_eq!(foreign_keys, 1, "foreign_keys must be ON for cascades");
    }

    #[test]
    fn replace_file_round_trips_rows() {
        let mut store = Store::in_memory().expect("in-memory store");
        let counts = store
            .replace_file(
                &file_row("src/a.rs", "h1"),
                &[psym("alpha", 1, 3)],
                &[],
                &[chunk_row(1, 3, "pub fn alpha() {}", Some(0))],
            )
            .expect("replace_file");
        assert_eq!(counts.symbols, 1);
        assert_eq!(counts.chunks, 1);
        assert_eq!(counts.edges, 0);
        assert_eq!(count(&store, "SELECT COUNT(*) FROM files"), 1);
        assert_eq!(count(&store, "SELECT COUNT(*) FROM symbols"), 1);
        assert_eq!(count(&store, "SELECT COUNT(*) FROM chunks"), 1);
        let (name, kind, start, end): (String, String, i64, i64) = store
            .conn
            .query_row(
                "SELECT name, kind, start_line, end_line FROM symbols",
                [],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?)),
            )
            .expect("symbol row");
        assert_eq!(name, "alpha");
        assert_eq!(kind, "function");
        assert_eq!(start, 1);
        assert_eq!(end, 3);
    }

    #[test]
    fn file_hash_returns_stored_hash() {
        let mut store = Store::in_memory().expect("in-memory store");
        assert_eq!(store.file_hash("src/a.rs").expect("file_hash"), None);
        store
            .replace_file(&file_row("src/a.rs", "h1"), &[], &[], &[])
            .expect("replace_file");
        assert_eq!(
            store.file_hash("src/a.rs").expect("file_hash"),
            Some("h1".to_string())
        );
    }

    #[test]
    fn delete_file_cascades_symbols_edges_chunks_and_fts() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(
                &file_row("src/a.rs", "h1"),
                &[psym("alpha", 1, 3), psym("beta", 5, 7)],
                &[pref(Some(1), "alpha", 6)],
                &[
                    chunk_row(1, 3, "pub fn alpha() {}", Some(0)),
                    chunk_row(5, 7, "pub fn beta() { alpha(); }", Some(1)),
                ],
            )
            .expect("replace_file");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges"), 1);
        assert_eq!(
            count(
                &store,
                "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'alpha'"
            ),
            2
        );
        store.delete_file("src/a.rs").expect("delete_file");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM files"), 0);
        assert_eq!(count(&store, "SELECT COUNT(*) FROM symbols"), 0);
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges"), 0);
        assert_eq!(count(&store, "SELECT COUNT(*) FROM chunks"), 0);
        assert_eq!(
            count(
                &store,
                "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'alpha'"
            ),
            0,
            "triggers must keep FTS in sync on delete"
        );
    }

    #[test]
    fn changed_file_replace_drops_stale_rows() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(
                &file_row("src/a.rs", "h1"),
                &[psym("old_name", 1, 1)],
                &[],
                &[chunk_row(1, 1, "pub fn old_name() {}", Some(0))],
            )
            .expect("first replace");
        store
            .replace_file(
                &file_row("src/a.rs", "h2"),
                &[psym("new_name", 1, 1)],
                &[],
                &[chunk_row(1, 1, "pub fn new_name() {}", Some(0))],
            )
            .expect("second replace");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM files"), 1);
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM symbols WHERE name = 'old_name'"),
            0
        );
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM symbols WHERE name = 'new_name'"),
            1
        );
        assert_eq!(
            count(
                &store,
                "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'old_name'"
            ),
            0,
            "stale chunk must leave FTS"
        );
        assert_eq!(
            store.file_hash("src/a.rs").expect("file_hash"),
            Some("h2".to_string())
        );
    }

    #[test]
    fn reference_fan_out_sets_confidence_one_over_n() {
        let mut store = Store::in_memory().expect("in-memory store");
        // Two definitions of `dup` in one file...
        store
            .replace_file(
                &file_row("src/a.rs", "ha"),
                &[psym("dup", 1, 2), psym("dup", 4, 5)],
                &[],
                &[],
            )
            .expect("replace a");
        // ...and one reference from `caller` in another file: two edges, 1/2 each.
        store
            .replace_file(
                &file_row("src/b.rs", "hb"),
                &[psym("caller", 1, 3)],
                &[pref(Some(0), "dup", 2)],
                &[],
            )
            .expect("replace b");
        let mut stmt = store
            .conn
            .prepare("SELECT confidence, source, edge_type FROM edges ORDER BY dst_id")
            .expect("prepare");
        let edges: Vec<(f64, String, String)> = stmt
            .query_map([], |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)))
            .expect("query")
            .collect::<Result<_, _>>()
            .expect("rows");
        assert_eq!(edges.len(), 2, "reference fans out to both definitions");
        for (confidence, source, edge_type) in &edges {
            assert!((confidence - 0.5).abs() < 1e-9, "confidence 1/2, got {confidence}");
            assert_eq!(source, "tree-sitter");
            assert_eq!(edge_type, "references");
        }
    }

    #[test]
    fn definition_changes_recompute_confidence_globally() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(&file_row("src/a.rs", "ha"), &[psym("dup", 1, 2)], &[], &[])
            .expect("replace a");
        store
            .replace_file(
                &file_row("src/c.rs", "hc"),
                &[psym("caller", 1, 3)],
                &[pref(Some(0), "dup", 2)],
                &[],
            )
            .expect("replace c");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges"), 1);
        // A second definition of `dup` appears in another file: the existing
        // reference must fan out to it and its confidence recompute to 1/2.
        store
            .replace_file(&file_row("src/b.rs", "hb"), &[psym("dup", 1, 2)], &[], &[])
            .expect("replace b");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges"), 2);
        let max_conf: f64 = store
            .conn
            .query_row("SELECT MAX(confidence) FROM edges", [], |row| row.get(0))
            .expect("confidence");
        assert!((max_conf - 0.5).abs() < 1e-9, "confidence 1/2, got {max_conf}");
        // Deleting one definition file restores full confidence on the other.
        store.delete_file("src/b.rs").expect("delete b");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges"), 1);
        let conf: f64 = store
            .conn
            .query_row("SELECT confidence FROM edges", [], |row| row.get(0))
            .expect("confidence");
        assert!((conf - 1.0).abs() < 1e-9, "confidence must return to 1, got {conf}");
    }

    // ---- V2: embeddings table (schema v3) --------------------------------

    /// f32-LE blob for embedding rows, matching the documented BLOB layout.
    fn f32_le_blob(values: &[f32]) -> Vec<u8> {
        values.iter().flat_map(|v| v.to_le_bytes()).collect()
    }

    fn insert_embedding(store: &Store, chunk_id: i64, model_id: &str, vector: &[f32], hash: &str) {
        store
            .conn
            .execute(
                "INSERT INTO embeddings (chunk_id, model_id, dims, vector, content_hash)
                 VALUES (?1, ?2, ?3, ?4, ?5)",
                params![chunk_id, model_id, vector.len() as i64, f32_le_blob(vector), hash],
            )
            .expect("insert embedding row");
    }

    fn only_chunk_id(store: &Store) -> i64 {
        store
            .conn
            .query_row("SELECT id FROM chunks", [], |row| row.get(0))
            .expect("single chunk id")
    }

    #[test]
    fn schema_v3_has_embeddings_table_and_round_trips_vectors() {
        let mut store = Store::in_memory().expect("in-memory store");
        let user_version: i64 = store
            .conn
            .query_row("PRAGMA user_version", [], |row| row.get(0))
            .expect("user_version");
        assert_eq!(user_version, 3, "V2 bumps the schema to v3");

        store
            .replace_file(
                &file_row("src/a.rs", "h1"),
                &[psym("alpha", 1, 3)],
                &[],
                &[chunk_row(1, 3, "pub fn alpha() {}", Some(0))],
            )
            .expect("replace_file");
        let chunk_id = only_chunk_id(&store);
        insert_embedding(&store, chunk_id, "openai/text-embedding-3-small", &[0.25, -1.5, 3.0], "h1");

        let (dims, blob, hash): (i64, Vec<u8>, String) = store
            .conn
            .query_row(
                "SELECT dims, vector, content_hash FROM embeddings
                 WHERE chunk_id = ?1 AND model_id = ?2",
                params![chunk_id, "openai/text-embedding-3-small"],
                |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)),
            )
            .expect("embedding row round-trips");
        assert_eq!(dims, 3);
        assert_eq!(blob, f32_le_blob(&[0.25, -1.5, 3.0]), "vector is f32-LE, dims*4 bytes");
        assert_eq!(blob.len(), 12);
        assert_eq!(hash, "h1");

        // (chunk_id, model_id) is the primary key: same chunk, second model.
        insert_embedding(&store, chunk_id, "voyage/voyage-code-3", &[1.0, 0.0, 0.0], "h1");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM embeddings"), 2);
    }

    #[test]
    fn unknown_schema_v2_database_is_dropped_and_rebuilt_on_open() {
        let db_path = std::env::temp_dir().join(format!(
            "carina-index-v2-rebuild-test-{}.sqlite",
            std::process::id()
        ));
        let _ = std::fs::remove_file(&db_path);
        {
            // Fabricate a v2-era database: files table, no embeddings, version 2.
            let conn = rusqlite::Connection::open(&db_path).expect("raw open");
            conn.execute_batch(
                "CREATE TABLE files (
                   path TEXT PRIMARY KEY, content_hash TEXT NOT NULL,
                   lang TEXT NOT NULL, indexed_at TEXT NOT NULL
                 );
                 INSERT INTO files VALUES ('old.rs', 'h0', 'rust', '2026-01-01T00:00:00Z');
                 PRAGMA user_version = 2;",
            )
            .expect("fabricate v2 db");
        }
        {
            let store = Store::open(&db_path).expect("open v2 db");
            let user_version: i64 = store
                .conn
                .query_row("PRAGMA user_version", [], |row| row.get(0))
                .expect("user_version");
            assert_eq!(user_version, 3, "v2 must be dropped and rebuilt as v3");
            assert_eq!(
                count(&store, "SELECT COUNT(*) FROM files"),
                0,
                "the index is derived: an old-schema database is rebuilt empty"
            );
            assert_eq!(
                count(&store, "SELECT COUNT(*) FROM embeddings"),
                0,
                "the embeddings table must exist after the rebuild"
            );
        }
        let _ = std::fs::remove_file(&db_path);
        let _ = std::fs::remove_file(db_path.with_extension("sqlite-wal"));
        let _ = std::fs::remove_file(db_path.with_extension("sqlite-shm"));
    }

    #[test]
    fn replace_file_cascade_drops_embeddings() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(
                &file_row("src/a.rs", "h1"),
                &[psym("old_fn", 1, 1)],
                &[],
                &[chunk_row(1, 1, "pub fn old_fn() {}", Some(0))],
            )
            .expect("first replace");
        insert_embedding(&store, only_chunk_id(&store), "m", &[1.0, 0.0], "h1");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM embeddings"), 1);

        store
            .replace_file(
                &file_row("src/a.rs", "h2"),
                &[psym("new_fn", 1, 1)],
                &[],
                &[chunk_row(1, 1, "pub fn new_fn() {}", Some(0))],
            )
            .expect("second replace");
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM embeddings"),
            0,
            "replacing a file must cascade-drop its chunks' embeddings"
        );
    }

    #[test]
    fn delete_file_cascade_drops_embeddings() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(
                &file_row("src/a.rs", "h1"),
                &[psym("gone_fn", 1, 1)],
                &[],
                &[chunk_row(1, 1, "pub fn gone_fn() {}", Some(0))],
            )
            .expect("replace_file");
        insert_embedding(&store, only_chunk_id(&store), "m", &[1.0, 0.0], "h1");
        store.delete_file("src/a.rs").expect("delete_file");
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM embeddings"),
            0,
            "deleting a file must cascade-drop its chunks' embeddings"
        );
    }

    // ---- V4: LSP-sourced precise edges (edges_store + dedup) --------------

    fn espec(src_path: &str, src_line: u32, dst_path: &str, dst_line: u32) -> EdgeSpec {
        EdgeSpec {
            src_path: src_path.into(),
            src_line,
            dst_path: dst_path.into(),
            dst_line,
        }
    }

    fn symbol_id(store: &Store, path: &str, name: &str) -> i64 {
        store
            .conn
            .query_row(
                "SELECT id FROM symbols WHERE path = ?1 AND name = ?2",
                params![path, name],
                |row| row.get(0),
            )
            .expect("symbol id")
    }

    /// (src_id, dst_id, edge_type, confidence, source), fully ordered.
    fn edge_rows(store: &Store) -> Vec<(i64, i64, String, f64, String)> {
        let mut stmt = store
            .conn
            .prepare(
                "SELECT src_id, dst_id, edge_type, confidence, source FROM edges
                 ORDER BY src_id, dst_id, edge_type",
            )
            .expect("prepare edge rows");
        stmt.query_map([], |row| {
            Ok((row.get(0)?, row.get(1)?, row.get(2)?, row.get(3)?, row.get(4)?))
        })
        .expect("query edge rows")
        .collect::<Result<_, _>>()
        .expect("edge rows")
    }

    #[test]
    fn edges_store_resolves_endpoints_and_upserts_lsp_edge() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(&file_row("a.rs", "ha"), &[psym("zz_e_alpha", 1, 3)], &[], &[])
            .expect("replace a");
        store
            .replace_file(&file_row("b.rs", "hb"), &[psym("zz_e_caller", 1, 5)], &[], &[])
            .expect("replace b");

        let report = store
            .edges_store(&[espec("b.rs", 2, "a.rs", 1)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "got {report:?}");
        assert!(report.skipped.is_empty(), "got {report:?}");

        let rows = edge_rows(&store);
        assert_eq!(rows.len(), 1, "got {rows:?}");
        let (src, dst, edge_type, confidence, source) = &rows[0];
        assert_eq!(*src, symbol_id(&store, "b.rs", "zz_e_caller"));
        assert_eq!(*dst, symbol_id(&store, "a.rs", "zz_e_alpha"));
        assert_eq!(edge_type, "references");
        assert!((confidence - 1.0).abs() < 1e-9, "lsp edges carry confidence 1.0, got {confidence}");
        assert_eq!(source, "lsp");
    }

    #[test]
    fn edges_store_replaces_tree_sitter_edge_for_the_same_pair() {
        let mut store = Store::in_memory().expect("in-memory store");
        // Two definitions of zz_e_dup: the tree-sitter edge fans out 1/2 each.
        store
            .replace_file(&file_row("a.rs", "ha"), &[psym("zz_e_dup", 1, 2)], &[], &[])
            .expect("replace a");
        store
            .replace_file(&file_row("c.rs", "hc"), &[psym("zz_e_dup", 1, 2)], &[], &[])
            .expect("replace c");
        store
            .replace_file(
                &file_row("b.rs", "hb"),
                &[psym("zz_e_caller", 1, 5)],
                &[pref(Some(0), "zz_e_dup", 2)],
                &[],
            )
            .expect("replace b");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges"), 2);

        // The LSP answer disambiguates: caller -> a.rs definition, precisely.
        let report = store
            .edges_store(&[espec("b.rs", 2, "a.rs", 1)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "got {report:?}");

        let caller = symbol_id(&store, "b.rs", "zz_e_caller");
        let precise = symbol_id(&store, "a.rs", "zz_e_dup");
        let pair_rows: i64 = store
            .conn
            .query_row(
                "SELECT COUNT(*) FROM edges WHERE src_id = ?1 AND dst_id = ?2",
                params![caller, precise],
                |row| row.get(0),
            )
            .expect("pair count");
        assert_eq!(pair_rows, 1, "dedup: at most one row per (src, dst, edge_type)");
        let (confidence, source): (f64, String) = store
            .conn
            .query_row(
                "SELECT confidence, source FROM edges WHERE src_id = ?1 AND dst_id = ?2",
                params![caller, precise],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .expect("upgraded row");
        assert!((confidence - 1.0).abs() < 1e-9, "got {confidence}");
        assert_eq!(source, "lsp", "the tree-sitter row must be replaced, not duplicated");
        // The other fan-out target keeps its honest tree-sitter row.
        let other = symbol_id(&store, "c.rs", "zz_e_dup");
        let (other_conf, other_source): (f64, String) = store
            .conn
            .query_row(
                "SELECT confidence, source FROM edges WHERE src_id = ?1 AND dst_id = ?2",
                params![caller, other],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .expect("untouched row");
        assert!((other_conf - 0.5).abs() < 1e-9, "got {other_conf}");
        assert_eq!(other_source, "tree-sitter");
    }

    #[test]
    fn edges_store_resolves_the_smallest_enclosing_symbol() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(
                &file_row("a.rs", "ha"),
                &[psym("zz_e_outer", 1, 10), psym("zz_e_inner", 2, 5)],
                &[],
                &[],
            )
            .expect("replace a");
        store
            .replace_file(&file_row("b.rs", "hb"), &[psym("zz_e_caller", 1, 5)], &[], &[])
            .expect("replace b");

        let report = store
            .edges_store(&[espec("b.rs", 2, "a.rs", 3)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "got {report:?}");
        let rows = edge_rows(&store);
        assert_eq!(rows.len(), 1, "got {rows:?}");
        assert_eq!(
            rows[0].1,
            symbol_id(&store, "a.rs", "zz_e_inner"),
            "line 3 must resolve to the smallest enclosing symbol"
        );
    }

    #[test]
    fn edges_store_skips_unresolvable_and_self_edges_as_success() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(&file_row("a.rs", "ha"), &[psym("zz_e_only", 1, 3)], &[], &[])
            .expect("replace a");

        let report = store
            .edges_store(&[
                espec("a.rs", 99, "a.rs", 1),      // src line outside every symbol
                espec("missing.rs", 1, "a.rs", 1), // src path never indexed
                espec("a.rs", 1, "a.rs", 2),       // both endpoints resolve to the same symbol
            ])
            .expect("skips are success, never an error");
        assert_eq!(report.stored, 0, "got {report:?}");
        assert_eq!(report.skipped.len(), 3, "got {report:?}");
        assert!(
            report.skipped.iter().all(|s| !s.reason.is_empty()),
            "every skip carries a reason: {report:?}"
        );
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges"), 0);
    }

    #[test]
    fn tree_sitter_recompute_preserves_lsp_edge_without_duplicates() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(&file_row("a.rs", "ha"), &[psym("zz_e_surv", 1, 2)], &[], &[])
            .expect("replace a");
        store
            .replace_file(
                &file_row("b.rs", "hb"),
                &[psym("zz_e_caller_b", 1, 3)],
                &[pref(Some(0), "zz_e_surv", 2)],
                &[],
            )
            .expect("replace b");
        let report = store
            .edges_store(&[espec("b.rs", 2, "a.rs", 1)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "got {report:?}");

        // A third file referencing the same name triggers
        // resolve_edges_for_name("zz_e_surv"): both endpoint files of the LSP
        // edge are untouched, so the precise relation still holds — the
        // recompute must not downgrade it to tree-sitter nor duplicate it.
        store
            .replace_file(
                &file_row("d.rs", "hd"),
                &[psym("zz_e_caller_d", 1, 3)],
                &[pref(Some(0), "zz_e_surv", 2)],
                &[],
            )
            .expect("replace d");

        let caller_b = symbol_id(&store, "b.rs", "zz_e_caller_b");
        let def = symbol_id(&store, "a.rs", "zz_e_surv");
        let rows: Vec<(f64, String)> = {
            let mut stmt = store
                .conn
                .prepare("SELECT confidence, source FROM edges WHERE src_id = ?1 AND dst_id = ?2")
                .expect("prepare");
            stmt.query_map(params![caller_b, def], |row| Ok((row.get(0)?, row.get(1)?)))
                .expect("query")
                .collect::<Result<_, _>>()
                .expect("rows")
        };
        assert_eq!(rows.len(), 1, "the surviving LSP edge must never be duplicated: {rows:?}");
        assert!((rows[0].0 - 1.0).abs() < 1e-9, "got {rows:?}");
        assert_eq!(rows[0].1, "lsp", "a same-name recompute must not downgrade the LSP edge");
        // The new caller's tree-sitter edge lands alongside it.
        let caller_d = symbol_id(&store, "d.rs", "zz_e_caller_d");
        let d_rows: i64 = store
            .conn
            .query_row(
                "SELECT COUNT(*) FROM edges WHERE src_id = ?1 AND dst_id = ?2 AND source = 'tree-sitter'",
                params![caller_d, def],
                |row| row.get(0),
            )
            .expect("d row");
        assert_eq!(d_rows, 1);
    }

    /// Builds def (a.rs) + caller (b.rs) with a persisted LSP edge; callers
    /// then invalidate one endpoint and assert the cascade.
    fn lsp_edge_fixture() -> Store {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(&file_row("a.rs", "ha"), &[psym("zz_e_casc", 1, 2)], &[], &[])
            .expect("replace a");
        store
            .replace_file(&file_row("b.rs", "hb"), &[psym("zz_e_casc_caller", 1, 3)], &[], &[])
            .expect("replace b");
        let report = store
            .edges_store(&[espec("b.rs", 2, "a.rs", 1)])
            .expect("edges_store");
        assert_eq!(report.stored, 1, "fixture must persist the LSP edge, got {report:?}");
        assert_eq!(count(&store, "SELECT COUNT(*) FROM edges WHERE source = 'lsp'"), 1);
        store
    }

    #[test]
    fn replacing_the_src_file_cascade_drops_the_lsp_edge() {
        let mut store = lsp_edge_fixture();
        store
            .replace_file(&file_row("b.rs", "hb2"), &[psym("zz_e_casc_caller", 1, 4)], &[], &[])
            .expect("replace b");
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM edges WHERE source = 'lsp'"),
            0,
            "replacing the src file must cascade-drop the LSP edge"
        );
    }

    #[test]
    fn replacing_the_dst_file_cascade_drops_the_lsp_edge() {
        let mut store = lsp_edge_fixture();
        store
            .replace_file(&file_row("a.rs", "ha2"), &[psym("zz_e_casc", 1, 3)], &[], &[])
            .expect("replace a");
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM edges WHERE source = 'lsp'"),
            0,
            "replacing the dst file must cascade-drop the LSP edge"
        );
    }

    #[test]
    fn deleting_either_endpoint_file_cascade_drops_the_lsp_edge() {
        let mut store = lsp_edge_fixture();
        store.delete_file("a.rs").expect("delete a");
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM edges WHERE source = 'lsp'"),
            0,
            "deleting the dst file must cascade-drop the LSP edge"
        );

        let mut store = lsp_edge_fixture();
        store.delete_file("b.rs").expect("delete b");
        assert_eq!(
            count(&store, "SELECT COUNT(*) FROM edges WHERE source = 'lsp'"),
            0,
            "deleting the src file must cascade-drop the LSP edge"
        );
    }

    #[test]
    fn fts_bm25_ranks_denser_match_first() {
        let mut store = Store::in_memory().expect("in-memory store");
        store
            .replace_file(
                &file_row("src/a.rs", "h1"),
                &[],
                &[],
                &[
                    chunk_row(1, 3, "kernel governs the kernel via kernel checks", None),
                    chunk_row(5, 7, "the kernel appears once in this chunk body", None),
                ],
            )
            .expect("replace_file");
        let mut stmt = store
            .conn
            .prepare(
                "SELECT c.start_line FROM chunks_fts f JOIN chunks c ON c.id = f.rowid \
                 WHERE chunks_fts MATCH 'kernel' ORDER BY bm25(chunks_fts)",
            )
            .expect("prepare");
        let order: Vec<i64> = stmt
            .query_map([], |row| row.get(0))
            .expect("query")
            .collect::<Result<_, _>>()
            .expect("rows");
        assert_eq!(order, vec![1, 5], "denser chunk must rank first under BM25");
    }
}
