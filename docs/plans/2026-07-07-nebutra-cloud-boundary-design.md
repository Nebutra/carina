# Nebutra Cloud Boundary For Identity And Sync

## Goal

Codex-style cloud/app-server coupling should not be absorbed into the Carina
local runtime. If Carina gains multi-endpoint identity or sync, that product
surface belongs behind Nebutra Cloud (云毓智能, `nebutra.com`).

This design makes the boundary explicit in docs and code without shipping a
sync connector.

## Approach

Use three layers:

1. **Product boundary document**: define what stays local, what Nebutra Cloud
   owns, and what a future sync connector may sync.
2. **Config guard**: add `nebutra_cloud_endpoint` and `nebutra_sync_mode`.
   Defaults are `https://nebutra.com` and `off`.
3. **Runtime observability**: expose the configured endpoint and sync mode in
   daemon status, while keeping local action authority unchanged.

## Non-Goals

- No cloud sync loop.
- No app-server protocol.
- No raw workspace or secret upload.
- No change to BYOK priority.
- No cloud-originated bypass around the capability kernel.

## Validation

- `nebutra_sync_mode` accepts only `off` until a Nebutra connector exists.
- non-local Nebutra endpoints must be HTTPS.
- daemon status must show the boundary configuration without enabling sync.

