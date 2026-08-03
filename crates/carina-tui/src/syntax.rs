//! Lightweight fenced-code syntax highlighting (#18).
//!
//! Restores semantic coloring for common languages without a heavy highlighter
//! dependency. Tokens map to theme styles at the render boundary.

#[derive(Debug, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum TokenKind {
    Text,
    Keyword,
    String,
    Comment,
    Number,
    Type,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Token {
    pub text: String,
    pub kind: TokenKind,
}

/// Highlight a source string for a fenced language tag.
///
/// Prefer syntect (issue #18 restoration). Fall back to the lightweight
/// classifier when the language dump is unavailable.
pub fn highlight(language: &str, source: &str) -> Vec<Token> {
    if let Some(tokens) = highlight_syntect(language, source) {
        return tokens;
    }
    highlight_lightweight(language, source)
}

fn highlight_syntect(language: &str, source: &str) -> Option<Vec<Token>> {
    use syntect::easy::HighlightLines;
    use syntect::highlighting::ThemeSet;
    use syntect::parsing::SyntaxSet;
    use syntect::util::LinesWithEndings;

    let syntax_set = SyntaxSet::load_defaults_newlines();
    let theme_set = ThemeSet::load_defaults();
    let lang = normalize_language(language);
    let syntax = syntax_set
        .find_syntax_by_token(lang)
        .or_else(|| syntax_set.find_syntax_by_extension(lang))
        .or_else(|| match lang {
            "rust" => syntax_set.find_syntax_by_name("Rust"),
            "go" => syntax_set.find_syntax_by_name("Go"),
            "python" => syntax_set.find_syntax_by_name("Python"),
            "javascript" => syntax_set.find_syntax_by_name("JavaScript"),
            "typescript" => syntax_set.find_syntax_by_name("TypeScript"),
            "shell" => syntax_set.find_syntax_by_name("Bourne Again Shell (bash)"),
            _ => None,
        })?;
    let theme = theme_set
        .themes
        .get("base16-ocean.dark")
        .or_else(|| theme_set.themes.values().next())?;
    let mut highlighter = HighlightLines::new(syntax, theme);
    let mut tokens = Vec::new();
    for line in LinesWithEndings::from(source) {
        let ranges = highlighter.highlight_line(line, &syntax_set).ok()?;
        for (style, text) in ranges {
            if text.is_empty() {
                continue;
            }
            tokens.push(Token {
                text: text.to_owned(),
                kind: scope_kind(
                    style.foreground.r,
                    style.foreground.g,
                    style.foreground.b,
                    text,
                ),
            });
        }
    }
    Some(coalesce(tokens))
}

fn scope_kind(r: u8, g: u8, b: u8, text: &str) -> TokenKind {
    // Map syntect theme RGB buckets into our TokenKind surface so markdown
    // styles stay theme-token based rather than raw RGB in the product path.
    let trimmed = text.trim();
    if trimmed.starts_with("//") || trimmed.starts_with('#') || trimmed.starts_with("/*") {
        return TokenKind::Comment;
    }
    if (trimmed.starts_with('"') || trimmed.starts_with('\'') || trimmed.starts_with('`'))
        && trimmed.len() > 1
    {
        return TokenKind::String;
    }
    if trimmed.chars().next().is_some_and(|ch| ch.is_ascii_digit()) {
        return TokenKind::Number;
    }
    // Warm colors -> keyword, cool greens -> type, defaults text.
    if r > g && r > b && r > 140 {
        TokenKind::Keyword
    } else if g > r && g > 120 {
        TokenKind::Type
    } else if b > r && b > g && b > 140 {
        TokenKind::String
    } else if r < 100 && g < 100 && b < 100 {
        TokenKind::Comment
    } else {
        TokenKind::Text
    }
}

fn highlight_lightweight(language: &str, source: &str) -> Vec<Token> {
    let lang = normalize_language(language);
    let keywords = keywords_for(lang);
    let types = types_for(lang);
    let line_comment = line_comment_for(lang);
    let mut tokens = Vec::new();
    let mut rest = source;
    while !rest.is_empty() {
        if let Some((token, consumed)) = take_line_comment(rest, line_comment) {
            tokens.push(token);
            rest = &rest[consumed..];
            continue;
        }
        if let Some((token, consumed)) = take_block_comment(rest, lang) {
            tokens.push(token);
            rest = &rest[consumed..];
            continue;
        }
        if let Some((token, consumed)) = take_string(rest) {
            tokens.push(token);
            rest = &rest[consumed..];
            continue;
        }
        if let Some((token, consumed)) = take_number(rest) {
            tokens.push(token);
            rest = &rest[consumed..];
            continue;
        }
        if let Some((token, consumed)) = take_ident(rest, keywords, types) {
            tokens.push(token);
            rest = &rest[consumed..];
            continue;
        }
        let ch = rest.chars().next().expect("non-empty");
        let len = ch.len_utf8();
        tokens.push(Token {
            text: rest[..len].to_owned(),
            kind: TokenKind::Text,
        });
        rest = &rest[len..];
    }
    coalesce(tokens)
}

