import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
const require = createRequire(import.meta.url)
const compile = name => ts.transpileModule(readFileSync(new URL(`../src/pages/AssemblyPage/${name}`, import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText
const grouping = { exports: {} }; vm.runInNewContext(compile('groupProblemHistory.ts'), grouping)
const group = grouping.exports.default
const events = Object.freeze([3, 1, 2].map(n => Object.freeze({ id: `event-${n}`, ordinal: n - 1, kind: 'check', createdAt: `2026-10-14T10:00:0${n}Z`, title: 'Проверка', message: 'Недостаточно: 2 шт.', outcome: 'failure' })))
const before = JSON.stringify(events), grouped = group(events)
assert.equal(grouped.length, 1); assert.equal(grouped[0].latest.id, 'event-3')
assert.equal(grouped[0].previous.map(e => e.id).join(','), 'event-1,event-2')
assert.equal(JSON.stringify(events), before)
const sameTextDifferentOperations = [1, 2].map(n => ({ ...events[0], kind: 'pick', operationId: `operation-${n}` }))
assert.equal(group(sameTextDifferentOperations).length, 2)
assert.equal(group([{ ...events[0], kind: 'check' }, { ...events[0], kind: 'mapping' }]).length, 2)
const history = { exports: {}, require: id => id === './groupProblemHistory' ? { __esModule: true, default: group } : require(id) }
vm.runInNewContext(compile('ProblemHistory.tsx'), history)
const full = renderToStaticMarkup(React.createElement(history.exports.default, { events: [...events].sort((a, b) => a.ordinal - b.ordinal), fullByDefault: true }))
assert.equal((full.match(/<li/g) ?? []).length, 3); assert.doesNotMatch(full, /problemOrders__repeatBadge/)
const collapsed = renderToStaticMarkup(React.createElement(history.exports.default, { events: [...events] }))
assert.match(collapsed, />\+2</); assert.match(collapsed, /10:00:01Z/); assert.match(collapsed, /10:00:02Z/)
assert.match(renderToStaticMarkup(React.createElement(history.exports.ProblemTime, { value: null })), /Время неизвестно/)

const states = [], effects = []
let stateIndex = 0, effectIndex = 0
const hooks = {
  useState(initial) { const i = stateIndex++; if (!(i in states)) states[i] = initial; return [states[i], next => { states[i] = typeof next === 'function' ? next(states[i]) : next }] },
  useEffect(fn, deps) { const i = effectIndex++; const old = effects[i]; if (old && deps.every((dep, n) => Object.is(dep, old.deps[n]))) return; old?.cleanup?.(); effects[i] = { deps, cleanup: fn() } },
}
const calls = [], order = { source: 'preview', number: 'OLD', items: [{ sku: 'A', name: 'Alpha', quantity: 2 }] }
const row = { problemId: 'problem', number: 'OLD', source: 'preview', type: 'insufficient_stock', outcome: 'returned', createdAt: '2026-10-14T10:00:00Z', closedAt: null, eventCount: 3, positions: [{ sku: 'A', name: 'Alpha' }] }
const detail = { entry: { problemId: 'problem', order, problem: { type: 'insufficient_stock', resolutionStatus: 'returned', comment: 'Later note', creationContext: { complete: false, source: 'assembly', positions: [] } }, assembly: { picks: [{ sku: 'A', cell: 'A1' }] } }, originalOrder: order, originalProblem: { type: 'insufficient_stock', comment: 'Original comment' }, historyRecords: [...events], relatedProblems: [], journals: {} }
let failDetail = false
const archive = { exports: {}, AbortController, URLSearchParams, require: id => {
  if (id === 'react') return hooks
  if (id.includes('config/api')) return { API_BASE_URL: '/api' }
  if (id === './ProblemHistory') return history.exports
  if (id === './problemTypes') return { allProblemTypes: [['insufficient_stock', 'Недостаточно товара']] }
  if (id.endsWith('.css')) return {}
  return require(id)
}, fetch: async (url, options) => {
  calls.push({ url, options }); assert.equal(options.method, undefined); assert.equal(options.body, undefined)
  if (url.endsWith('/history')) return { ok: !failDetail, status: failDetail ? 503 : 200, json: async () => detail }
  return { ok: true, json: async () => ({ problems: [row], total: 1, page: 1, pageCount: 1 }) }
} }
vm.runInNewContext(compile('ProblemArchive.tsx'), archive)
const props = { refreshToken: 1, onClose() {} }
const render = () => { stateIndex = 0; effectIndex = 0; return archive.exports.default(props) }
const settle = () => new Promise(resolve => setImmediate(resolve))
const nodes = tree => tree && typeof tree === 'object' ? [tree, ...[tree.props?.children].flat(Infinity).flatMap(nodes)] : []
const button = (tree, label) => nodes(tree).find(n => n.type === 'button' && n.props.children === label)
render(); await settle(); let tree = render()
assert.equal(calls.length, 1); assert.ok(calls[0].url.includes('/archive?')); assert.match(calls[0].url, /pageSize=5/)
button(tree, 'История').props.onClick(); render(); await settle(); tree = render()
assert.ok(calls.at(-1).url.endsWith('/problem/history'))
assert.ok(nodes(tree).some(n => n.type === archive.exports.ArchiveProblemDetail))
const html = renderToStaticMarkup(React.createElement(archive.exports.ArchiveProblemDetail, { data: detail, onSelect() {} }))
assert.match(html, /Original comment/); assert.doesNotMatch(html, /Later note/)
assert.match(html, /Время скана неизвестно/); assert.match(html, /Точное время старых сканов неизвестно/)
assert.doesNotMatch(html, /Начать сборку|Сопоставить товар|Обновить остатки|Отправить в проблемные/)
failDetail = true; props.refreshToken = 2; render(); await settle(); tree = render()
assert.ok(nodes(tree).some(n => n.type === 'p' && n.props.role === 'alert'))
assert.ok(button(tree, 'Повторить загрузку'))
const input = nodes(tree).find(n => n.type === 'input' && n.props.type === 'search')
input.props.onChange({ target: { value: '  Alpha  ' } }); render(); await settle(); tree = render()
assert.ok(calls.some(call => call.url.includes('q=Alpha')))
assert.ok(!nodes(tree).some(n => n.type === archive.exports.ArchiveProblemDetail))
console.log('Archive checks passed: separate read-only loading, immutable history grouping, full timeline, source snapshots, unknown scan times, retry and filters.')
