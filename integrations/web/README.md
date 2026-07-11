# Carina Web Operator

Static, local-first operator dashboard for agents, workflows, cost, and
needs-input state. Serve this directory over HTTP and connect it to the daemon's
WebSocket Gateway. Remote endpoints require `wss://`; plain `ws://` is accepted
only for explicit loopback IPs. Tokens are held in page memory only.

The dashboard requests observer/read scope by default. Write controls are hidden
until explicitly enabled, show the exact RPC parameters, and require confirmation.
Use a separately issued short-lived operator token for writes. Browser origin
must be allowlisted by the daemon.
