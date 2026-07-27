use ratatui::style::{Color, Modifier, Style};

#[derive(Debug, Clone, Copy)]
pub struct Theme {
    pub background: Color,
    pub surface: Color,
    pub raised: Color,
    pub border: Color,
    pub text: Color,
    pub muted: Color,
    pub brand: Color,
    pub accent: Color,
    pub success: Color,
    pub warning: Color,
    pub danger: Color,
}

impl Theme {
    pub fn carina(no_color: bool) -> Self {
        if no_color {
            return Self {
                background: Color::Reset,
                surface: Color::Reset,
                raised: Color::Reset,
                border: Color::DarkGray,
                text: Color::Reset,
                muted: Color::DarkGray,
                brand: Color::Reset,
                accent: Color::Reset,
                success: Color::Reset,
                warning: Color::Reset,
                danger: Color::Reset,
            };
        }
        Self {
            background: Color::Rgb(0x0d, 0x12, 0x14),
            surface: Color::Rgb(0x14, 0x1b, 0x1d),
            raised: Color::Rgb(0x1c, 0x25, 0x27),
            border: Color::Rgb(0x34, 0x41, 0x44),
            text: Color::Rgb(0xf3, 0xf0, 0xe8),
            muted: Color::Rgb(0xb0, 0xb7, 0xb3),
            brand: Color::Rgb(0x8e, 0x40, 0x53),
            accent: Color::Rgb(0x8e, 0xdb, 0xd2),
            success: Color::Rgb(0x68, 0xd2, 0xa3),
            warning: Color::Rgb(0xe8, 0xa8, 0x5f),
            danger: Color::Rgb(0xff, 0x7c, 0x78),
        }
    }

    pub fn focus(self) -> Style {
        Style::default()
            .fg(self.accent)
            .add_modifier(Modifier::BOLD)
    }

    pub fn selected(self) -> Style {
        Style::default().fg(self.background).bg(self.accent)
    }
}
