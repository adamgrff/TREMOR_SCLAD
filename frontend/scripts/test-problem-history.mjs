import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const context = { exports: {}, require: () => ({ allProblemTypes: [['insufficient_stock', 'Недостаточно товара']] }) }
const source = readFileSync(new URL('../src/pages/AssemblyPage/createProblemHistory.ts', import.meta.url), 'utf8')
vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context)
const creation = context.exports.default
const order = { number: 'test', items: [{ sku: 'WHITE', name: 'White', quantity: 4, cells: 'A1 ×2', cellStocks: { A1: 2 }, stock: 2 }] }
const details = { type: 'insufficient_stock', positionSku: 'WHITE' }
const timestamp = '2026-10-11T10:00:00Z'
const queue = creation(order, details, timestamp)
assert.equal(queue[0].title, 'Обнаружена до начала сборки')
assert.equal(queue[1].title, 'Заказ передан в проблемные')
const active = creation(order, details, timestamp, { cell: 'B3', picks: [{ sku: 'WHITE', cell: 'B3' }] })
assert.match(active[0].message, /Ячейка: B3/)
assert.match(active[0].message, /Позиция: White/)
assert.match(active[0].message, /собрано: 1 \/ 4/)
console.log('Problem history fallback checks passed: queue origin, assembly context, and progress counters.')
