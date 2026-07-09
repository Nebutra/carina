# Carina Code Intelligence (carina-index) — V1 Design

Status: **built** (V1, V2, V3, and V4 — the deferred-backlog closure — are
all implemented and green; see the per-version status lines below). This
document is the design *and* the as-built record; where implementation
diverged from the accepted design, the text says "as built" and describes
what shipped.

V1 status: **as built** — carina-index crate (tree-sitter + SQLite/FTS5 +
RRF fusion + PageRank repo map + content_hash invalidation),
`kernel.index.build/update/search/symbols/map` gated by the `CodeIndex`
capability, Go tools `code.search`/`code.symbols`/`code.map`, patch/rollback
invalidation hooks.

## Goal

Give agents a governed code-intelligence layer: fast keyword search, symbol
lookup, and a compact repo map — all mediated by the capability kernel, all
audited, all local. The index is a *derived artifact of files the session is
allowed to read*; it must never become a policy bypass (a session that cannot
read a file must not see its content through search results).

## V1 scope

- New Rust crate `crates/carina-index`:
  - tree-sitter parsing for four grammars: rust, go, typescript, python;
  - AST-aware chunking (chunks follow symbol boundaries, capped by lines);
  - SQLite storage via `rusqlite` with the `bundled` feature (FTS5 available,
    zero system dependency, zero network at runtime);
  - keyword search fusing FTS5 BM25 with exact-match results via Reciprocal
    Rank Fusion (RRF, k=60);
  - symbol lookup (definitions + approximate references,
    `confidence = "tree-sitter"`);
  - repo map: Aider-style symbol ranking by PageRank over the edges graph,
    rendered as a compact text map within a token budget.
- Ingestion is **policy-scoped**: the crate receives an explicit allowlist of
  `(path, content)` pairs. It never opens files or walks the filesystem
  itself. The caller (the kernel service) decides what may be ingested by
  evaluating `FileRead` per path.
- Invalidation is **caller-driven**: on patch apply/rollback the kernel passes
  the changed paths; the index drops and re-ingests those files keyed by
  `content_hash`. No file watchers in V1.
- Kernel integration: `kernel.index.build/update/search/symbols/map` JSON-RPC
  methods on `carina-kernel-service`, each mediated by a new `CodeIndex`
  capability and recorded in the hash-chained audit log.
- Go daemon: agent tools `code.search`, `code.symbols`, `code.map` routed to
  the kernel methods; the index is built lazily on first use and invalidated
  after every applied patch / completed rollback.

Non-goals for V1: embeddings, LSP-precise references, cross-repo indexes,
file watchers, incremental tree-sitter re-parse (whole-file re-parse on
change is fine at workspace scale).

## Crate layout

```
crates/carina-index/
  Cargo.toml
  src/
    lib.rs        # CodeIndex facade, IndexError, public types
    store.rs      # SQLite open/migrate + transactional row writes
    lang.rs       # Lang enum, extension detection, grammar registry
    extract.rs    # tree-sitter symbol + reference extraction
    chunker.rs    # AST-aware chunking
    search.rs     # FTS5 BM25 + exact match + RRF fusion
    symbols.rs    # def/refs lookup
    repomap.rs    # PageRank over edges + budgeted text rendering
```

Workspace changes (root `Cargo.toml`): add `"crates/carina-index"` to
`[workspace.members]`; add to `[workspace.dependencies]`:

```toml
rusqlite = { version = "0.32", features = ["bundled"] }
tree-sitter = "0.24"
tree-sitter-rust = "0.23"
tree-sitter-go = "0.23"
tree-sitter-typescript = "0.23"
tree-sitter-python = "0.23"
carina-index = { path = "crates/carina-index" }
```

(Exact grammar versions pinned at implementation time to whatever set of
tree-sitter ABI-compatible releases builds together; cargo fetches at build
time only.)

`carina-kernel` gains a dependency on `carina-index`. Edition 2021, same
`thiserror`/`serde`/`serde_json`/`sha2` workspace deps as the sibling crates.

## SQLite schema (DDL)

One database per workspace, stored by the kernel service under
`<state_dir>/index/<sha256(workspace_root)>.sqlite`. `user_version` carries
the schema version (V1 = 1); an unknown version drops and rebuilds (the index
is a derived artifact, never the source of truth).

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS files (
  path         TEXT PRIMARY KEY,          -- workspace-relative, '/'-separated
  content_hash TEXT NOT NULL,             -- sha256 hex of file content
  lang         TEXT NOT NULL,             -- 'rust'|'go'|'typescript'|'python'
  indexed_at   TEXT NOT NULL              -- RFC 3339
);

