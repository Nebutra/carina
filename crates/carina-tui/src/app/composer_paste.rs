use ratatui::text::Line;

use crate::clipboard_image::{
    paste_chip_line, paste_line_count, should_chip_paste, PASTE_ELEMENT_KIND,
};
use crate::i18n::{format as tr_format, text as tr, MessageId, Notice};

use super::{App, AsyncMessage};

impl App {
    pub(super) fn capture_clipboard(&mut self, mode: crate::clipboard_image::PasteMode) {
        self.clipboard_generation = self.clipboard_generation.saturating_add(1);
        let generation = self.clipboard_generation;
        let session_id = self
            .active_session
            .as_ref()
            .map(|session| session.session_id.clone());
        let locale = self.ui_locale();
        let paste_label = tr(locale, MessageId::ClipboardPasteLabel);
        let reading_label = tr(locale, MessageId::ClipboardReadingLabel);
        let pending = crate::clipboard_image::PendingPaste::insert(
            &mut self.composer,
            generation,
            session_id,
            paste_label,
            reading_label,
        );
        self.pending_pastes.insert(generation, pending);
        self.notice = match mode {
            crate::clipboard_image::PasteMode::Rich => {
                Notice::localized(MessageId::ReadingClipboard)
            }
            crate::clipboard_image::PasteMode::Text => {
                Notice::localized(MessageId::ReadingClipboardText)
            }
        };
        let tx = self.async_tx.clone();
        std::thread::spawn(move || {
            let result = crate::clipboard_image::capture(mode);
            let _ = tx.send(AsyncMessage::ClipboardCaptured { generation, result });
        });
    }

    pub(super) fn cancel_pending_pastes(&mut self) {
        for (_, request) in self.pending_pastes.drain() {
            let _ = request.remove(&mut self.composer);
        }
        self.submit_after_paste = false;
    }

    pub(super) fn insert_composer_paste(&mut self, text: &str) {
        if should_chip_paste(text) {
            self.insert_paste_chip(text);
        } else {
            self.composer.insert_str(text);
        }
        self.media.reconcile(&self.composer);
        self.sync_context_completion();
    }

    pub(super) fn insert_paste_chip(&mut self, text: &str) {
        let display = self.paste_chip_display(text);
        self.composer.begin_undo_group();
        self.composer
            .insert_element(text, PASTE_ELEMENT_KIND, Some(display));
        self.composer.insert_str(" ");
        self.composer.end_undo_group();
    }

    pub(super) fn resolve_clipboard_text(
        &mut self,
        request: &crate::clipboard_image::PendingPaste,
        text: &str,
    ) -> bool {
        if should_chip_paste(text) {
            let display = self.paste_chip_display(text);
            request.resolve_chip(&mut self.composer, text, display)
        } else {
            request.resolve_text(&mut self.composer, text)
        }
    }

    pub(super) fn resolve_clipboard_image(
        &mut self,
        request: &crate::clipboard_image::PendingPaste,
        image: crate::clipboard_image::TemporaryImage,
    ) -> bool {
        let Some(range) = request.range(&self.composer) else {
            return false;
        };
        let cursor_before = self.composer.cursor();
        let start = range.start;
        let end = range.end;
        self.composer.replace_range(range, "");
        let cursor_without_placeholder = self.composer.cursor();
        self.composer.set_cursor(start);
        if !self.attach_image(image.into_path(), true) {
            self.composer.set_cursor(cursor_without_placeholder);
            return false;
        }
        let cursor_after_image = self.composer.cursor();
        if cursor_before <= start {
            self.composer.set_cursor(cursor_before);
        } else if cursor_before > end {
            self.composer
                .set_cursor(cursor_without_placeholder.saturating_add(cursor_after_image - start));
        }
        true
    }

    fn paste_chip_display(&self, text: &str) -> Line<'static> {
        let locale = self.ui_locale();
        let lines = paste_line_count(text);
        let size = if lines >= crate::clipboard_image::PASTE_CHIP_MIN_LINES {
            tr_format(
                locale,
                MessageId::PasteChipLines,
                &[("count", &lines.to_string())],
            )
        } else {
            tr_format(
                locale,
                MessageId::PasteChipChars,
                &[("count", &text.chars().count().to_string())],
            )
        };
        paste_chip_line(tr(locale, MessageId::ClipboardPasteLabel), &size)
    }
}
