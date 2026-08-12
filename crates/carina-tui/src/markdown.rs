use crate::glyphs::Glyphs;
use crate::syntax::{self, TokenKind};
use pulldown_cmark::{Alignment, CodeBlockKind, Event, HeadingLevel, Options, Parser, Tag, TagEnd};
use ratatui::style::{Modifier, Style};
use ratatui::text::{Line, Span};
use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthStr;

#[derive(Debug, Clone, Copy)]
pub struct MarkdownTheme {
    pub glyphs: Glyphs,
    pub text: Style,
    pub accent: Style,
    pub muted: Style,
    pub code: Style,
    pub quote: Style,
    pub link: Style,
    pub headings: [Style; 6],
    /// Keyword style for fenced code (#18). Defaults to `accent` when unset via helper.
    pub code_keyword: Style,
    pub code_string: Style,
    pub code_comment: Style,
    pub code_number: Style,
    pub code_type: Style,
}

impl MarkdownTheme {
    /// Fill syntax colors from the base code/accent/muted styles when callers
    /// only set the classic fields.
    pub fn with_default_syntax(mut self) -> Self {
        if self.code_keyword == Style::default() {
            self.code_keyword = self.accent;
        }
        if self.code_string == Style::default() {
            self.code_string = self.code;
        }
        if self.code_comment == Style::default() {
            self.code_comment = self.muted.add_modifier(Modifier::ITALIC);
        }
        if self.code_number == Style::default() {
            self.code_number = self.code;
        }
        if self.code_type == Style::default() {
            self.code_type = self.accent.add_modifier(Modifier::ITALIC);
        }
        self
    }
}

#[derive(Debug, Clone, Default)]
pub struct StyledPrefix {
    initial: Vec<Span<'static>>,
    continuation: Vec<Span<'static>>,
}

impl StyledPrefix {
    pub fn hanging(initial: Vec<Span<'static>>, continuation_style: Style) -> Self {
        let width = spans_width(&initial);
        Self::hanging_with_width(initial, width, continuation_style)
    }

    pub fn hanging_with_width(
        initial: Vec<Span<'static>>,
        continuation_width: usize,
        continuation_style: Style,
    ) -> Self {
        debug_assert!(continuation_width <= spans_width(&initial));
        Self {
            initial,
            continuation: vec![Span::styled(
                " ".repeat(continuation_width),
                continuation_style,
            )],
        }
    }

    pub fn repeating(prefix: Vec<Span<'static>>) -> Self {
        Self {
            initial: prefix.clone(),
            continuation: prefix,
        }
    }

    pub fn width(&self) -> usize {
        spans_width(&self.initial)
    }

    pub fn continuation_width(&self) -> usize {
        spans_width(&self.continuation)
    }
}

#[derive(Debug)]
struct ListState {
    next: Option<u64>,
}

#[derive(Debug, Default)]
struct Table {
    alignments: Vec<Alignment>,
    rows: Vec<Vec<Vec<Span<'static>>>>,
    header_rows: usize,
}

pub fn render(input: &str, width: u16, theme: MarkdownTheme) -> Vec<Line<'static>> {
    render_prefixed(input, width, theme, &StyledPrefix::default())
}

pub fn render_prefixed(
    input: &str,
    width: u16,
    theme: MarkdownTheme,
    prefix: &StyledPrefix,
) -> Vec<Line<'static>> {
    let width = usize::from(width.max(1));
    let parser = Parser::new_ext(
        input,
        Options::ENABLE_STRIKETHROUGH | Options::ENABLE_TASKLISTS | Options::ENABLE_TABLES,
    );
    let theme = theme.with_default_syntax();
    let mut out = MarkdownWriter {
        lines: vec![Line::default()],
        styles: vec![theme.text],
        lists: Vec::new(),
        quote_depth: 0,
        code_block: false,
        code_language: String::new(),
        code_buffer: String::new(),
        table: None,
        width: width.saturating_sub(prefix.continuation_width()).max(1),
        theme,
    };
    for event in parser {
        out.event(event);
    }
    while out.lines.last().is_some_and(|line| line.spans.is_empty()) {
        out.lines.pop();
    }
    if out.lines.is_empty() {
        out.lines.push(Line::default());
    }
    wrap_styled_lines(out.lines, width, prefix)
}

