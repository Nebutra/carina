# Carina for VS Code

Local operator surface for `carina-daemon`. It exposes Agent View, attach, steer,
approval/question resolution, patch previews, and an explicit offline status.
It connects only to the configured local Unix socket; policy remains authoritative
in the daemon. Build with `npm install && npm test`, then package with `vsce`.
