import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { webcrypto } from 'node:crypto'
import vm from 'node:vm'
import ts from 'typescript'

const code = ts.transpileModule(readFileSync(new URL('../src/pages/AssemblyPage/useAssemblyDraft.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
let version = 0, state = { picks: [], marker: 'initial' }, dropAfterCommit = false, offline = false, failLoad = false, gate
const receipts = new Map(), requests = []
async function fetchServer(url, options = {}) {
  requests.push({ url, ...options })
  if (offline || (!options.method && failLoad)) throw new Error('network unavailable')
  if (!options.method) return { ok: true, json: async () => ({ version, state: structuredClone(state) }) }
  if (gate) await gate
  const body = JSON.parse(options.body)
  let result = receipts.get(body.requestId)
  if (!result) {
    if (body.version !== version) return { ok: false, status: 409, json: async () => ({ error: 'Другая вкладка обновила сборку' }) }
    version++
    state = { picks: [...state.picks, body.code], marker: body.code }
    result = { version, state: structuredClone(state), applied: true, message: `Отобран ${body.code}` }
    receipts.set(body.requestId, result)
  }
  if (dropAfterCommit) { dropAfterCommit = false; throw new Error('response lost after commit') }
  return { ok: true, status: 200, json: async () => structuredClone(result) }
}
function createTab() {
  const states = [], refs = []
  let si = 0, ri = 0, mounted = false
  const hooks = {
    useState(initial) { const i = si++; if (!(i in states)) states[i] = initial; return [states[i], next => { states[i] = next }] },
    useRef(initial) { const i = ri++; return refs[i] ??= { current: initial } },
    useEffect(effect) { if (!mounted) { mounted = true; effect() } },
  }
  const context = { exports: {}, require: id => id === 'react' ? hooks : { API_BASE_URL: '/api' }, crypto: webcrypto, AbortController, fetch: fetchServer }
  vm.runInNewContext(code, context)
  return () => { si = 0; ri = 0; return context.exports.default({ picks: [], marker: 'empty' }) }
}
const settle = () => new Promise(resolve => setImmediate(resolve))
const tabA = createTab(); tabA(); await settle()
dropAfterCommit = true
assert.equal(await tabA().assemblyAction('scan', { code: 'A' }), false)
const pending = requests.at(-1)
assert.equal(tabA().blocked, true)
assert.equal(state.picks.length, 1)
// While A has an uncertain response, a fresh tab picks another legitimate unit.
const tabB = createTab(); tabB(); await settle()
assert.equal(await tabB().assemblyAction('scan', { code: 'B' }), true)
assert.deepEqual(state.picks, ['A', 'B'])
assert.equal(await tabA().retry(), true)
assert.equal(receipts.size, 2)
assert.equal(requests.filter(r => r.method && r.body === pending.body).length, 2)
assert.deepEqual(Array.from(tabA().state.picks), ['A', 'B'], 'Retry must reconcile current state instead of showing the cached snapshot preceding tab B')

// A repeated click while the first command is in flight must not submit a second intent.
let release
gate = new Promise(resolve => { release = resolve })
const count = requests.filter(r => r.method).length
const first = tabA().assemblyAction('scan', { code: 'C' })
assert.equal(await tabA().assemblyAction('scan', { code: 'C' }), false)
assert.equal(requests.filter(r => r.method).length, count + 1)
gate = null; release(); assert.equal(await first, true)

// Stale B receives a conflict, loads A's state, and retry is read-only.
assert.equal(await tabB().assemblyAction('scan', { code: 'D' }), false)
assert.deepEqual(Array.from(tabB().state.picks), ['A', 'B', 'C'])
const rejectedCount = requests.filter(r => r.method).length
assert.equal(await tabB().retry(), true)
assert.equal(requests.filter(r => r.method).length, rejectedCount)

// Failure BEFORE reaching the server retries the original UUID; fresh mounts load persisted progress.
offline = true
assert.equal(await tabA().assemblyAction('scan', { code: 'E' }), false)
const beforeReach = requests.at(-1)
offline = false
assert.equal(await tabA().retry(), true)
assert.ok(requests.some((r, i) => i > requests.indexOf(beforeReach) && r.body === beforeReach.body))
const reloaded = createTab(); reloaded(); await settle()
assert.deepEqual(Array.from(reloaded().state.picks), ['A', 'B', 'C', 'E'])

// If the reconciliation GET itself fails, retain the same pending request and block new scans.
dropAfterCommit = true
assert.equal(await tabA().assemblyAction('scan', { code: 'F' }), false)
const uncertain = requests.at(-1)
failLoad = true
assert.equal(await tabA().retry(), false)
assert.equal(tabA().blocked, true)
assert.equal(await tabA().assemblyAction('scan', { code: 'G' }), false)
failLoad = false
assert.equal(await tabA().retry(), true)
assert.equal(receipts.size, 5)
assert.equal(requests.filter(r => r.method && r.body === uncertain.body).length, 3)
assert.deepEqual(Array.from(tabA().state.picks), ['A', 'B', 'C', 'E', 'F'])
console.log('Recovery checks passed: two tabs, lost committed response, current-state reconciliation, double click, conflict, offline retry, reload and interrupted recovery.')
