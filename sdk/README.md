# Pi-OS SDKs

Thin JSON-RPC 2.0 clients for the pi-daemon socket. All three SDKs speak the same protocol (`protocol/jsonrpc/methods.json`); events conform to `protocol/schemas/`.

| SDK | Package | Status |
|-----|---------|--------|
| TypeScript | `@pi-os/sdk` | Phase 0 — session/task calls |
| Python | `pi-os-sdk` | Phase 0 — session/task calls |
| Go | `github.com/TsekaLuk/pi-os/sdk/go` | Phase 0 — raw client |

```ts
import { PiClient } from '@pi-os/sdk'
const pi = new PiClient()
const session = await pi.createSession(process.cwd())
await pi.submitTask(session.session_id, 'fix failing tests')
console.log(await pi.replaySession(session.session_id))
```

Event streaming (`task.events.stream`) and approval flow (`task.action.approve`) land in Phase 1.
