# Carina Harness Desktop

This is the Tauri shell for the Carina Harness React surface. It deliberately
owns no session or policy logic: the Vite-built React UI and the local Gateway
remain the same authority used by the browser surface and the TUI.

## Architecture

```text
Tauri window
    -> integrations/web/dist (Vite + React shared Harness UI)
    -> WebSocket Gateway (gateway.hello / runtime.initialize)
    -> Carina daemon + capability kernel
```

The shell keeps the native window small and predictable (1180x760, minimum
860x600), enables only the WebSocket origins required for a local Gateway, and
does not expose arbitrary filesystem or shell commands to the webview. The
typed `pick_workspace` command opens the OS folder picker and returns only the
selected directory to the shared UI; file attachments still use the bounded
image upload contract handled by the Gateway.

Selecting a workspace in the desktop shell invokes the restricted
`carina harness bootstrap` owner adapter. It starts or reuses that workspace's
runtime, enables an ephemeral loopback Gateway, and mints a short-lived,
tenant-bound token. The token is held only in the React process memory; the
remembered workspace path is the only persisted desktop preference. The shell
does not expose arbitrary command execution or a remote token issuer. The
matching Carina CLI and runtime must be installed and discoverable (or supplied
through the absolute `CARINA_CLI_BIN` override).

Windows desktop installers are intentionally not published yet. The current
owner transport is Unix-socket based; a Windows named-pipe implementation with
equivalent ACL and process-identity guarantees must land before that target is
advertised.

Before a dev or release build, the `predev`/`prebuild` hook synchronizes the
approved Web Harness assets and regenerates the desktop PNG/ICNS/ICO set from
the canonical raster symbol in `docs/brand/`, then builds the already-installed
Web workspace. The release packager installs both lockfiles before entering the
npm build lifecycle. The generated `src-tauri/gen/` schema cache is ignored and
can be recreated by Cargo.

## Development

From the repository root, start the Vite UI and then the Tauri dev shell:

```sh
cd integrations/web
npm install
npm run dev
cd integrations/tauri
npm install
npm run dev
```

The current React frontend is intentionally shared rather than copied. The
Tauri `predev` and `prebuild` hooks build `integrations/web/dist` before the
desktop shell starts. Run `npm run build` only on a host with the Tauri 2
toolchain and platform WebView dependencies installed.