pub fn wrap_styled_lines(
    lines: impl IntoIterator<Item = Line<'static>>,
    width: usize,
    prefix: &StyledPrefix,
) -> Vec<Line<'static>> {
    let width = width.max(1);
    let mut first_logical_line = true;
    lines
        .into_iter()
        .flat_map(|line| {
            let initial = if first_logical_line {
                &prefix.initial
            } else {
                &prefix.continuation
            };
            first_logical_line = false;
            wrap_spans_prefixed(&line.spans, width, initial, &prefix.continuation)
                .into_iter()
                .map(Line::from)
        })
        .collect()
}

struct MarkdownWriter {
    lines: Vec<Line<'static>>,
    styles: Vec<Style>,
    lists: Vec<ListState>,
    quote_depth: usize,
    code_block: bool,
    code_language: String,
    code_buffer: String,
    table: Option<Table>,
    width: usize,
    theme: MarkdownTheme,
}

impl MarkdownWriter {
    fn event(&mut self, event: Event<'_>) {
        if self.table_event(&event) {
            return;
        }
        match event {
            Event::Start(tag) => self.start(tag),
            Event::End(tag) => self.end(tag),
            Event::Text(text) => self.text(&text),
            Event::Code(code) => self.push(code.into_string(), self.theme.code),
            Event::Html(html) | Event::InlineHtml(html) => self.text(&html),
            Event::SoftBreak | Event::HardBreak => self.newline(),
            Event::Rule => {
                self.finish_paragraph();
                self.push(
                    self.theme.glyphs.rule().repeat(self.width.min(24)),
                    self.theme.muted,
                );
                self.newline();
            }
            Event::TaskListMarker(checked) => self.push(
                if checked { "[x] " } else { "[ ] " }.into(),
                self.theme.accent,
            ),
            Event::FootnoteReference(reference) => {
                self.push(format!("[{reference}]"), self.theme.accent)
            }
            Event::InlineMath(math) | Event::DisplayMath(math) => {
                self.push(math.into_string(), self.theme.code)
            }
        }
    }

    fn start(&mut self, tag: Tag<'_>) {
        match tag {
            Tag::Table(alignments) => {
                self.finish_paragraph();
                self.table = Some(Table {
                    alignments,
                    ..Table::default()
                });
            }
            Tag::Paragraph => self.ensure_content_line(),
            Tag::Heading { level, .. } => {
                self.ensure_content_line();
                let style = self.theme.headings[match level {
                    HeadingLevel::H1 => 0,
                    HeadingLevel::H2 => 1,
                    HeadingLevel::H3 => 2,
                    HeadingLevel::H4 => 3,
                    HeadingLevel::H5 => 4,
                    HeadingLevel::H6 => 5,
                }];
                self.styles.push(style);
            }
            Tag::BlockQuote(_) => self.quote_depth += 1,
            Tag::CodeBlock(kind) => {
                self.finish_paragraph();
                self.code_block = true;
                self.code_buffer.clear();
                self.code_language.clear();
                if let CodeBlockKind::Fenced(language) = kind {
                    let language = language.into_string();
                    if !language.trim().is_empty() {
                        self.code_language = language.clone();
                        self.push(language, self.theme.muted);
                        self.newline();
                    }
                }
            }
            Tag::List(start) => self.lists.push(ListState { next: start }),
            Tag::Item => {
                self.ensure_content_line();
                let depth = self.lists.len().saturating_sub(1);
                let prefix = match self.lists.last_mut().and_then(|list| list.next.as_mut()) {
                    Some(next) => {
                        let prefix = format!("{next}. ");
                        *next += 1;
                        prefix
                    }
                    None => format!("{} ", self.theme.glyphs.bullet()),
                };
                self.push(format!("{}{prefix}", "  ".repeat(depth)), self.theme.accent);
            }
            Tag::Emphasis => self
                .styles
                .push(self.current_style().add_modifier(Modifier::ITALIC)),
            Tag::Strong => self
                .styles
                .push(self.current_style().add_modifier(Modifier::BOLD)),
            Tag::Strikethrough => self
                .styles
                .push(self.current_style().add_modifier(Modifier::CROSSED_OUT)),
            Tag::Link { .. } => self.styles.push(self.theme.link),
            Tag::Image { .. } => self.styles.push(self.theme.muted),
            _ => {}
        }
    }

