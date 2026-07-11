# Enterprise Deployment (Phase 5)

Carina ships with the controls an organization needs to run agents on shared
infrastructure. Everything here is opt-in — a local single user pays no cost.

Configure a policy directory (default `~/.carina/policy`, or `carina-daemon
--policy <dir>`). All files are optional.

## 1. Policy bundle — `bundle.toml`

Organization-wide rules that **can only tighten** a session's permission
profile. A deny in the bundle overrides any allow the profile grants. See
[`examples/policy/bundle.toml`](../examples/policy/bundle.toml).

```toml
name = "acme-corp"
deny_capabilities = ["RemoteExecute"]   # always denied
max_command_risk = 2                    # hard ceiling on command risk
deny_network_hosts = ["pastebin.com"]   # always blocked
require_approval = ["PatchApply"]        # force approval even if auto-allowed
```

The bundle is applied after profile evaluation, so it is impossible to author
a bundle that loosens a profile — verified by `policy_bundle_cannot_loosen`.

## 2. Role-based approval — `approval.json`

Maps a minimum command risk level to the role required to approve it. An
approver lacking the role is rejected and the rejection is audited.

```json
[
  { "min_risk": 2, "role": "tech-lead" },
  { "min_risk": 4, "role": "security-lead" }
]
```

```bash
carina approve <session> <decision_id> tech-lead   # role supplied at approval time
```

Roles come from the `IdentityProvider` seam (`go/daemon/identity.go`) — the
integration point for SSO/OIDC. The default `LocalIdentity` treats the OS
user as an admin.

For Nebutra-managed deployments, SSO/OIDC role resolution belongs to Nebutra
Cloud (`nebutra.com`). Carina consumes resolved identity through the
`IdentityProvider` seam; it should not embed account, organization, or
multi-endpoint sync state in the local runtime. See
[`docs/nebutra-cloud-boundary.md`](nebutra-cloud-boundary.md).

## 3. Signed plugins — `trusted-keys`

One base64 ed25519 public key per line. When any key is trusted, **every**
plugin must be signed by one of them; unsigned or wrongly-signed modules are
refused before instantiation.

```bash
# publisher signs the module
openssl ... # or any ed25519 tool -> module.sig
echo "<base64 pubkey>" >> ~/.carina/policy/trusted-keys
carina plugin run <session> plugin.toml module.wasm module.sig
```

Verified end to end by `TestSignedPluginEnforcement` (unsigned refused,
rogue-signed refused, trusted-signed runs).

## 4. Extension lockdown — `extensions.json`

`<PolicyDir>/extensions.json` is the org tier of the tri-level extension
enable merge (`safe_mode > org > project > user`). It is disable-only: it can
switch extensions off but structurally cannot enable anything, and an
org-disabled extension cannot be re-enabled by project or user config
(`ErrOrgDisabled`). A present-but-malformed or unreadable file fails closed
to disable-all — a broken org lockdown never silently loosens.

```json
{ "disabled": ["some-extension"], "disable_all": false }
```

## 5. Managed configuration — `/etc/carina/managed.json`

An admin-owned managed file (`/etc/carina/managed.json` on Unix,
`C:\ProgramData\carina\managed.json` on Windows) carries `values` plus
`locked_keys`. Locked keys are re-applied after every other config layer
(defaults → managed → global → project → env), so no global file, project
file, or environment variable can override them, and an explicitly-set CLI
flag that conflicts with a locked key is a startup error naming the lock's
source. The file is watched, so managed edits trigger a reload.

```json
{ "values": { "offline": true }, "locked_keys": ["offline"] }
```

## 6. Centralized audit — `carina export`

```bash
carina export <session_id>   # full audit bundle: profile + every event, in order
```

The bundle is suitable for shipping to a central audit store. Every side
effect in the session is present with its permission decision.

Future Nebutra Cloud audit sync should upload explicit bundles or checkpoints
from this surface, preserving local event hashes rather than rewriting local
history.

## 7. Offline mode

```bash
carina-daemon --offline   # only the mock model provider; no request leaves the host
```

## What's enforced

| Control | Mechanism | Test |
|---------|-----------|------|
| Org-wide mandatory denies | policy bundle, tighten-only | `policy_bundle_only_tightens`, `TestEnterprisePolicyBundleAndRBAC` |
| Role-gated approval | approval policy + `approve_as` | `TestRBACApprovalRequiresRole` |
| Signed plugins | ed25519 verify before run | `TestSignedPluginEnforcement`, `signing::tests` |
| Extension lockdown | org tier of the enable merge, fail-closed | `TestEffectiveEnabledTruthTable`, `TestLoadOrgPolicyMissingZeroAndMalformedFailsClosed` |
| Managed-locked config | locked keys re-applied after all layers | `TestManagedLockedKeySurvivesAllLayers`, `TestManagedReloadReappliesLocks` |
| Centralized audit | `audit.export` | `TestEnterprisePolicyBundleAndRBAC` |
| Offline mode | provider registration gate | — |
