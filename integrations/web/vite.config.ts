import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const here = path.dirname(fileURLToPath(import.meta.url))
const webPackage = JSON.parse(fs.readFileSync(path.join(here, 'package.json'), 'utf8')) as { version: string }

export default defineConfig(({ command, isPreview }) => ({
  root: 'src-app',
  base: command === 'build' || isPreview ? './' : '/integrations/web/',
  publicDir: '../assets',
  define: { __CARINA_VERSION__: JSON.stringify(webPackage.version) },
  plugins: [react()],
  resolve: { alias: { '@': path.join(here, 'src-app') } },
  build: {
    outDir: '../dist',
    emptyOutDir: true,
  },
}))