    fn end(&mut self, tag: TagEnd) {
        match tag {
            TagEnd::Table => {
                if let Some(table) = self.table.take() {
                    self.render_table(table);
                    self.finish_paragraph();
                }
            }
            TagEnd::Paragraph => self.finish_paragraph(),
            TagEnd::Heading(_) => {
                self.styles.pop();
                self.finish_paragraph();
            }
            TagEnd::BlockQuote(_) => {
                self.quote_depth = self.quote_depth.saturating_sub(1);
                self.finish_paragraph();
            }
            TagEnd::CodeBlock => {
                self.flush_code_buffer();
                self.code_block = false;
                self.code_language.clear();
                self.finish_paragraph();
            }
            TagEnd::List(_) => {
                self.lists.pop();
                self.finish_paragraph();
            }
            TagEnd::Item => self.newline_if_content(),
            TagEnd::Emphasis
            | TagEnd::Strong
            | TagEnd::Strikethrough
            | TagEnd::Link
            | TagEnd::Image => {
                self.styles.pop();
            }
            _ => {}
        }
    }

    fn table_event(&mut self, event: &Event<'_>) -> bool {
        if self.table.is_none() {
            return false;
        }
        match event {
            Event::End(TagEnd::Table) => return false,
            Event::Start(Tag::TableHead) => self
                .table
                .as_mut()
                .expect("table exists")
                .rows
                .push(Vec::new()),
            Event::End(TagEnd::TableHead) => {
                let table = self.table.as_mut().expect("table exists");
                table.header_rows = table.rows.len();
            }
            Event::Start(Tag::TableRow) => self
                .table
                .as_mut()
                .expect("table exists")
                .rows
                .push(Vec::new()),
            Event::Start(Tag::TableCell) => {
                if let Some(row) = self.table.as_mut().expect("table exists").rows.last_mut() {
                    row.push(Vec::new());
                }
            }
            Event::Text(text) => self.push_table(text.to_string(), self.current_style()),
            Event::Code(code) => self.push_table(code.to_string(), self.theme.code),
            Event::SoftBreak | Event::HardBreak => {
                self.push_table(" ".into(), self.current_style())
            }
            Event::TaskListMarker(checked) => self.push_table(
                if *checked { "[x] " } else { "[ ] " }.into(),
                self.theme.accent,
            ),
            Event::Start(Tag::Emphasis) => self
                .styles
                .push(self.current_style().add_modifier(Modifier::ITALIC)),
            Event::Start(Tag::Strong) => self
                .styles
                .push(self.current_style().add_modifier(Modifier::BOLD)),
            Event::Start(Tag::Strikethrough) => self
                .styles
                .push(self.current_style().add_modifier(Modifier::CROSSED_OUT)),
            Event::Start(Tag::Link { .. }) => self.styles.push(self.theme.link),
            Event::End(
                TagEnd::Emphasis | TagEnd::Strong | TagEnd::Strikethrough | TagEnd::Link,
            ) => {
                self.styles.pop();
            }
            _ => {}
        }
        true
    }

    fn push_table(&mut self, content: String, style: Style) {
        if let Some(cell) = self
            .table
            .as_mut()
            .and_then(|table| table.rows.last_mut())
            .and_then(|row| row.last_mut())
        {
            cell.push(Span::styled(content, style));
        }
    }

