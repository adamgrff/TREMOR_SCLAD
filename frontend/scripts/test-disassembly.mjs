import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
const require = createRequire(import.meta.url)
const context = { exports: {}, require: id => id === './useScanErrorFeedback' ? { __esModule: true, default: () => ({ signalError() {}, overlay: null }) } : id.endsWith('.css') ? {} : require(id) }
vm.runInNewContext(ts.transpileModule(readFileSync(new URL('../src/pages/AssemblyPage/Disassembly.tsx', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText, context)
const session = { id: 'session', number: 'REAL', reason: 'cancelled_order', status: 'active', pendingItemId: 'item2', items: [1, 2].map(n => ({ id: `item${n}`, sku: 'OZON', warehouseSku: 'A', name: 'Alpha', cell: 'A1', cellId: n, warehouse: 'Warehouse', operationId: `operation${n}`, returned: n === 1, returnable: true })), lastScan: 'A', log: 'Ожидается ячейка', error: false }
const render = () => renderToStaticMarkup(React.createElement(context.exports.default, { sessions: [session], blocked: false, onAction() {} }))
assert.match(render(), /СКАНИРУЙТЕ ЯЧЕЙКУ/)
assert.match(render(), /A1 · CELL:2/)
assert.match(render(), /aria-label="Код исходной ячейки"/)
assert.match(render(), /disabled="">Завершить разбор/)
assert.match(render(), /A · Ozon: OZON/)
const originalItems = JSON.stringify(session.items)
const productTable = render().match(/class="disassembly__items"[\s\S]*?<\/table>/)[0]
assert.equal((productTable.match(/<strong>Alpha<\/strong>/g) ?? []).length, 1, 'One SKU must have one summary row across multiple source cells')
assert.match(productTable, /disassembly__productCount">1 \/ 2/)
assert.match(productTable, /A1 · CELL:1/)
assert.match(productTable, /A1 · CELL:2/)
assert.match(render(), /Было собрано/)
assert.match(render(), /Требуется вернуть/)
assert.match(render(), /Уже возвращено/)
assert.equal(JSON.stringify(session.items), originalItems, 'Rendering summaries must not mutate source picks')
session.pendingItemId = null
assert.match(render(), /СКАНИРУЙТЕ ТОВАР/)
session.items[1].returned = true
assert.match(render(), /ВСЕ ЕДИНИЦЫ ВОЗВРАЩЕНЫ/)
assert.doesNotMatch(render(), /disabled="">Завершить разбор/)
session.status = 'completed'
assert.doesNotMatch(render(), />REAL</)
assert.match(render(), /Заказов на разбор пока нет/)
assert.doesNotMatch(render(), /disassembly__completed/)
assert.doesNotMatch(render(), /0 заказов/)
session.status = 'waiting'
assert.match(render(), /Начать разбор/)
assert.match(render(), /Ещё не начат/)
session.startedAt = '2026-10-14T10:00:00Z'
assert.match(render(), /Отложен/)
assert.match(render(), /Продолжить/)
const empty = renderToStaticMarkup(React.createElement(context.exports.default, { sessions: [], blocked: false, onAction() {} }))
assert.match(empty, /Товары и исходные ячейки появятся/)
assert.match(empty, /disabled=""[^>]*\/?>/)
console.log('Disassembly UI checks passed: SKU totals, source cell details, explicit quantities, pause status, pending item, finish gate, immutable inputs and terminal queue filtering.')
