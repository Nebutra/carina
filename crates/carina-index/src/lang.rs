//! Language detection and the tree-sitter grammar registry.
//!
//! V1 grammars: rust, go, typescript, python — versions pinned in the
//! workspace manifest for tree-sitter ABI compatibility. Only this crate
//! depends on the grammar crates, keeping them off the workspace hot path.

use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Lang {
    Rust,
    Go,
    TypeScript,
    Python,
}

impl Lang {
    /// Detects by extension: .rs / .go / .ts .tsx / .py (None = not indexed).
    pub fn from_path(path: &str) -> Option<Lang> {
        let extension = std::path::Path::new(path).extension()?.to_str()?;
        match extension {
            "rs" => Some(Lang::Rust),
            "go" => Some(Lang::Go),
            "ts" | "tsx" => Some(Lang::TypeScript),
            "py" => Some(Lang::Python),
            _ => None,
        }
    }

    pub(crate) fn ts_language(self) -> tree_sitter::Language {
        match self {
            Lang::Rust => tree_sitter_rust::LANGUAGE.into(),
            Lang::Go => tree_sitter_go::LANGUAGE.into(),
            Lang::TypeScript => tree_sitter_typescript::LANGUAGE_TYPESCRIPT.into(),
            Lang::Python => tree_sitter_python::LANGUAGE.into(),
        }
    }

    /// Grammar for `path` within this language: `.tsx` needs the TSX dialect —
    /// the plain TypeScript grammar parses JSX into ERROR nodes and silently
    /// drops the symbols around it.
    pub(crate) fn grammar_for_path(self, path: &str) -> tree_sitter::Language {
        let extension = std::path::Path::new(path).extension().and_then(|e| e.to_str());
        if self == Lang::TypeScript && extension == Some("tsx") {
            return tree_sitter_typescript::LANGUAGE_TSX.into();
        }
        self.ts_language()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detects_rust_go_typescript_python_extensions() {
        assert_eq!(Lang::from_path("src/lib.rs"), Some(Lang::Rust));
        assert_eq!(Lang::from_path("go/daemon/agent.go"), Some(Lang::Go));
        assert_eq!(Lang::from_path("web/app.ts"), Some(Lang::TypeScript));
        assert_eq!(Lang::from_path("web/App.tsx"), Some(Lang::TypeScript));
        assert_eq!(Lang::from_path("tools/gen.py"), Some(Lang::Python));
    }

    #[test]
    fn rejects_unknown_extensions() {
        assert_eq!(Lang::from_path("README.md"), None);
        assert_eq!(Lang::from_path("Makefile"), None);
        assert_eq!(Lang::from_path("archive.rss"), None);
        assert_eq!(Lang::from_path("noext"), None);
        assert_eq!(Lang::from_path(""), None);
    }

    #[test]
    fn serializes_lowercase() {
        assert_eq!(serde_json::to_string(&Lang::Rust).unwrap(), "\"rust\"");
        assert_eq!(serde_json::to_string(&Lang::Go).unwrap(), "\"go\"");
        assert_eq!(
            serde_json::to_string(&Lang::TypeScript).unwrap(),
            "\"typescript\""
        );
        assert_eq!(serde_json::to_string(&Lang::Python).unwrap(), "\"python\"");
    }

    #[test]
    fn deserializes_lowercase() {
        assert_eq!(serde_json::from_str::<Lang>("\"go\"").unwrap(), Lang::Go);
        assert_eq!(
            serde_json::from_str::<Lang>("\"typescript\"").unwrap(),
            Lang::TypeScript
        );
    }

    #[test]
    fn tsx_paths_use_the_tsx_grammar() {
        let mut parser = tree_sitter::Parser::new();
        parser
            .set_language(&Lang::TypeScript.grammar_for_path("web/App.tsx"))
            .expect("tsx grammar");
        let tree = parser
            .parse("const A = () => <div />;\n", None)
            .expect("parse tsx");
        assert!(!tree.root_node().has_error(), "JSX must parse without errors");
        parser
            .set_language(&Lang::TypeScript.grammar_for_path("web/app.ts"))
            .expect("ts grammar");
        let tree = parser
            .parse("const x: number = 1;\n", None)
            .expect("parse ts");
        assert!(!tree.root_node().has_error());
    }

    #[test]
    fn grammar_registry_loads_all_four_grammars() {
        for lang in [Lang::Rust, Lang::Go, Lang::TypeScript, Lang::Python] {
            let mut parser = tree_sitter::Parser::new();
            parser
                .set_language(&lang.ts_language())
                .unwrap_or_else(|e| panic!("grammar for {lang:?} failed to load: {e}"));
            let tree = parser.parse("", None).expect("parse empty source");
            assert_eq!(tree.root_node().end_byte(), 0);
        }
    }
}
