import { readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { spawnSync } from 'node:child_process'

const directory = new URL('./', import.meta.url)
const tests = readdirSync(directory).filter(name => /^test-.+\.mjs$/.test(name)).sort()
if (!tests.length) throw new Error('No frontend checks found')

for (const name of tests) {
  const result = spawnSync(process.execPath, [fileURLToPath(new URL(name, directory))], { stdio: 'inherit' })
  if (result.error) throw result.error
  if (result.status !== 0) process.exit(result.status ?? 1)
}
console.log(`All ${tests.length} frontend checks passed.`)