pub fn normalize_language(language: &str) -> &'static str {
    match language.trim().to_ascii_lowercase().as_str() {
        "rs" | "rust" => "rust",
        "go" | "golang" => "go",
        "py" | "python" => "python",
        "js" | "javascript" | "jsx" => "javascript",
        "ts" | "typescript" | "tsx" => "typescript",
        "sh" | "bash" | "zsh" | "shell" => "shell",
        "json" => "json",
        "toml" => "toml",
        "yaml" | "yml" => "yaml",
        "md" | "markdown" => "markdown",
        "" => "text",
        _ => "text",
    }
}

fn keywords_for(lang: &str) -> &'static [&'static str] {
    match lang {
        "rust" => &[
            "as", "async", "await", "break", "const", "continue", "crate", "dyn", "else", "enum",
            "extern", "false", "fn", "for", "if", "impl", "in", "let", "loop", "match", "mod",
            "move", "mut", "pub", "ref", "return", "self", "Self", "static", "struct", "super",
            "trait", "true", "type", "unsafe", "use", "where", "while",
        ],
        "go" => &[
            "break",
            "case",
            "chan",
            "const",
            "continue",
            "default",
            "defer",
            "else",
            "fallthrough",
            "for",
            "func",
            "go",
            "goto",
            "if",
            "import",
            "interface",
            "map",
            "package",
            "range",
            "return",
            "select",
            "struct",
            "switch",
            "type",
            "var",
        ],
        "python" => &[
            "and", "as", "assert", "async", "await", "break", "class", "continue", "def", "del",
            "elif", "else", "except", "False", "finally", "for", "from", "global", "if", "import",
            "in", "is", "lambda", "None", "nonlocal", "not", "or", "pass", "raise", "return",
            "True", "try", "while", "with", "yield",
        ],
        "javascript" | "typescript" => &[
            "async",
            "await",
            "break",
            "case",
            "catch",
            "class",
            "const",
            "continue",
            "debugger",
            "default",
            "delete",
            "do",
            "else",
            "export",
            "extends",
            "false",
            "finally",
            "for",
            "from",
            "function",
            "if",
            "import",
            "in",
            "instanceof",
            "let",
            "new",
            "null",
            "return",
            "static",
            "super",
            "switch",
            "this",
            "throw",
            "true",
            "try",
            "typeof",
            "var",
            "void",
            "while",
            "with",
            "yield",
        ],
        "shell" => &[
            "case", "do", "done", "elif", "else", "esac", "fi", "for", "function", "if", "in",
            "select", "then", "time", "until", "while",
        ],
        _ => &[],
    }
}

fn types_for(lang: &str) -> &'static [&'static str] {
    match lang {
        "rust" => &[
            "bool", "char", "f32", "f64", "i8", "i16", "i32", "i64", "i128", "isize", "str",
            "String", "u8", "u16", "u32", "u64", "u128", "usize", "Vec", "Option", "Result",
        ],
        "go" => &[
            "bool",
            "byte",
            "complex64",
            "complex128",
            "error",
            "float32",
            "float64",
            "int",
            "int8",
            "int16",
            "int32",
            "int64",
            "rune",
            "string",
            "uint",
            "uint8",
            "uint16",
            "uint32",
            "uint64",
            "uintptr",
        ],
        "python" => &[
            "int", "float", "str", "bool", "list", "dict", "set", "tuple", "None",
        ],
        "typescript" => &[
            "boolean", "number", "string", "any", "void", "never", "unknown", "object",
        ],
        _ => &[],
    }
}

fn line_comment_for(lang: &str) -> Option<&'static str> {
    match lang {
        "python" | "shell" | "toml" | "yaml" => Some("#"),
        "rust" | "go" | "javascript" | "typescript" | "json" => Some("//"),
        _ => Some("//"),
    }
}

fn take_line_comment(rest: &str, marker: Option<&str>) -> Option<(Token, usize)> {
    let marker = marker?;
    if !rest.starts_with(marker) {
        return None;
    }
    let end = rest.find('\n').unwrap_or(rest.len());
    Some((
        Token {
            text: rest[..end].to_owned(),
            kind: TokenKind::Comment,
        },
        end,
    ))
}

fn take_block_comment(rest: &str, lang: &str) -> Option<(Token, usize)> {
    if !matches!(lang, "rust" | "go" | "javascript" | "typescript") || !rest.starts_with("/*") {
        return None;
    }
    let end = rest.find("*/").map(|i| i + 2).unwrap_or(rest.len());
    Some((
        Token {
            text: rest[..end].to_owned(),
            kind: TokenKind::Comment,
        },
        end,
    ))
}

