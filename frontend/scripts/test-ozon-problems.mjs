import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
const require = createRequire(import.meta.url)
const compile = path => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText
const context = { exports: {}, require: id => {
  if (id === 'react-router') return { Link: props => React.createElement('a', props, props.children) }
  if (id === './SkuMappingDialog') return { __esModule: true, default: () => null }
  if (id === './groupProblemHistory') return { __esModule: true, default: history => history.map((event, index) => ({ key: String(index), latest: event, previous: [] })) }
  if (id === './createProblemHistory') return { __esModule: true, default: () => [] }
  if (id === './problemTypes') return { allProblemTypes: [['ozon_error', 'Ошибка Ozon'], ['cancelled_order', 'Отменённый заказ'], ['insufficient_stock', 'Недостаточно товара']] }
  if (id.endsWith('.svg')) return '/test-icon.svg'
  if (id.endsWith('.css')) return {}
  return require(id)
} }
vm.runInNewContext(compile('../src/pages/AssemblyPage/ProblemOrders.tsx'), context)
const order = { source: 'ozon', number: 'REAL', items: [{ sku: 'A', name: 'Alpha', quantity: 1, stock: 1, cells: '', cellStocks: {} }] }
const entry = { problemId: 'problem-id', order, status: 'problem', problem: { type: 'ozon_error', createdAt: '2026-10-12T10:00:00Z', shortages: [], comment: '', resolutionStatus: 'awaiting', ozonOperationId: 'operation-id', ozonPurpose: 'confirm_shipment', ozonOutcome: 'unknown' } }
const render = orders => renderToStaticMarkup(React.createElement(context.exports.default, { orders, blocked: false, assemblyBusy: false, onReturn() {}, onStockCheck() {}, onOzonAction() {} }))
assert.match(render([entry]), /Повторить запрос Ozon/)
assert.match(render([entry]), /При неизвестном результате повторная отправка запрещена/)
entry.problem.resolutionStatus = 'resolved'; entry.problem.ozonOutcome = 'applied'
assert.match(render([entry]), /Повторить запрос Ozon/)
assert.doesNotMatch(render([entry]), />Вернуть в сборку</)
entry.problem.ozonPurpose = 'refresh_order'
assert.match(render([entry]), /type="button"[^>]*title="Продолжить сборку с сохранённым прогрессом"[^>]*>Вернуть в сборку</)
const shortage = { ...entry, problemId: 'shortage', problem: { ...entry.problem, type: 'insufficient_stock', resolutionStatus: 'awaiting' } }
assert.match(render([entry, shortage]), /type="button"[^>]*disabled=""[^>]*>Вернуть в сборку</)
entry.problem.closedOutcome = 'completed'
assert.doesNotMatch(render([entry]), />REAL</)
delete entry.problem.closedOutcome; entry.problem.type = 'cancelled_order'; entry.problem.resolutionStatus = 'awaiting'
assert.match(render([entry]), /Закрыть отменённый заказ/)
entry.assembly = { picks: [{ sku: 'A', cell: 'A1', cellId: 1 }] }; entry.problem.requiresDisassembly = true
assert.doesNotMatch(render([entry]), /Закрыть отменённый заказ/)
assert.match(render([entry]), /Передать в разбор/)
console.log('Ozon problem UI checks passed: original-operation retry, purpose-specific return gate, other blockers, terminal outcomes and picked cancellation.')
