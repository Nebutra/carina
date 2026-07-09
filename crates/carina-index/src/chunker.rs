//! AST-aware chunking.
//!
//! Chunks follow symbol boundaries: each symbol becomes its own chunk (split
//! at `max_lines` when oversized, every piece keeping the symbol link) and
//! top-level gaps between symbols become file-level chunks. Together the
//! chunks cover every source line exactly once, in order.

use crate::extract::PendingSymbol;

#[derive(Debug, Clone, PartialEq)]
pub(crate) struct Chunk {
    pub start_line: u32,
    pub end_line: u32,
    pub content: String,
    /// Index into `Extraction::symbols`; None = file-level gap chunk.
    pub symbol_index: Option<usize>,
}

pub(crate) fn chunk(source: &str, symbols: &[PendingSymbol], max_lines: usize) -> Vec<Chunk> {
    let lines: Vec<&str> = source.lines().collect();
    let total = lines.len() as u32;
    if total == 0 {
        return Vec::new();
    }
    let max_lines = max_lines.max(1) as u32;

    // Outermost non-overlapping symbol spans in line order: nested symbols
    // (e.g. methods inside a class) stay inside their parent's chunk.
    let mut spans: Vec<(usize, u32, u32)> = symbols
        .iter()
        .enumerate()
        .filter(|(_, s)| s.start_line >= 1 && s.start_line <= s.end_line && s.start_line <= total)
        .map(|(index, s)| (index, s.start_line, s.end_line.min(total)))
        .collect();
    // Sort by start ascending, then longest span first so the outermost wins.
    spans.sort_by(|a, b| a.1.cmp(&b.1).then(b.2.cmp(&a.2)));
    let mut owners: Vec<(usize, u32, u32)> = Vec::new();
    let mut covered_end = 0u32;
    for (index, start, end) in spans {
        if start > covered_end {
            owners.push((index, start, end));
            covered_end = end;
        }
    }

    // Emit [start, end] as chunks of at most `max_lines`, in order.
    let mut chunks: Vec<Chunk> = Vec::new();
    let emit = |chunks: &mut Vec<Chunk>, start: u32, end: u32, symbol_index: Option<usize>| {
        let mut piece_start = start;
        while piece_start <= end {
            let piece_end = end.min(piece_start + max_lines - 1);
            chunks.push(Chunk {
                start_line: piece_start,
                end_line: piece_end,
                content: lines[(piece_start - 1) as usize..piece_end as usize].join("\n"),
                symbol_index,
            });
            piece_start = piece_end + 1;
        }
    };
    let mut cursor = 1u32;
    for (index, start, end) in owners {
        if cursor < start {
            emit(&mut chunks, cursor, start - 1, None);
        }
        emit(&mut chunks, start, end, Some(index));
        cursor = end + 1;
    }
    if cursor <= total {
        emit(&mut chunks, cursor, total, None);
    }
    chunks
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::extract::SymbolKind;

    fn numbered_source(lines: usize) -> String {
        (1..=lines).map(|i| format!("line {i}\n")).collect()
    }

    fn sym(name: &str, start: u32, end: u32) -> PendingSymbol {
        PendingSymbol {
            name: name.into(),
            qualified_name: name.into(),
            kind: SymbolKind::Function,
            start_line: start,
            end_line: end,
        }
    }

    /// Which chunk covers `line`, if any.
    fn owner(chunks: &[Chunk], line: u32) -> Option<&Chunk> {
        chunks
            .iter()
            .find(|c| c.start_line <= line && line <= c.end_line)
    }

    #[test]
    fn chunks_cover_every_line_exactly_once() {
        let src = numbered_source(30);
        let chunks = chunk(&src, &[sym("a", 5, 15), sym("b", 20, 25)], 120);
        assert!(!chunks.is_empty());
        let mut expected_start = 1u32;
        for c in &chunks {
            assert_eq!(c.start_line, expected_start, "chunks must be contiguous");
            assert!(c.end_line >= c.start_line);
            expected_start = c.end_line + 1;
        }
        assert_eq!(expected_start, 31, "chunks must cover all 30 lines");
    }

    #[test]
    fn no_chunk_splits_a_symbol() {
        let src = numbered_source(30);
        let symbols = [sym("a", 5, 15), sym("b", 20, 25)];
        let chunks = chunk(&src, &symbols, 120);
        for c in &chunks {
            for s in &symbols {
                let overlaps = c.start_line <= s.end_line && c.end_line >= s.start_line;
                if overlaps {
                    assert!(
                        c.start_line >= s.start_line && c.end_line <= s.end_line,
                        "chunk {}-{} straddles symbol {} ({}-{})",
                        c.start_line,
                        c.end_line,
                        s.name,
                        s.start_line,
                        s.end_line
                    );
                }
            }
        }
    }

    #[test]
    fn chunks_carry_symbol_context() {
        let src = numbered_source(30);
        let chunks = chunk(&src, &[sym("a", 5, 15), sym("b", 20, 25)], 120);
        assert_eq!(owner(&chunks, 10).expect("line 10 covered").symbol_index, Some(0));
        assert_eq!(owner(&chunks, 22).expect("line 22 covered").symbol_index, Some(1));
    }

    #[test]
    fn gaps_become_file_level_chunks() {
        let src = numbered_source(30);
        let chunks = chunk(&src, &[sym("a", 5, 15), sym("b", 20, 25)], 120);
        for line in [1u32, 4, 16, 19, 26, 30] {
            let c = owner(&chunks, line)
                .unwrap_or_else(|| panic!("gap line {line} must be covered"));
            assert_eq!(c.symbol_index, None, "gap line {line} must be file-level");
        }
    }

    #[test]
    fn oversized_symbol_splits_at_max_lines() {
        let src = numbered_source(300);
        let chunks = chunk(&src, &[sym("big", 1, 300)], 120);
        assert!(chunks.len() >= 3, "300 lines / 120 max needs >= 3 chunks");
        let mut expected_start = 1u32;
        for c in &chunks {
            assert_eq!(c.start_line, expected_start);
            let len = (c.end_line - c.start_line + 1) as usize;
            assert!(len <= 120, "chunk {}-{} exceeds max_lines", c.start_line, c.end_line);
            assert_eq!(
                c.symbol_index,
                Some(0),
                "every split piece keeps the symbol link"
            );
            expected_start = c.end_line + 1;
        }
        assert_eq!(expected_start, 301);
    }

    #[test]
    fn chunk_content_matches_source_lines() {
        let src = numbered_source(30);
        let lines: Vec<&str> = src.lines().collect();
        let chunks = chunk(&src, &[sym("a", 5, 15)], 120);
        for c in &chunks {
            let expected: Vec<&str> =
                lines[(c.start_line - 1) as usize..c.end_line as usize].to_vec();
            let got: Vec<&str> = c.content.lines().collect();
            assert_eq!(got, expected, "chunk {}-{}", c.start_line, c.end_line);
        }
    }

    #[test]
    fn empty_source_produces_no_chunks() {
        assert!(chunk("", &[], 120).is_empty());
    }
}
