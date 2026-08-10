use crate::diff_render::{
    DIFF_HARD_LINE_LIMIT, DiffRenderWindow, bounded_diff_source, render_unified_diff_window,
};
use crate::rpc::WorkspacePatch;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum PatchDisposition {
    Pending,
    Applied,
    Accepted,
    Rejected,
    Failed,
    Unknown,
}

impl PatchDisposition {
    pub(crate) fn from_patch(patch: &WorkspacePatch) -> Self {
        let status = patch.status.trim().to_ascii_lowercase();
        let verification = patch
            .verify_result
            .as_ref()
            .map(|result| result.status.trim().to_ascii_lowercase())
            .unwrap_or_default();
        if matches!(status.as_str(), "failed" | "error" | "conflict")
            || matches!(verification.as_str(), "failed" | "error" | "mismatch")
        {
            Self::Failed
        } else if matches!(
            status.as_str(),
            "rolled_back" | "rejected" | "denied" | "cancelled" | "canceled"
        ) {
            Self::Rejected
        } else if matches!(status.as_str(), "verified" | "committed") {
            Self::Accepted
        } else if status == "applied" {
            if matches!(
                verification.as_str(),
                "verified" | "passed" | "success" | "ok"
            ) {
                Self::Accepted
            } else {
                Self::Applied
            }
        } else if matches!(
            status.as_str(),
            "proposed" | "validated" | "approved" | "pending" | "queued" | "requested"
        ) {
            Self::Pending
        } else {
            Self::Unknown
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum ReviewFileKind {
    Added,
    Modified,
    Deleted,
}

impl ReviewFileKind {
    pub(crate) const fn marker(self) -> char {
        match self {
            Self::Added => 'A',
            Self::Modified => 'M',
            Self::Deleted => 'D',
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct PatchReviewFile {
    pub path: String,
    pub kind: ReviewFileKind,
    pub additions: usize,
    pub deletions: usize,
    pub hunk_count: usize,
    pub attributed_hunks: usize,
    pub window: DiffRenderWindow,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct PatchReview {
    pub patch_id: String,
    pub transaction_id: String,
    pub disposition: PatchDisposition,
    pub files: Vec<PatchReviewFile>,
    pub additions: usize,
    pub deletions: usize,
    pub source_hard_capped: bool,
    pub source_omitted_lines: usize,
    pub source_omitted_exact: bool,
}

impl PatchReview {
    pub(crate) fn from_patch(patch: &WorkspacePatch) -> Self {
        let (source, byte_capped) = bounded_diff_source(&patch.diff);
        let (mut accumulators, source_omitted_lines) = split_files(source, &patch.affected_files);
        for path in &patch.affected_files {
            if !path.trim().is_empty()
                && !accumulators
                    .iter()
                    .any(|file| file.path.as_deref() == Some(path.as_str()))
            {
                accumulators.push(FileAccumulator::for_path(path.clone()));
            }
        }
        if accumulators.is_empty() && !source.is_empty() {
            accumulators.push(FileAccumulator {
                path: patch.affected_files.first().cloned(),
                diff: source.to_owned(),
                ..FileAccumulator::default()
            });
        }

        let files = accumulators
            .into_iter()
            .filter_map(|file| file.finish(patch))
            .collect::<Vec<_>>();
        let additions = files.iter().map(|file| file.additions).sum();
        let deletions = files.iter().map(|file| file.deletions).sum();
        Self {
            patch_id: patch.patch_id.clone(),
            transaction_id: patch.transaction_id.clone(),
            disposition: PatchDisposition::from_patch(patch),
            files,
            additions,
            deletions,
            source_hard_capped: byte_capped || source_omitted_lines > 0,
            source_omitted_lines: source_omitted_lines.max(usize::from(byte_capped)),
            source_omitted_exact: !byte_capped,
        }
    }
}

pub(crate) fn project_patch_reviews(patches: &[WorkspacePatch]) -> Vec<PatchReview> {
    patches.iter().map(PatchReview::from_patch).collect()
}

#[derive(Debug, Default)]
struct FileAccumulator {
    path: Option<String>,
    old_path: Option<String>,
    new_path: Option<String>,
    diff: String,
}

impl FileAccumulator {
    fn for_path(path: String) -> Self {
        Self {
            path: Some(path),
            ..Self::default()
        }
    }

    fn has_content(&self) -> bool {
        !self.diff.is_empty()
            || self.path.is_some()
            || self.old_path.is_some()
            || self.new_path.is_some()
    }

    fn finish(self, patch: &WorkspacePatch) -> Option<PatchReviewFile> {
        let path = self
            .new_path
            .as_deref()
            .filter(|path| *path != "/dev/null")
            .or_else(|| self.old_path.as_deref().filter(|path| *path != "/dev/null"))
            .or(self.path.as_deref())?
            .to_owned();
        let kind = if self.old_path.as_deref() == Some("/dev/null") {
            ReviewFileKind::Added
        } else if self.new_path.as_deref() == Some("/dev/null") {
            ReviewFileKind::Deleted
        } else {
            ReviewFileKind::Modified
        };
        let mut window = render_unified_diff_window(&self.diff, DIFF_HARD_LINE_LIMIT);
        while window.lines.last().is_some_and(|line| {
            line.kind == crate::diff_render::DiffLineKind::Context
                && line.spans.iter().all(|span| span.text.is_empty())
        }) {
            window.lines.pop();
        }
        let additions = window
            .lines
            .iter()
            .filter(|line| line.kind == crate::diff_render::DiffLineKind::Add)
            .count();
        let deletions = window
            .lines
            .iter()
            .filter(|line| line.kind == crate::diff_render::DiffLineKind::Remove)
            .count();
        let hunk_count = window
            .lines
            .iter()
            .filter(|line| line.kind == crate::diff_render::DiffLineKind::Hunk)
            .count();
        let attributed_hunks = patch
            .hunk_attributions
            .iter()
            .filter(|hunk| hunk.file == path)
            .count();
        Some(PatchReviewFile {
            path,
            kind,
            additions,
            deletions,
            hunk_count,
            attributed_hunks,
            window,
        })
    }
}

fn split_files(source: &str, affected_files: &[String]) -> (Vec<FileAccumulator>, usize) {
    let mut files = Vec::new();
    let mut current = FileAccumulator::default();
    let mut source_lines = source.lines();
    for line in source_lines.by_ref().take(DIFF_HARD_LINE_LIMIT) {
        let starts_next_file = (line.starts_with("diff --git ") && current.has_content())
            || (line.starts_with("--- ")
                && current.new_path.is_some()
                && current.diff.lines().any(|line| line.starts_with("@@")));
        if starts_next_file {
            files.push(current);
            current = FileAccumulator::default();
        }

        if let Some(rest) = line.strip_prefix("diff --git ") {
            current.path = rest
                .split_whitespace()
                .nth(1)
                .and_then(clean_diff_path)
                .or_else(|| rest.split_whitespace().next().and_then(clean_diff_path));
        } else if let Some(rest) = line.strip_prefix("--- ") {
            current.old_path = clean_diff_path(rest);
        } else if let Some(rest) = line.strip_prefix("+++ ") {
            current.new_path = clean_diff_path(rest);
            current.path = current
                .new_path
                .clone()
                .filter(|path| path != "/dev/null")
                .or_else(|| current.old_path.clone());
        }
        current.diff.push_str(line);
        current.diff.push('\n');
    }
    if current.has_content() {
        files.push(current);
    }
    if files.len() == 1 && files[0].path.is_none() {
        files[0].path = affected_files.first().cloned();
    }
    (files, source_lines.count())
}

fn clean_diff_path(value: &str) -> Option<String> {
    let value = value
        .split('\t')
        .next()
        .unwrap_or(value)
        .trim()
        .trim_matches('"');
    if value.is_empty() {
        return None;
    }
    Some(
        value
            .strip_prefix("a/")
            .or_else(|| value.strip_prefix("b/"))
            .unwrap_or(value)
            .to_owned(),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::rpc::{PatchHunkAttribution, PatchVerifyResult};

    fn multi_file_patch() -> WorkspacePatch {
        WorkspacePatch {
            patch_id: "patch-7".into(),
            transaction_id: "tx-7".into(),
            status: "verified".into(),
            affected_files: vec!["src/lib.rs".into(), "notes.md".into(), "extra.txt".into()],
            diff: concat!(
                "diff --git a/src/lib.rs b/src/lib.rs\n",
                "--- a/src/lib.rs\n+++ b/src/lib.rs\n",
                "@@ -1,2 +1,2 @@\n-old\n+new\n context\n",
                "diff --git a/notes.md b/notes.md\n",
                "--- /dev/null\n+++ b/notes.md\n",
                "@@ -0,0 +1,2 @@\n+one\n+two\n"
            )
            .into(),
            verify_result: Some(PatchVerifyResult {
                status: "passed".into(),
                ..PatchVerifyResult::default()
            }),
            hunk_attributions: vec![PatchHunkAttribution {
                file: "src/lib.rs".into(),
                hunk_index: 0,
                transaction_id: "tx-7".into(),
                ..PatchHunkAttribution::default()
            }],
            ..WorkspacePatch::default()
        }
    }

    #[test]
    fn multi_file_transaction_projects_a_typed_file_and_hunk_tree() {
        let review = PatchReview::from_patch(&multi_file_patch());
        assert_eq!(review.patch_id, "patch-7");
        assert_eq!(review.transaction_id, "tx-7");
        assert_eq!(review.disposition, PatchDisposition::Accepted);
        assert_eq!(review.files.len(), 3);
        assert_eq!(review.additions, 3);
        assert_eq!(review.deletions, 1);
        assert_eq!(review.files[0].path, "src/lib.rs");
        assert_eq!(review.files[0].kind, ReviewFileKind::Modified);
        assert_eq!(review.files[0].hunk_count, 1);
        assert_eq!(review.files[0].attributed_hunks, 1);
        assert_eq!(review.files[1].kind, ReviewFileKind::Added);
        assert_eq!(review.files[2].path, "extra.txt");
        assert!(review.files[2].window.lines.is_empty());
    }

    #[test]
    fn disposition_never_invents_acceptance_without_lifecycle_evidence() {
        for (status, verify, expected) in [
            ("proposed", "", PatchDisposition::Pending),
            ("proposed", "passed", PatchDisposition::Pending),
            ("applied", "", PatchDisposition::Applied),
            ("applied", "passed", PatchDisposition::Accepted),
            ("rolled_back", "passed", PatchDisposition::Rejected),
            ("failed", "", PatchDisposition::Failed),
            ("future", "", PatchDisposition::Unknown),
            ("future", "passed", PatchDisposition::Unknown),
        ] {
            let patch = WorkspacePatch {
                status: status.into(),
                verify_result: (!verify.is_empty()).then(|| PatchVerifyResult {
                    status: verify.into(),
                    ..PatchVerifyResult::default()
                }),
                ..WorkspacePatch::default()
            };
            assert_eq!(PatchDisposition::from_patch(&patch), expected, "{status}");
        }
    }

    #[test]
    fn source_and_render_work_are_bounded_before_the_frame_path() {
        let mut patch = multi_file_patch();
        patch.affected_files = vec!["huge.txt".into()];
        patch.diff = std::iter::repeat_n("+x", DIFF_HARD_LINE_LIMIT + 10)
            .collect::<Vec<_>>()
            .join("\n");
        let review = PatchReview::from_patch(&patch);
        assert!(review.source_hard_capped);
        assert_eq!(review.source_omitted_lines, 10);
        assert!(review.source_omitted_exact);
        assert!(review.files[0].window.lines.len() <= DIFF_HARD_LINE_LIMIT);
    }

    #[test]
    fn byte_capped_source_never_claims_an_exact_omission_count() {
        let mut patch = multi_file_patch();
        patch.affected_files = vec!["huge.txt".into()];
        patch.diff = format!(
            "+{}",
            "x".repeat(crate::diff_render::DIFF_HARD_BYTE_LIMIT + 8)
        );
        let review = PatchReview::from_patch(&patch);
        assert!(review.source_hard_capped);
        assert!(!review.source_omitted_exact);
        assert!(review.source_omitted_lines > 0);
    }

    #[test]
    fn utf8_quoted_deleted_path_retains_file_kind_and_counts() {
        let patch = WorkspacePatch {
            affected_files: vec!["src/界面/旧 名.rs".into()],
            diff: concat!(
                "diff --git \"a/src/界面/旧 名.rs\" \"b/src/界面/旧 名.rs\"\n",
                "--- \"a/src/界面/旧 名.rs\"\n",
                "+++ /dev/null\n",
                "@@ -1,2 +0,0 @@\n",
                "-旧内容\n",
                "-second\n"
            )
            .into(),
            ..WorkspacePatch::default()
        };

        let review = PatchReview::from_patch(&patch);
        assert_eq!(review.files.len(), 1);
        assert_eq!(review.files[0].path, "src/界面/旧 名.rs");
        assert_eq!(review.files[0].kind, ReviewFileKind::Deleted);
        assert_eq!(review.files[0].additions, 0);
        assert_eq!(review.files[0].deletions, 2);
        assert_eq!(review.files[0].hunk_count, 1);
    }
}