CREATE TABLE IF NOT EXISTS symbols (
  id             INTEGER PRIMARY KEY,
  path           TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  qualified_name TEXT NOT NULL,           -- e.g. module::Type::method / pkg.Recv.Method
  kind           TEXT NOT NULL,           -- function|method|struct|enum|trait|interface|class|const|type_alias|module|variable
  start_line     INTEGER NOT NULL,        -- 1-based, inclusive
  end_line       INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS symbols_by_name ON symbols(name);
CREATE INDEX IF NOT EXISTS symbols_by_path ON symbols(path);

CREATE TABLE IF NOT EXISTS edges (
  src_id     INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  dst_id     INTEGER NOT NULL REFERENCES symbols(id) ON DELETE CASCADE,
  edge_type  TEXT NOT NULL,               -- 'references' | 'calls' | 'imports'
  confidence REAL NOT NULL,               -- 1/n over n same-name candidates (V1)
  source     TEXT NOT NULL,               -- 'tree-sitter' (V2 adds 'lsp')
  PRIMARY KEY (src_id, dst_id, edge_type)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS chunks (
  id         INTEGER PRIMARY KEY,
  path       TEXT NOT NULL REFERENCES files(path) ON DELETE CASCADE,
  symbol_id  INTEGER REFERENCES symbols(id) ON DELETE SET NULL,
  start_line INTEGER NOT NULL,
  end_line   INTEGER NOT NULL,
  content    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS chunks_by_path ON chunks(path);

-- External-content FTS5 table kept in sync by triggers.
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
  content,
  path UNINDEXED,
  content = 'chunks',
  content_rowid = 'id',
  tokenize = "unicode61 tokenchars '_$'"
);
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
  INSERT INTO chunks_fts(rowid, content, path) VALUES (new.id, new.content, new.path);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
  INSERT INTO chunks_fts(chunks_fts, rowid, content, path)
  VALUES ('delete', old.id, old.content, old.path);
END;
```

Invalidation invariant: re-ingesting a file whose `content_hash` is unchanged
is a no-op; otherwise `DELETE FROM files WHERE path = ?` cascades symbols,
edges, and chunks (triggers keep FTS in sync), then the file is re-ingested —
all inside one SQLite transaction per file.

Cross-file edges are name-approximate in V1: a reference to name `N` from
enclosing symbol `S` produces `edges(S -> D, 'references', 1.0/n,
'tree-sitter')` for each of the `n` definitions of `N` in the index. Edges are
recomputed for a file on re-ingest; dangling edges disappear via the CASCADE.

## Crate public API (Rust)

```rust
// lib.rs
#[derive(Debug, thiserror::Error)]
pub enum IndexError {
    #[error("sqlite: {0}")]
    Sqlite(#[from] rusqlite::Error),
    #[error("parse error in {path}: {message}")]
    Parse { path: String, message: String },
    #[error("invalid input: {0}")]
    InvalidInput(String),
}

/// One file the caller has ALREADY read under FileRead policy. The crate
/// never touches the filesystem: paths are workspace-relative identifiers.
pub struct IngestFile {
    pub path: String,
    pub content: String,
}

/// A caller-reported change (patch apply / rollback outcome).
pub enum FileChange {
    Upsert { path: String, content: String },
    Delete { path: String },
}

pub struct IngestReport {
    pub indexed: usize,             // files (re)ingested
    pub unchanged: usize,           // content_hash matched, skipped
    pub skipped: Vec<SkippedFile>,  // unsupported lang / parse failure
    pub symbols: usize,
    pub edges: usize,
    pub chunks: usize,
}
pub struct SkippedFile { pub path: String, pub reason: String }

pub struct CodeIndex { /* store::Store */ }

impl CodeIndex {
    /// Opens (or creates+migrates) the index database at `db_path`.
    pub fn open(db_path: &std::path::Path) -> Result<Self, IndexError>;
    /// In-memory index for tests.
    pub fn in_memory() -> Result<Self, IndexError>;

    /// Ingests exactly the given files (the policy-scoped allowlist).
    /// Files whose content_hash is unchanged are skipped.
    pub fn ingest(&mut self, files: &[IngestFile]) -> Result<IngestReport, IndexError>;

    /// Applies caller-reported changes: drop + re-ingest upserts, drop deletes.
    pub fn update(&mut self, changes: &[FileChange]) -> Result<IngestReport, IndexError>;

    /// Keyword search: FTS5 BM25 + exact substring match, fused with RRF (k=60).
    pub fn search(&self, query: &str, opts: &SearchOptions) -> Result<Vec<SearchHit>, IndexError>;

    /// Definitions and approximate references for a symbol name.
    pub fn symbol_lookup(&self, name: &str, opts: &SymbolOptions) -> Result<SymbolReport, IndexError>;

    /// Aider-style repo map: PageRank over edges, rendered within a token budget.
    pub fn repo_map(&self, opts: &RepoMapOptions) -> Result<RepoMap, IndexError>;

    /// Row counts for diagnostics / build results.
    pub fn stats(&self) -> Result<IndexStats, IndexError>;
}

pub struct IndexStats { pub files: usize, pub symbols: usize, pub edges: usize, pub chunks: usize }
```

```rust
// lang.rs
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Lang { Rust, Go, TypeScript, Python }

impl Lang {
    /// Detects by extension: .rs / .go / .ts .tsx / .py (None = not indexed).
    pub fn from_path(path: &str) -> Option<Lang>;
    pub(crate) fn ts_language(self) -> tree_sitter::Language;
}
```

```rust
// extract.rs
#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SymbolKind {
    Function, Method, Struct, Enum, Trait, Interface, Class,
    Const, TypeAlias, Module, Variable,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct SymbolRecord {
    pub id: i64,
    pub path: String,
    pub name: String,
    pub qualified_name: String,
    pub kind: SymbolKind,
    pub start_line: u32,
    pub end_line: u32,
}

pub(crate) struct Extraction {
    pub symbols: Vec<PendingSymbol>,      // defs, pre-insert (no id yet)
    pub references: Vec<PendingReference>, // (enclosing symbol idx, referenced name, line)
}
pub(crate) fn extract(lang: Lang, path: &str, source: &str) -> Result<Extraction, IndexError>;
```

```rust
// chunker.rs — chunks follow symbol boundaries; oversized symbols are split,
// top-level gaps become file-level chunks. max_lines default: 120.
pub(crate) struct Chunk {
    pub start_line: u32,
    pub end_line: u32,
    pub content: String,
    pub symbol_index: Option<usize>, // index into Extraction::symbols
}
pub(crate) fn chunk(source: &str, symbols: &[PendingSymbol], max_lines: usize) -> Vec<Chunk>;
```

```rust
// search.rs
pub struct SearchOptions {
    pub limit: usize,                // default 20
    pub lang: Option<Lang>,
    pub path_prefix: Option<String>,
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct SearchHit {
    pub path: String,
    pub start_line: u32,
    pub end_line: u32,
    pub snippet: String,             // chunk content, truncated for transport
    pub score: f64,                  // RRF-fused score
    pub sources: Vec<String>,        // subset of ["bm25", "exact"]
    pub symbol: Option<SymbolRecord>,
}
/// Reciprocal Rank Fusion: score(d) = sum over lists of 1 / (k + rank(d)),
/// k = 60, rank 1-based. Pure function, unit-testable.
pub(crate) fn rrf_fuse(ranked_lists: &[Vec<i64>], k: f64) -> Vec<(i64, f64)>;
```

```rust
// symbols.rs
pub struct SymbolOptions {
    pub kind: Option<SymbolKind>,
    pub include_references: bool,    // default true
    pub limit: usize,                // default 50
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct ReferenceSite { pub path: String, pub line: u32, pub text: String }
#[derive(Debug, Clone, serde::Serialize)]
pub struct SymbolReport {
    pub definitions: Vec<SymbolRecord>,
    pub references: Vec<ReferenceSite>,
    /// Always "tree-sitter" in V1 — references are name-approximate.
    pub confidence: &'static str,
}
```

```rust
// repomap.rs
pub struct RepoMapOptions {
    pub token_budget: usize,         // default 1024 (~4 chars/token estimate)
    pub focus_paths: Vec<String>,    // bias rank toward these files (empty = none)
    pub damping: f64,                // default 0.85
    pub iterations: usize,           // default 20
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct RankedSymbol {
    pub qualified_name: String,
    pub path: String,
    pub kind: SymbolKind,
    pub rank: f64,
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct RepoMap {
    pub text: String,                // rendered map, grouped by path
    pub ranked: Vec<RankedSymbol>,
    pub token_estimate: usize,
}
/// Confidence-weighted PageRank over the symbol graph. Pure, unit-testable.
pub(crate) fn pagerank(nodes: usize, edges: &[(usize, usize, f64)], damping: f64, iterations: usize) -> Vec<f64>;
```

Rendered map format (one line per symbol, indented under its file, highest
rank first, cut at the budget using the same `len/4 + 1` token estimate the Go
daemon uses):

```
crates/carina-kernel/src/lib.rs:
  struct Kernel
  fn Kernel::request (255-306)
go/daemon/agent.go:
  func (d *Daemon) dispatchAction (496-583)
```

## CodeIndex capability semantics

`protocol/capabilities/capabilities.json` `capabilities` gains `"CodeIndex"`,
and `carina_policy::Capability` gains a `CodeIndex` variant (same pattern as
the `MemoryWrite` addition).

Policy verdict (in `PolicyEngine::verdict`, `crates/carina-policy/src/lib.rs`):

- `CodeIndex` is **derived read access**: `Allowed` iff
  `profile.file_read_in_workspace`, else `Denied`. All seven builtin profiles
  set `file_read_in_workspace = true`, so index queries work everywhere by
  default, and a custom profile that denies FileRead also denies the index.
- Org policy bundles tighten as usual: `deny_capabilities = ["CodeIndex"]`
  kills it; `require_approval = ["CodeIndex"]` escalates each query to
  approval. Approval overlays / approval modes apply unchanged because the
  gate is the ordinary `Kernel::request` path.

Ingestion scoping (**the load-bearing rule**): `kernel.index.build` and
`kernel.index.update` never trust their path list. For every candidate path
the service evaluates `Capability::FileRead` via `Kernel::request_file_read`
before reading the file and handing its content to the crate:

- `Denied` (out of workspace, `is_sensitive_file`, `guard_special_path`) →
  the path is *skipped*, not an error; it appears in the result's `skipped`
  list with the denial reason, and the denial itself is already audited as a
  `PolicyViolation` by `Kernel::request`.
- Only `Allowed` paths are read and ingested. Because `.env`, key material,
  and friends are denied by `is_sensitive_file`, they can never leak into
  chunks or search snippets.

Auditing: every `kernel.index.*` call performs one `CodeIndex` capability
request whose decision is appended to the hash chain by the existing
`Kernel::request` machinery (`ToolApproved` / `ToolRequested` /
`PolicyViolation` with `permission_decision_id`). Resource strings are
prefix-friendly (the kernel's overlay cache keys on the first token):

| method               | resource                                   |
|----------------------|--------------------------------------------|
| `kernel.index.build` | `index build files=<n>`                    |
| `kernel.index.update`| `index update changed=<n> deleted=<m>`     |
| `kernel.index.search`| `index search <query, truncated to 200>`   |
| `kernel.index.symbols`| `index symbols <name>`                    |
| `kernel.index.map`   | `index map budget=<tokens>`                |

Build/update completion additionally records a status event through
`Kernel::record_event` — `EventType::ToolApproved` with payload
`{"status": "index_build_completed", "indexed": n, "unchanged": u,
"skipped": s, "symbols": …, "edges": …, "chunks": …, "duration_ms": …}`,
linked to the decision id (same idiom as the `approval_overlay_created`
status event in `crates/carina-kernel/src/lib.rs`). No new `EventType`
variants in V1: the twenty canonical types stay closed.

## kernel.index.* RPC contracts

Dispatch: five new arms in `Service::dispatch`
(`crates/carina-kernel/src/bin/carina-kernel-service.rs`), following the
`kernel.patch.*` handler style. `SessionCtx` gains
`index: Option<carina_index::CodeIndex>`, opened lazily at
`<state_dir>/index/<sha256(workspace_root)>.sqlite` on first use.

Error shape is the service's existing one — any handler `Err(String)` becomes
`{"jsonrpc":"2.0","id":…,"error":{"code":-32603,"message":"<msg>"}}`; parse
failures are `-32700`. Notable messages: `"unknown session <id>"`,
`"query is required"`, `"code index denied: <reason>"` (when the CodeIndex
decision is not `allowed` after approval flow), `"index error: <IndexError>"`.

### kernel.index.build

```jsonc
// params
{ "session_id": "string", "paths": "string[]" }  // workspace-relative allowlist
// result
{
  "indexed": 42, "unchanged": 3,
  "skipped": [ { "path": ".env", "reason": "sensitive file …" },
               { "path": "README.md", "reason": "unsupported language" } ],
  "symbols": 512, "edges": 1930, "chunks": 640,
  "db_path": "<state_dir>/index/<hash>.sqlite"
}
```

Flow: `CodeIndex` capability request → per-path `FileRead` requests (denied →
`skipped`) → read allowed files → `CodeIndex::ingest` → status event.
Paths are validated with `path_within_workspace` against the session root
(plus `additional_roots`) exactly like `kernel.patch.propose` validates patch
paths.

### kernel.index.update

```jsonc
// params
{ "session_id": "string", "changed_paths": "string[]", "deleted_paths": "string[]?" }
// result — same shape as build; deleted files count into "indexed" as drops
```

Changed paths that no longer exist on disk are treated as deletes. Same
per-path FileRead gating as build.

### kernel.index.search

```jsonc
// params
{ "session_id": "string", "query": "string", "limit": "number?",   // default 20
  "lang": "rust|go|typescript|python?", "path_prefix": "string?" }
// result
{ "hits": [ {
    "path": "go/daemon/agent.go", "start_line": 496, "end_line": 583,
    "snippet": "func (d *Daemon) dispatchAction(…", "score": 0.0325,
    "sources": ["bm25", "exact"],
    "symbol": { "name": "dispatchAction", "qualified_name": "daemon.Daemon.dispatchAction",
                "kind": "method", "path": "go/daemon/agent.go",
                "start_line": 496, "end_line": 583 }
} ] }
```

Errors: `"index not built — call kernel.index.build first"` when the session
has no index database yet.

### kernel.index.symbols

```jsonc
// params
{ "session_id": "string", "name": "string", "kind": "string?",
  "include_refs": "boolean?",  // default true
  "limit": "number?" }         // default 50
// result
{ "definitions": [ SymbolRecord ],
  "references": [ { "path": "…", "line": 12, "text": "…" } ],
  "confidence": "tree-sitter" }
```

### kernel.index.map

```jsonc
// params
{ "session_id": "string", "token_budget": "number?",  // default 1024
  "focus_paths": "string[]?" }
// result
{ "map": "crates/…/lib.rs:\n  struct Kernel\n  …",
  "ranked": [ { "qualified_name": "…", "path": "…", "kind": "…", "rank": 0.031 } ],
  "token_estimate": 987 }
```

These methods are kernel-internal (Go ↔ Rust stdio), like every other
`kernel.*` method they are not listed in `protocol/jsonrpc/methods.json`
(that registry describes the daemon's operator-facing socket API). If an
operator surface is wanted later it lands there as a `code` group delegating
to these.

## Go daemon integration

1. `go/kernel/kernel.go` — typed wrappers, one per method, in the existing
   thin-client style:

   ```go
   type IndexReport struct {
       Indexed   int           `json:"indexed"`
       Unchanged int           `json:"unchanged"`
       Skipped   []SkippedFile `json:"skipped"`
       Symbols   int           `json:"symbols"`
       Edges     int           `json:"edges"`
       Chunks    int           `json:"chunks"`
   }
   type SkippedFile struct { Path, Reason string }

   func (s *Service) IndexBuild(sessionID string, paths []string) (*IndexReport, error)
   func (s *Service) IndexUpdate(sessionID string, changed, deleted []string) (*IndexReport, error)
   func (s *Service) IndexSearch(sessionID, query string, limit int) (json.RawMessage, error)
   func (s *Service) IndexSymbols(sessionID, name string, includeRefs bool) (json.RawMessage, error)
   func (s *Service) IndexMap(sessionID string, tokenBudget int) (json.RawMessage, error)
   ```

2. `go/daemon/codeintel.go` (new) — `ensureIndex` + the three tool
   observations. `ensureIndex(sess, task)`: if the session has no built index,
   list candidate files with `d.tools.Scan` (Zig `carina-scan`, already
   FileRead-gated by the caller) filtered to the four supported extensions,
   then `d.kern.IndexBuild`. A `sync.Map`-guarded per-session flag prevents
   duplicate builds; index freshness beyond that is maintained by the
   invalidation hooks.

3. `go/daemon/agent.go`:
   - `toolsHelp` gains:
     ```
     - {"tool":"code.search","query":"free text or identifier"}      ranked code search (BM25+exact)
     - {"tool":"code.symbols","name":"SymbolName"}                   definitions + references
     - {"tool":"code.map"}                                           compact ranked repo map
     ```
   - `action` struct gains `Query string \`json:"query"\`` and
     `Name string \`json:"name"\`` fields.
   - `dispatchAction` gains `case "code.search"`, `"code.symbols"`,
     `"code.map"`, each: `ensureIndex` → kernel call → observation string.
     Denials surface as `"DENIED: <reason>"` like every other tool; results
     are truncated for the transcript (audit keeps full metadata kernel-side).
   - `isReadOnlyTool` adds the three names so they are batchable
     (`{"actions":[…]}`), matching their side-effect-free semantics.
   - Invalidation on write: in `agentPatch`, after a successful
     `d.kern.PatchApply`, call `d.kern.IndexUpdate(sess.SessionID,
     []string{path}, nil)` (best-effort; an index error never fails the
     patch).

4. `go/daemon/daemon.go` — `handlePatchRollback` (the
   `workspace.patch.rollback` RPC) invalidates the rolled-back patch's
   `AffectedFiles` via `IndexUpdate` after `d.kern.PatchRollback` succeeds.

The daemon itself never opens the SQLite database — Go's view of the index is
exclusively through `kernel.index.*`, preserving the "kernel is the single
governor" invariant.

## Testing plan (TDD order)

Tests are written first at each layer; never weakened to pass.

1. `crates/carina-index` unit tests (in-memory DB): schema round-trip;
   `content_hash` no-op re-ingest; delete cascades (symbols/edges/chunks/FTS);
   per-language extraction fixtures (rust/go/ts/python snippets → expected
   symbols/kinds/lines); chunker boundary + oversize-split cases; `rrf_fuse`
   against hand-computed scores (k=60); exact-match-only and bm25-only hits
   both fused; `pagerank` on a tiny graph with a known ranking; repo-map
   budget truncation.
2. `carina-kernel` service tests: `kernel.index.build` skips a denied
   (`.env`) path and audits the `PolicyViolation`; queries append
   decision-linked events; `deny_capabilities = ["CodeIndex"]` bundle blocks
   search; build → patch apply → `kernel.index.update` reflects the edit.
3. Go: `go/kernel/kernel_test.go`-style round-trips for the new wrappers
   (skipped when the kernel binary isn't built, same as existing tests);
   `go/daemon` test that `code.search` after a `patch` sees the new content.

Commands: `cargo test -p carina-index`, `cargo test --workspace`,
`cd go && go test ./...`.

## Risks

- **Grammar/version drift**: tree-sitter core and the four grammar crates
  must agree on ABI; pin exact versions and keep them out of the workspace
  hot path (only carina-index depends on them).
- **Build-time cost**: `rusqlite/bundled` + four grammars add minutes to cold
  builds; acceptable, but keep them out of other crates.
- **Approximate references**: name-based edges produce false positives for
  common names; mitigated by `confidence = 1/n` weighting, the
  `confidence: "tree-sitter"` label on results, and V2's LSP source.
- **Stale index**: only patch/rollback invalidate; edits made outside the
  runtime are invisible until the next build. `content_hash` keying makes
  rebuilds cheap; V2+ may add an mtime sweep at session init.
- **Index leakage**: the DB outlives sessions on shared state dirs; keyed by
  workspace root and only readable through a session whose policy allows
  CodeIndex, and never ingesting FileRead-denied content keeps it inside the
  same trust boundary as the workspace itself.

## V2 design — semantic layer

Status: **as built** — embeddings table (schema v3), brute-force cosine
top-k, three-way RRF via optional query vector,
`kernel.index.embed_store`/`pending_chunks` (policy-gated), Go embedding
pipeline via model-router (openai/voyage backends, governed egress,
graceful no-provider degrade), `code.def`/`code.refs` LSP-first with
tree-sitter fallback, and build-as-full-sync-to-allowlist (denied/vanished
paths pruned). The V1 sections above remain authoritative for everything
they cover; V2 is additive.

### V2 goal

Add a semantic retrieval channel (vector cosine similarity fused with V1's
BM25 + exact channels) and precise LSP-backed def/ref — without moving a
single byte of network I/O into the kernel and without letting derived data
outlive its policy context (the V1 critical-bug lesson).

Iron rules, restated as V2 invariants:

- **Content equivalence**: embedding vectors and pending-chunk text are
  content-equivalent derived data. Every RPC that returns them or matches on
  them goes through the same query-time `CodeIndex` gate as `kernel.index.search`
  (which `carina-policy` derives from FileRead — see
  `bundle_file_read_deny_blocks_queries_over_a_previously_built_index`).
  V2 adds the analogous denial tests for vector search and `pending_chunks`.
- **Kernel zero network**: the Rust kernel/crate performs no network I/O.
  Only the Go daemon talks to embedding providers, through the model-router
  provider idiom (BYOK catalog + `auth.Chain` credentials + the shared
  `providerBase` HTTP adapter — the same governed path chat providers use;
  the egress proxy continues to govern agent *command children*, and daemon
  provider traffic remains first-party, credential-scoped, catalog-declared).
- **Cascade invalidation**: embeddings die with their chunks. A stale vector
  must never surface a deleted or changed chunk — enforced structurally
  (FK CASCADE + content-hash join at query time) and tested.
- **Provenance**: vector-sourced hits carry the same `content_hash` /
  `indexed_at` provenance as V1 hits (they hydrate through the same path).
- **Determinism**: identical inputs produce identical rankings. Candidate
  lists are built from `ORDER BY`-stable SQL, cosine ties break by ascending
  `chunk_id`, and RRF fusion keeps its first-seen accumulation order. No
  `HashMap` iteration order reaches any ranking.
- **Graceful degrade**: no configured embedding provider (or a provider
  error) means V1 behavior, silently — `code.search` keeps working two-way,
  `code.def`/`code.refs` fall back to tree-sitter results. Both degrade
  paths are tested.

### V2 scope

- `crates/carina-index`: `embeddings` table (schema v3), brute-force cosine
  top-k, three-way RRF, `pending_chunks` / `embed_store` facade methods.
  **No `sqlite-vec`, no native extensions, no ANN**: at local scale
  (<100k chunks, ≤1536 dims) a brute-force scan is a few tens of
  milliseconds, fully deterministic, and adds zero build/loader risk.
- `carina-kernel-service`: `kernel.index.embed_store`,
  `kernel.index.pending_chunks`, and a `query_vector_base64` extension to
  `kernel.index.search` — all `CodeIndex`-gated and audited like V1 methods.
- `go/model-router`: an `EmbeddingsProvider` interface + `Router.Embed`
  mirroring the `Provider`/`Complete` idiom (registration order fallback,
  `provider/model` targeting, usage accounting).
- `go/daemon`: an OpenAI-compatible `/embeddings` adapter on `providerBase`
  (covers OpenAI and Voyage), a bounded post-index embedding sync, and
  query-side embedding in `code.search`.
- `go/lsp` + `go/daemon`: a persistent LSP `Session` (initialize/didOpen/
  definition/references) and the agent tools `code.def` / `code.refs`,
  LSP-first with tree-sitter fallback.
- V1 deferred minors fixed here: `index_update` read-error conflation (D1)
  and `str_array_param` silent element drops (D2).

Non-goals for V2: persisting LSP results into the `edges` table (the
`source = 'lsp'` column stays reserved for V3 impact analysis — V2 LSP is
query-time only, so precise data can never go stale in the DB), ANN indexes,
local embedding models, reranking (V3), multi-repo.

### Schema changes (v3)

`SCHEMA_VERSION` bumps 2 → 3. The index is a derived artifact: v2 databases
are dropped and rebuilt on first open (existing `DROP_DDL` + `SCHEMA_DDL`
path, plus `DROP TABLE IF EXISTS embeddings` in `DROP_DDL`). No migration.

```sql
CREATE TABLE IF NOT EXISTS embeddings (
  chunk_id     INTEGER NOT NULL REFERENCES chunks(id) ON DELETE CASCADE,
  model_id     TEXT NOT NULL,             -- '<provider>/<model>', e.g. 'openai/text-embedding-3-small'
  dims         INTEGER NOT NULL,          -- vector length; LENGTH(vector) == dims * 4
  vector       BLOB NOT NULL,             -- f32 little-endian, dims * 4 bytes
  content_hash TEXT NOT NULL,             -- files.content_hash the chunk text was embedded from
  PRIMARY KEY (chunk_id, model_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS embeddings_by_model ON embeddings(model_id);
```

Invalidation is structural, twice over:

1. `replace_file`/`delete_file` cascade `files → chunks → embeddings`
   (replaced files get *new* chunk rowids, so an old vector cannot even be
   re-attached by id reuse — and `embed_store` additionally verifies
   `content_hash`, closing the ingest-during-embed race).
2. The cosine candidate query joins
   `embeddings e JOIN chunks c ON c.id = e.chunk_id JOIN files f ON f.path = c.path
   AND e.content_hash = f.content_hash` — a mismatched vector is structurally
   incapable of surfacing, even if (1) were ever bypassed.

`model_id` keys vectors per embedding model: switching providers/models
leaves old vectors inert (different `model_id`) and `pending_chunks` reports
everything as pending for the new model. Old-model rows are garbage-collected
by the v3 rebuild or by chunk churn; no eager sweep in V2.

### Crate API additions (Rust)

New module `src/embed.rs`; `search.rs` grows the cosine channel.

```rust
// embed.rs
#[derive(Debug, Clone, serde::Serialize)]
pub struct PendingChunk {
    pub chunk_id: i64,
    pub path: String,
    pub start_line: u32,
    pub end_line: u32,
    pub content: String,        // full chunk text (content-equivalent data!)
    pub content_hash: String,   // files.content_hash to echo back to embed_store
}

/// One vector to store, echoing the content_hash from pending_chunks.
#[derive(Debug, Clone)]
pub struct ChunkEmbedding {
    pub chunk_id: i64,
    pub content_hash: String,
    pub vector: Vec<f32>,
}

#[derive(Debug, Clone, serde::Serialize)]
pub struct EmbedStoreReport {
    pub stored: usize,
    /// Chunk ids rejected: unknown id (chunk replaced/deleted since
    /// pending_chunks) or content_hash mismatch. Never an error — staleness
    /// is expected under concurrent edits; the caller just re-syncs.
    pub stale: Vec<i64>,
}

impl CodeIndex {
    /// Chunks lacking an embedding for model_id, ascending chunk_id,
    /// at most `limit`. Second tuple element: total pending count.
    pub fn pending_chunks(&self, model_id: &str, limit: usize)
        -> Result<(Vec<PendingChunk>, usize), IndexError>;

    /// Upserts vectors (INSERT OR REPLACE) inside one transaction. All
    /// vectors must share one dims; dims mismatch within the batch or a
    /// vector.len() == 0 is InvalidInput. Unknown chunk_id / stale
    /// content_hash rows are skipped into `stale`.
    pub fn embed_store(&mut self, model_id: &str, items: &[ChunkEmbedding])
        -> Result<EmbedStoreReport, IndexError>;
}
```

```rust
// search.rs — additions
pub struct SearchOptions {
    pub limit: usize,
    pub lang: Option<Lang>,
    pub path_prefix: Option<String>,
    /// Set together or not at all; enables the third (cosine) RRF channel.
    pub query_vector: Option<Vec<f32>>,
    pub model_id: Option<String>,
}

/// Cosine over candidate rows fetched in ascending chunk_id order:
/// dot(a,b) / (|a||b|); zero-norm vectors are skipped; rows whose dims
/// differ from the query are skipped (a model change mid-flight must not
/// error queries). Sort by score desc, then ascending chunk_id — pure
/// function, unit-testable, deterministic.
pub(crate) fn cosine_rank(candidates: &[(i64, Vec<f32>)], query: &[f32], top: usize) -> Vec<i64>;
```

`run()` builds the vector candidate list only when
`query_vector`+`model_id` are present (else exact V1 behavior — bit-identical
two-way scores), applies the same `lang`/`path_prefix` filters in the
candidate SQL, and fuses `[bm25, exact, vector]` with the existing
`rrf_fuse(k = 60)`. `SearchHit.sources` gains `"vector"`. Hydration is the
V1 `hydrate_hit`, so provenance comes for free.

### kernel.index.* RPC additions

Vector transport is base64-encoded f32 little-endian (`dims * 4` bytes),
matching the BLOB layout and the service's existing dependency-free
`base64_decode` / `*_base64` param idiom (`wasm_base64`). The kernel only
ever *decodes* vectors; it never returns one.

Dispatch grows two arms (`kernel.index.embed_store`,
`kernel.index.pending_chunks`); `kernel.index.search` gains two optional
params. All three go through `index_gate` (the ordinary `Kernel::request`
path), so org bundles, approval modes, overlays, and hash-chain audit apply
unchanged, and — because `CodeIndex` is *derived FileRead* in
`carina-policy` — a bundle that denies `FileRead` denies all of them at
query time, even over a previously built index.

| method                      | resource (prefix-friendly)                          |
|-----------------------------|-----------------------------------------------------|
| `kernel.index.embed_store`  | `index embed_store model=<model_id> chunks=<n>`     |
| `kernel.index.pending_chunks` | `index pending_chunks model=<model_id> limit=<n>` |
| `kernel.index.search` (+vec)| unchanged: `index search <query, truncated to 200>` |

#### kernel.index.pending_chunks

**Policy-gated content egress**: chunk text leaves the kernel here, exactly
like search snippets — hence the same query-time gate.

```jsonc
// params
{ "session_id": "string", "model_id": "string",
  "limit": "number?" }   // default 64, capped at 256
// result
{ "chunks": [ { "chunk_id": 7, "path": "go/daemon/agent.go",
                "start_line": 496, "end_line": 583,
                "content": "func (d *Daemon) dispatchAction(…",
                "content_hash": "9f2c…" } ],
  "total_pending": 412 }
```

Deterministic: ascending `chunk_id`. Errors: `"model_id is required"`,
`"index not built — call kernel.index.build first"` (via `existing_index`).

#### kernel.index.embed_store

The caller supplies vectors; the kernel never computes or fetches them.

```jsonc
// params
{ "session_id": "string", "model_id": "string", "dims": 1536,
  "embeddings": [ { "chunk_id": 7, "content_hash": "9f2c…",
                    "vector_base64": "<base64 of dims*4 bytes f32-LE>" } ] }
// result
{ "stored": 63, "stale": [12], "total_pending": 349 }
```

Validation (all invalid-params style `Err(String)`): `dims` in 1..=4096;
each `vector_base64` decodes to exactly `dims * 4` bytes; at most 256
embeddings per call (mirrors the pending_chunks cap). `stale` ids are the
crate's report (chunk replaced/deleted since it was fetched) — success, not
error. Completion records a decision-linked status event
(`{"status": "index_embed_completed", "model_id": …, "stored": …,
"stale": …, "duration_ms": …}`), same idiom as `index_build_completed`.

#### kernel.index.search (extended)

```jsonc
// params — V1 params unchanged, plus:
{ "query_vector_base64": "string?",   // base64 f32-LE; enables the third channel
  "model_id": "string?" }             // required iff query_vector_base64 is set
// result — V1 shape; hits may now carry "vector" in "sources". With a
// query vector the result also carries the channel-liveness counters
// (V3 observable degrade — counts only, never content):
//   "vector_channel": { "stored": N,   // live-content vectors for model_id
//                       "live": M }    // subset whose dims match the query
```

Without `query_vector_base64` the method is bit-for-bit V1 two-way. With it:
decode, validate non-empty and `len % 4 == 0`, run three-way RRF. A model_id
with no stored vectors simply contributes an empty list (deterministic,
not an error) — and `vector_channel` lets the caller see that the cosine
channel contributed nothing (`stored > 0, live == 0` is the observable
dims-mismatch state, e.g. a provider dims change across a daemon restart).

### Go model-router: embeddings provider interface

`go/model-router/embeddings.go`, mirroring `Provider`/`Complete`:

```go
type EmbeddingsRequest struct {
    Model  string   `json:"model"`   // "" / "default", or "provider/model" targeting
    Inputs []string `json:"inputs"`
}

type EmbeddingsResponse struct {
    Provider    string      `json:"provider"`
    Model       string      `json:"model"`   // resolved model — used in the index model_id
    Vectors     [][]float32 `json:"vectors"` // len == len(Inputs), all same dims
    InputTokens int         `json:"input_tokens"`
}

// EmbeddingsProvider is implemented by embedding backends (BYOK).
type EmbeddingsProvider interface {
    Name() string
    Embed(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error)
}

func (r *Router) RegisterEmbeddingsProvider(p EmbeddingsProvider)
// Embed tries embeddings providers in registration order (same fallback +
// "provider/model" targeting semantics as Complete) and records usage
// under the provider name (input tokens only).
func (r *Router) Embed(ctx context.Context, req EmbeddingsRequest) (*EmbeddingsResponse, error)
// HasEmbeddingsProvider is the degrade-path signal: false means the
// semantic layer is disabled and every consumer silently stays V1.
func (r *Router) HasEmbeddingsProvider() bool
```

Deliberate delta from chat providers: there is **no mock embeddings
provider registered by default** — a mock would defeat degrade detection.
Tests register a fake explicitly.

### Go daemon: adapter, registration, pipeline

**Adapter** (`go/daemon/embeddings.go`): one `openAIEmbeddingsProvider`
embedding `providerBase` — `POST endpoint("embeddings")` with
`{"model": …, "input": [...], "encoding_format": "float"}`, `Authorization:
Bearer` from the `auth.Chain`, `statusError` on non-2xx. Voyage's API is
OpenAI-compatible (`https://api.voyageai.com/v1/embeddings`), so both ship
as instances of the same type. Same `providerHTTPTimeout` client idiom as
every other provider adapter.

**Registration** (`provider_registry.go` sibling, called from daemon
startup next to `registerProviders`): a small explicit BYOK table —

| id       | baseURL                          | default model              | env               |
|----------|----------------------------------|----------------------------|-------------------|
| `openai` | `https://api.openai.com/v1`      | `text-embedding-3-small`   | `OPENAI_API_KEY`  |
| `voyage` | `https://api.voyageai.com/v1`    | `voyage-code-3`            | `VOYAGE_API_KEY`  |

`CARINA_EMBEDDINGS_MODEL` (`provider/model`) overrides targeting; an
override whose provider prefix is **not a registered embeddings provider**
— a known backend missing its key *or* an unknown provider (`cohere/…`) —
switches the semantic layer off entirely (V3 hardening): router
registration-order fallback must never send workspace chunks to a provider
the user did not select, nor store vectors under a false `model_id`.
Providers register **only when their `auth.Chain` resolves a credential**
(deliberate delta from chat providers, which register unconditionally and
fail at call time: embeddings must degrade *before* any network attempt,
silently). In
`offline` mode nothing registers. Detection is therefore exactly
`router.HasEmbeddingsProvider()`; the daemon logs one line at session start
when the semantic layer is off, and agent-visible behavior is simply
V1-style results (no `"vector"` in `sources`).

**Index model id** is `"<provider>/<resolved model>"` — changing either
re-embeds under a fresh key.

**Pipeline** (`codeintel.go`): `syncEmbeddings(sessionID)` runs best-effort
(like `invalidateIndex` — an error never fails the tool) after
`ensureIndex`'s `IndexBuild` and after each `invalidateIndex` update:

1. If `!HasEmbeddingsProvider()` → return nil (the tested degrade path).
2. Loop: `kern.IndexPendingChunks(session, modelID, 64)` → truncate each
   chunk text to `maxEmbedChars = 8192` → `router.Embed` (one batch, ≤64
   inputs) → `kern.IndexEmbedStore`. Stop when `total_pending == 0`, on
   error (log, keep V1 behavior), or at a `maxSyncBatches = 200` guard.
3. Chunk order is ascending `chunk_id` end-to-end: deterministic.

**Query side**: `agentCodeSearch` embeds the query first when
`HasEmbeddingsProvider()` (single-input `Embed` with a short context
timeout — as built, `embedTimeout`, added at V4 closure: until then only
`providerHTTPTimeout` bounded it); on any error it falls back cleanly to the plain
`IndexSearch` call — the fallback is exercised in tests. New kernel wrapper
`IndexSearchVector(sessionID, query string, limit int, modelID string,
vector []float32) (json.RawMessage, error)` (base64-encodes f32-LE);
the existing `IndexSearch` signature stays untouched.

### code.def / code.refs — LSP-first with tree-sitter fallback

**`go/lsp` additions** — a persistent session reusing the existing framing
(`writeMsg`/`readMsg`) and handshake:

```go
type Location struct { Path string; Line int; Char int } // 1-based line

// env is the child's complete environment: the daemon passes lspEnv() —
// os.Environ() minus credential-bearing variables (API keys, tokens,
// secrets) plus the governed egress proxy overrides — so an
// agent-triggerable language server never holds daemon secrets and its
// network I/O flows through the same egress chokepoint as agent command
// children (V3 hardening; lsp.Diagnose takes the same parameter).
func StartSession(bin string, args []string, rootDir string, timeout time.Duration, env []string) (*Session, error)
func (s *Session) DidOpen(path, languageID, content string) error
func (s *Session) Definition(path string, line, char int) ([]Location, error)      // textDocument/definition
func (s *Session) References(path string, line, char int, includeDecl bool) ([]Location, error)
func (s *Session) Close() error // shutdown/exit then Kill — never leaks a child
```

**Daemon tools** (`codeintel.go` + `agent.go` dispatch, `action.Name` reuse;
both added to `toolsHelp` and `isReadOnlyTool`):

`code.def {"name": "Symbol"}` / `code.refs {"name": "Symbol"}` flow:

1. `ensureIndex` → `kern.IndexSymbols(name)` — this is the **policy gate**:
   a CodeIndex denial returns `DENIED:` here and LSP never runs. It also
   yields the anchor: the best tree-sitter definition (path, line) and the
   column of `name` on that line.
2. If `serverForExt` (the existing `lsp_probe.go` table: gopls, rust-analyzer,
   typescript-language-server, pyright) has a server **and** it is on PATH:
   `StartSession(bin, args, workspaceRoot, 8s, lspEnv())`, `DidOpen` the
   anchor file,
   then `Definition` (code.def) or `References(…, includeDecl=false)`
   (code.refs) at the anchor position. Results render with
   `confidence:"lsp"`.
3. Fallback — server missing, spawn/handshake error, timeout, or empty
   result: render the V1 `kernel.index.symbols` definitions/references with
   `confidence:"tree-sitter"`. Absent-binary degrade is tested by pointing
   the server table at a nonexistent binary name.

**Workspace scoping** (LSP reads are file reads): the anchor file is opened
only after verifying its absolute path is inside `sess.WorkspaceRoot`
(lexical clean + prefix check, same as other daemon reads), the session
`rootUri` is the workspace root, and every returned `Location` outside the
workspace root is filtered out before rendering. The daemon never hands an
LSP server a path outside the session workspace.

### V1 deferred minors — fixed in V2

1. **D1, `index_update` read-error conflation**
   (`carina-kernel-service.rs`, the `std::fs::read_to_string` `Err` arm,
   ~line 766): only `ErrorKind::NotFound` means the change is a delete.
   Any other error (permission denied, non-UTF-8 `InvalidData`) becomes a
   `skipped` entry — `{"path": …, "reason": "read error (kept indexed
   rows): …"}` — and the existing rows are kept: the path's FileRead
   verdict was Allowed, so keeping its previously ingested content is not
   a policy leak, whereas silently deleting on a transient error loses
   index coverage. (The `invalidate_index_after_patch` hook intentionally
   keeps its conservative delete-on-any-failure behavior: it runs right
   after the kernel itself wrote the file, where a failed read really does
   approximate deletion, and dropping derived data is always safe.)
2. **D2, `str_array_param` silent drops** (~line 1131): replace
   `filter_map(Value::as_str)` with an element-wise check that returns
   `Err(format!("{key} must be an array of strings"))` when any element is
   not a string (surfacing through the standard error response), instead of
   silently indexing/deleting a different path set than the caller named.

### V2 testing plan (TDD order)

Failing test first at each layer; never weakened to pass.

1. `crates/carina-index` — store/embed:
   `embeddings` v3 round-trip; `embed_store` upserts and reports `stale`
   for unknown chunk ids and mismatched `content_hash`; `pending_chunks`
   excludes embedded chunks, is per-`model_id` independent, ascending
   `chunk_id`, correct `total_pending`; **replace_file/delete_file cascade
   drops embeddings and a re-ingested file's chunks are pending again — a
   vector search after a content change must never return the old chunk**
   (the stale-vector test); v2 database drops and rebuilds on open.
2. `crates/carina-index` — search: `cosine_rank` against hand-computed
   similarities (incl. zero-norm skip, dims-mismatch row skip, ascending-id
   tie-break); three-way `rrf_fuse` hand-computed; `search` with
   `query_vector` fuses `"vector"` into `sources` and carries
   `content_hash`/`indexed_at`; without `query_vector` results are
   identical to V1; two identical three-way searches return identical
   orderings (determinism); `lang`/`path_prefix` filter the vector channel.
3. `carina-kernel` service tests (`index_service_test.rs` style):
   `pending_chunks` → `embed_store` → vector search round-trip;
   **`deny_capabilities = ["FileRead"]` bundle blocks `pending_chunks` and
   vector search over a previously built+embedded index** (the analogous
   denial tests to the V1 checkpoint test); `embed_store` after a patch
   apply reports the replaced chunk `stale`; vector search after patch
   apply cannot surface pre-patch content; base64/dims validation errors;
   embed status event is decision-linked. D-fix tests: `index_update` with
   an unreadable-but-present file (chmod 000, `#[cfg(unix)]`) and with a
   non-UTF-8 file keeps rows and reports `skipped`, while a removed file
   still deletes; `str_array_param` rejects `[1, "a"]` with an
   invalid-params error (both written failing-first against current code).
4. `go/model-router`: `Embed` fallback order, `provider/model` targeting,
   usage accounting, `HasEmbeddingsProvider` (mirrors `router_test.go`).
5. `go/daemon`: **no-provider degrade** — `code.search` with zero
   embeddings providers behaves exactly V1 (no error, no `"vector"`
   source); query-embed failure falls back to plain search; pipeline test
   with a fake in-process embeddings provider + built kernel binary:
   `ensureIndex` triggers sync, hits gain `"vector"`, and a patch apply
   re-embeds the touched file (skipped when the kernel binary isn't built,
   like existing tests). `code.def`/`code.refs`: LSP path via the
   mock-stream `collect`-style seam / a scripted fake server binary;
   absent-binary fallback renders `confidence:"tree-sitter"`;
   out-of-workspace locations are filtered.

Commands: `cargo test -p carina-index`, `cargo test --workspace`,
`cd go && go test ./...`.

### V2 risks

- **Embedding latency on first build**: a large workspace embeds thousands
  of chunks; the sync is post-build best-effort and batched, so `code.*`
  tools are usable immediately (keyword-only) while vectors fill in on
  subsequent syncs. The `maxSyncBatches` guard bounds any single pass.
- **Vector staleness under concurrent edits**: closed by the
  `content_hash` echo through `pending_chunks → embed_store` plus the
  query-time hash join; the race window collapses to "vector for identical
  content", which is correct by definition.
- **BLOB growth**: 100k chunks × 1536 dims × 4 B ≈ 600 MB worst case;
  realistic workspaces (<10k chunks) stay under 60 MB. Acceptable for a
  derived, droppable artifact; V3 can add quantization if needed.
- **LSP server variance**: gopls/rust-analyzer index asynchronously and may
  return empty results within the 8 s budget; empty-result fallback to
  tree-sitter keeps the tool useful and honest (`confidence` labels which
  path answered).
- **Provider drift**: the `/embeddings` wire format is the most stable
  OpenAI-compatible surface; Voyage compatibility is pinned by an adapter
  test with a recorded response shape.

## V3 design — impact analysis, observable degrade, reranker seam

Status: **as built** — `code.impact` (bounded, deterministic transitive
dependents over the tree-sitter edges graph via `kernel.index.impact`),
observable degrade on all three surfaces (result headers, audit-chain reason
events, `daemon.status.code_intel`), the daemon-side reranker seam (no-op
default, env-selected, fake-provider tested, failure falls back to kernel
order), and all four V2 deferred minors closed (D1–D4 below). V1/V2 sections
above remain authoritative for everything they cover; V3 is additive. (This
section supersedes the earlier "V3 outlook": the reranker seam lives in the
Go daemon search path, not behind a Rust trait, and `code.impact` is seeded
by a symbol name — the patch-driven risk-review integration is deferred.)

### V3 goal

Answer "what breaks if I change this symbol" (bounded, deterministic,
honestly-labeled transitive dependents over the tree-sitter edges graph),
make every degrade path *observable* (V2's silent fallbacks get explicit
channel/precision labels, audit metadata, and a status surface), and cut the
reranker seam without shipping a reranker.

Iron rules, restated as V3 invariants:

- **Derived data never escapes policy context**: impact results are derived
  from edges, which are derived from file content — `kernel.index.impact`
  goes through the same query-time `index_gate` (CodeIndex, itself derived
  FileRead) as search, so a bundle that denies FileRead denies impact over a
  previously built index.
- **Bounded output/resources**: depth ≤ 5, result limit ≤ 200, and a hard
  row cap on the graph walk itself — a dense name-approximate graph must not
  balloon the kernel process.
- **Deterministic**: the walk dequeues breadth-first with a fully specified
  `ORDER BY`; results order by `(depth, confidence DESC, path, symbol id)`.
- **All degrade paths observable**: results state which channels/precision
  actually answered; degrade *reasons* (never content) land in the audit
  chain; embedding-sync failures stop being `_ =` swallowed.
- **Provenance**: impacted symbols reuse `SymbolRecord`, which already
  carries `content_hash` / `indexed_at`.
- **Kernel zero network** unchanged; the reranker seam is daemon-side.

### A. Impact analysis — `code.impact`

#### Crate: `src/impact.rs`

Transitive *dependents* of a symbol name: an incoming-edge walk (edges point
`referencer -> definition`, so dependents follow `e.dst_id = current`,
surfacing `e.src_id`). Per-hop confidence decays multiplicatively with the
edge's `1/n` fan-out confidence; `source` is `"tree-sitter"` throughout (V3
persists no LSP edges — the `source = 'lsp'` column stays reserved).

```rust
pub struct ImpactOptions {
    pub max_depth: usize, // default 3, clamped to 1..=5
    pub limit: usize,     // default 50, clamped to 1..=200
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct ImpactedSymbol {
    pub symbol: SymbolRecord, // provenance (content_hash/indexed_at) included
    pub depth: u32,           // hops from the seed; 1 = direct dependent
    pub confidence: f64,      // product of edge confidences on the strongest path
    pub source: String,       // "tree-sitter"
}
#[derive(Debug, Clone, serde::Serialize)]
pub struct ImpactReport {
    pub seeds: Vec<SymbolRecord>,      // definitions of `name` (walk origins),
                                       // bounded by the clamped limit — the walk
                                       // still starts from every definition
    pub dependents: Vec<ImpactedSymbol>,
    pub truncated: bool,               // report incomplete: hit `limit` (fetched
                                       // with limit+1) OR the walk filled
                                       // IMPACT_WALK_ROW_CAP (a capped walk must
                                       // never look complete)
}
impl CodeIndex {
    pub fn impact(&self, name: &str, opts: &ImpactOptions)
        -> Result<ImpactReport, IndexError>;
}
```

The recursive CTE (`IMPACT_WALK_ROW_CAP = 10_000` bounds the walk; the
`ORDER BY 2, 1, 3 DESC` on the recursive member makes dequeue order — and
therefore which rows survive the cap — deterministic):

```sql
WITH RECURSIVE dependents(id, depth, confidence) AS (
  SELECT s.id, 0, 1.0 FROM symbols s WHERE s.name = :name
  UNION ALL
  SELECT e.src_id, d.depth + 1, d.confidence * e.confidence
  FROM dependents d
  JOIN edges e ON e.dst_id = d.id
  WHERE d.depth < :max_depth
  ORDER BY 2, 1, 3 DESC       -- breadth-first, ascending id: deterministic
  LIMIT :row_cap              -- hard resource bound on the walk itself
)
SELECT d.id, MIN(d.depth) AS depth, MAX(d.confidence) AS confidence,
       s.path, s.name, s.qualified_name, s.kind, s.start_line, s.end_line,
       f.content_hash, f.indexed_at
FROM dependents d
JOIN symbols s ON s.id = d.id
JOIN files f ON f.path = s.path
WHERE d.depth > 0
GROUP BY d.id
ORDER BY depth ASC, confidence DESC, s.path ASC, d.id ASC
LIMIT :limit_plus_one;
```

`GROUP BY d.id` collapses multiple paths to one row per dependent (shortest
depth, strongest confidence); a seed reached again through a cycle keeps
`depth > 0` rows only, so seeds never list themselves. Cycles terminate via
the depth bound; the row cap is a second, resource-level bound — and a walk
that *fills* the cap flags the report `truncated` (as built: the walk CTE is
counted first; a capped walk may have dropped reachable dependents and must
never be indistinguishable from a complete one). The `seeds` list is bounded
by the clamped `limit` too (as built: a ubiquitous name like `new` must not
hydrate thousands of definition rows into the RPC response; the walk's own
name subquery is unaffected). Unknown symbol name → empty `seeds`, empty
`dependents` (not an error).

#### Kernel: `kernel.index.impact`

One new dispatch arm, `index_gate`-gated and audited exactly like
`kernel.index.search` (resource string is prefix-friendly):

| method               | resource                                    |
|----------------------|---------------------------------------------|
| `kernel.index.impact`| `index impact <name> depth=<d> limit=<n>`   |

```jsonc
// params
{ "session_id": "string", "name": "string",
  "max_depth": "number?",   // default 3, values clamped to 1..=5
  "limit": "number?" }      // default 50, values clamped to 1..=200
// result
{ "seeds": [ SymbolRecord ],
  "dependents": [ { "symbol": SymbolRecord, "depth": 1,
                    "confidence": 0.5, "source": "tree-sitter" } ],
  "truncated": false }
```

Errors: `"name is required"`, `"index not built — call kernel.index.build
first"` (via `existing_index`). Queries record no extra status event (same
as search/symbols; the gate decision is already in the chain).

#### Go: wrapper + `code.impact` tool

`go/kernel/kernel.go`: `IndexImpact(sessionID, name string, maxDepth, limit
int) (json.RawMessage, error)` in the thin-client style. `go/daemon`:
`code.impact {"name": "Symbol"}` — `toolsHelp` entry, `action.Name` reuse,
`dispatchAction` case, `describeAction` case, `isReadOnlyTool` addition.
Rendering (`agentCodeImpact` in `codeintel.go`): `ensureIndex` →
`IndexImpact` → a dependency-impact report grouped by confidence tier —
`high` (confidence ≥ 0.5), `medium` (≥ 0.1), `low` (< 0.1) — each line
`path:start-end  kind qualified_name  (depth N, confidence 0.50)`, capped at
the kernel's limit, with a `truncated` note and the honest header
`impact of <name> (source: tree-sitter, depth<=D): N dependents`. Denials
surface as `DENIED:` via `codeIntelError`, like every code.* tool.

### B. Observable degrade

V2's degrade paths worked but were silent. V3 makes them visible on three
surfaces — the agent observation, the audit chain (reasons, never content),
and the operator status RPC — without changing what degrades or when.

#### Result headers

`code.search` observations gain a first line stating the channels actually
used for *this* query:

```
channels: keyword:on semantic:on
channels: keyword:on semantic:off(no-provider)
channels: keyword:on semantic:off(provider-error)
channels: keyword:on semantic:off(dims-mismatch)
```

Reasons: `no-provider` (`embeddingsModelID() == ""`), `provider-error`
(query-time `Embed` failed / returned a malformed vector — or, as built, a
pre-search sync retry failed, see below), `kernel-error` (a failed retry
classified so), `dims-mismatch` (query vector dims differ from the dims
recorded at the session's last successful `embed_store` — or, as built, from
the kernel's `vector_channel` counters: `stored > 0, live == 0`, which also
catches a dims change this process has no memory of, e.g. after a restart).
When a reranker is configured (§C) the header also carries ` rerank:<name>`
or ` rerank:off(rerank-error)`. `searchIndex` changes signature to return
the degrade state alongside the raw result so `agentCodeSearch` can render
it.

As built, `semantic:on` is kernel-truth, not "the query embed worked": a
session whose last sync failed — or that has no sync record in this process
— re-syncs before the search (`embedSyncHealthy` / `noteEmbedSyncHealthy`),
so a recovered provider *heals* on the next `code.search` instead of
staying degraded until the next write, and an empty or dims-dead vector
store can never render `semantic:on` while the backlog sits unembedded.

`code.def` / `code.refs` headers switch from `confidence=` to `precision:`
with an explicit reason on fallback (V1 `code.symbols` keeps its honest
`confidence=tree-sitter` label — it never claims precision):

```
definitions (2, precision:lsp):
references (5, precision:tree-sitter(lsp-unavailable)):
```

Reasons: `read-denied` (kernel FileRead denial on the anchor file),
`no-boundary-match` (`findNamePosition` failed), `lsp-unavailable`
(everything else: no server for the extension, binary absent,
spawn/handshake/query error, timeout, empty-after-filter result).
`lspLocations` changes to return `(locs []lsp.Location, reason string)`
with `reason == ""` meaning the LSP path answered.

As built, language-server children are also *governed* children: every LSP
spawn (`StartSession` for code.def/code.refs, `Diagnose` for the post-edit
probe) receives `lspEnv()` — the daemon environment with credential-bearing
variables (API keys, tokens, secrets) scrubbed and the egress proxy
overrides appended — so an agent-triggerable server (gopls resolving a
patched `go.mod`, …) can neither read daemon secrets nor open an ungoverned
network path around the egress chokepoint.

#### Audit metadata

Degrade reasons (only — never chunk text, never error bodies) are recorded
via the existing daemon status-event idiom (`d.record(sessionID,
"ToolApproved", taskID, "go", payload, "")`), *only when a degrade
happened* (the happy path adds no chain noise):

| event payload keys | values |
|---|---|
| `{"status":"code_search_degraded","semantic":"off","reason":…}` | `no-provider` \| `provider-error` \| `dims-mismatch` \| `kernel-error` |
| `{"status":"code_rerank_degraded","reranker":<name>,"reason":"rerank-error"}` | reranker configured but failed/invalid |
| `{"status":"code_lookup_degraded","tool":…,"precision":"tree-sitter","reason":…}` | `lsp-unavailable` \| `read-denied` \| `no-boundary-match` |
| `{"status":"embedding_sync_failed","reason":…}` | `provider-error` \| `dims-mismatch` \| `kernel-error` |

An operator reading the chain can now see *that* and *why* the semantic
layer was down or LSP was bypassed, per event, decision-linked to nothing
new (status idiom, no new EventType variants).

#### Embedding sync failures surface

`ensureIndex` / `invalidateIndex` stop discarding `syncEmbeddings` errors:

- one daemon log line (existing idiom):
  `carina-daemon: embedding sync failed (session <id>): <err>`;
- the `embedding_sync_failed` audit status event above (classified reason);
- a per-session status record in a new `d.codeIntelStatus sync.Map`:

```go
type codeIntelStatus struct {
    ModelID       string    // "" = semantic layer off
    EmbedDims     int       // dims of the last successful embed_store batch
    LastSyncAt    time.Time
    LastSyncError string    // "" = healthy; error text, log/status only
    LastSyncReason string   // classified: provider-error|dims-mismatch|kernel-error
}
```

Exposed on the existing operator status surface: `daemon.status` gains a
`code_intel` map keyed by session id (bounded: sessions with a built index
only) with `{semantic: on|off, reason, model_id, embed_dims,
last_sync_error, last_sync_at}` — same additive pattern as
`context_engine`. Sync failures remain best-effort (never fail the calling
tool); they are just no longer invisible.

### C. Reranker seam (a seam, not a product)

Daemon-side, after `kernel.index.search` returns and before rendering —
retrieval (kernel, RRF) is untouched. `go/daemon/rerank.go`:

```go
// rerankCandidate is the subset of a search hit a reranker may see.
type rerankCandidate struct {
    Path      string
    StartLine int
    EndLine   int
    Snippet   string
    Score     float64
}

// Reranker reorders candidates. It returns a permutation of candidate
// indices — a reranker can reorder governed results but never inject,
// drop, or rewrite them. Any failure (error, wrong length, duplicate or
// out-of-range index) falls back to the original kernel order.
type Reranker interface {
    Name() string
    Rerank(ctx context.Context, query string, candidates []rerankCandidate) ([]int, error)
}
```

Selection mirrors the embeddings model idiom: `CARINA_RERANKER` env var,
resolved once at daemon startup. Unset/empty → no reranker, the stage is
skipped entirely (bit-identical V2 rendering, no header segment). An
unrecognized value → reranker stays off plus one daemon log line
(observable, never an error). **V3 registers no real reranker** — the
resolver table is empty; tests inject a fake through a seam variable (the
`lspServerForExt` pattern). Rerank failure emits the `code_rerank_degraded`
status event and the `rerank:off(rerank-error)` header segment, and renders
the un-reranked order.

### D. V2 deferred minors — fixed in V3

1. **embed_store finiteness** — as built the check lives in the crate's
   `CodeIndex::embed_store` (`crates/carina-index/src/embed.rs`), the single
   store-side chokepoint every caller (including `kernel.index.embed_store`)
   funnels through: any NaN/±Inf component is `InvalidInput`
   (`"non-finite embedding component <v> (chunk <id>)"`), surfaced by the
   kernel service as the standard invalid-params error, and the whole batch
   is rejected before its transaction commits — invalid vectors are never
   stored (a NaN vector would silently rank as garbage in `cosine_rank`
   forever).
2. **LSP UTF-16 positions** (`go/lsp/position.go`, new): LSP wire columns
   are UTF-16 code units; the daemon computes byte columns. Exported
   helpers `UTF16Col(line string, byteCol int) int` and `ByteCol(line
   string, utf16Col int) int` (0-based). Outbound: the daemon converts the
   `findNamePosition` byte column using the anchor line before
   `Definition`/`References`. Inbound: rendered locations (≤ 20) convert
   the returned UTF-16 column back to a 1-based rune column using the
   target line — anchor-file lines come from the already-read content; other
   workspace files get one kernel-FileRead-gated read per unique file
   (cached per call); on read denial/failure the location renders with the
   raw UTF-16 column (documented, never dropped for this reason). Tested
   with CJK-containing lines.
3. **LSP `file://` URIs + symlinked roots** (`go/lsp/uri.go`, new):
   `PathToURI(path string) string` percent-encodes path segments (RFC 3986,
   `/` preserved); `URIToPath(uri string) (string, bool)` percent-decodes
   and rejects non-`file:` schemes. Used by `StartSession` (rootUri),
   `DidOpen`, `positionParams`, and `parseLocations`. Daemon containment
   checks (`lspLocations` anchor check, `filterWorkspaceLocations`)
   canonicalize both sides with `filepath.EvalSymlinks` before the prefix
   compare (macOS `/tmp` vs `/private/tmp`). Tested with space, CJK, and
   symlinked-root paths.
4. **dims-mismatch degrade tests** (`syncEmbeddings` already errors on
   inconsistent/zero dims — the tests are what's missing): a fake provider
   returning wrong-dims or zero-length vectors must degrade cleanly — sync
   returns an error, nothing is stored for the bad batch, `code.search`
   stays keyword-only (no `"vector"` source), and (with §B) the failure is
   visible in the log, the audit chain, and `daemon.status.code_intel`.

### E. Multi-repo headroom (design only — no code in V3)

The current layout already isolates by root:
`<state_dir>/index/<sha256(workspace_root)>.sqlite`, one database per
workspace. The extension to multiple roots (`/add-dir`
`additional_roots`, future multi-repo sessions) is one database per root,
each keyed `sha256(root)`:

- **Build/update** fan out per root (each path list is resolved against its
  owning root; FileRead gating is already root-aware via
  `path_within_workspace` + `additional_roots`).
- **Queries** fan out per root and fuse the per-root ranked lists with the
  existing `rrf_fuse` — deterministic given per-root determinism plus a
  stable root iteration order (sorted root paths). Hit paths gain a root
  discriminator (`<root-alias>/<rel-path>`) at the RPC layer only.
- **Impact and repo map stay per-root**: cross-repo edges do not exist
  (edges are derived from same-DB `refs`), so no schema change is needed —
  a cross-repo impact walk is a V4+ feature, not a latent bug.
- Policy is unchanged: each per-root DB is only reachable through a session
  whose FileRead covers that root; dropping a root drops its DB's
  reachability (and the derived artifact can simply be deleted).

Nothing in V3 code depends on this beyond what impact/observability need;
this section exists so V3 decisions (per-DB determinism, path-relative
keys, no cross-DB joins) do not foreclose the multi-root shape.

### V3 testing plan (TDD order)

Failing test first at each layer; never weakened to pass.

1. `crates/carina-index` — `impact.rs`: direct dependents (depth 1) with
   `1/n` confidence; transitive walk decays confidence multiplicatively and
   reports shortest depth / strongest confidence per symbol; cycles
   terminate and never self-list seeds; `max_depth`/`limit` clamping;
   `truncated` flag; deterministic ordering across two identical calls;
   unknown name → empty report; dependents carry
   `content_hash`/`indexed_at` provenance; post-`update` walk reflects
   edge re-resolution (a re-ingested caller stays a dependent).
2. `carina-kernel` service tests (`index_service_test.rs`):
   `kernel.index.impact` round-trip over a small fixture; **`deny_capabilities
   = ["FileRead"]` bundle blocks impact over a previously built index** (the
   V1/V2 checkpoint-test analogue); param clamping; `name is required`;
   index-not-built error. D1: `embed_store` with a NaN/+Inf/−Inf component
   is a param error and stores nothing (failing-first against current code).
3. `go/lsp`: `UTF16Col`/`ByteCol` round-trips on ASCII, CJK, and emoji
   (surrogate-pair) lines; `PathToURI`/`URIToPath` round-trips for space,
   CJK, and reserved-char paths; `parseLocations` decodes percent-encoded
   URIs; session position params carry UTF-16 columns (mock-stream test).
4. `go/daemon` — impact & degrade: `code.impact` renders tiers and DENIED
   propagation (kernel-binary-gated, like existing tests); `code.search`
   header states `semantic:off(no-provider)` with zero providers,
   `provider-error` with a failing fake, `dims-mismatch` with a
   wrong-dims fake after a successful sync; `code.def`/`code.refs` headers
   state `precision:lsp` and each `precision:tree-sitter(<reason>)` variant
   (absent binary, denied anchor read, unfindable name); degrade events land
   in the audit chain with reason keys only; `daemon.status` exposes
   `code_intel` after a failed sync; symlinked-root and CJK-path LSP
   containment (D3).
5. `go/daemon` — reranker seam: default (env unset) path is bit-identical
   to V2 rendering; a fake reranker (seam-injected) reorders hits and the
   header shows `rerank:<name>`; a failing fake (error and invalid
   permutation cases) falls back to the un-reranked kernel order with
   `rerank:off(rerank-error)` + the audit status event.

Commands: `cargo test -p carina-index`, `cargo test --workspace`,
`cd go && go test ./...`.

### V3 risks

- **Name-approximate blast radius**: impact inherits V1's name-based edges —
  common names fan out with low confidence and can dominate the walk. The
  multiplicative decay, confidence tiers, `source: tree-sitter` labeling,
  and hard bounds keep the report honest and cheap rather than pretending
  precision (LSP-sourced edges stay deferred).
- **Walk cost on dense graphs**: `UNION ALL` path enumeration can be large
  before `GROUP BY`; the depth bound (≤5), the 10k row cap on the recursive
  member, and breadth-first dequeue bound the worst case deterministically.
- **Header/format churn**: agents may have learned V2's headerless
  `code.search` output; the header is one prepended line and the hit format
  is unchanged, so transcript-parsing tests only gain a line.
- **UTF-16 render reads**: converting returned columns can add up to one
  gated read per unique result file (≤20 rendered); reads are cached
  per call, policy-gated, and skipped (raw column) on denial — never a new
  policy surface.
- **Status-surface growth**: `daemon.status.code_intel` is bounded by
  active sessions with a built index and carries reasons/timestamps only —
  no content, no queries.

## V4 design — staleness sweep, LSP-sourced edges, real rerankers, closure

Status: **as built** — every layer shipped and every suite is green
(`cargo test -p carina-index`, the `index_service_test` cases,
`cargo test --workspace`, `cd go && go test ./...` across all packages).
The crate and kernel-service layers (edges_store + dedup + cascade, impact
`source: lsp` labeling, `kernel.index.edges_store`, D1 query-vector
finiteness, D4 kernel invalidation surfacing) and the Go layers — the sweep
(§A), edge write-through (§B daemon side), `Router.Rerank` + BYOK adapters
+ selection/deadline (§C), and the D2/D3/D4-daemon minors — are all
implemented, pinned by their tests in `go/model-router/rerank_test.go`,
`go/lsp/lsp_v4_test.go`, `go/lsp/position_test.go` (surrogate cases), and
`go/daemon/codeintel_v4_test.go` (all passing).
V4 closes the deferred backlog: the out-of-band-edit blind spot (A), the
`source = 'lsp'` edges column reserved since V1 (B), real rerank providers
behind the V3 §C seam (C), and every remaining review minor (D). V1–V3
sections above remain authoritative for everything they cover; V4 is
additive.

Iron rules, restated as V4 invariants:

- **Derived data policy-gated at query time** unchanged: the sweep and the
  edges store are new *ingestion* surfaces; both re-gate every path through
  FileRead kernel-side, and every query keeps the `index_gate`.
- **Full-sync semantics**: the sweep converges the index to what is on disk
  and readable — changed/removed out-of-band edits can no longer serve
  stale rows past the next `code.*` call.
- **All degrade paths observable**: sweep failures, invalidation failures
  (previously `_ =`-swallowed), and rerank on/off(+reason) all surface via
  the established header / audit-metadata / `daemon.status` mechanism.
- **Bounded resources**: the sweep is stat-only in the daemon (no content
  reads); `edges_store` is capped per call; rerank runs under a deadline.
- **Content egress only to explicitly configured providers**: rerank sends
  query + candidate snippets, so the stage is off unless the user set
  `CARINA_RERANK_MODEL` to a registered provider — no registration-order
  fallback, exactly the embeddings no-fallback guard.
- **Zero network I/O in Rust**; determinism everywhere (the sweep diff is
  order-independent; edge dedup is structural; rerank failure falls back to
  the deterministic kernel order).

### A. mtime staleness sweep

Closes the V1 risk note ("edits made outside the runtime are invisible
until the next build"). The daemon keeps a per-session snapshot of
`path -> (mtime, size)` for the supported-language files of the last index
sync, and diffs it cheaply on each `code.*` call.

- **Metadata source** (as built): the shipped `carina-scan` binary emits
  **no** mtime field (`toolchain.FileEntry` declares
  `Mtime int64 \`json:"mtime"\`` — Unix seconds — but it is always zero),
  and the sweep deliberately does not consume it even when present: a
  second-granularity stamp cannot carry a same-second, same-size edit. The
  daemon instead issues one `os.Stat` per supported file and keys stamps on
  **nanosecond** mtime + size + **mode bits** — still metadata only. The
  mode bits close the readability blind spot (final closure): a `chmod`
  changes ctime only, so a file that was stat-able but unreadable at build
  time (kernel read failed, never ingested) would otherwise stay invisible
  forever after `chmod +r`; the mode diff routes it through
  `kernel.index.update`, whose read now succeeds. The daemon never
  reads file bytes for the sweep; content is read (and re-gated per path
  through FileRead) exclusively kernel-side in `kernel.index.update`.
- **Snapshot storage**: session memory — `d.indexSnapshot sync.Map`
  (session id → `*sweepSnapshot{stamps: path -> fileStamp{mtime,size},
  scannedAt}`), following the `indexBuilt` / `codeIntelStatus` pattern. No session-store persistence:
  a fresh daemon process has no `indexBuilt` flag either, so `ensureIndex`
  performs a full (content-hash-cheap) build that re-primes the snapshot.
- **Trigger**: inside `ensureIndex`, on the already-built branch (every
  `code.*` tool passes through it). Flow: scan → filter to
  `.rs/.go/.ts/.tsx/.py` → diff against the snapshot — changed stamps and
  added paths become `changed_paths` (as built, a path also counts as
  changed when its snapshot stamp is **racy** — mtime not strictly older
  than the stat pass that captured it, git's racy-clean rule — so a write
  landing on the very same timestamp right after a scan still converges on
  the next sweep; the redundant update is content-hash cheap), vanished
  paths become `deleted_paths` → one
  `d.kern.IndexUpdate(session, changed, deleted)`
  (the kernel re-gates FileRead per path and already drops on
  deny/NotFound) → best-effort `syncEmbeddings` (failures surface via
  `noteEmbeddingSyncFailure` as in V3) → store the fresh snapshot. An
  empty diff makes no kernel call.
- **Opt-out**: `CARINA_INDEX_SWEEP=off|0|false` disables the sweep
  (perf-sensitive setups); **default is on**. Disabled or not, the
  existing freshness mechanisms (patch/rollback invalidation, the
  mutating-`run` `indexBuilt` reset) are unchanged.
- **Failure surfacing** shares D4's mechanism: a failed sweep update logs
  one daemon line, records `{"status":"index_sweep_failed","reason":…}`
  (status idiom, reason only), and clears `indexBuilt` so the next call
  heals with a full build.

### B. LSP-sourced precise edges

Opportunistic write-through only — **no full-workspace LSP crawl**:
enrichment happens exactly when `code.def` / `code.refs` already obtained
LSP locations (kernel-FileRead-gated anchor, credential-scrubbed governed
child, workspace-filtered results).

#### Crate: edge dedup + `edges_store`

The `edges` schema is untouched (V1 reserved `(confidence, source)`).
Storage-level dedup rule — at most one row per `(src_id, dst_id,
edge_type)`, preferring `source='lsp'`:

- New `CodeIndex::edges_store(&mut self, edges: &[EdgeSpec]) ->
  Result<EdgesStoreReport, IndexError>` with `EdgeSpec { src_path,
  src_line, dst_path, dst_line }`: each endpoint resolves to the smallest
  symbol on that path whose `[start_line, end_line]` contains the line
  (deterministic `ORDER BY (end_line - start_line), id`); unresolvable
  endpoints and self-edges go to `skipped` (success, not error — symbols
  churn under concurrent edits). Resolved pairs upsert
  `INSERT … ON CONFLICT(src_id, dst_id, edge_type) DO UPDATE SET
  confidence = 1.0, source = 'lsp'` with `edge_type = 'references'`.
- `resolve_edges_for_name` (tree-sitter recompute) scopes its delete to
  `AND source = 'tree-sitter'` and keeps `INSERT OR IGNORE`: an LSP edge
  whose endpoint symbols still exist survives a same-name recompute (both
  its files are unchanged, so the precise relation still holds), and the
  ignored tree-sitter re-insert never downgrades it.
- **Invalidation is the existing cascade** (verified by test, both ends):
  replacing or deleting either endpoint file deletes its symbol rows and
  the `ON DELETE CASCADE` drops the LSP edge with them — new symbol ids on
  re-ingest make id reuse structurally impossible. A dropped LSP edge is
  re-persisted the next time a tool-triggered query reproduces it.
- `impact()` stops hardcoding `source: "tree-sitter"`: the walk CTE
  carries a per-path source (`'lsp'` iff **every** hop on the path is
  `'lsp'`, else `'tree-sitter'`), and the final aggregation reports
  `confidence = MAX(path confidence)` plus
  `lsp_confidence = MAX(confidence over all-lsp paths)`; a dependent is
  labeled `source: "lsp"` iff `lsp_confidence == confidence` (deterministic
  tie-break toward the precise label). Dedup means an `'lsp'` edge already
  contributes its 1.0 confidence to the walk and to PageRank.
- Repo map: **verified** — `pagerank` consumes edge `confidence` (an LSP
  edge upgrades the weight to 1.0 via the dedup REPLACE); the rendered map
  labels symbols, not edges, so no change is needed there.

#### Kernel: `kernel.index.edges_store`

One new dispatch arm, gated and audited like its siblings:

| method                    | resource                        |
|---------------------------|---------------------------------|
| `kernel.index.edges_store`| `index edges_store edges=<n>`   |

```jsonc
// params
{ "session_id": "string",
  "edges": [ { "src_path": "go/daemon/agent.go", "src_line": 512,
               "dst_path": "go/daemon/codeintel.go", "dst_line": 33 } ] }
// result
{ "stored": 3, "skipped": [ { "src_path": "…", "dst_path": "…",
                              "reason": "no symbol at src_path:line" } ] }
```

Validation: at most 256 edges per call (the `MAX_EMBED_BATCH` idiom);
`src_line`/`dst_line` ≥ 1; both paths normalize via `rel_and_abs` **and
re-pass the FileRead gate** (denied → `skipped` with the denial reason,
audited as usual — an edge may never smuggle a relation about content the
session cannot read); both endpoints must already resolve to indexed
symbols (the index stays a derived artifact of ingested files — the
edges store never ingests). Completion records a decision-linked
`{"status":"index_edges_stored","stored":…,"skipped":…,"duration_ms":…}`
status event. Errors: `"edges array required"`, `"index not built …"`.

#### Daemon: write-through in `code.def` / `code.refs`

`go/kernel`: `IndexEdgesStore(sessionID string, edges []IndexEdge)
(*EdgesStoreResult, error)` in the thin-client style. In
`agentSemanticLookup`, after the LSP path answered (`degradeReason == ""`),
best-effort (an error never fails the tool; one daemon log line — the
gated kernel call itself is already in the audit chain):

- `code.refs`: each workspace-filtered location is a precise reference to
  the anchor definition — edges `{src: location (path,line), dst: anchor
  definition (path, start_line)}`, capped at 256.
- `code.def`: only locations that differ from the anchor position persist
  — edges `{src: anchor definition, dst: location}` (the anchor's code
  resolves to that target, e.g. an alias/re-export); self-edges are
  skipped kernel-side anyway.

Locations are already canonicalized inside the workspace; the daemon
converts them workspace-relative before the RPC.

### C. Real rerank providers behind the V3 seam

`go/model-router/rerank.go`, mirroring `embeddings.go` exactly:

```go
type RerankRequest struct {
    Model     string   // "" / "default", or "provider/model" targeting
    Query     string
    Documents []string
    TopK      int      // callers pass len(Documents): full ordering
}
type RerankResult struct{ Index int; Score float64 }
type RerankResponse struct {
    Provider string
    Model    string
    Results  []RerankResult // descending relevance, provider order
}
type RerankProvider interface {
    Name() string
    Rerank(ctx context.Context, req RerankRequest) (*RerankResponse, error)
}
func (r *Router) RegisterRerankProvider(p RerankProvider)
func (r *Router) Rerank(ctx …, req …) (*RerankResponse, error) // registration-order + targeting
func (r *Router) HasRerankProviderNamed(name string) bool      // the no-fallback guard hook
```

**BYOK registration table** (`go/daemon`, next to
`registerEmbeddingsProviders`; a backend registers **only when its
`auth.Chain` resolves a credential**, offline registers nothing):

| id       | endpoint                              | default model        | env              |
|----------|---------------------------------------|----------------------|------------------|
| `voyage` | `POST https://api.voyageai.com/v1/rerank` | `rerank-2`       | `VOYAGE_API_KEY` |
| `cohere` | `POST https://api.cohere.com/v2/rerank`   | `rerank-english-v3.0` | `COHERE_API_KEY` |

Wire shapes (public API docs; both return `(index, relevance_score)` pairs
sorted descending — no document text comes back when `return_documents`
is off/omitted):

```jsonc
// voyage request                          // voyage response
{ "model": "rerank-2", "query": "…",       { "object": "list",
  "documents": ["…"], "top_k": 10,           "data": [ { "index": 3,
  "return_documents": false }                            "relevance_score": 0.92 } ],
                                             "model": "rerank-2",
                                             "usage": { "total_tokens": 123 } }
// cohere request                          // cohere response
{ "model": "rerank-english-v3.0",          { "id": "…",
  "query": "…", "documents": ["…"],          "results": [ { "index": 3,
  "top_n": 10 }                                            "relevance_score": 0.98 } ],
                                             "meta": { "billed_units":
                                                       { "search_units": 1 } } }
```

Both adapters ride `providerBase` (`Authorization: Bearer` from the
`auth.Chain`, `statusError` on non-2xx, `providerHTTPTimeout` client) —
the same governed first-party egress as embeddings providers.

**Selection — explicit, no fallback** (content egress: query + candidate
snippets leave the machine only to a provider the user named):
`CARINA_RERANK_MODEL` = `provider/model` (e.g. `voyage/rerank-2`).
`configuredReranker` resolves it once per query: unset → stage off, no
header segment (bit-identical V3 rendering, existing tests unchanged); set
but the prefix is not a *registered rerank provider* (unknown provider, a
known backend missing its key, offline) → stage off with the header
segment `rerank:off(no-provider)` and a `code_rerank_degraded` audit event
(`reason: no-provider`) — router fallback must never route snippets to an
unselected provider. The V3 `CARINA_RERANKER` variable keeps its as-built
semantics as a legacy bare-name alias (unrecognized value → off with one
log line; the V3 test stays green).

**The rerank stage gains a deadline**: `rerankHits` wraps the call in
`context.WithTimeout(context.Background(), rerankTimeout)`
(`rerankTimeout = 10 * time.Second`, the `lspQueryTimeout` convention —
bounded well under `providerHTTPTimeout`). Timeout, error, or an invalid
permutation all fall back to the un-reranked kernel order with
`rerank:off(rerank-error)` + the existing audit event — the V3 fallback
test keeps passing. The router adapter converts provider `Results` into a
permutation (returned indices in provider order, then any unreturned
indices in kernel order — a partial provider answer is a success, pinned by
test, while duplicate/out-of-range indices still degrade) and
`validPermutation` still guards it: only
result **ordering** ever changes; snippets and provenance are untouched.
Snippets sent to the provider are capped at `maxEmbedChars` like
embedding inputs.

The embeddings path gets the same deadline treatment (final closure):
every `router.Embed` — the query-time embed and each sync batch — runs
through the `embedWithDeadline` chokepoint under `embedTimeout`
(10 s, a test-shrinkable var like `rerankTimeout`), so a hanging
embeddings endpoint degrades to keyword-only
(`semantic:off(provider-error)` + the audit event) in seconds instead of
stalling every `code.search` for the full `providerHTTPTimeout` (120 s),
matching the V2 "short context timeout" claim above.

### D. Remaining review minors — all fixed

1. **`kernel.index.search` query-vector finiteness**
   (`carina-kernel-service.rs`, the `query_vector_base64` arm): verified
   missing — `f32_from_le_bytes` output goes straight into
   `opts.query_vector` (the crate's finiteness chokepoint is
   `embed_store`, which search never crosses). V4 rejects any NaN/±Inf
   component with the standard param error
   (`"query_vector_base64 contains a non-finite component"`) before the
   search runs.
2. **`Diagnose`/`collect` URI handling** (`go/lsp/lsp.go`): verified stale
   — line 53 still builds `"file://"+rootDir` / `"file://"+filePath` by
   concatenation and `collect` matches `publishDiagnostics` with
   `p.URI == fileURI` exact string compare. V4 routes both through the V3
   helpers: `PathToURI` for the outbound rootUri/didOpen URIs, and an
   inbound match via `URIToPath` + symlink-canonical path comparison (the
   `filepath.EvalSymlinks` treatment `filterWorkspaceLocations` got),
   so percent-encoding servers (spaces, CJK) and symlinked roots
   (`/tmp` vs `/private/tmp`) no longer lose diagnostics.
3. **`ByteCol` surrogate boundary** (`go/lsp/position.go`): verified bug —
   a UTF-16 column *inside* a surrogate pair (`col < utf16Col <
   col + 2`) falls through to the next rune's byte offset, violating the
   documented "start of the rune" contract (`ByteCol("😀x", 1)` returns 4,
   not 0). Fix: return the current rune's byte offset whenever
   `utf16Col < col + utf16RuneLen(r)`; emoji-line tests pin
   `ByteCol`/`UTF16Col` as proper inverses at pair boundaries.
4. **Silently swallowed invalidation failures**: verified in two places —
   `go/daemon/codeintel.go` `invalidateIndex` (`_, _ =
   d.kern.IndexUpdate(…)`) and the kernel's
   `invalidate_index_after_patch` (`let _ = index.update(&changes);` plus
   an `if let Ok` around `ensure_index`). Both now surface through the
   established mechanism: the daemon logs one line, records
   `{"status":"index_invalidation_failed","reason":"kernel-error"}`
   (status idiom, reason only), and clears `indexBuilt` so the next
   `code.*` call heals with a full build (full-sync semantics); the kernel
   hook records the analogous `index_invalidation_failed` status event
   (error text, never content) via `record_event` — the patch itself still
   never fails, but a stale index is no longer invisible.

**Final-closure minors** (post-review pass over the shipped V4):

5. **Sweep readability blind spot**: stamps gained the stat **mode bits**
   (§A) — a build-time-unreadable file now converges after `chmod +r`
   instead of staying permanently unindexed behind an unchanged
   (mtime, size) stamp.
6. **Sweep-failure test coverage**: both `noteSweepFailure` branches
   (kernel-error via an obstructed index database, scan-error via a
   hard scanner failure) are pinned by tests — log line, audit event
   with the classified reason, `indexBuilt` reset, and the heal-on-next-
   call path.
7. **Embeddings deadline**: every `router.Embed` runs under
   `embedTimeout` via the `embedWithDeadline` chokepoint (§C) — the V2
   "short context timeout" claim is now true in code, and a hanging
   provider can no longer stall each `code.search` for
   `providerHTTPTimeout` on every retrying sync.

### V4 testing plan (TDD order)

Failing test first at each layer; never weakened to pass.

1. `crates/carina-index`: `edges_store` resolves endpoints to enclosing
   symbols, upserts `source='lsp'` `confidence=1.0`, replaces an existing
   tree-sitter row for the same pair, and reports unresolvable/self edges
   in `skipped`; a tree-sitter re-resolve (`resolve_edges_for_name`) keeps
   a surviving LSP edge and never duplicates it; **replacing/deleting the
   src file and the dst file each cascade-drops the LSP edge**; `impact`
   surfaces `source: "lsp"` (and the 1.0 confidence) for lsp paths, mixed
   paths stay `tree-sitter`; pagerank weight reflects the upgraded edge.
2. `carina-kernel` service tests: `edges_store` round-trip; per-endpoint
   FileRead re-gate (a denied path lands in `skipped`, never stored);
   >256 edges is a param error; unknown-symbol endpoints skipped;
   `deny_capabilities=["FileRead"]` blocks the method; the status event is
   decision-linked. D1: a NaN/±Inf `query_vector_base64` component is a
   param error (failing-first). D4: a failing index update inside
   `invalidate_index_after_patch` records `index_invalidation_failed`.
3. `go/lsp`: `ByteCol` at surrogate boundaries (emoji lines) and inverse
   round-trips with `UTF16Col`; `collect` matches percent-encoded and
   symlink-canonical `publishDiagnostics` URIs (mock stream).
4. `go/daemon` — sweep: an out-of-band edit/removal/addition between two
   `code.search` calls is reflected (kernel-binary-gated), including a
   same-second same-size edit (nanosecond stamps), an identical-stamp
   racy write (racy-clean rule), and a readability flip (chmod-000 at
   build, `chmod +r` later — mode-bit stamps); the sweep makes
   no kernel call on an empty diff; `CARINA_INDEX_SWEEP=off` skips it;
   both sweep-failure branches surface (log + audit + `indexBuilt` reset:
   a failing `kernel.index.update` and a hard scanner failure). Embed
   deadline: a hanging embeddings provider degrades within `embedTimeout`
   at query time and inside the sync batches. Edges:
   a successful fake-LSP `code.refs` persists edges and a following
   `code.impact` reports `source: lsp`. D4: a failing `IndexUpdate` in
   `invalidateIndex` logs, audits, and heals on the next call.
5. `go/model-router` + `go/daemon` — rerank: `Router.Rerank`
   targeting/fallback/usage; adapter request/response fixtures for the
   voyage and cohere shapes; `CARINA_RERANK_MODEL` guard semantics (unset
   → bit-identical off; unknown prefix / missing key →
   `rerank:off(no-provider)` + audit event; registered → `rerank:<name>`
   ordering-only change); deadline: a hanging fake provider falls back to
   kernel order within the timeout (the V3 fallback test keeps passing).

Commands: `cargo test -p carina-index`, `cargo test --workspace`,
`cd go && go test ./...`.

### V4 risks

- **Sweep stamp precision** (as built): no Zig rebuild shipped — the sweep
  stats each supported file itself for nanosecond mtimes, and the
  racy-stamp rule covers writes sharing a timestamp with the scan that
  recorded them, so coarse or identical timestamps degrade to one redundant
  (content-hash cheap) update, never to stale rows.
- **Sweep cost**: one native walk + one stat per supported file + a map
  diff per `code.*` call; stat metadata only, and the env kill-switch
  covers pathological trees.
- **LSP edge half-life**: precise edges die with either endpoint file and
  only regrow through tool-triggered queries — impact labels stay honest
  (`lsp` only when a fully precise path produced the number).
- **Rerank egress surface**: snippets leave the machine — mitigated by the
  explicit-selection guard (no env, no egress; no fallback routing), the
  snippet-length cap, and the audit-visible degrade states.
- **Recompute/dedup interplay**: scoping the tree-sitter delete to
  `source='tree-sitter'` is load-bearing; the crate tests pin both the
  survival and the no-duplicate invariants.

## Deferred beyond V4

A–D above close the entire V3 deferred backlog (LSP-sourced edges, real
rerank providers, the mtime sweep, and all residual minors; patch-seeded
impact is subsumed by the sweep + `code.impact` composition: the sweep
keeps edges fresh across out-of-band edits and `code.impact` seeds by any
symbol a patch touches). Exactly three items remain deferred, each for a
stated reason:

- **Multi-root indexes** (§E design headroom): blocked on Carina sessions
  supporting multiple workspace roots — an architecture dependency of the
  session model, not an indexing gap.
- **Cloud semantic sync**: an explicit Nebutra Cloud product boundary —
  the index stays a strictly local, per-workspace derived artifact, and
  the only network I/O in the feature remains the daemon's governed
  provider calls.
- **ANN indexes / vector quantization**: deliberately rejected at local
  scale — brute-force cosine is deterministic and audit-reproducible, and
  it is only worth revisiting past ~100k chunks.
