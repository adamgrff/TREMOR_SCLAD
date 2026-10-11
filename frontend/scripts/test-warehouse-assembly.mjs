import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
const require = createRequire(import.meta.url)
const code = ts.transpileModule(readFileSync(new URL('../src/pages/AssemblyPage/ActiveAssembly.tsx', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText
const context = { exports: {}, require: id => id === './useScanErrorFeedback' ? { __esModule: true, default: () => ({ signalError() {}, overlay: null }) } : id === './OrderProblemDialog' ? { __esModule: true, default: () => null } : require(id) }
vm.runInNewContext(code, context)
const plan = (cellId, required, picked) => ({ cellId, cell: 'A1', warehouse: 'Warehouse', scanCode: `CELL:${cellId}`, required, picked, available: required - picked })
const order = { source: 'ozon', number: 'REAL', suggestedCellId: 3, items: [{ sku: 'A', name: 'Alpha', quantity: 3, stock: 1, cells: '', cellStocks: {}, pickingPlan: [plan(1, 1, 1), plan(3, 2, 1)] }] }
const state = { picks: [{ sku: 'A', cell: 'A1', cellId: 1 }, { sku: 'A', cell: 'A1', cellId: 3 }], confirmedCell: 'A1', confirmedCellId: 3, shipmentConfirmed: false, lastScan: 'A', log: '', error: false }
const render = () => renderToStaticMarkup(React.createElement(context.exports.default, { order, state, blocked: false, onChange() {}, onComplete() {}, onProblem() {}, onServerAction() {} }))
assert.match(render(), /activeAssembly__cellCount">1\/2/)
assert.doesNotMatch(render(), /activeAssembly__cellCount">2\/2/)
state.confirmedCell = null; state.confirmedCellId = null
assert.match(render(), /A1 · CELL:3/)
state.picks.push({ sku: 'A', cell: 'A1', cellId: 3 }); order.items[0].pickingPlan[1].picked = 2
assert.match(render(), /activeAssembly__row--done/)
assert.match(render(), /СКАНИРУЙТЕ ОТПРАВЛЕНИЕ/)
const page = readFileSync(new URL('../src/pages/AssemblyPage/AssemblyPage.tsx', import.meta.url), 'utf8')
assert.doesNotMatch(page, /const previewOrders/)
assert.match(page, /\/assembly\/orders/)
assert.doesNotMatch(page, /draft\.save\(/)
console.log('Warehouse UI checks passed: cell ID separation, server suggestion, completed row and authoritative DB queue/actions.')
