---
name: carina-docs
description: Use Carina documentation to answer questions about the local-first AI agent runtime (policy kernel, audit, sessions, JSON-RPC, workflows).
---

# Carina docs skill

## When to use
- Installing or running the Carina CLI / daemon
- Policy profiles, capabilities, approvals
- Sessions, tasks, audit, rollback
- JSON-RPC methods and gateway usage
- Workflows, tools, MCP, workers

## Sources
- Human docs: https://docs.carina.dev
- LLM index: https://docs.carina.dev/llms.txt
- Full index: https://docs.carina.dev/llms-full.txt
- Method catalog (stable): https://docs.carina.dev/data/rpc-catalog-0.6.x.json
- Method catalog (next): https://docs.carina.dev/data/rpc-catalog-next.json

## Preferred workflow
1. Read llms.txt for the page map.
2. Fetch the relevant page as Markdown (`…/path/index.md`).
3. For RPC details, consult the method catalog JSON.
4. Cite the docs URL in answers.

## Install
```bash
# Cursor / skill runners (example)
npx skills add https://docs.carina.dev
```
