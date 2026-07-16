# Repository Agent Guidance

Preserve unrelated working-tree changes. This repository may contain concurrent runtime work.

Before changing a README hero or badge, a logo/icon, VS Code branding, TUI colors, typography,
or design tokens, read `docs/brand/AGENTS.md` and use the canonical assets under
`docs/brand/assets/`. Run `make brand-check` after any brand-facing change.

Do not copy design explorations, rejected generations, mockups, or local absolute paths into
product-consumable directories. Keep generated binaries and derived brand assets reproducible
from their documented canonical source.
