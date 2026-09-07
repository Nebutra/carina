import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const root = path.resolve(here, '../../..')
const web = path.join(root, 'integrations/web')
const brand = path.join(root, 'docs/brand')
const assets = path.join(web, 'assets')
const tauriIcons = path.join(root, 'integrations/tauri/src-tauri/icons')

const files = [
  ['design-system/variables.css', 'brand-variables.css'],
  ['assets/logo/carina-symbol.svg', 'logo/carina-symbol.svg'],
  ['assets/logo/carina-symbol-high-contrast.svg', 'logo/carina-symbol-high-contrast.svg'],
  ['assets/logo/carina-horizontal-brand.svg', 'logo/carina-horizontal-brand.svg'],
  ['assets/logo/carina-horizontal-monochrome.svg', 'logo/carina-horizontal-monochrome.svg'],
  ['assets/logo/carina-sprite.svg', 'logo/carina-sprite.svg'],
  ['assets/fonts/geist-sans-latin-variable.woff2', 'fonts/geist-sans-latin-variable.woff2'],
  ['assets/fonts/geist-mono-latin-variable.woff2', 'fonts/geist-mono-latin-variable.woff2'],
]

function canonicalizeIcns(file) {
  const input = fs.readFileSync(file)
  if (input.length < 8 || input.toString('ascii', 0, 4) !== 'icns') {
    throw new Error('generated Tauri ICNS has an invalid header')
  }
  const chunks = []
  for (let offset = 8; offset < input.length;) {
    const length = input.readUInt32BE(offset + 4)
    if (length < 8 || offset + length > input.length) {
      throw new Error('generated Tauri ICNS has an invalid chunk')
    }
    chunks.push(input.subarray(offset, offset + length))
    offset += length
  }
  chunks.sort((left, right) => left.subarray(0, 4).compare(right.subarray(0, 4)))
  const header = Buffer.alloc(8)
  header.write('icns', 0, 'ascii')
  header.writeUInt32BE(8 + chunks.reduce((total, chunk) => total + chunk.length, 0), 4)
  fs.writeFileSync(file, Buffer.concat([header, ...chunks]))
}

for (const [source, target] of files) {
  const destination = path.join(assets, target)
  fs.mkdirSync(path.dirname(destination), { recursive: true })
  fs.copyFileSync(path.join(brand, source), destination)
}

if (!process.argv.includes('--web-only')) {
  const iconSource = path.join(brand, 'assets/logo/raster/carina-symbol.png')
  const tauriCli = path.join(root, 'integrations/tauri/node_modules/@tauri-apps/cli/tauri.js')
  const generated = fs.mkdtempSync(path.join(os.tmpdir(), 'carina-tauri-icons-'))
  try {
    const result = spawnSync(
      process.execPath,
      [tauriCli, 'icon', iconSource, '--output', generated],
      { stdio: 'inherit' },
    )
    if (result.status !== 0) {
      throw new Error(`Tauri icon generation failed with status ${result.status ?? 'unknown'}`)
    }
    canonicalizeIcns(path.join(generated, 'icon.icns'))
    fs.mkdirSync(tauriIcons, { recursive: true })
    for (const name of [
      '32x32.png',
      '128x128.png',
      '128x128@2x.png',
      'icon.png',
      'icon.icns',
      'icon.ico',
    ]) {
      fs.copyFileSync(path.join(generated, name), path.join(tauriIcons, name))
    }
  } finally {
    fs.rmSync(generated, { recursive: true, force: true })
  }
}

const desktopSummary = process.argv.includes('--web-only') ? '' : ' and the Tauri desktop icons'
console.log(`synced ${files.length} canonical Carina assets${desktopSummary}`)
