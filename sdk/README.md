# Carina SDKs

Typed JSON-RPC 2.0 clients for the `carina-daemon` socket. The TypeScript and
Python packages use independent semver (`0.2.0`); the Go package follows the
repository tag. All three currently target Carina Runtime `0.6.5` and use the authoritative registry in
`protocol/jsonrpc/methods.json`; events conform to `protocol/schemas/`.

| SDK | Package | Status |
|-----|---------|--------|
| TypeScript | `@carina/sdk` | Typed core parity, async event callbacks, bounded concurrent calls |
| Python | `carina-sdk` | Typed core parity, blocking event iterator, bounded calls |
| Go | `github.com/Nebutra/carina/sdk/go` | Typed core parity over the native RPC client, typed event stream with bounded overflow-safe delivery |

```ts
import { CarinaClient } from '@carina/sdk'
const carina = new CarinaClient()
const session = await carina.createSession(process.cwd())
await carina.submitTask(session.session_id, 'fix failing tests')
console.log(await carina.replaySession(session.session_id))
```

Typed parity covers session attach/stream/fork, resumable threads
(start/resume/fork with streamed turns), usage, steering and questions,
checkpoints (list/preview/summarize/restore), artifact inspection and verified
download, plus workflow control, workers, approval relay, doctor, agent
inventory, trusted channel injection, and the local extension inventory. Transport
failures reject or raise pending calls instead of leaving them suspended. Use
the public `call` method for less common registry methods. Security and hosting
boundaries are documented in [runtime ecosystem contracts](../docs/runtime-ecosystem.md).

Run conformance tests with:

```bash
(cd sdk/typescript && npm ci && npm test)
(cd sdk/python && PYTHONPATH=src python3 -m unittest discover -s tests -v)
go test -race ./sdk/go
```

Packaged-daemon CI can enable the opt-in read-only smoke contract with
`CARINA_CONFORMANCE_SOCKET=/path/to/daemon.sock`.

SDK initialization accepts Runtime protocol `1.x` additive evolution, but
requires event schema `0.3.x`. While the event schema is pre-1.0, a minor bump
may change event semantics; patch releases on the same minor line are treated
as additive. Clients fail initialization instead of guessing how to interpret
an incompatible authoritative event stream.
