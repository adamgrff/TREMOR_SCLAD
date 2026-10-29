import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import vm from 'node:vm'
import ts from 'typescript'

const root = fileURLToPath(new URL('../src/pages/HomePage/', import.meta.url))
function loader(overrides = {}) {
  const cache = new Map()
  return function load(path) {
    if (cache.has(path)) return cache.get(path)
    const exports = {}
    cache.set(path, exports)
    const code = ts.transpileModule(readFileSync(path, 'utf8'), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX } }).outputText
    vm.runInNewContext(code, {
      exports, ...overrides,
      require: name => {
        if (name === 'react') return overrides.react
        if (name === 'react/jsx-runtime') return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) }
        if (name.endsWith('.css')) return {}
        if (name.endsWith('/config/api')) return { API_BASE_URL: 'http://test/api' }
        if (overrides.modules?.[name]) return overrides.modules[name]
        return load(resolve(dirname(path), `${name}.ts`))
      },
    })
    return exports
  }
}
const data = loader()(resolve(root, 'homeStatisticsData.ts'))
const payload = (sessions = 0) => ({
  receiving: { sessions, receivedQuantity: null, placedQuantity: 20, pendingQuantity: 0 },
  stock: { totalQuantity: 50, occupiedCells: 2, totalCells: 60, skuCount: 10, lowStockCount: 1, lowStockItems: [{ sku: 'REAL-SKU', quantity: 3 }] },
  assembly: { completedOrders: null, queuedOrders: 8, activeOrders: 0, problemOrders: 2, averageAssemblySeconds: null },
})
assert.equal(data.statisticsPeriodValues['Сегодня'], 'today')
assert.equal(data.statisticsPeriodValues['7 дней'], '7d')
assert.equal(data.statisticsPeriodValues['30 дней'], '30d')
assert.equal(data.formatStatistic(null, 'ед.'), '—')
assert.equal(data.formatStatistic(undefined), '—')
assert.equal(data.formatStatistic(0, 'ед.'), '0 ед.')
assert.equal(data.formatAssemblyDuration(null), '—')
assert.equal(data.formatAssemblyDuration(0), '0 с')
assert.equal(data.formatAssemblyDuration(45), '45 с')
assert.equal(data.formatAssemblyDuration(360), '6 мин')
assert.equal(data.formatAssemblyDuration(90), '1 мин 30 с')
assert.equal(data.formatAssemblyDuration(59.6), '1 мин')
assert.equal(data.parseHomeStatistics(payload()).receiving.receivedQuantity, null)
assert.throws(() => data.parseHomeStatistics({}))
const bad = payload(); bad.stock.lowStockItems = [{ sku: 'BAD', quantity: -1 }]
assert.throws(() => data.parseHomeStatistics(bad))

function mount() {
  let state = null, previous, effect, cleanup, writes = 0
  const requests = []
  const load = loader({
    AbortController,
    react: {
      useState: () => [state, next => { state = next; writes++ }],
      useEffect: (callback, dependencies) => {
        if (dependencies[0] !== previous) { previous = dependencies[0]; effect = callback }
      },
    },
    fetch: (url, options) => new Promise((resolve, reject) => requests.push({ url, options, resolve, reject })),
  })
  const hook = load(resolve(root, 'useHomeStatisticsData.ts')).useHomeStatisticsData
  return {
    requests, render: period => hook(period), writes: () => writes,
    effects: () => { if (effect) { cleanup?.(); cleanup = effect(); effect = null } },
    unmount: () => cleanup?.(),
    respond: (index, body, ok = true) => requests[index].resolve({ ok, status: ok ? 200 : 500, json: async () => body }),
  }
}
const settle = () => new Promise(setImmediate)
const fixture = mount()
assert.equal(fixture.render('today'), null)
fixture.effects()
assert.equal(fixture.requests[0].url, 'http://test/api/home/statistics?period=today')
assert.equal(fixture.requests[0].options.cache, 'no-store')
fixture.respond(0, payload(1)); await settle()
assert.equal(fixture.render('today').receiving.sessions, 1)
assert.equal(fixture.render('7d'), null, 'Do not flash the previous period before its effect runs')
fixture.effects()
assert.equal(fixture.requests[0].options.signal.aborted, true)
assert.equal(fixture.requests[1].url.endsWith('period=7d'), true)
assert.equal(fixture.render('30d'), null)
fixture.effects()
fixture.respond(2, payload(30)); await settle()
assert.equal(fixture.render('30d').receiving.sessions, 30)
fixture.respond(1, payload(7)); await settle()
assert.equal(fixture.render('30d').receiving.sessions, 30, 'Late response cannot overwrite the selected period')
fixture.render('today'); fixture.effects()
fixture.respond(3, payload(99), false); await settle()
assert.equal(fixture.render('today'), null, 'HTTP error must not retain old values')
fixture.render('7d'); fixture.effects()
fixture.requests[4].reject(new Error('offline')); await settle()
assert.equal(fixture.render('7d'), null)
fixture.render('30d'); fixture.effects()
fixture.respond(5, {}); await settle()
assert.equal(fixture.render('30d'), null, 'Malformed response is unknown, not zero')
fixture.render('today'); fixture.effects()
fixture.unmount()
const writes = fixture.writes()
fixture.respond(6, payload(999)); await settle()
assert.equal(fixture.writes(), writes, 'Unmount ignores pending work')

// Render the actual window with the real formatters and fixed hook data.
function renderWindow(value) {
  const load = loader({
    react: { useState: initial => [initial, () => {}], useRef: current => ({ current }), useEffect: () => {} },
    modules: { './useHomeHistory': { useHomeHistory: () => ({ status: 'ready', events: [] }) }, './useHomeStatisticsData': { useHomeStatisticsData: () => value } },
  })
  const tree = load(resolve(root, 'StatisticsDraft.tsx')).default()
  const nodes = []
  function walk(node) {
    if (Array.isArray(node)) return node.forEach(walk)
    if (node == null || typeof node !== 'object') return
    if (typeof node.type === 'function') return walk(node.type(node.props))
    nodes.push(node)
    walk(node.props.children)
  }
  walk(tree)
  const text = node => Array.isArray(node) ? node.map(text).join('') : node == null ? '' : typeof node === 'object' ? text(node.props.children) : String(node)
  return { nodes, text }
}
const loaded = renderWindow(payload())
const metrics = loaded.nodes.filter(node => node.type === 'dd').map(node => loaded.text(node))
assert.deepEqual(metrics, ['0', '—', '20 ед.', '0 ед.', '50 ед.', '2 из 60', '10', '1', '—', '8', '0', '2', '—'])
assert.ok(loaded.nodes.some(node => node.type === 'li' && loaded.text(node) === 'SKU REAL-SKU3 ед.'))
const empty = payload(); empty.stock.lowStockItems = []; empty.stock.lowStockCount = 0
const emptyWindow = renderWindow(empty)
assert.ok(emptyWindow.nodes.some(node => node.type === 'li' && emptyWindow.text(node) === 'Нет позиций с низким остатком'))
const unknown = renderWindow(null)
assert.ok(unknown.nodes.filter(node => node.type === 'dd').every(node => unknown.text(node) === '—'))
assert.equal(unknown.nodes.filter(node => node.type === 'li').map(node => unknown.text(node)).join(''), '—')
assert.equal(unknown.nodes.filter(node => node.type === 'tbody').length, 1)
console.log('Home statistics data checks passed: period mapping, null/zero, field binding, duration, low-stock/empty states, independent history, request races, HTTP/offline errors and abort cleanup.')
