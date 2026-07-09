//! Symbol lookup: definitions by name (optionally filtered by kind) plus
//! approximate reference sites.
//!
//! Every report is labeled `confidence = "tree-sitter"` so callers know
//! references are name-based, not resolved.

use std::collections::HashSet;

use rusqlite::params;

use crate::extract::{SymbolKind, SymbolRecord};
use crate::store::{kind_to_str, Store};
use crate::IndexError;

pub struct SymbolOptions {
    pub kind: Option<SymbolKind>,
    pub include_references: bool,
    pub limit: usize,
}

impl Default for SymbolOptions {
    fn default() -> Self {
        SymbolOptions {
            kind: None,
            include_references: true,
            limit: 50,
        }
    }
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct ReferenceSite {
    pub path: String,
    pub line: u32,
    pub text: String,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct SymbolReport {
    pub definitions: Vec<SymbolRecord>,
    pub references: Vec<ReferenceSite>,
    /// Always "tree-sitter" in V1 — references are name-approximate.
    pub confidence: &'static str,
}

/// DB decoding of `SymbolKind` (inverse of `store::kind_to_str`).
pub(crate) fn kind_from_str(kind: &str) -> Option<SymbolKind> {
    match kind {
        "function" => Some(SymbolKind::Function),
        "method" => Some(SymbolKind::Method),
        "struct" => Some(SymbolKind::Struct),
        "enum" => Some(SymbolKind::Enum),
        "trait" => Some(SymbolKind::Trait),
        "interface" => Some(SymbolKind::Interface),
        "class" => Some(SymbolKind::Class),
        "const" => Some(SymbolKind::Const),
        "type_alias" => Some(SymbolKind::TypeAlias),
        "module" => Some(SymbolKind::Module),
        "variable" => Some(SymbolKind::Variable),
        _ => None,
    }
}

pub(crate) fn record_from_row(row: &rusqlite::Row<'_>) -> rusqlite::Result<SymbolRecord> {
    let kind: String = row.get(4)?;
    Ok(SymbolRecord {
        id: row.get(0)?,
        path: row.get(1)?,
        name: row.get(2)?,
        qualified_name: row.get(3)?,
        kind: kind_from_str(&kind).unwrap_or(SymbolKind::Function),
        start_line: row.get(5)?,
        end_line: row.get(6)?,
        content_hash: row.get(7)?,
        indexed_at: row.get(8)?,
    })
}

/// Symbol columns joined with the owning file's provenance (`s` = symbols,
/// `f` = files); pair with `SYMBOL_JOIN`.
pub(crate) const SYMBOL_COLUMNS: &str = "s.id, s.path, s.name, s.qualified_name, s.kind, \
                              s.start_line, s.end_line, f.content_hash, f.indexed_at";
pub(crate) const SYMBOL_JOIN: &str = "symbols s JOIN files f ON f.path = s.path";

/// One symbol row by id (chunk → symbol context for search hits).
pub(crate) fn symbol_by_id(store: &Store, id: i64) -> Result<Option<SymbolRecord>, IndexError> {
    use rusqlite::OptionalExtension;
    Ok(store
        .conn
        .query_row(
            &format!("SELECT {SYMBOL_COLUMNS} FROM {SYMBOL_JOIN} WHERE s.id = ?1"),
            [id],
            record_from_row,
        )
        .optional()?)
}

/// Definitions by exact name (optionally kind-filtered) plus approximate
/// reference sites scanned out of the indexed chunks.
pub(crate) fn lookup(
    store: &Store,
    name: &str,
    opts: &SymbolOptions,
) -> Result<SymbolReport, IndexError> {
    let kind = opts.kind.map(kind_to_str);
    let mut stmt = store.conn.prepare(&format!(
        "SELECT {SYMBOL_COLUMNS} FROM {SYMBOL_JOIN}
         WHERE s.name = ?1 AND (?2 IS NULL OR s.kind = ?2)
         ORDER BY s.path, s.start_line, s.id
         LIMIT ?3",
    ))?;
    let definitions: Vec<SymbolRecord> = stmt
        .query_map(params![name, kind, opts.limit as i64], record_from_row)?
        .collect::<Result<_, _>>()?;

    let references = if opts.include_references && !name.is_empty() {
        reference_sites(store, name, opts.limit)?
    } else {
        Vec::new()
    };

    Ok(SymbolReport {
        definitions,
        references,
        confidence: "tree-sitter",
    })
}

/// Name-approximate reference sites: identifier occurrences in chunk content
/// (word-boundary checked), excluding the declaration lines of same-name
/// definitions. Chunks tile the file without overlap, so (path, line) sites
/// are naturally unique.
fn reference_sites(
    store: &Store,
    name: &str,
    limit: usize,
) -> Result<Vec<ReferenceSite>, IndexError> {
    // Declaration lines of every definition of `name` are not references.
    let mut def_stmt = store
        .conn
        .prepare("SELECT path, start_line FROM symbols WHERE name = ?1")?;
    let definition_lines: HashSet<(String, u32)> = def_stmt
        .query_map([name], |row| Ok((row.get(0)?, row.get(1)?)))?
        .collect::<Result<_, _>>()?;

    let mut chunk_stmt = store.conn.prepare(
        "SELECT path, start_line, content FROM chunks
         WHERE instr(content, ?1) > 0
         ORDER BY path, start_line, id",
    )?;
    let chunks: Vec<(String, u32, String)> = chunk_stmt
        .query_map([name], |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)))?
        .collect::<Result<_, _>>()?;