    fn render_table(&mut self, table: Table) {
        let columns = table.rows.iter().map(Vec::len).max().unwrap_or(0);
        if columns == 0 {
            return;
        }
        if self.width < columns.saturating_mul(4).saturating_add(1) {
            self.render_stacked_table(&table, columns);
            return;
        }
        let available = self.width.saturating_sub(columns + 1);
        let mut widths = (0..columns)
            .map(|column| {
                table
                    .rows
                    .iter()
                    .filter_map(|row| row.get(column))
                    .map(|cell| spans_width(cell))
                    .max()
                    .unwrap_or(1)
                    .max(1)
            })
            .collect::<Vec<_>>();
        while widths.iter().sum::<usize>() > available {
            let Some((index, _)) = widths
                .iter()
                .enumerate()
                .filter(|(_, width)| **width > 3)
                .max_by_key(|(_, width)| **width)
            else {
                break;
            };
            widths[index] -= 1;
        }
        let table_glyphs = self.theme.glyphs.table();
        let (tl, tj, tr) = table_glyphs.top;
        let (ml, mj, mr) = table_glyphs.middle;
        let (bl, bj, br) = table_glyphs.bottom;
        let vertical = table_glyphs.vertical;
        let horizontal = self.theme.glyphs.rule();
        table_rule(
            &mut self.lines,
            &widths,
            tl,
            tj,
            tr,
            horizontal,
            self.theme.muted,
        );
        for (row_index, row) in table.rows.iter().enumerate() {
            let wrapped = (0..columns)
                .map(|column| {
                    wrap_spans(
                        row.get(column).map(Vec::as_slice).unwrap_or(&[]),
                        widths[column],
                    )
                })
                .collect::<Vec<_>>();
            let row_height = wrapped.iter().map(Vec::len).max().unwrap_or(1);
            for visual_row in 0..row_height {
                let mut line = Line::from(Span::styled(vertical.to_string(), self.theme.muted));
                for column in 0..columns {
                    let content = wrapped[column].get(visual_row).cloned().unwrap_or_default();
                    let padding = widths[column].saturating_sub(spans_width(&content));
                    let alignment = table
                        .alignments
                        .get(column)
                        .copied()
                        .unwrap_or(Alignment::None);
                    let (left, right) = alignment_padding(alignment, padding);
                    line.spans.push(Span::raw(" ".repeat(left)));
                    line.spans.extend(content);
                    line.spans.push(Span::raw(" ".repeat(right)));
                    line.spans
                        .push(Span::styled(vertical.to_string(), self.theme.muted));
                }
                self.lines.push(line);
            }
            if row_index + 1 == table.header_rows && row_index + 1 < table.rows.len() {
                table_rule(
                    &mut self.lines,
                    &widths,
                    ml,
                    mj,
                    mr,
                    horizontal,
                    self.theme.muted,
                );
            }
        }
        table_rule(
            &mut self.lines,
            &widths,
            bl,
            bj,
            br,
            horizontal,
            self.theme.muted,
        );
    }

    fn render_stacked_table(&mut self, table: &Table, columns: usize) {
        let header = table.rows.first().filter(|_| table.header_rows > 0);
        let start = usize::from(header.is_some());
        for (record_index, row) in table.rows.iter().enumerate().skip(start) {
            if record_index > start {
                self.lines.push(Line::default());
            }
            for column in 0..columns {
                let label = header
                    .and_then(|row| row.get(column))
                    .map(|spans| spans_plain(spans))
                    .filter(|label| !label.is_empty())
                    .unwrap_or_else(|| format!("Column {}", column + 1));
                let prefix = format!("{label}: ");
                let prefix_width = prefix.width();
                if prefix_width >= self.width {
                    let label_spans = [Span::styled(
                        format!("{label}:"),
                        self.theme.accent.add_modifier(Modifier::BOLD),
                    )];
                    self.lines.extend(
                        wrap_spans(&label_spans, self.width)
                            .into_iter()
                            .map(Line::from),
                    );
                    let indent = self.width.min(2);
                    for content in wrap_spans(
                        row.get(column).map(Vec::as_slice).unwrap_or(&[]),
                        self.width.saturating_sub(indent).max(1),
                    ) {
                        let mut line = Line::from(Span::raw(" ".repeat(indent)));
                        line.spans.extend(content);
                        self.lines.push(line);
                    }
                    continue;
                }
                let wrapped = wrap_spans(
                    row.get(column).map(Vec::as_slice).unwrap_or(&[]),
                    self.width.saturating_sub(prefix_width).max(1),
                );
                for (line_index, content) in wrapped.into_iter().enumerate() {
                    let mut line = if line_index == 0 {
                        Line::from(Span::styled(
                            prefix.clone(),
                            self.theme.accent.add_modifier(Modifier::BOLD),
                        ))
                    } else {
                        Line::from(Span::raw(" ".repeat(prefix_width)))
                    };
                    line.spans.extend(content);
                    self.lines.push(line);
                }
            }
        }
    }

    fn text(&mut self, text: &str) {
        if self.code_block {
            self.code_buffer.push_str(text);
            return;
        }
        let mut parts = text.split('\n').peekable();
        while let Some(part) = parts.next() {
            if !part.is_empty() {
                self.push(part.to_owned(), self.current_style());
            }
            if parts.peek().is_some() {
                self.newline();
            }
        }
    }

