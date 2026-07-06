# Carina SDKs

Thin JSON-RPC 2.0 clients for the carina-daemon socket. All three SDKs speak the same protocol (`protocol/jsonrpc/methods.json`); events conform to `protocol/schemas/`.

| SDK | Package | Status |
|-----|---------|--------|
| TypeScript | `@carina/sdk` | Phase 0 — session/task calls |
| Python | `carina-sdk` | Phase 0 — session/task calls |
| Go | `github.com/Nebutra/carina/sdk/go` | Phase 0 — raw client |

```ts
import { CarinaClient } from '@carina/sdk'
const carina = new CarinaClient()
const session = await carina.createSession(process.cwd())
await carina.submitTask(session.session_id, 'fix failing tests')
console.log(await carina.replaySession(session.session_id))
```

Event streaming (`task.events.stream`) and approval flow (`task.action.approve`) land in Phase 1.
