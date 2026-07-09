//! tree-sitter symbol and reference extraction.
//!
//! Per-language queries produce symbol definitions (name, qualified name,
//! kind, 1-based inclusive line span) and name-level references attributed to
//! their enclosing symbol. Cross-file edges stay name-approximate in V1: a
//! reference to name `N` fans out to all `n` definitions of `N` with
//! confidence `1/n`, source `"tree-sitter"`.

use std::collections::HashSet;
use std::ops::Range;

use serde::{Deserialize, Serialize};
use streaming_iterator::StreamingIterator;
use tree_sitter::{Node, Parser, Query, QueryCursor};

use crate::lang::Lang;
use crate::IndexError;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SymbolKind {
    Function,
    Method,
    Struct,
    Enum,
    Trait,
    Interface,
    Class,
    Const,
    TypeAlias,
    Module,
    Variable,
}

#[derive(Debug, Clone, Serialize)]
pub struct SymbolRecord {
    pub id: i64,
    pub path: String,
    pub name: String,
    pub qualified_name: String,
    pub kind: SymbolKind,
    pub start_line: u32,
    pub end_line: u32,
    /// Provenance: hash of the file content this row was extracted from,
    /// so callers can detect results that no longer match the workspace.
    pub content_hash: String,
    /// Provenance: when the file version was ingested (RFC 3339).
    pub indexed_at: String,
}

/// A definition before insertion (no id yet).
#[derive(Debug, Clone)]
pub(crate) struct PendingSymbol {
    pub name: String,
    pub qualified_name: String,
    pub kind: SymbolKind,
    pub start_line: u32,
    pub end_line: u32,
}

/// A name-level reference site, attributed to its enclosing symbol
/// (`symbol_index` into `Extraction::symbols`; None = file level).
#[derive(Debug, Clone)]
pub(crate) struct PendingReference {
    pub symbol_index: Option<usize>,
    pub name: String,
    pub line: u32,
    pub text: String,
}

pub(crate) struct Extraction {
    pub symbols: Vec<PendingSymbol>,
    pub references: Vec<PendingReference>,
}

/// Definition patterns capture `@name` plus `@def.<kind>` on the defining
/// node (its rows become the symbol span); reference patterns capture `@ref`
/// on the referenced identifier. Kind fix-ups that queries cannot express
/// (method-vs-function, Go type aliases, TS const bindings) happen in code.
struct LangQueries {
    definitions: &'static str,
    references: &'static str,
}

const RUST_QUERIES: LangQueries = LangQueries {
    definitions: r#"
        (struct_item name: (type_identifier) @name) @def.struct
        (enum_item name: (type_identifier) @name) @def.enum
        (union_item name: (type_identifier) @name) @def.struct
        (trait_item name: (type_identifier) @name) @def.trait
        (function_item name: (identifier) @name) @def.function
        (function_signature_item name: (identifier) @name) @def.function
        (const_item name: (identifier) @name) @def.const
        (static_item name: (identifier) @name) @def.const
        (type_item name: (type_identifier) @name) @def.type_alias
        (mod_item name: (identifier) @name) @def.module
    "#,
    references: r#"
        (call_expression function: (identifier) @ref)
        (call_expression function: (scoped_identifier name: (identifier) @ref))
        (call_expression function: (field_expression field: (field_identifier) @ref))
        (generic_function function: (identifier) @ref)
        (macro_invocation macro: (identifier) @ref)
        (struct_expression name: (type_identifier) @ref)
    "#,
};

const GO_QUERIES: LangQueries = LangQueries {
    definitions: r#"
        (function_declaration name: (identifier) @name) @def.function
        (method_declaration name: (field_identifier) @name) @def.method
        (type_declaration (type_spec name: (type_identifier) @name type: (struct_type)) @def.struct)
        (type_declaration (type_spec name: (type_identifier) @name type: (interface_type)) @def.interface)
        (type_declaration (type_spec name: (type_identifier) @name) @def.type_alias)
        (const_declaration (const_spec name: (identifier) @name) @def.const)
        (var_declaration (var_spec name: (identifier) @name) @def.variable)
    "#,
    references: r#"
        (call_expression function: (identifier) @ref)
        (call_expression function: (selector_expression field: (field_identifier) @ref))
        (composite_literal type: (type_identifier) @ref)
    "#,
};