    fn flush_code_buffer(&mut self) {
        if self.code_buffer.is_empty() {
            return;
        }
        let source = std::mem::take(&mut self.code_buffer);
        // Drop a trailing newline that pulldown-cmark often includes before fence end.
        let source = source.strip_suffix('\n').unwrap_or(&source);
        let tokens = syntax::highlight(&self.code_language, source);
        for (index, line_tokens) in group_tokens_by_line(tokens).into_iter().enumerate() {
            if index > 0 {
                self.newline();
            }
            if line_tokens.is_empty() {
                self.ensure_line_prefix();
                continue;
            }
            for token in line_tokens {
                let style = match token.kind {
                    TokenKind::Keyword => self.theme.code_keyword,
                    TokenKind::String => self.theme.code_string,
                    TokenKind::Comment => self.theme.code_comment,
                    TokenKind::Number => self.theme.code_number,
                    TokenKind::Type => self.theme.code_type,
                    TokenKind::Text => self.theme.code,
                };
                self.push(token.text, style);
            }
        }
    }

    fn push(&mut self, content: String, style: Style) {
        self.ensure_line_prefix();
        self.lines
            .last_mut()
            .expect("markdown always owns a line")
            .spans
            .push(Span::styled(content, style));
    }

    fn ensure_line_prefix(&mut self) {
        let line = self.lines.last_mut().expect("markdown always owns a line");
        if !line.spans.is_empty() {
            return;
        }
        if self.quote_depth > 0 {
            line.spans.push(Span::styled(
                format!("{} ", self.theme.glyphs.quote().repeat(self.quote_depth)),
                self.theme.quote,
            ));
        }
        if self.code_block {
            line.spans.push(Span::styled("  ", self.theme.muted));
        }
    }

    fn current_style(&self) -> Style {
        self.styles.last().copied().unwrap_or(self.theme.text)
    }

    fn newline(&mut self) {
        self.lines.push(Line::default());
    }

    fn newline_if_content(&mut self) {
        if self.lines.last().is_some_and(|line| !line.spans.is_empty()) {
            self.newline();
        }
    }

    fn ensure_content_line(&mut self) {
        if self.lines.len() > 1
            && self.lines.last().is_some_and(|line| line.spans.is_empty())
            && self.lines[self.lines.len() - 2].spans.is_empty()
        {
            self.lines.pop();
        }
    }

    fn finish_paragraph(&mut self) {
        self.newline_if_content();
        if self.lines.len() > 1
            && !self.lines[self.lines.len() - 2].spans.is_empty()
            && self.lists.is_empty()
        {
            self.newline();
        }
    }
}

fn group_tokens_by_line(tokens: Vec<syntax::Token>) -> Vec<Vec<syntax::Token>> {
    let mut lines: Vec<Vec<syntax::Token>> = vec![Vec::new()];
    for token in tokens {
        let mut remaining = token.text.as_str();
        loop {
            if let Some((head, tail)) = remaining.split_once('\n') {
                if !head.is_empty() {
                    lines.last_mut().expect("line").push(syntax::Token {
                        text: head.to_owned(),
                        kind: token.kind,
                    });
                }
                lines.push(Vec::new());
                remaining = tail;
            } else {
                if !remaining.is_empty() {
                    lines.last_mut().expect("line").push(syntax::Token {
                        text: remaining.to_owned(),
                        kind: token.kind,
                    });
                }
                break;
            }
        }
    }
    lines
}

