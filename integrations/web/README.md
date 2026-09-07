# Carina Harness Web Surface

The primary Carina Harness surface: a quiet, session-first workspace inspired by
the useful parts of DeepSeek Harness (narrow navigation, explicit workspace and
mode selection, and a large composer) while retaining Carina's governed Gateway
contract. The left rail opens Harness, Inbox, Runs, Trajectory, Graph, and
Settings views; the main surface stays focused on a single authoritative
session.

The product UI is built with Vite 8 + React 19 and authored in TypeScript/TSX.
The checked-in `assets/` bundle
is generated from the canonical files under `docs/brand/`. Start the browser
surface from this directory:

```sh
npm install
npm run dev
open http://127.0.0.1:4173/integrations/web/
```

Run `npm run typecheck` before shipping. TypeScript strict mode covers the shared
browser and desktop frontend, including Gateway RPC payloads, session projections,
attachments, and Tauri workspace picker bindings.

Production output is written to `dist/`, which is the frontend loaded by the
Tauri shell. `src-app/` is the only source of the browser and desktop UI; serve
the Vite build output for production instead of opening source files directly.
Development keeps the `/integrations/web/` route for the repository workflow,
while production asset URLs are relative to `dist/index.html`. The same build
therefore works at an extracted archive root and inside Tauri without a fixed
server path prefix.

The standalone release archive can be served from its extracted directory:

```sh
cd carina-web-operator-<version>
python3 -m http.server 4173
open http://127.0.0.1:4173/
```

Regenerate the local assets after an approved brand change with
`node integrations/tauri/scripts/sync-assets.mjs`; do not edit the generated
files by hand.

Remote endpoints require `wss://`; plain `ws://` is accepted only for explicit
loopback IPs, including `ws://127.0.0.1:8777` with or without a path. Gateway
URLs with embedded credentials are rejected. On launch, the Harness automatically
connects to the configured Gateway unless **Settings > General > Connect on
launch** is disabled.

Non-sensitive preferences such as Gateway URL, role, theme, and panel visibility
are stored in `localStorage`. Gateway tokens are never written there: they are
kept in `sessionStorage`, scoped to the current browser tab, and discarded when
that tab closes. After an expired or rejected token, open **Settings > Gateway**,
replace the token, and reconnect. The Web Harness never bypasses Gateway token
validation; authentication and scope enforcement remain Gateway-authoritative.

Observer mode is the default and only requests read/stream scope. Operator mode
reconnects with a separately issued operator token and requests only read,
stream, and write scope. New runs use the remote-safe `harness.submit`
composition; the local-only
`session.create`, `artifact.upload`, and `execution.start` methods remain hidden
behind the daemon boundary. Approval actions still show exact RPC parameters
and require confirmation. Tauri uses its native folder picker for new local
workspaces. The browser reuses Gateway-authoritative workspace roots from the
session roster and never claims that a browser-local directory is a daemon
path. The composer accepts up to four PNG,
JPEG, GIF, or WebP images (4 MiB total) and sends them through the bounded
media input of `harness.submit`. Browser origin must be allowlisted by the
daemon.

The same Vite-built React TypeScript frontend is used by the browser and the Tauri
shell; there is no second UI implementation to drift. Trajectory tails canonical
`session.events.stream` notifications with a per-session `raw_cursor`, then
coalesces them into refreshes of the durable `session.items` projection. Session
switches unsubscribe the old stream, reconnects resume from the last cursor, and
the ephemeral debug ring is never a data source.
