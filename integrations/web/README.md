# Carina Web Operator

Static, local-first operator dashboard for the authoritative `agent.view` roster,
workflows, cost, and needs-input state. It consumes session notifications and
resynchronizes snapshots after reconnect with bounded exponential backoff. Serve
this directory over HTTP and connect it to the daemon's WebSocket Gateway. Remote
endpoints require `wss://`; plain `ws://` is accepted only for explicit loopback
IPs, including `ws://127.0.0.1:8765` with or without a path. Gateway URLs with
embedded credentials are rejected. Tokens are held in page memory only.

The dashboard requests observer read/stream scope by default. Operator mode
reconnects with a separately issued operator token and requested write/admin
scopes. Every write is checked against the server's `gateway.methods` descriptors,
shows exact RPC parameters, and requires confirmation. Methods that are local-only
are presented as such instead of issuing an RPC that the Gateway must reject.
Browser origin must be allowlisted by the daemon.
