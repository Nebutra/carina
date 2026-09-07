import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const webDir = path.dirname(fileURLToPath(import.meta.url))
const sourceRoot = path.join(webDir, 'src-app')

function walk(directory) {
  const entries = fs.readdirSync(directory, { withFileTypes: true })
  return entries.flatMap((entry) => {
    const target = path.join(directory, entry.name)
    if (entry.isDirectory()) return walk(target)
    return /\.(?:tsx?|css)$/.test(entry.name) ? [target] : []
  })
}

function relative(target) {
  return path.relative(webDir, target).split(path.sep).join('/')
}

function fail(message) {
  throw new Error(`design-system-check: ${message}`)
}

const sourceFiles = walk(sourceRoot)
const featureFiles = sourceFiles.filter((file) => {
  const name = relative(file)
  return !name.startsWith('src-app/design-system/')
})

// A third-party primitive or icon is a supplier concern. Features and
// patterns consume the project-owned design-system barrel instead.
const supplierImport = /(?:from\s*|import\s*\(\s*)['"]([^'"]+)['"]/g
const blockedPackages = /^(?:lucide-react(?:\/|$)|@radix-ui\/|@react-aria\/|react-aria(?:\/|$)|@ariakit\/|@headlessui\/|@chakra-ui\/|@mui\/|@mantine\/|@heroui\/|antd(?:\/|$)|@astryxdesign\/)/
for (const file of featureFiles) {
  const text = fs.readFileSync(file, 'utf8')
  if (/from\s*['"][^'"]*components\/ui(?:['"]|\/)/.test(text)) fail(`${relative(file)} bypasses the design-system barrel; import from src-app/design-system instead`)
  if (/style\s*=\s*\{\{[^}]*#[0-9a-f]{3,8}\b/i.test(text)) fail(`${relative(file)} contains a raw inline color; use a semantic token`)
  for (const match of text.matchAll(supplierImport)) {
    if (blockedPackages.test(match[1])) fail(`${relative(file)} imports third-party UI supplier ${match[1]}; consume src-app/design-system instead`)
  }
  // Feature and pattern code must consume the project-owned control API.
  // Native controls remain an implementation detail of components/ui.
  for (const nativeControl of ['button', 'input', 'textarea', 'label']) {
    if (new RegExp(`<${nativeControl}\\b`).test(text)) fail(`${relative(file)} renders native <${nativeControl}>; consume the project-owned design-system primitive`)
  }
}

const barrel = path.join(sourceRoot, 'design-system', 'index.ts')
assert.ok(fs.existsSync(barrel), 'missing src-app/design-system/index.ts project boundary')
const barrelText = fs.readFileSync(barrel, 'utf8')
for (const exportName of ['Button', 'Input', 'Textarea', 'Badge', 'Checkbox', 'Switch', 'Dialog', 'Tooltip', 'Icon', 'IconButton', 'cn']) {
  assert.match(barrelText, new RegExp(`\\b${exportName}\\b`), `design-system barrel must export ${exportName}`)
}

const requiredComponents = ['button.tsx', 'input.tsx', 'textarea.tsx', 'badge.tsx', 'checkbox.tsx', 'switch.tsx', 'dialog.tsx', 'tooltip.tsx', 'icon.tsx', 'label.tsx']
for (const component of requiredComponents) {
  assert.ok(fs.existsSync(path.join(sourceRoot, 'design-system/primitives', component)), `missing core component ${component}`)
}

const requiredPatterns = ['composer.tsx', 'settings-dialog.tsx', 'empty-state.tsx', 'session-list.tsx', 'trajectory.tsx', 'page-header.tsx', 'metric-group.tsx', 'chat-start.tsx', 'surface.tsx', 'conversation-tabs.tsx', 'files-panel.tsx', 'terminal-panel.tsx', 'context-panel.tsx']
for (const pattern of requiredPatterns) {
  assert.ok(fs.existsSync(path.join(sourceRoot, 'patterns', pattern)), `missing project pattern ${pattern}`)
}

const cssFiles = sourceFiles.filter((file) => file.endsWith('.css'))
for (const file of cssFiles) {
  const text = fs.readFileSync(file, 'utf8')
  const rawHex = text.match(/#[0-9a-f]{3,8}\b/gi)
  if (rawHex) fail(`${relative(file)} contains raw color ${rawHex[0]}; use a semantic brand token`)
  if (/\b(?:rgb|rgba|hsl|hsla|oklch)\s*\(/i.test(text)) fail(`${relative(file)} contains raw color function; use a semantic brand token`)
  if (/border-radius\s*:\s*(?:\d+(?:\.\d+)?px|\d+(?:\.\d+)?rem)\b/i.test(text)) fail(`${relative(file)} contains raw border radius; use a radius token`)
  if (/box-shadow\s*:\s*(?!none\b|var\(--focus-ring\))/i.test(text)) fail(`${relative(file)} contains a layout shadow; use flat token hierarchy (focus ring is the only exception)`)
}

const app = fs.readFileSync(path.join(sourceRoot, 'App.tsx'), 'utf8')
assert.match(app, /from ['"](?:\.\/design-system|@\/design-system)['"]/)
assert.match(app, /from ['"](?:\.\/patterns|@\/patterns)['"]/)
assert.match(app, /aria-label|aria-labelledby/)

const dialogs = sourceFiles.filter((file) => /dialog/i.test(path.basename(file)))
for (const file of dialogs) {
  const text = fs.readFileSync(file, 'utf8')
  // Feature dialogs inherit the keyboard/focus contract from the project
  // primitive. Validate the primitive itself below, then only require that
  // feature code composes DialogContent rather than recreating a modal DOM.
  if (!relative(file).startsWith('src-app/design-system/primitives/')) {
    assert.match(text, /<Dialog(?:\s|>)/)
    assert.match(text, /<DialogContent(?:\s|>)/)
    continue
  }
  // Radix owns the dialog role, aria-modal/label wiring, Escape handling,
  // focus containment, and focus restoration. The project primitive must
  // compose those parts instead of duplicating their DOM implementation.
  assert.match(text, /DialogPrimitive\.Content/)
  assert.match(text, /DialogPrimitive\.Portal/)
  assert.match(text, /Escape|focus/i)
}

const brandTokens = fs.readFileSync(path.join(webDir, 'assets/brand-variables.css'), 'utf8')
assert.match(fs.readFileSync(path.join(sourceRoot, 'styles.css'), 'utf8'), /brand-variables\.css/)
assert.match(brandTokens, /--spacing-4:/)
assert.match(brandTokens, /--radius-md:/)
assert.match(brandTokens, /--focus-ring:/)

console.log(`design-system quality gate: ok (${featureFiles.length} feature files, ${requiredComponents.length} core components)`)