const TS_QUERIES: LangQueries = LangQueries {
    definitions: r#"
        (interface_declaration name: (type_identifier) @name) @def.interface
        (class_declaration name: (type_identifier) @name) @def.class
        (method_definition name: (property_identifier) @name) @def.method
        (function_declaration name: (identifier) @name) @def.function
        (enum_declaration name: (identifier) @name) @def.enum
        (type_alias_declaration name: (type_identifier) @name) @def.type_alias
        (lexical_declaration (variable_declarator name: (identifier) @name) @def.variable)
        (variable_declaration (variable_declarator name: (identifier) @name) @def.variable)
    "#,
    references: r#"
        (call_expression function: (identifier) @ref)
        (call_expression function: (member_expression property: (property_identifier) @ref))
        (new_expression constructor: (identifier) @ref)
    "#,
};

const PY_QUERIES: LangQueries = LangQueries {
    definitions: r#"
        (class_definition name: (identifier) @name) @def.class
        (function_definition name: (identifier) @name) @def.function
    "#,
    references: r#"
        (call function: (identifier) @ref)
        (call function: (attribute attribute: (identifier) @ref))
    "#,
};

fn queries(lang: Lang) -> &'static LangQueries {
    match lang {
        Lang::Rust => &RUST_QUERIES,
        Lang::Go => &GO_QUERIES,
        Lang::TypeScript => &TS_QUERIES,
        Lang::Python => &PY_QUERIES,
    }
}

pub(crate) fn extract(lang: Lang, path: &str, source: &str) -> Result<Extraction, IndexError> {
    let parse_err = |message: String| IndexError::Parse {
        path: path.to_string(),
        message,
    };
    let language = lang.grammar_for_path(path);
    let mut parser = Parser::new();
    parser
        .set_language(&language)
        .map_err(|e| parse_err(format!("grammar load failed: {e}")))?;
    let tree = parser
        .parse(source, None)
        .ok_or_else(|| parse_err("tree-sitter produced no tree".to_string()))?;
    let root = tree.root_node();
    let queries = queries(lang);
    let defs_query = Query::new(&language, queries.definitions)
        .map_err(|e| parse_err(format!("definitions query: {e}")))?;
    let refs_query = Query::new(&language, queries.references)
        .map_err(|e| parse_err(format!("references query: {e}")))?;

    let mut symbols: Vec<PendingSymbol> = Vec::new();
    // Byte range of each symbol's defining node, for enclosing-symbol lookup.
    let mut symbol_ranges: Vec<Range<usize>> = Vec::new();
    let mut seen_defs: HashSet<(usize, usize)> = HashSet::new();
    let mut cursor = QueryCursor::new();
    let mut matches = cursor.matches(&defs_query, root, source.as_bytes());
    while let Some(m) = matches.next() {
        let mut name_node: Option<Node> = None;
        let mut def_node: Option<Node> = None;
        let mut kind_name = "";
        for capture in m.captures {
            let capture_name = defs_query.capture_names()[capture.index as usize];
            if capture_name == "name" {
                name_node = Some(capture.node);
            } else if let Some(kind) = capture_name.strip_prefix("def.") {
                def_node = Some(capture.node);
                kind_name = kind;
            }
        }
        let (Some(name_node), Some(def_node)) = (name_node, def_node) else {
            continue;
        };
        // Mark seen only on success: a node can match several patterns and a
        // rejected fix-up (e.g. Go's type_spec fallback on a struct spec)
        // must not shadow the pattern that accepts it.
        if seen_defs.contains(&(def_node.id(), name_node.id())) {
            continue;
        }
        let Some(pending) = build_symbol(lang, kind_name, def_node, name_node, source) else {
            continue;
        };
        seen_defs.insert((def_node.id(), name_node.id()));
        symbol_ranges.push(def_node.byte_range());
        symbols.push(pending);
    }

    let lines: Vec<&str> = source.lines().collect();
    let mut references: Vec<PendingReference> = Vec::new();
    let mut seen_refs: HashSet<(String, u32)> = HashSet::new();
    let mut matches = cursor.matches(&refs_query, root, source.as_bytes());
    while let Some(m) = matches.next() {
        for capture in m.captures {
            if refs_query.capture_names()[capture.index as usize] != "ref" {
                continue;
            }
            let node = capture.node;
            let name = source[node.byte_range()].to_string();
            let line = node.start_position().row as u32 + 1;
            if !seen_refs.insert((name.clone(), line)) {
                continue;
            }
            references.push(PendingReference {
                symbol_index: enclosing_symbol(&symbol_ranges, node.start_byte()),
                name,
                line,
                text: lines
                    .get((line - 1) as usize)
                    .map(|l| l.trim().to_string())
                    .unwrap_or_default(),
            });
        }
    }

    Ok(Extraction {
        symbols,
        references,
    })
}