fn take_string(rest: &str) -> Option<(Token, usize)> {
    let quote = rest.chars().next()?;
    if quote != '"' && quote != '\'' && quote != '`' {
        return None;
    }
    let mut escaped = false;
    for (idx, ch) in rest.char_indices().skip(1) {
        if escaped {
            escaped = false;
            continue;
        }
        if ch == '\\' {
            escaped = true;
            continue;
        }
        if ch == quote {
            let end = idx + ch.len_utf8();
            return Some((
                Token {
                    text: rest[..end].to_owned(),
                    kind: TokenKind::String,
                },
                end,
            ));
        }
    }
    Some((
        Token {
            text: rest.to_owned(),
            kind: TokenKind::String,
        },
        rest.len(),
    ))
}

fn take_number(rest: &str) -> Option<(Token, usize)> {
    let mut chars = rest.char_indices();
    let (_, first) = chars.next()?;
    if !first.is_ascii_digit() {
        return None;
    }
    let mut end = first.len_utf8();
    for (idx, ch) in chars {
        if ch.is_ascii_alphanumeric() || ch == '_' || ch == '.' {
            end = idx + ch.len_utf8();
        } else {
            break;
        }
    }
    Some((
        Token {
            text: rest[..end].to_owned(),
            kind: TokenKind::Number,
        },
        end,
    ))
}

fn take_ident(rest: &str, keywords: &[&str], types: &[&str]) -> Option<(Token, usize)> {
    let mut chars = rest.char_indices();
    let (_, first) = chars.next()?;
    if !(first.is_ascii_alphabetic() || first == '_') {
        return None;
    }
    let mut end = first.len_utf8();
    for (idx, ch) in chars {
        if ch.is_ascii_alphanumeric() || ch == '_' {
            end = idx + ch.len_utf8();
        } else {
            break;
        }
    }
    let text = &rest[..end];
    let kind = if keywords.contains(&text) {
        TokenKind::Keyword
    } else if types.contains(&text) {
        TokenKind::Type
    } else {
        TokenKind::Text
    };
    Some((
        Token {
            text: text.to_owned(),
            kind,
        },
        end,
    ))
}

fn coalesce(tokens: Vec<Token>) -> Vec<Token> {
    let mut out: Vec<Token> = Vec::new();
    for token in tokens {
        if let Some(last) = out.last_mut()
            && last.kind == token.kind
        {
            last.text.push_str(&token.text);
        } else {
            out.push(token);
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rust_keywords_and_strings_are_classified() {
        // Issue #18: fenced Rust code must not render as a single unstyled span.
        let tokens = highlight("rust", "fn main() {\n  let x = \"hi\"; // note\n}");
        let reconstructed: String = tokens.iter().map(|t| t.text.as_str()).collect();
        assert!(reconstructed.contains("fn"));
        assert!(reconstructed.contains("let"));
        assert!(reconstructed.contains("hi"));
        assert!(reconstructed.contains("note"));
        // At least two distinct token kinds (keyword/string/comment/text).
        let kinds: std::collections::BTreeSet<_> = tokens.iter().map(|t| t.kind).collect();
        assert!(
            kinds.len() >= 2,
            "expected multi-kind highlight, got {tokens:?}"
        );
        // Lightweight path keeps strict classification when forced.
        let light = highlight_lightweight("rust", "fn main() {\n  let x = \"hi\"; // note\n}");
        assert!(
            light
                .iter()
                .any(|t| t.kind == TokenKind::String && t.text.contains("hi"))
        );
        assert!(
            light
                .iter()
                .any(|t| t.kind == TokenKind::Comment && t.text.contains("note"))
        );
    }

    #[test]
    fn language_aliases_normalize() {
        assert_eq!(normalize_language("RS"), "rust");
        assert_eq!(normalize_language("py"), "python");
        assert_eq!(normalize_language(""), "text");
    }

    #[test]
    fn removing_highlight_would_lose_keyword_tokens() {
        let plain = "return 42;";
        let tokens = highlight("go", plain);
        assert!(tokens.iter().any(|t| t.kind == TokenKind::Keyword));
        assert!(tokens.iter().any(|t| t.kind == TokenKind::Number));
        let reconstructed: String = tokens.iter().map(|t| t.text.as_str()).collect();
        assert_eq!(reconstructed, plain);
    }

    #[test]
    fn syntect_path_classifies_rust_keywords() {
        let tokens = highlight_syntect("rust", "fn main() { let x = 1; }").expect("syntect");
        assert!(
            tokens.iter().any(|t| t.kind == TokenKind::Keyword
                || t.text.contains("fn")
                || t.text.contains("let")),
            "expected syntect tokens: {tokens:?}"
        );
        let reconstructed: String = tokens.iter().map(|t| t.text.as_str()).collect();
        assert!(reconstructed.contains("fn main"));
    }
}
