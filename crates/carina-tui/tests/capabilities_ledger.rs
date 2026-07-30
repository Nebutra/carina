//! Issue #25: capability ledger must exist and track the six historical surfaces.

#[test]
fn capabilities_ledger_lists_six_inline_surfaces() {
    let text = include_str!("../CAPABILITIES.md");
    for needle in [
        "insert_before",
        "emit_to_scrollback",
        "with_synchronized_output",
        "OSC 8",
        "diff_large",
        "scrolling-regions",
    ] {
        assert!(
            text.contains(needle),
            "CAPABILITIES.md missing {needle}"
        );
    }
    assert!(text.contains("**wired**") || text.contains("| **wired**"));
}

#[test]
fn voice_guide_exists_with_formula() {
    let text = include_str!("../VOICE.md");
    assert!(text.contains("What happened"));
    assert!(text.contains("escape hatch") || text.contains("escape"));
}