/// Applies the kind fix-ups queries cannot express and builds the record.
fn build_symbol(
    lang: Lang,
    kind_name: &str,
    def_node: Node,
    name_node: Node,
    source: &str,
) -> Option<PendingSymbol> {
    let mut kind = match kind_name {
        "function" => SymbolKind::Function,
        "method" => SymbolKind::Method,
        "struct" => SymbolKind::Struct,
        "enum" => SymbolKind::Enum,
        "trait" => SymbolKind::Trait,
        "interface" => SymbolKind::Interface,
        "class" => SymbolKind::Class,
        "const" => SymbolKind::Const,
        "type_alias" => SymbolKind::TypeAlias,
        "module" => SymbolKind::Module,
        "variable" => SymbolKind::Variable,
        _ => return None,
    };

    // Go: the bare type_spec pattern also matches struct/interface specs
    // already captured by their dedicated patterns.
    if lang == Lang::Go && kind == SymbolKind::TypeAlias {
        let type_kind = def_node.child_by_field_name("type").map(|n| n.kind());
        if matches!(type_kind, Some("struct_type" | "interface_type")) {
            return None;
        }
    }

    // TS: index only top-level bindings; `const` bindings are Const.
    if lang == Lang::TypeScript && kind == SymbolKind::Variable {
        let declaration = def_node.parent()?;
        let scope = declaration.parent().map(|n| n.kind());
        if !matches!(scope, Some("program" | "export_statement")) {
            return None;
        }
        if declaration.child(0).map(|n| n.kind()) == Some("const") {
            kind = SymbolKind::Const;
        }
    }

    let name = source[name_node.byte_range()].to_string();
    let mut qualified_name = name.clone();
    let qualifier = match (lang, kind) {
        (Lang::Rust, SymbolKind::Function) | (Lang::Python, SymbolKind::Function) => {
            method_container(lang, def_node, source)
        }
        (Lang::Go, SymbolKind::Method) => def_node
            .child_by_field_name("receiver")
            .and_then(|receiver| first_of_kind(receiver, "type_identifier"))
            .map(|n| source[n.byte_range()].to_string()),
        (Lang::TypeScript, SymbolKind::Method) => method_container(lang, def_node, source),
        _ => None,
    };
    if let Some(qualifier) = qualifier {
        if matches!(kind, SymbolKind::Function) {
            kind = SymbolKind::Method;
        }
        let separator = if lang == Lang::Rust { "::" } else { "." };
        qualified_name = format!("{qualifier}{separator}{name}");
    }

    Some(PendingSymbol {
        name,
        qualified_name,
        kind,
        start_line: def_node.start_position().row as u32 + 1,
        end_line: def_node.end_position().row as u32 + 1,
    })
}

/// Name of the impl/trait/class an ancestor chain puts `def_node` in, if any.
/// Stops at an enclosing function: nested functions are not methods.
fn method_container(lang: Lang, def_node: Node, source: &str) -> Option<String> {
    let container_text = |node: Option<Node>| node.map(|n| source[n.byte_range()].to_string());
    let mut current = def_node.parent();
    while let Some(node) = current {
        match (lang, node.kind()) {
            (Lang::Rust, "function_item" | "closure_expression")
            | (Lang::Python, "function_definition")
            | (
                Lang::TypeScript,
                "function_declaration" | "function_expression" | "arrow_function",
            ) => return None,
            (Lang::Rust, "impl_item") => {
                return container_text(
                    node.child_by_field_name("type")
                        .and_then(|t| first_of_kind(t, "type_identifier")),
                );
            }
            (Lang::Rust, "trait_item")
            | (Lang::Python, "class_definition")
            | (Lang::TypeScript, "class_declaration" | "abstract_class_declaration") => {
                return container_text(node.child_by_field_name("name"));
            }
            _ => {}
        }
        current = node.parent();
    }
    None
}

/// First descendant of `kind` (depth-first), including `node` itself.
fn first_of_kind<'a>(node: Node<'a>, kind: &str) -> Option<Node<'a>> {
    if node.kind() == kind {
        return Some(node);
    }
    let mut cursor = node.walk();
    let children: Vec<Node> = node.children(&mut cursor).collect();
    children.into_iter().find_map(|child| first_of_kind(child, kind))
}

