# Carina SDKs

Typed JSON-RPC 2.0 clients for the `carina-daemon` socket. The TypeScript and
Python packages use independent semver (`0.2.0`); the Go package follows the
repository tag. All three currently target Carina Runtime `0.6.1` and use the authoritative registry in
`protocol/jsonrpc/methods.json`; events conform to `protocol/schemas/`.

| SDK | Package | Status |
|-----|---------|--------|
| TypeScript | `@carina/sdk` | Typed core parity, async event callbacks, bounded concurrent calls |
| Python | `carina-sdk` | Typed core parity, blocking event iterator, bounded calls |
| Go | `github.com/Nebutra/carina/sdk/go` | Typed core parity over the native RPC client |

```ts
import { CarinaClient } from '@carina/sdk'
const carina = new CarinaClient()
const session = await carina.createSession(process.cwd())
await carina.submitTask(session.session_id, 'fix failing tests')
console.log(await carina.replaySession(session.session_id))
```

Core parity covers `session.attach`, `session.events.stream`, `session.fork`,
`usage.cost`, `task.steer`, and `task.user.answer`. Transport failures reject or
raise pending calls instead of leaving them suspended. The SDKs do not promise
one wrapper per daemon RPC yet; use the public `call` method for less common
registry methods.

Run conformance tests with:

```bash
(cd sdk/typescript && npm ci && npm test)
(cd sdk/python && PYTHONPATH=src python3 -m unittest discover -s tests -v)
go test -race ./sdk/go
```
