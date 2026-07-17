# Authoring guide (Carina docs)

Inspired by Claude Code / Mintlify information architecture, adapted for a **local agent runtime**.

## Audience paths

| Path | Sidebar focus | Goal |
| --- | --- | --- |
| Beginner | Get started only | Install → doctor → run → audit → rollback in &lt; 15 min |
| Operator | Use Carina | Daily CLI/TUI, workflows, workers |
| Integrator | Embed Carina | JSON-RPC, Gateway, SDKs |
| Security | How Carina works | Policy, audit, profiles |

Do not put protocol dumps or math demos on the beginner path.

## Page template

```markdown
# Title
> One sentence: what you can do after reading

## What you'll get
- 2–4 concrete outcomes

## Fastest path
Steps + copyable commands (match real CLI)

## How this ties to Carina
Link to policy / audit / session when relevant (differentiation)

## If it fails
2–5 failure modes → FAQ / doctor

## Go deeper
Links to source-of-truth + related pages
```

**Recipe pages** (common workflows): goal → prerequisites → steps → expected output → related capabilities.

## Voice

- Short paragraphs; prefer tables and steps over essays.
- Use Carina terms: *session*, *profile*, *capability*, *patch*, *worker* — not Claude-specific *skills/hooks* unless we literally have them.
- Never document vaporware as GA; use `Badge` alpha + boundary.
- EN and zh-cn share structure; translate, don't invent different IA.

## Consistency

See `FEATURE_MAP.md`. Protocol and CLI win over narrative docs.

## Components (prefer these)

| Need | Component |
| --- | --- |
| Sequential install | `Steps` / `Step` |
| Multi-language samples | `Tabs` / `Tab` or Code Group |
| Params | `ParamField` / `ResponseField` |
| Warnings | `Callout` |
| Trees | `Tree` |
| Diagrams | `Mermaid` |
| Term hints | `Tooltip` |
| Progressive detail | `Expandable` |
| Math | `$…$` / `$$…$$` or `<Math>` |
