# Client and Platform Boundaries

Carina's runtime owns policy, execution, sessions, events, approvals, audit, and
structured artifacts. Clients render and operate those contracts; they do not
duplicate authority.

| Surface | Supported contract | Boundary |
|---|---|---|
| VS Code | Checksummed VSIX, local daemon socket, Agent View and explicit operator commands | IDE client only; daemon remains authoritative |
| Web | Checksummed static release archive and read-first WebSocket Gateway with scoped, short-lived tokens | No token persistence; writes require operator scope and confirmation |
| Tauri desktop | Shared `integrations/web` Harness UI in a native window, same scoped Gateway and event contracts | Shell owns window lifecycle only; no duplicate policy or session authority |
| Mobile | Future client over the same Gateway and event contracts | Not embedded in the runtime |
| Browser/computer use | Future sandboxed worker adapter | Never an implicit runtime capability |
| Artifacts | Runtime emits typed references, provenance and policy labels | Rendering and sharing belong to clients/Nebutra Cloud |

Distribution sources live under `packaging/`: native npm packages, a Homebrew
formula template, Linux tar/deb/rpm recipes, and non-root daemon/worker
container images. Release builds must publish checksums, SPDX SBOMs, and
platform provenance/attestations. A file or template in this repository is not
a claim that the corresponding package is already public.