/// Innermost symbol whose defining node contains `byte`.
fn enclosing_symbol(symbol_ranges: &[Range<usize>], byte: usize) -> Option<usize> {
    symbol_ranges
        .iter()
        .enumerate()
        .filter(|(_, range)| range.contains(&byte))
        .min_by_key(|(_, range)| range.end - range.start)
        .map(|(index, _)| index)
}

#[cfg(test)]
mod tests {
    use super::*;

    const RUST_FIXTURE: &str = "\
pub struct Config {
    pub name: String,
}

impl Config {
    pub fn load(path: &str) -> Config {
        parse_file(path)
    }
}

pub fn parse_file(path: &str) -> Config {
    Config { name: path.to_string() }
}

pub enum Mode {
    Fast,
    Slow,
}

pub trait Runner {
    fn run(&self);
}

pub const MAX_RETRIES: u32 = 3;

pub type ConfigMap = Vec<Config>;
";

    const GO_FIXTURE: &str = "\
package server

type Server struct {
\tAddr string
}

func (s *Server) Start() error {
\treturn listen(s.Addr)
}

func listen(addr string) error {
\treturn nil
}

const DefaultAddr = \":8080\"

type Handler interface {
\tHandle() error
}
";

    const TS_FIXTURE: &str = "\
export interface Options {
  depth: number;
}

export class Walker {
  walk(dir: string): string[] {
    return readEntries(dir);
  }
}

export function readEntries(dir: string): string[] {
  return [];
}

export const MAX_DEPTH = 8;

export type Entry = { name: string };
";

    const TSX_FIXTURE: &str = "\
export const App = () => (
  <>
    <Header />
  </>
);

const Header = () => <div className=\"h\" />;

export function afterJsxFn(): number {
  return 1;
}

export class Store {
  get(): string {
    return \"x\";
  }
}
";

    const PY_FIXTURE: &str = "\
class Parser:
    def parse(self, text):
        return tokenize(text)


def tokenize(text):
    return text.split()