    let mut sites = Vec::new();
    'chunks: for (path, chunk_start, content) in chunks {
        for (offset, line_text) in content.lines().enumerate() {
            if !contains_identifier(line_text, name) {
                continue;
            }
            let line = chunk_start + offset as u32;
            if definition_lines.contains(&(path.clone(), line)) {
                continue;
            }
            sites.push(ReferenceSite {
                path: path.clone(),
                line,
                text: line_text.trim().to_string(),
            });
            if sites.len() == limit {
                break 'chunks;
            }
        }
    }
    Ok(sites)
}

/// True when `line` contains `name` delimited by non-identifier characters
/// (identifier characters match the FTS tokenizer: alphanumerics, `_`, `$`).
fn contains_identifier(line: &str, name: &str) -> bool {
    let is_ident = |c: char| c.is_alphanumeric() || c == '_' || c == '$';
    let mut search_from = 0;
    while let Some(found) = line[search_from..].find(name) {
        let start = search_from + found;
        let end = start + name.len();
        let boundary_before = line[..start].chars().next_back().is_none_or(|c| !is_ident(c));
        let boundary_after = line[end..].chars().next().is_none_or(|c| !is_ident(c));
        if boundary_before && boundary_after {
            return true;
        }
        search_from = end;
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{CodeIndex, IngestFile};

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

    #[test]
    fn symbol_options_default() {
        let opts = SymbolOptions::default();
        assert!(opts.kind.is_none());
        assert!(opts.include_references);
        assert_eq!(opts.limit, 50);
    }

    #[test]
    fn lookup_finds_definitions_across_files() {
        let idx = fixture_index(&[
            ("a.rs", "pub fn multi_def() {}\n"),
            ("b.rs", "pub fn multi_def() {}\n"),
        ]);
        let report = idx
            .symbol_lookup("multi_def", &SymbolOptions::default())
            .expect("lookup");
        assert_eq!(report.definitions.len(), 2);
        let mut paths: Vec<&str> = report.definitions.iter().map(|d| d.path.as_str()).collect();
        paths.sort_unstable();
        assert_eq!(paths, ["a.rs", "b.rs"]);
        for def in &report.definitions {
            assert_eq!(def.name, "multi_def");
            assert_eq!(def.kind, SymbolKind::Function);
            assert!(def.id > 0, "definitions carry their row id");
        }
    }

    #[test]
    fn lookup_filters_by_kind() {
        let idx = fixture_index(&[
            ("a.rs", "pub struct Widget {\n    pub id: u32,\n}\n"),
            ("b.rs", "pub fn Widget() {}\n"),
        ]);
        let opts = SymbolOptions {
            kind: Some(SymbolKind::Struct),
            ..SymbolOptions::default()
        };
        let report = idx.symbol_lookup("Widget", &opts).expect("lookup");
        assert_eq!(report.definitions.len(), 1);
        assert_eq!(report.definitions[0].path, "a.rs");
        assert_eq!(report.definitions[0].kind, SymbolKind::Struct);
    }

    #[test]
    fn lookup_reports_reference_sites() {
        let idx = fixture_index(&[
            ("a.rs", "pub fn target_fn() {}\n"),
            ("b.rs", "pub fn use_target() {\n    target_fn();\n}\n"),
        ]);
        let report = idx
            .symbol_lookup("target_fn", &SymbolOptions::default())
            .expect("lookup");
        assert_eq!(report.definitions.len(), 1);
        let site = report
            .references
            .iter()
            .find(|r| r.path == "b.rs")
            .expect("reference site in b.rs");
        assert_eq!(site.line, 2);
        assert!(site.text.contains("target_fn"));
    }

    #[test]
    fn lookup_can_exclude_references() {
        let idx = fixture_index(&[
            ("a.rs", "pub fn target_fn() {}\n"),
            ("b.rs", "pub fn use_target() {\n    target_fn();\n}\n"),
        ]);
        let opts = SymbolOptions {
            include_references: false,
            ..SymbolOptions::default()
        };
        let report = idx.symbol_lookup("target_fn", &opts).expect("lookup");
        assert_eq!(report.definitions.len(), 1);
        assert!(report.references.is_empty());
    }

    #[test]
    fn lookup_respects_limit() {
        let idx = fixture_index(&[
            ("a.rs", "pub fn multi_def() {}\n"),
            ("b.rs", "pub fn multi_def() {}\n"),
            ("c.rs", "pub fn multi_def() {}\n"),
        ]);
        let opts = SymbolOptions {
            limit: 2,
            ..SymbolOptions::default()
        };
        let report = idx.symbol_lookup("multi_def", &opts).expect("lookup");
        assert_eq!(report.definitions.len(), 2);
    }

    #[test]
    fn report_confidence_is_tree_sitter() {
        let idx = fixture_index(&[("a.rs", "pub fn lone_fn() {}\n")]);
        let report = idx
            .symbol_lookup("lone_fn", &SymbolOptions::default())
            .expect("lookup");
        assert_eq!(report.confidence, "tree-sitter");
        let missing = idx
            .symbol_lookup("no_such_symbol", &SymbolOptions::default())
            .expect("lookup");
        assert_eq!(missing.confidence, "tree-sitter");
        assert!(missing.definitions.is_empty());
    }
}
