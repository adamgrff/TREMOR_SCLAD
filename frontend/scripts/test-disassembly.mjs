import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
import React from 'react'
import { disassemblyFixtures } from './fixtures/disassembly.mjs'
import { renderToStaticMarkup } from 'react-dom/server'
const require = createRequire(import.meta.url)
const context = { exports: {}, require: id => id === './useScanErrorFeedback' ? { __esModule: true, default: () => ({ signalError() {}, overlay: null }) } : id.endsWith('.css') ? {} : require(id) }
vm.runInNewContext(ts.transpileModule(readFileSync(new URL('../src/pages/AssemblyPage/Disassembly.tsx', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText, context)
const fixtures = disassemblyFixtures()
const render = (props) => renderToStaticMarkup(React.createElement(context.exports.default, { ...props, onAction: async () => true }))
const button = (html, label) => {
  const match = [...html.matchAll(/<button([^>]*)>([^<]*)<\/button>/g)].find(m => m[2] === label)
  assert.ok(match, `Missing button: ${label}`)
  return { disabled: /\bdisabled=""/.test(match[1]) }
}
const queue = html => html.match(/class="disassembly__queue"[\s\S]*?<\/table>/)[0]
const productTable = html => html.match(/class="disassembly__items"[\s\S]*?<\/table>/)[0]
const before = JSON.stringify(fixtures)
for (const [name, props] of Object.entries(fixtures)) {
  const html = render(props)
  for (const heading of ['Было собрано', 'Требуется вернуть', 'Уже возвращено']) assert.ok(html.includes(heading), `${name}: ${heading}`)
  const finished = name === 'complete'
  assert.equal(button(html, 'Завершить разбор').disabled, props.blocked || !finished, `${name}: finish gate`)
  assert.equal(button(html, 'Отложить').disabled, props.blocked || ['empty', 'waiting', 'paused'].includes(name), `${name}: pause gate`)
}
const waiting = render(fixtures.waiting)
assert.match(queue(waiting), /Ещё не начат/)
assert.equal(button(waiting, 'Начать разбор').disabled, false)
const paused = render(fixtures.paused)
assert.match(queue(paused), /Отложен/)
assert.equal(button(paused, 'Продолжить').disabled, false)
const active = render(fixtures.product)
assert.match(queue(active), /Активен/)
assert.equal(button(active, 'Активен').disabled, true)
assert.match(active, /СКАНИРУЙТЕ ТОВАР/)
assert.match(active, /aria-label="Код возвращаемого товара"/)
assert.match(active, /Ожидаемая исходная ячейка<\/small><span>—<\/span>/)
assert.match(active, /Возвращено <strong>1 \/ 2/)
const cell = render(fixtures.cell)
assert.match(cell, /СКАНИРУЙТЕ ЯЧЕЙКУ/)
assert.match(cell, /aria-label="Код исходной ячейки"/)
assert.match(cell, /Ожидаемая исходная ячейка<\/small><span>A1 · CELL:2/)
const table = productTable(cell)
assert.equal((table.match(/<strong>Тестовый товар<\/strong>/g) ?? []).length, 1, 'Aggregate one SKU across source cells')
assert.match(table, /disassembly__productCount">1 \/ 2/)
assert.match(table, /A1 · CELL:1/)
assert.match(table, /A1 · CELL:2/)
assert.match(table, /A · Ozon: OZON/)
const complete = render(fixtures.complete)
assert.match(complete, /ВСЕ ЕДИНИЦЫ ВОЗВРАЩЕНЫ/)
assert.equal(button(complete, 'Завершить разбор').disabled, false)
assert.doesNotMatch(complete, /aria-label="Код возвращаемого товара"/)
assert.match(productTable(complete), /Возврат завершён/)
const terminal = structuredClone(fixtures.complete.sessions[0]); terminal.status = 'completed'; terminal.number = 'CLOSED'
const mixed = render({ sessions: [
  { ...fixtures.waiting.sessions[0], id: 'new', number: 'NEW' },
  { ...fixtures.paused.sessions[0], id: 'paused', number: 'PAUSED' },
  { ...fixtures.product.sessions[0], number: 'ACTIVE' }, terminal,
], blocked: false })
for (const number of ['NEW', 'PAUSED', 'ACTIVE']) assert.ok(queue(mixed).includes(number))
assert.doesNotMatch(queue(mixed), /CLOSED/)
assert.equal(button(mixed, 'Начать разбор').disabled, true, 'Cannot start a second active disassembly')
assert.equal(button(mixed, 'Продолжить').disabled, true)
const closed = render({ sessions: [terminal], blocked: false })
assert.match(closed, /Заказов на разбор пока нет/)
assert.doesNotMatch(queue(closed), /CLOSED/)
assert.match(render(fixtures.error), /ui-v1-feedback--error[^>]*>Неверная ячейка/)
assert.match(render(fixtures.blocked), /aria-label="Код исходной ячейки"[^>]*disabled=""/)
assert.equal(JSON.stringify(fixtures), before, 'Rendering must not mutate fixtures/source picks')
console.log('Disassembly UI checks passed: eight states, mixed queue, start/resume, active exclusivity, both scanner steps, exact finish/pause gates, SKU aggregation, unique source cells, errors and immutable inputs.')
