# Carina for VS Code

Local operator surface for `carina-daemon`. It consumes the authoritative
`agent.view` roster, subscribes to session notifications, and resynchronizes its
snapshot after reconnect with bounded exponential backoff. It exposes actionable
session inspection, steer, approval/question resolution, patch previews, and an
explicit stale/offline status.
It connects only to the configured local Unix socket; policy remains authoritative
in the daemon. Build with `npm install && npm test`, then package with `vsce`.
