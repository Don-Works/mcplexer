import { mkdir, readdir, rm, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = path.dirname(fileURLToPath(import.meta.url))
const repoRoot = path.resolve(scriptDir, '..', '..')
const distDir = path.join(repoRoot, 'internal', 'web', 'dist')
const keepFile = path.join(distDir, '.gitkeep')

await mkdir(distDir, { recursive: true })

for (const entry of await readdir(distDir)) {
  if (entry === '.gitkeep') continue
  await rm(path.join(distDir, entry), { recursive: true, force: true })
}

await writeFile(keepFile, '')
