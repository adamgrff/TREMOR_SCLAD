import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/pages/AssemblyPage/checkProblemStock.ts', import.meta.url), 'utf8')
const context = { exports: {}, require: () => ({ API_BASE_URL: '/api' }), AbortSignal, fetch: null }
vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context)
const check = context.exports.default
const order = { number: 'test', items: [{ sku: 'WHITE', name: 'White', quantity: 4, cells: 'A1 ×2', cellStocks: { A1: 2 }, stock: 2 }] }
const initial = { type: 'insufficient_stock', shortages: [], createdAt: '2026-10-11T00:00:00Z', resolutionStatus: 'awaiting' }
const stock = (quantity) => { context.fetch = async () => ({ ok: true, json: async () => ({ sku: 'WHITE', cells: [{ cellCode: 'A1', quantity }] }) }) }
stock(2)
const shortage = await check(order, initial)
assert.equal(shortage.problem.resolutionStatus, 'awaiting')
assert.equal(shortage.problem.shortages[0].missing, 2)
assert.equal(shortage.problem.history[0].kind, 'check')
assert.equal(shortage.problem.history[1].outcome, 'failure')
stock(5)
const resolved = await check(order, shortage.problem)
assert.equal(resolved.problem.resolutionStatus, 'resolved')
assert.equal(resolved.problem.history.length, 5)
assert.equal(resolved.problem.history[1].outcome, 'failure')
assert.equal(resolved.problem.history[3].outcome, 'success')
assert.equal(resolved.problem.history[4].kind, 'resolution')
assert.equal(resolved.order.items[0].cells, 'A1 ×4')
context.fetch = async () => ({ ok: false, status: 404 })
const unknown = await check(order, resolved.problem)
assert.equal(unknown.problem.resolutionStatus, 'awaiting')
assert.equal(unknown.problem.history.length, 7)
assert.equal(unknown.problem.history[6].outcome, 'error')
context.fetch = async () => { throw new Error('offline') }
assert.equal((await check(order, initial)).problem.history[1].outcome, 'error')
stock(3)
const partial = await check(order, initial, { picks: [{ sku: 'WHITE', cell: 'B1' }] })
assert.equal(partial.problem.resolutionStatus, 'resolved')
assert.match(partial.order.items[0].cells, /B1 ×1/)
assert.match(partial.order.items[0].cells, /A1 ×3/)
context.fetch = async () => ({ ok: true, json: async () => ({ sku: 'WHITE', cells: [{ cellCode: 'A1', quantity: 5 }, { cellCode: 'A1', quantity: 5 }] }) })
assert.equal((await check(order, initial)).problem.resolutionStatus, 'awaiting')
console.log('Problem stock checks passed: shortage, resolution, append-only history, missing SKU, offline, partial picking, ambiguous cells.')

const historyContext = { exports: {}, require: () => ({ allProblemTypes: [['insufficient_stock', 'Недостаточно товара']] }) }
const historySource = readFileSync(new URL('../src/pages/AssemblyPage/createProblemHistory.ts', import.meta.url), 'utf8')
vm.runInNewContext(ts.transpileModule(historySource, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, historyContext)
const creation = historyContext.exports.default
const timestamp = '2026-10-11T10:00:00Z'
const queueHistory = creation(order, initial, timestamp)
assert.equal(queueHistory[0].title, 'Обнаружена до начала сборки')
assert.equal(queueHistory[1].title, 'Заказ передан в проблемные')
const activeHistory = creation(order, { ...initial, positionSku: 'WHITE' }, timestamp, { cell: 'B3', picks: [{ sku: 'WHITE', cell: 'B3' }] })
assert.match(activeHistory[0].message, /Ячейка: B3/)
assert.match(activeHistory[0].message, /Позиция: White/)
assert.match(activeHistory[0].message, /собрано: 1 \/ 4/)
stock(5)
const again = await check(order, resolved.problem)
assert.equal(again.problem.history.filter((event) => event.kind === 'resolution').length, 1)
console.log('Problem history checks passed: queue origin, assembly context, counters, and no duplicate resolution transition.')
