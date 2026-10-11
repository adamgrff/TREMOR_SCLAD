import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { webcrypto } from 'node:crypto'
import vm from 'node:vm'
import ts from 'typescript'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

const require = createRequire(import.meta.url)
const compile = (path) => ts.transpileModule(readFileSync(new URL(path, import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText
const states = [], refs = []
let stateIndex = 0, refIndex = 0, initialized = false
const reactHooks = {
  useState(initial) { const i = stateIndex++; if (!(i in states)) states[i] = initial; return [states[i], (next) => { states[i] = next }] },
  useRef(initial) { const i = refIndex++; return refs[i] ??= { current: initial } },
  useEffect(effect) { if (!initialized) { initialized = true; effect() } },
}
const calls = []
let version = 2, serverState = { marker: 'loaded' }, mode = 'offline'
const context = {
  exports: {}, AbortController, crypto: webcrypto,
  require: (id) => id === 'react' ? reactHooks : { API_BASE_URL: '/api' },
  fetch: async (url, options = {}) => {
    calls.push({ url, method: options.method ?? 'GET', body: options.body })
    if (!options.method) return { ok: true, json: async () => ({ version, state: serverState }) }
    if (mode === 'offline') throw new Error('offline')
    if (mode === 'conflict') return { ok: false, status: 409, json: async () => ({ error: 'Состояние изменилось' }) }
    if (mode === 'declined') return { ok: true, status: 200, json: async () => ({ version: ++version, state: serverState = { marker: 'return denied' }, applied: false, message: 'Возврат отклонён: товара недостаточно.' }) }
    return { ok: true, status: 200, json: async () => ({ version: ++version, state: serverState = { marker: 'problem saved' } }) }
  },
}
vm.runInNewContext(compile('../src/pages/AssemblyPage/useAssemblyDraft.ts'), context)
const render = () => { stateIndex = 0; refIndex = 0; return context.exports.default({ marker: 'initial' }) }
const settle = () => new Promise((resolve) => setImmediate(resolve))
render(); await settle()
let draft = render()
assert.equal(draft.ready, true)
assert.equal(await draft.createProblem({ source: 'assembly', orderNumber: 'ORDER', details: { type: 'damaged_product', positionSku: 'A', comment: '' } }), false)
draft = render()
assert.equal(draft.blocked, true)
assert.equal(await draft.createProblem({ source: 'assembly', orderNumber: 'OTHER', details: { type: 'other', comment: 'issue' } }), false)
mode = 'success'
assert.equal(await draft.retry(), true)
draft = render()
assert.equal(draft.state.marker, 'problem saved')
const posts = calls.filter((call) => call.method === 'POST')
assert.equal(posts.length, 2)
assert.equal(posts[0].url, '/api/assembly/problems')
assert.equal(posts[0].body, posts[1].body)
const intent = JSON.parse(posts[0].body)
assert.equal(intent.source, 'assembly')
assert.equal(intent.version, 2)
assert.equal(intent.orderNumber, 'ORDER')
assert.equal('picks' in intent, false)
assert.equal('activeState' in intent, false)
assert.equal('order' in intent, false)
mode = 'conflict'; version = 8; serverState = { marker: 'other tab' }
assert.equal(await draft.createProblem({ source: 'queue', orderNumber: 'QUEUE', order: { number: 'QUEUE', items: [] }, details: { type: 'unknown_sku', positionSku: 'A', comment: '' } }), false)
draft = render()
assert.equal(draft.state.marker, 'other tab')
const oldPostCount = calls.filter((call) => call.method === 'POST').length
assert.equal(await draft.retry(), true)
assert.equal(calls.filter((call) => call.method === 'POST').length, oldPostCount)

draft = render(); mode = 'declined'
assert.equal(await draft.problemAction('problem-id', 'return'), false)
draft = render()
assert.equal(draft.state.marker, 'return denied')
assert.equal(draft.blocked, false)
assert.match(draft.actionNotice, /Возврат отклонён/)
assert.equal(draft.getActionMessage(), draft.actionNotice)
mode = 'offline'
assert.equal(await draft.problemAction('problem-id', 'check-stock'), false)
const nativeRequest = calls.at(-1)
assert.equal(nativeRequest.url, '/api/assembly/problems/problem-id/check-stock')
assert.deepEqual(Object.keys(JSON.parse(nativeRequest.body)).sort(), ['requestId', 'version'])
mode = 'success'
draft = render()
assert.equal(await draft.retry(), true)
assert.equal(calls.at(-1).body, nativeRequest.body)
draft = render(); mode = 'offline'
assert.equal(await draft.assemblyAction('scan', { code: 'A' }), false)
const scanRequest = calls.at(-1)
assert.equal(scanRequest.url, '/api/assembly/scan')
assert.deepEqual(Object.keys(JSON.parse(scanRequest.body)).sort(), ['code', 'requestId', 'version'])
mode = 'success'; draft = render()
assert.equal(await draft.retry(), true)
assert.equal(calls.at(-1).body, scanRequest.body)
draft = render()
assert.equal(await draft.assemblyAction('start', { orderNumber: 'REAL' }), true)
assert.deepEqual(Object.keys(JSON.parse(calls.at(-1).body)).sort(), ['orderNumber', 'requestId', 'version'])

draft = render(); mode = 'offline'
assert.equal(await draft.mapProduct('mapping-problem', 'EXT-A', 42), false)
const mappingRequest = calls.at(-1)
assert.equal(mappingRequest.url, '/api/assembly/problems/mapping-problem/map-product')
const mappingBody = JSON.parse(mappingRequest.body)
assert.deepEqual(Object.keys(mappingBody).sort(), ['confirmed', 'positionSku', 'productId', 'requestId', 'version'])
assert.equal(mappingBody.confirmed, true)
assert.equal(mappingBody.productId, 42)
assert.equal(draft.state.marker, serverState.marker)
mode = 'success'; draft = render()
assert.equal(await draft.retry(), true)
assert.equal(calls.at(-1).body, mappingRequest.body)

draft = render(); mode = 'offline'
assert.equal(await draft.problemAction('original-operation-problem', 'retry-ozon'), false)
const ozonRetry = calls.at(-1)
assert.equal(ozonRetry.url, '/api/assembly/problems/original-operation-problem/retry-ozon')
assert.deepEqual(Object.keys(JSON.parse(ozonRetry.body)).sort(), ['requestId', 'version'])
mode = 'success'; draft = render(); assert.equal(await draft.retry(), true)
assert.equal(calls.at(-1).body, ozonRetry.body)
draft = render()
assert.equal(await draft.problemAction('cancelled-problem', 'close-cancelled'), true)
assert.equal(calls.at(-1).url, '/api/assembly/problems/cancelled-problem/close-cancelled')

const dialogContext = { exports: {}, require: (id) => id === './problemTypes' ? { problemTypes: [['insufficient_stock', 'Недостаточно товара'], ['other', 'Другая проблема']] } : require(id) }
vm.runInNewContext(compile('../src/pages/AssemblyPage/OrderProblemDialog.tsx'), dialogContext)
const props = { positions: [{ sku: 'A', name: 'Alpha' }], initialPositionSku: 'A', onCancel() {}, onConfirm() {} }
const queueDialog = renderToStaticMarkup(React.createElement(dialogContext.exports.default, { ...props, initialType: 'insufficient_stock' }))
assert.match(queueDialog, /Выбрать позицию/)
assert.match(queueDialog, /Alpha · A/)
const otherDialog = renderToStaticMarkup(React.createElement(dialogContext.exports.default, { ...props, initialType: 'other' }))
assert.match(otherDialog, /<textarea[^>]*required/)
assert.match(otherDialog, /type="submit"[^>]*disabled/)
const blockedDialog = renderToStaticMarkup(React.createElement(dialogContext.exports.default, { ...props, initialType: 'insufficient_stock', blocked: true }))
assert.match(blockedDialog, /type="submit"[^>]*disabled/)
console.log('Problem UI checks passed: queue position, required comment, server actions, persisted return rejection, pending guard, retry identity, conflict reload, blocked dialog.')
