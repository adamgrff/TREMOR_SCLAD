import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import vm from 'node:vm'
import ts from 'typescript'

const root = fileURLToPath(new URL('../src/', import.meta.url))
function loader(overrides = {}) {
  const cache = new Map()
  return function load(path) {
    if (cache.has(path)) return cache.get(path)
    const exports = {}
    cache.set(path, exports)
    const code = ts.transpileModule(readFileSync(path, 'utf8'), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText
    vm.runInNewContext(code, {
      exports, ...overrides,
      require: name => name === 'react' ? overrides.react : name.endsWith('/config/api') ? { API_BASE_URL: 'http://test/api' } : load(resolve(dirname(path), `${name}.ts`)),
    })
    return exports
  }
}
const load = loader()
const { getQueueOrders } = load(resolve(root, 'pages/AssemblyPage/assemblyQueue.ts'))
const { pendingQuantity, stockQuantity, queueQuantity } = load(resolve(root, 'pages/HomePage/homeSummaryData.ts'))
const order = (number, source = 'ozon') => ({ number, source })
const orders = [order('A'), order('A', 'preview'), order('B'), order('B', 'preview'), order('C'), order('D'), order('E')]
const state = {
  activeOrder: order('A'), completedOrders: [order('B')],
  problemOrders: [
    { order: order('C'), problem: { resolutionStatus: 'resolved' } },
    { order: order('D'), problem: { resolutionStatus: 'returned' } },
  ],
}
const before = JSON.stringify({ orders, state })
assert.deepEqual(Array.from(getQueueOrders(orders, state), item => `${item.source}:${item.number}`), ['preview:A', 'preview:B', 'ozon:D', 'ozon:E'])
assert.equal(JSON.stringify({ orders, state }), before, 'Shared selector must not mutate assembly state')
assert.equal(queueQuantity({ orders }, { state }), 4)
assert.equal(queueQuantity({ orders: [] }, { state: null }), 0)
assert.equal(queueQuantity({ orders }, { state: null }), 7)
assert.equal(pendingQuantity({ pendingQuantity: 0 }), 0)
assert.equal(stockQuantity({ products: [{ quantity: 3 }, { quantity: 5, archived: true }] }), 8)
assert.equal(stockQuantity({ products: [] }), 0)
for (const invalid of [{}, { pendingQuantity: null }, { pendingQuantity: -1 }, { pendingQuantity: '0' }]) assert.throws(() => pendingQuantity(invalid))
assert.throws(() => stockQuantity({}))
assert.throws(() => stockQuantity({ products: [{ quantity: null }] }))
assert.throws(() => queueQuantity({ orders }, {}), 'Missing draft is unknown, not empty')
assert.throws(() => queueQuantity({ orders }, { state: {} }))

function mount() {
  let summary, cleanup, writes = 0
  const requests = new Map()
  const load = loader({
    AbortController,
    react: {
      useState: initial => { summary = initial; return [summary, update => { summary = update(summary); writes++ }] },
      useEffect: callback => { cleanup = callback() },
    },
    fetch: (url, options) => new Promise((resolve, reject) => requests.set(url.replace('http://test/api', ''), { resolve, reject, options })),
  })
  load(resolve(root, 'pages/HomePage/useHomeSummary.ts')).useHomeSummary()
  return {
    requests, summary: () => summary, writes: () => writes, unmount: () => cleanup(),
    respond: (path, data, ok = true) => requests.get(path).resolve({ ok, status: ok ? 200 : 500, json: async () => data }),
  }
}
const settle = () => new Promise(setImmediate)
const success = mount()
assert.equal(Object.values(success.summary()).every(value => value === null), true)
assert.equal(success.requests.size, 4)
for (const request of success.requests.values()) assert.equal(request.options.cache, 'no-store')
success.respond('/receiving/pending-summary', { pendingQuantity: 0 })
success.respond('/product-catalog', { products: [{ quantity: 3, archived: true }, { quantity: 9 }] })
success.respond('/assembly/orders', { orders })
await settle()
assert.equal(success.summary().pending, 0)
assert.equal(success.summary().stock, 12)
assert.equal(success.summary().queue, null, 'Queue stays unknown until both requests complete')
success.respond('/assembly-preview/draft', { state })
await settle()
assert.equal(success.summary().queue, 4)
success.unmount()
for (const request of success.requests.values()) assert.equal(request.options.signal.aborted, true)

for (const failure of ['/receiving/pending-summary', '/product-catalog', '/assembly/orders', '/assembly-preview/draft']) {
  const fixture = mount()
  const responses = {
    '/receiving/pending-summary': { pendingQuantity: 2 },
    '/product-catalog': { products: [{ quantity: 5 }] },
    '/assembly/orders': { orders: [] },
    '/assembly-preview/draft': { state: null },
  }
  for (const [path, data] of Object.entries(responses)) fixture.respond(path, data, path !== failure)
  await settle()
  assert.equal(fixture.summary().pending, failure === '/receiving/pending-summary' ? null : 2)
  assert.equal(fixture.summary().stock, failure === '/product-catalog' ? null : 5)
  assert.equal(fixture.summary().queue, failure.startsWith('/assembly') ? null : 0)
  fixture.unmount()
}
const offline = mount()
for (const request of offline.requests.values()) request.reject(new Error('offline'))
await settle()
assert.equal(Object.values(offline.summary()).every(value => value === null), true)
offline.unmount()
const cancelled = mount()
cancelled.unmount()
cancelled.respond('/receiving/pending-summary', { pendingQuantity: 99 })
await settle()
assert.equal(cancelled.writes(), 0, 'Late response cannot update an unmounted page')
console.log('Home summary checks passed: shared queue exclusions, source identity, returned problems, archived stock, zero/unknown, independent HTTP failures, offline and abort cleanup.')
