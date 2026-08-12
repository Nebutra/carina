use std::fs;
use std::path::Path;

#[test]
fn renderers_do_not_bypass_semantic_theme_colors() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("src");
    let forbidden = [
        "Color::Black",
        "Color::Red",
        "Color::Green",
        "Color::Yellow",
        "Color::Blue",
        "Color::Magenta",
        "Color::Cyan",
        "Color::Gray",
        "Color::DarkGray",
        "Color::White",
    ];
    for entry in fs::read_dir(root).expect("read source directory") {
        let path = entry.expect("source entry").path();
        if path.extension().and_then(|value| value.to_str()) != Some("rs")
            || path.file_name().and_then(|value| value.to_str()) == Some("theme.rs")
        {
            continue;
        }
        let source = fs::read_to_string(&path).expect("read Rust source");
        for token in forbidden {
            assert!(
                !source.contains(token),
                "{} bypasses theme with {token}",
                path.display()
            );
        }
    }
}

#[test]
fn transcript_uses_owned_role_and_accent_helpers() {
    let render =
        fs::read_to_string(Path::new(env!("CARGO_MANIFEST_DIR")).join("src/app/render.rs"))
            .expect("read transcript renderer");
    let transcript = render
        .split_once("fn render_transcript")
        .and_then(|(_, tail)| tail.split_once("fn render_composer"))
        .map(|(transcript, _)| transcript)
        .expect("transcript renderer stays between named functions");
    for owned in [
        "transcript_user()",
        "transcript_assistant()",
        "transcript_thinking()",
        "transcript_tool()",
        "transcript_tool_settled()",
        "transcript_metadata()",
        "transcript_added()",
        "transcript_removed()",
        "transcript_link()",
    ] {
        assert!(
            transcript.contains(owned),
            "transcript renderer must consume {owned}"
        );
    }
    for bypass in [
        "self.theme.success",
        "self.theme.link",
        "self.theme.thinking",
        "self.theme.diff_add()",
        "self.theme.diff_remove()",
    ] {
        assert!(
            !transcript.contains(bypass),
            "transcript renderer bypasses its three-hue contract with {bypass}"
        );
    }

    let contract = fs::read_to_string(Path::new(env!("CARGO_MANIFEST_DIR")).join("styles.md"))
        .expect("read terminal style contract");
    assert!(contract.contains("no more than three saturated foreground hues"));
    assert!(contract.contains("same ten-cell first-line prefix"));
    assert!(contract.contains("Below 72"));
    assert!(contract.contains("two-cell semantic rail"));
}
