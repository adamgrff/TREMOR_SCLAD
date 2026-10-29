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
    const exports = {}; cache.set(path, exports)
    const code = ts.transpileModule(readFileSync(path, 'utf8'), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX } }).outputText
    vm.runInNewContext(code, { exports, ...overrides, require: name => {
      if (name === 'react') return overrides.react
      if (name === 'react/jsx-runtime') return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) }
      if (name.endsWith('.css')) return {}
      if (name.endsWith('/config/api')) return { API_BASE_URL: 'http://test/api' }
      if (overrides.modules?.[name]) return overrides.modules[name]
      return load(resolve(dirname(path), `${name}.ts`))
    } })
    return exports
  }
}
const data = loader()(resolve(root, 'homeHistoryData.ts'))
const event = (id, occurredAt, process = 'receiving') => ({ id, occurredAt, process, event: 'Реальное событие', description: 'Факт из БД', references: [{ type: 'sku', value: 'REAL' }, { type: 'cell', value: 'A1' }, { type: 'order', value: '123' }] })
const events = [event('a', '2026-10-28T23:59:00Z'), event('b', '2026-10-29T00:01:00Z', 'assembly'), event('c', '2026-10-29T00:01:00Z', 'ozon')]
assert.equal(data.parseHomeHistory({ events }).length, 3)
assert.equal(data.sortHomeHistory(events, 'desc').map(x => x.id).join(), 'c,b,a')
assert.equal(data.sortHomeHistory(events, 'asc').map(x => x.id).join(), 'a,b,c')
assert.equal(events[0].id, 'a', 'Sorting must not mutate the loaded array')
assert.equal(events[0].references.map(data.formatHistoryReference).join(), 'SKU REAL,A1,№123')
assert.equal(data.historyProcessLabels.cells, 'ЯЧЕЙКИ')
assert.ok(data.formatHistoryTime(events[0].occurredAt).includes('29.10.2026'))
for (const bad of [{}, { events: null }, { events: [{ ...events[0], occurredAt: 'oops' }] }, { events: [{ ...events[0], process: 'fake' }] }, { events: [events[0], events[0]] }, { events: [{ ...events[0], references: [null] }] }]) assert.throws(() => data.parseHomeHistory(bad))

function mount() {
  let state = { status: 'loading', events: [] }, effect, cleanup, writes = 0
  const requests = []
  const load = loader({ AbortController, react: {
    useState: () => [state, value => { state = value; writes++ }],
    useEffect: callback => { if (!effect && !cleanup) effect = callback },
  }, fetch: (url, options) => new Promise((resolve, reject) => requests.push({ url, options, resolve, reject })) })
  const hook = load(resolve(root, 'useHomeHistory.ts')).useHomeHistory
  return { render: () => hook(), requests, writes: () => writes,
    effects: () => { if (effect) { cleanup = effect(); effect = null } }, unmount: () => cleanup?.(),
    respond: (body, ok = true) => requests[0].resolve({ ok, json: async () => body }),
  }
}
const settle = () => new Promise(setImmediate)
const mounted = mount()
assert.equal(mounted.render().status, 'loading'); mounted.effects()
assert.equal(mounted.requests[0].url, 'http://test/api/home/history?limit=20')
assert.equal(mounted.requests[0].options.cache, 'no-store')
mounted.respond({ events }); await settle()
assert.equal(mounted.render().status, 'ready'); mounted.render(); mounted.effects()
assert.equal(mounted.requests.length, 1, 'Rerender / period changes must not reload history')
for (const scenario of ['empty', 'http', 'offline', 'malformed', 'unmount']) {
  const fixture = mount(); fixture.render(); fixture.effects()
  if (scenario === 'unmount') { fixture.unmount(); fixture.respond({ events }) }
  else if (scenario === 'offline') fixture.requests[0].reject(new Error('offline'))
  else fixture.respond(scenario === 'empty' ? { events: [] } : scenario === 'malformed' ? {} : { events }, scenario !== 'http')
  await settle()
  const expected = scenario === 'empty' ? 'ready' : scenario === 'unmount' ? 'loading' : 'error'
  assert.equal(fixture.render().status, expected)
  assert.equal(fixture.render().events.length, 0)
  if (scenario === 'unmount') { assert.equal(fixture.writes(), 0); assert.equal(fixture.requests[0].options.signal.aborted, true) }
}
function renderWindow(history, order = 'desc') {
  const tree = loader({ react: { useState: initial => [initial === 'desc' ? order : initial, () => {}], useRef: current => ({ current }), useEffect: () => {} }, modules: {
    './useHomeStatisticsData': { useHomeStatisticsData: () => null }, './useHomeHistory': { useHomeHistory: () => history },
  } })(resolve(root, 'StatisticsDraft.tsx')).default()
  const nodes = []
  function walk(node) {
    if (Array.isArray(node)) return node.forEach(walk)
    if (node == null || typeof node !== 'object') return
    if (typeof node.type === 'function') return walk(node.type(node.props))
    nodes.push(node); walk(node.props.children)
  }
  walk(tree)
  const text = node => Array.isArray(node) ? node.map(text).join('') : node == null ? '' : typeof node === 'object' ? text(node.props.children) : String(node)
  return { nodes, text }
}
for (const [status, message] of [['loading', 'Загрузка действий…'], ['error', 'История временно недоступна'], ['ready', 'Действий пока нет']]) {
  const view = renderWindow({ status, events: [] })
  const body = view.nodes.find(n => n.type === 'tbody')
  assert.equal(view.text(body), message)
}
const view = renderWindow({ status: 'ready', events })
assert.equal(view.nodes.filter(n => n.type === 'time')[0].props.dateTime, events[2].occurredAt)
assert.equal(view.nodes.filter(n => n.props.className === 'statisticsDraft__process').map(view.text).slice(-3).join(), 'OZON,СБОРКА,ПРИЁМКА')
assert.ok(view.nodes.some(n => n.type === 'button' && n.props.title === 'Скопировать SKU REAL'))
const asc = renderWindow({ status: 'ready', events }, 'asc')
assert.equal(asc.nodes.filter(n => n.type === 'time')[0].props.dateTime, events[0].occurredAt)
console.log('Home history checks passed: DTO, full-date sorting, references, loading/empty/error states, independent loading and abort cleanup.')