";

    fn find<'a>(ex: &'a Extraction, name: &str) -> &'a PendingSymbol {
        ex.symbols
            .iter()
            .find(|s| s.name == name)
            .unwrap_or_else(|| {
                panic!(
                    "missing symbol {name}; got {:?}",
                    ex.symbols.iter().map(|s| &s.name).collect::<Vec<_>>()
                )
            })
    }

    fn assert_span(sym: &PendingSymbol, kind: SymbolKind, start: u32, end: u32) {
        assert_eq!(sym.kind, kind, "kind of {}", sym.name);
        assert_eq!(sym.start_line, start, "start_line of {}", sym.name);
        assert_eq!(sym.end_line, end, "end_line of {}", sym.name);
    }

    fn find_reference<'a>(ex: &'a Extraction, name: &str, line: u32) -> &'a PendingReference {
        ex.references
            .iter()
            .find(|r| r.name == name && r.line == line)
            .unwrap_or_else(|| {
                panic!(
                    "missing reference {name}@{line}; got {:?}",
                    ex.references
                        .iter()
                        .map(|r| (&r.name, r.line))
                        .collect::<Vec<_>>()
                )
            })
    }

    #[test]
    fn rust_fixture_extracts_symbols() {
        let ex = extract(Lang::Rust, "src/config.rs", RUST_FIXTURE).expect("extract");
        assert_span(find(&ex, "Config"), SymbolKind::Struct, 1, 3);
        let load = find(&ex, "load");
        assert_span(load, SymbolKind::Method, 6, 8);
        assert!(
            load.qualified_name.contains("Config"),
            "method qualified_name should carry its type: {}",
            load.qualified_name
        );
        assert_span(find(&ex, "parse_file"), SymbolKind::Function, 11, 13);
        assert_span(find(&ex, "Mode"), SymbolKind::Enum, 15, 18);
        assert_span(find(&ex, "Runner"), SymbolKind::Trait, 20, 22);
        assert_span(find(&ex, "MAX_RETRIES"), SymbolKind::Const, 24, 24);
        assert_span(find(&ex, "ConfigMap"), SymbolKind::TypeAlias, 26, 26);
    }

    #[test]
    fn rust_fixture_extracts_references() {
        let ex = extract(Lang::Rust, "src/config.rs", RUST_FIXTURE).expect("extract");
        let r = find_reference(&ex, "parse_file", 7);
        let enclosing = r.symbol_index.expect("reference has enclosing symbol");
        assert_eq!(ex.symbols[enclosing].name, "load");
        assert!(r.text.contains("parse_file"));
    }

    #[test]
    fn go_fixture_extracts_symbols() {
        let ex = extract(Lang::Go, "server/server.go", GO_FIXTURE).expect("extract");
        assert_span(find(&ex, "Server"), SymbolKind::Struct, 3, 5);
        let start = find(&ex, "Start");
        assert_span(start, SymbolKind::Method, 7, 9);
        assert!(
            start.qualified_name.contains("Server"),
            "method qualified_name should carry its receiver: {}",
            start.qualified_name
        );
        assert_span(find(&ex, "listen"), SymbolKind::Function, 11, 13);
        assert_span(find(&ex, "DefaultAddr"), SymbolKind::Const, 15, 15);
        assert_span(find(&ex, "Handler"), SymbolKind::Interface, 17, 19);
    }

    #[test]
    fn go_fixture_extracts_references() {
        let ex = extract(Lang::Go, "server/server.go", GO_FIXTURE).expect("extract");
        let r = find_reference(&ex, "listen", 8);
        let enclosing = r.symbol_index.expect("reference has enclosing symbol");
        assert_eq!(ex.symbols[enclosing].name, "Start");
    }

    #[test]
    fn typescript_fixture_extracts_symbols() {
        let ex = extract(Lang::TypeScript, "web/walker.ts", TS_FIXTURE).expect("extract");
        assert_span(find(&ex, "Options"), SymbolKind::Interface, 1, 3);
        assert_span(find(&ex, "Walker"), SymbolKind::Class, 5, 9);
        let walk = find(&ex, "walk");
        assert_span(walk, SymbolKind::Method, 6, 8);
        assert!(
            walk.qualified_name.contains("Walker"),
            "method qualified_name should carry its class: {}",
            walk.qualified_name
        );
        assert_span(find(&ex, "readEntries"), SymbolKind::Function, 11, 13);
        assert_span(find(&ex, "MAX_DEPTH"), SymbolKind::Const, 15, 15);
        assert_span(find(&ex, "Entry"), SymbolKind::TypeAlias, 17, 17);
    }

    #[test]
    fn typescript_fixture_extracts_references() {
        let ex = extract(Lang::TypeScript, "web/walker.ts", TS_FIXTURE).expect("extract");
        let r = find_reference(&ex, "readEntries", 7);
        let enclosing = r.symbol_index.expect("reference has enclosing symbol");
        assert_eq!(ex.symbols[enclosing].name, "walk");
    }

    #[test]
    fn tsx_fixture_extracts_symbols_despite_jsx() {
        // .tsx needs the TSX grammar: the plain TypeScript grammar parses JSX
        // into ERROR nodes and silently drops every symbol after it.
        let ex = extract(Lang::TypeScript, "web/app.tsx", TSX_FIXTURE).expect("extract");
        assert_span(find(&ex, "App"), SymbolKind::Const, 1, 5);
        assert_span(find(&ex, "Header"), SymbolKind::Const, 7, 7);
        assert_span(find(&ex, "afterJsxFn"), SymbolKind::Function, 9, 11);
        assert_span(find(&ex, "Store"), SymbolKind::Class, 13, 17);
        let get = find(&ex, "get");
        assert_eq!(get.kind, SymbolKind::Method, "kind of get");
        assert!(
            get.qualified_name.contains("Store"),
            "method qualified_name should carry its class: {}",
            get.qualified_name
        );
    }

    #[test]
    fn python_fixture_extracts_symbols() {
        let ex = extract(Lang::Python, "tools/parser.py", PY_FIXTURE).expect("extract");
        assert_span(find(&ex, "Parser"), SymbolKind::Class, 1, 3);
        let parse = find(&ex, "parse");
        assert_span(parse, SymbolKind::Method, 2, 3);
        assert!(
            parse.qualified_name.contains("Parser"),
            "method qualified_name should carry its class: {}",
            parse.qualified_name
        );
        assert_span(find(&ex, "tokenize"), SymbolKind::Function, 6, 7);
    }

    #[test]
    fn python_fixture_extracts_references() {
        let ex = extract(Lang::Python, "tools/parser.py", PY_FIXTURE).expect("extract");
        let r = find_reference(&ex, "tokenize", 3);
        let enclosing = r.symbol_index.expect("reference has enclosing symbol");
        assert_eq!(ex.symbols[enclosing].name, "parse");
    }
}
