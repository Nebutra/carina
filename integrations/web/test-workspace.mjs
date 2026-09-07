import assert from 'node:assert/strict'
import { decodeWorkspaceTree } from './src-app/lib/workspace.ts'

const bounded = decodeWorkspaceTree({
  files: [
    { path: 'src\\main.ts', size: 42, binary: false, large: false, language: 'TypeScript', mtime: 9 },
    { path: '../escape.txt', size: 1 },
    { path: '/absolute.txt', size: 1 },
    null,
  ],
  truncated: true,
})
assert.deepEqual(bounded, {
  files: [{ path: 'src/main.ts', size: 42, binary: false, large: false, language: 'TypeScript', mtime: 9 }],
  truncated: true,
})

const legacy = decodeWorkspaceTree([{ path: 'README.md', size: 'unknown', binary: true }])
assert.deepEqual(legacy, {
  files: [{ path: 'README.md', size: 0, binary: true, large: false, language: '', mtime: 0 }],
  truncated: false,
})

assert.deepEqual(decodeWorkspaceTree({ files: 'invalid', truncated: 'yes' }), { files: [], truncated: false })

console.log('workspace tree boundary decoder: ok')