fn spans_plain(spans: &[Span<'_>]) -> String {
    spans.iter().map(|span| span.content.as_ref()).collect()
}

fn spans_width(spans: &[Span<'_>]) -> usize {
    spans.iter().map(|span| span.content.width()).sum()
}

fn wrap_spans(spans: &[Span<'_>], width: usize) -> Vec<Vec<Span<'static>>> {
    wrap_spans_prefixed(spans, width, &[], &[])
}

fn wrap_spans_prefixed(
    spans: &[Span<'_>],
    width: usize,
    initial: &[Span<'_>],
    continuation: &[Span<'_>],
) -> Vec<Vec<Span<'static>>> {
    let width = width.max(1);
    let initial = fitted_prefix(initial, width);
    let continuation = fitted_prefix(continuation, width);
    let mut used = spans_width(&initial);
    let mut prefix_width = used;
    let mut lines: Vec<Vec<Span<'static>>> = vec![initial];
    for span in spans {
        for grapheme in span.content.graphemes(true) {
            let cell_width = grapheme.width();
            if used > prefix_width && used.saturating_add(cell_width) > width {
                let next = continuation.clone();
                used = spans_width(&next);
                prefix_width = used;
                lines.push(next);
            }
            if used == prefix_width && prefix_width > 0 && used.saturating_add(cell_width) > width {
                used = 0;
                prefix_width = 0;
                lines.push(Vec::new());
            }
            let line = lines.last_mut().expect("wrapped cell owns one line");
            if let Some(last) = line.last_mut()
                && last.style == span.style
            {
                last.content.to_mut().push_str(grapheme);
            } else {
                line.push(Span::styled(grapheme.to_owned(), span.style));
            }
            used += cell_width;
        }
    }
    lines
}

fn fitted_prefix(prefix: &[Span<'_>], width: usize) -> Vec<Span<'static>> {
    let max_width = width.saturating_sub(1);
    let mut fitted: Vec<Span<'static>> = Vec::new();
    let mut used = 0_usize;
    for span in prefix {
        for grapheme in span.content.graphemes(true) {
            let grapheme_width = grapheme.width();
            if used.saturating_add(grapheme_width) > max_width {
                return fitted;
            }
            if let Some(last) = fitted.last_mut()
                && last.style == span.style
            {
                last.content.to_mut().push_str(grapheme);
            } else {
                fitted.push(Span::styled(grapheme.to_owned(), span.style));
            }
            used += grapheme_width;
        }
    }
    fitted
}

fn alignment_padding(alignment: Alignment, padding: usize) -> (usize, usize) {
    match alignment {
        Alignment::Right => (padding, 0),
        Alignment::Center => (padding / 2, padding - padding / 2),
        Alignment::None | Alignment::Left => (0, padding),
    }
}

fn table_rule(
    lines: &mut Vec<Line<'static>>,
    widths: &[usize],
    left: char,
    join: char,
    right: char,
    horizontal: &str,
    style: Style,
) {
    let mut value = String::new();
    value.push(left);
    for (index, width) in widths.iter().enumerate() {
        value.push_str(&horizontal.repeat(*width));
        value.push(if index + 1 == widths.len() {
            right
        } else {
            join
        });
    }
    lines.push(Line::from(Span::styled(value, style)));
}

#[cfg(test)]
mod tests {
    use super::*;

    fn plain(lines: &[Line<'_>]) -> Vec<String> {
        lines
            .iter()
            .map(|line| {
                line.spans
                    .iter()
                    .map(|span| span.content.as_ref())
                    .collect()
            })
            .collect()
    }

    fn theme() -> MarkdownTheme {
        MarkdownTheme {
            glyphs: Glyphs::default(),
            text: Style::default(),
            accent: Style::default(),
            muted: Style::default(),
            code: Style::default(),
            quote: Style::default(),
            link: Style::default().add_modifier(Modifier::UNDERLINED),
            headings: [Style::default(); 6],
            code_keyword: Style::default(),
            code_string: Style::default(),
            code_comment: Style::default(),
            code_number: Style::default(),
            code_type: Style::default(),
        }
    }

    #[test]
    fn fenced_rust_code_uses_syntax_tokens() {
        // Issue #18: keyword spans must differ from plain code text.
        let mut theme = theme();
        theme.accent = Style::default().add_modifier(Modifier::BOLD);
        theme.code = Style::default();
        let lines = render("```rust\nfn main() {}\n```", 40, theme);
        let keywordish = lines.iter().any(|line| {
            line.spans
                .iter()
                .any(|span| span.content.contains("fn") && span.style != Style::default())
        });
        assert!(keywordish, "expected highlighted fn keyword: {lines:?}");
    }

    #[test]
    fn renders_structure_instead_of_raw_markdown_tokens() {
        let lines = render(
            "## Result\n\n- **Done** with `cargo test`\n- [Docs](https://example.com)\n\n> Safe",
            80,
            theme(),
        );
        let text = plain(&lines).join("\n");
        let glyphs = Glyphs::default();
        assert!(text.contains("Result"));
        assert!(text.contains(&format!("{} Done with cargo test", glyphs.bullet())));
        assert!(text.contains(&format!("{} Docs", glyphs.bullet())));
        assert!(text.contains(&format!("{} Safe", glyphs.quote())));
        assert!(!text.contains("##"));
        assert!(!text.contains("**"));
        assert!(!text.contains('`'));
    }

    #[test]
    fn lays_out_gfm_tables_to_the_terminal_width() {
        let input = "| Name | 状态 |\n|:--|--:|\n| compiler | 已完成并验证 |";
        let wide = render(input, 32, theme());
        assert!(
            plain(&wide)
                .iter()
                .any(|line| line.contains(Glyphs::default().table().top.1))
        );
        assert!(plain(&wide).iter().all(|line| line.width() <= 32));

        let narrow = render(input, 8, theme());
        let text = plain(&narrow).join("\n");
        assert!(text.contains("Name:"));
        assert!(text.contains("状态:"));
        assert!(plain(&narrow).iter().all(|line| line.width() <= 8));

        let long_header = render(
            "| 非常长的字段名称 | 状态 |\n|---|---|\n| value | ok |",
            6,
            theme(),
        );
        assert!(plain(&long_header).iter().all(|line| line.width() <= 6));
        let compact = plain(&long_header)
            .join("")
            .chars()
            .filter(|ch| !ch.is_whitespace())
            .collect::<String>();
        assert!(compact.contains("value"));
    }

    #[test]
    fn incomplete_streaming_markdown_remains_visible_and_width_bounded() {
        for input in [
            "A **partially emphasized response",
            "```rust\nfn main() {\n    println!(\"你好\");",
            "| Name | 状态 |\n|:--|--:|\n| compiler | 正在流式",
            "[an incomplete link](https://example",
        ] {
            let lines = render(input, 12, theme());
            let text = plain(&lines).join("\n");
            assert!(!text.trim().is_empty(), "lost streaming tail for {input:?}");
            assert!(
                plain(&lines).iter().all(|line| line.width() <= 12),
                "streaming tail exceeded layout width for {input:?}: {text:?}"
            );
        }
    }

    #[test]
    fn styled_hanging_prefix_is_part_of_the_wrap_budget() {
        let marker_style = Style::default().add_modifier(Modifier::BOLD);
        let prefix = StyledPrefix::hanging(
            vec![
                Span::styled(Glyphs::default().role_prefix(), marker_style),
                Span::styled("Carina  ", marker_style),
            ],
            Style::default().add_modifier(Modifier::DIM),
        );
        let lines = render_prefixed(
            "A response that crosses the available terminal width.",
            24,
            theme(),
            &prefix,
        );
        let visible = plain(&lines);

        assert_eq!(prefix.width(), 10);
        assert!(visible.len() > 1);
        assert!(visible[0].starts_with(&format!("{}Carina  ", Glyphs::default().role_prefix())));
        assert!(visible[1].starts_with("          "));
        assert!(visible.iter().all(|line| line.width() <= 24));
        assert_eq!(lines[0].spans[0].style, marker_style);
    }

    #[test]
    fn adaptive_hanging_prefix_reclaims_continuation_width() {
        let prefix = StyledPrefix::hanging_with_width(
            vec![Span::raw("* Carina  ")],
            2,
            Style::default().add_modifier(Modifier::DIM),
        );
        let lines = render_prefixed(
            "A response that crosses the available terminal width.",
            24,
            theme(),
            &prefix,
        );
        let visible = plain(&lines);

        assert_eq!(prefix.width(), 10);
        assert_eq!(prefix.continuation_width(), 2);
        assert!(visible.len() > 1);
        assert!(visible[0].starts_with("* Carina  "));
        assert!(visible[1].starts_with("  "));
        assert!(!visible[1].starts_with("          "));
        assert!(visible.iter().all(|line| line.width() <= 24));
    }

    #[test]
    fn adaptive_prefixed_cjk_and_url_wrapping_preserves_exact_graphemes() {
        let prefix = StyledPrefix::hanging_with_width(
            vec![Span::raw("* You      ")],
            2,
            Style::default().add_modifier(Modifier::DIM),
        );
        let body = "中文https://example.com/a/very/long/path?mode=exact&lang=zh-Hant#result";
        let lines = wrap_styled_lines(vec![Line::from(Span::raw(body))], 22, &prefix);
        let visible = plain(&lines);
        let restored = visible
            .iter()
            .enumerate()
            .map(|(index, line)| {
                if index == 0 {
                    line.strip_prefix("* You      ").expect("initial prefix")
                } else {
                    line.strip_prefix("  ").expect("continuation prefix")
                }
            })
            .collect::<String>();

        assert_eq!(prefix.width(), 11);
        assert_eq!(prefix.continuation_width(), 2);
        assert!(visible.len() > 1);
        assert!(visible.iter().all(|line| line.width() <= 22));
        assert_eq!(restored, body);
    }
}
