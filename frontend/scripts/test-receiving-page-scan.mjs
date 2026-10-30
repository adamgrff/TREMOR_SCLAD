import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { webcrypto } from 'node:crypto'
import vm from 'node:vm'
import ts from 'typescript'

const transpile = file => ts.transpileModule(readFileSync(new URL(file, import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX },
}).outputText
const pageCode = transpile('../src/pages/ReceivingPage/ReceivingPage.tsx')
const commandCode = transpile('../src/pages/ReceivingPage/receivingScanCommands.ts')
const stored = new Map([['tremor.receiving.activeSessionId', '1']]), receipts = new Map(), requests = []
let rejectFreshScan = false
let activeOverride = null, latestOverride = null, completedID = null, failAPI = false, missingID = null
let nextSession = 1, placementId = 0, placedQuantity = 0, stock = 0, revision = 0
let quantity = 0, loseResponse = false, denyStorage = false, denyWrites = false, sessionStatus = 'active', completeAfterPost = false
const storage = {
  get length() { if (denyStorage) throw new Error('SecurityError'); return stored.size },
  key: index => [...stored.keys()][index] ?? null,
  getItem: key => { if (denyStorage) throw new Error('SecurityError'); return stored.get(key) ?? null },
  setItem: (key, value) => { if (denyStorage || denyWrites) throw new Error('QuotaExceededError'); stored.set(key, value) },
  removeItem: key => { if (denyStorage) throw new Error('SecurityError'); stored.delete(key) },
}
async function fetchServer(url, options = {}) {
  requests.push({ url, ...options })
  if (failAPI) return { ok: false, status: 503 }
  if (url.endsWith('/sessions/active')) return { ok: true, json: async () => activeOverride ?? (sessionStatus === 'active' ? [{ id: nextSession, startedAt: '2026-10-30T00:00:00Z', pendingQuantity: quantity, placementCount: placedQuantity ? 1 : 0 }] : []) }
  if (url.endsWith('/sessions/latest')) return latestOverride ? { ok: true, json: async () => latestOverride } : { ok: false, status: 404 }
  if (url.endsWith('/sessions/' + missingID)) return { ok: false, status: 404 }
  if (url.includes('/products/')) return { ok: true, json: async () => ({ sku: 'TEST-SKU', name: 'Test' }) }
  if (options.method === 'POST' || options.method === 'DELETE') {
    const body = JSON.parse(options.body)
    assert.match(body.requestId, /^[0-9a-f-]{36}$/)
    let result = receipts.get(body.requestId)
    if (!result && rejectFreshScan && url.endsWith('/items')) {
      sessionStatus = 'completed'
      return { ok: false, status: 409, headers: { get: name => name === 'X-Receiving-Command-Rejected' ? 'true' : null }, json: async () => ({ error: 'receiving session is already completed' }) }
    }
    if (!result) {
      if (url === '/api/receiving/sessions') {
        nextSession++; sessionStatus = 'active'; quantity = 0; placedQuantity = 0; revision = 0
        result = { id: nextSession, status: 'active' }
      } else if (url.endsWith('/undo')) {
        quantity += placedQuantity; stock -= placedQuantity; placedQuantity = 0; revision++
        result = { id: placementId, status: 'undone' }
      } else if (url === '/api/receiving/placements') {
        placedQuantity = quantity; stock += quantity; quantity = 0; placementId++; revision++
        result = { id: placementId, cellCode: 'A1', createdAt: '2026-10-30T00:00:00Z' }
      } else if (url.endsWith('/finish')) {
        sessionStatus = 'completed'; result = { id: body.sessionId, placementCount: 1 }
      } else if (options.method === 'DELETE') {
        assert.equal(body.expectedRevision, String(revision)); quantity = 0; revision++; result = { status: 'deleted' }
      } else {
        quantity++; revision++; result = { status: 'saved' }
        if (completeAfterPost) sessionStatus = 'completed'
      }
      receipts.set(body.requestId, result)
    }
    if (loseResponse) { loseResponse = false; throw new Error('response lost after COMMIT') }
    return { ok: true, json: async () => result }
  }
  const id = Number(url.split('/').at(-1))
  return { ok: true, json: async () => ({ id, status: id === completedID ? 'completed' : sessionStatus, pendingRevision: String(revision),
    pendingItems: quantity ? [{ name: 'Test', sku: 'TEST-SKU', quantity }] : [],
    placedItems: placedQuantity ? [{ placementId, cellCode: 'A1', createdAt: '2026-10-30T00:00:00Z', name: 'Test', sku: 'TEST-SKU', quantity: placedQuantity }] : [] }) }
}

function mount() {
  const states = [], refs = [], effects = []
  let si = 0, ri = 0, mounted = false
  const react = {
    useState(initial) { const index = si++; if (!(index in states)) states[index] = typeof initial === 'function' ? initial() : initial; return [states[index], next => { states[index] = typeof next === 'function' ? next(states[index]) : next }] },
    useRef(initial) { return refs[ri++] ??= { current: initial } },
    useEffect(effect) { if (!mounted) effects.push(effect) },
  }
  const common = { Error, window: { localStorage: storage }, fetch: fetchServer, crypto: webcrypto, structuredClone, document: { getElementById: () => null } }
  const commands = { exports: {}, ...common, require: () => ({ API_BASE_URL: '/api' }) }
  vm.runInNewContext(commandCode, commands)
  const page = {
    exports: {}, ...common,
    require: name => {
      if (name === 'react') return react
      if (name === 'react-router') return { useLocation: () => ({ hash: '' }) }
      if (name === 'react/jsx-runtime') return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) }
      if (name === './receivingScanCommands') return commands.exports
      if (name.endsWith('/config/api')) return { API_BASE_URL: '/api' }
      return { default: () => null }
    },
  }
  vm.runInNewContext(pageCode, page)
  const render = () => { si = 0; ri = 0; const tree = page.exports.default({ theme: 'dark' }); if (!mounted) { mounted = true; for (const effect of effects) effect() } return tree }
  return render
}
function nodes(node) {
  if (!node || typeof node !== 'object') return []
  if (Array.isArray(node)) return node.flatMap(nodes)
  return [node, ...nodes(node.props?.children)]
}
const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(resolve => setImmediate(resolve)) }
const scanner = render => nodes(render()).find(node => node.type === 'form' && node.props.className === 'receivingPage__scanForm')
const submit = render => scanner(render).props.onSubmit({ preventDefault() {} })
const input = render => nodes(scanner(render)).find(node => node.type === 'input')
const button = render => nodes(scanner(render)).find(node => node.type === 'button')

const first = mount(); first(); await settle()
assert.equal(input(first).props.disabled, false)
input(first).props.onChange({ target: { value: 'TEST-SKU' } })
loseResponse = true
await submit(first)
assert.equal(quantity, 1)
assert.equal(input(first).props.disabled, true)
assert.equal(button(first).props.children, 'ПОВТОРИТЬ')
const original = requests.find(request => request.method === 'POST').body
// Refresh remounts the actual page, recovers the stored command and displays
// the server quantity instead of applying another optimistic increment.
const reload = mount(); reload(); await settle()
assert.equal(quantity, 1)
assert.equal(receipts.size, 1)
assert.equal(requests.filter(request => request.method === 'POST').at(-1).body, original)
assert.equal(input(reload).props.disabled, false)
const pending = nodes(reload()).find(node => node.type === 'td' && node.props.children === 'Test')
assert.ok(pending, 'Reload must show the server pending item')
input(reload).props.onChange({ target: { value: 'TEST-SKU' } }); await submit(reload)
assert.equal(quantity, 2)
assert.equal(receipts.size, 2)

denyStorage = true
const before = requests.length
let blocked = mount(); blocked(); await settle()
assert.equal(input(blocked).props.disabled, true)
assert.equal(button(blocked).props.disabled, true)
await submit(blocked)
assert.equal(requests.slice(before).some(request => request.method === 'POST'), false, 'Storage denial permits GET only')
assert.ok(nodes(blocked()).some(node => node.type === 'td' && node.props.children === 'Test'), 'Storage denial still shows server pending')
assert.ok(nodes(blocked()).some(node => node.props?.readOnly === true), 'History writes must also be disabled')
denyStorage = false
blocked = mount(); blocked(); await settle()
assert.equal(input(blocked).props.disabled, false)

// A committed command is still recognized if the session finishes before reload.
input(blocked).props.onChange({ target: { value: 'TEST-SKU' } }); loseResponse = true; await submit(blocked)
sessionStatus = 'completed'
const completed = mount(); completed(); await settle()
assert.equal(quantity, 3)
assert.equal(button(completed).props.children, 'ДОБАВИТЬ')
assert.equal(input(completed).props.disabled, true)
// Do not overwrite the authoritative completed state with a local 'scanned'
// status if another workstation places/completes between COMMIT and refresh.
sessionStatus = 'active'
stored.set('tremor.receiving.activeSessionId', '1')
const concurrent = mount(); concurrent(); await settle()
completeAfterPost = true
input(concurrent).props.onChange({ target: { value: 'TEST-SKU' } }); await submit(concurrent)
assert.equal(input(concurrent).props.disabled, true)
console.log('ReceivingPage integration passed: scanner POST contract, uncertain-state lock, actual page remount, same UUID recovery, independent scans, denied localStorage and completed-session replay.')

// All five page actions use the same durable queue. Losing the response and
// remounting must preserve the exact UUID/body and then restore server state.
completeAfterPost = false
let page = concurrent
const actionButton = (render, text) => nodes(render()).find(node => node.type === 'button' && node.props.children === text)
async function loseAndReload(action) {
  const before = receipts.size
  loseResponse = true
  await action()
  assert.equal(input(page).props.disabled, true)
  const sent = requests.filter(request => request.method === 'POST' || request.method === 'DELETE').at(-1)
  page = mount(); page(); await settle()
  const replay = requests.filter(request => request.method === 'POST' || request.method === 'DELETE').at(-1)
  assert.equal(replay.url, sent.url); assert.equal(replay.body, sent.body)
  assert.equal(receipts.size, before + 1)
}
await loseAndReload(() => actionButton(page, 'НОВАЯ ПРИЁМКА').props.onClick())
assert.equal(nextSession, 2)
assert.equal(input(page).props.disabled, false)
input(page).props.onChange({ target: { value: 'TEST-SKU' } }); await submit(page)
await loseAndReload(() => { input(page).props.onChange({ target: { value: 'A1' } }); return submit(page) })
assert.equal(stock, 1); assert.equal(quantity, 0)
await loseAndReload(() => actionButton(page, 'Отменить последнее действие').props.onClick())
assert.equal(stock, 0); assert.equal(quantity, 1)
await loseAndReload(() => nodes(page()).find(node => node.type === 'button' && node.props['aria-label'] === 'Удалить TEST-SKU из ожидающих').props.onClick())
assert.equal(quantity, 0)
input(page).props.onChange({ target: { value: 'TEST-SKU' } }); await submit(page)
input(page).props.onChange({ target: { value: 'A1' } }); await submit(page)
await loseAndReload(() => actionButton(page, 'ЗАВЕРШИТЬ ПРИЕМКУ').props.onClick())
assert.equal(sessionStatus, 'completed'); assert.equal(stock, 1)
console.log('ReceivingPage five-command recovery passed: create, place, undo, revision DELETE and finish, each with lost response, remount and exact UUID replay.')

// A new scan can lose the race with completion. A proven pre-COMMIT
// rejection refreshes the completed session and clears only that command.
sessionStatus = 'active'; stored.set('tremor.receiving.activeSessionId', String(nextSession))
let completionRace = mount(); completionRace(); await settle()
const beforeRejectedQuantity = quantity, beforeRejectedReceipts = receipts.size
rejectFreshScan = true
input(completionRace).props.onChange({ target: { value: 'TEST-SKU' } }); await submit(completionRace)
assert.equal(quantity, beforeRejectedQuantity); assert.equal(receipts.size, beforeRejectedReceipts)
assert.equal([...stored.keys()].filter(key => key.startsWith('tremor.receiving.scan.')).length, 0)
assert.equal(button(completionRace).props.children, 'ДОБАВИТЬ', 'Fresh rejection must not leave endless recovery')
assert.equal(actionButton(completionRace, 'НОВАЯ ПРИЁМКА').props.disabled, false)
const requestsBeforeReload = requests.filter(request => request.method === 'POST').length
rejectFreshScan = false
completionRace = mount(); completionRace(); await settle()
assert.equal(requests.filter(request => request.method === 'POST').length, requestsBeforeReload)
console.log('Fresh scan/completion rejection passed: authoritative refresh, resolved journal, no endless retry and no replay on reload.')

// Server discovery survives missing, invalid, stale and completed browser IDs.
const writes = () => requests.filter(request => ['POST', 'PUT', 'DELETE'].includes(request.method)).length
sessionStatus = 'active'; quantity = 2; placedQuantity = 3
for (const local of [null, 'broken', '999', '1.5']) {
  stored.clear(); if (local !== null) stored.set('tremor.receiving.activeSessionId', local)
  missingID = 999; activeOverride = [{ id: nextSession, startedAt: '2026-10-30T00:00:00Z', pendingQuantity: 2, placementCount: 1 }]
  const before = writes(), render = mount(); render(); await settle()
  assert.equal(input(render).props.disabled, false)
  assert.ok(nodes(render()).filter(node => node.type === 'td' && node.props.children === 'Test').length >= 2)
  assert.equal(writes(), before)
}
// A completed local session is stale when another active session exists.
stored.clear(); stored.set('tremor.receiving.activeSessionId', '900'); completedID = 900
let beforeDiscovery = writes(), fromCompleted = mount(); fromCompleted(); await settle()
assert.equal(input(fromCompleted).props.disabled, false)
assert.equal(stored.get('tremor.receiving.activeSessionId'), String(nextSession))
assert.equal(writes(), beforeDiscovery)
completedID = null
// With no active sessions, show the last completion without creating anything.
stored.clear(); activeOverride = []; latestOverride = { id: 800, items: [] }
const last = mount(); last(); await settle()
assert.ok(nodes(last()).some(node => node.type === 'p' && node.props.children === 'Загружена завершённая приёмка №800'))
assert.equal(writes(), beforeDiscovery)
latestOverride = null
stored.clear(); activeOverride = []
let render = mount(), beforeWrites = writes(); render(); await settle()
assert.equal(input(render).props.disabled, true)
assert.ok(actionButton(render, 'НОВАЯ ПРИЁМКА'))
assert.equal(writes(), beforeWrites, 'Empty discovery must never create a session')
activeOverride = [1, 2].map(id => ({ id, startedAt: '2026-10-30T00:00:00Z', pendingQuantity: id, placementCount: 0 }))
stored.clear(); render = mount(); render(); await settle()
assert.equal(input(render).props.disabled, true)
const choices = nodes(render()).filter(node => node.type === 'button' && Array.isArray(node.props.children) && node.props.children[0] === 'Приёмка №')
assert.equal(choices.length, 2)
assert.equal(writes(), beforeWrites)
await choices[1].props.onClick(); await settle()
assert.equal(input(render).props.disabled, false)
assert.equal(stored.get('tremor.receiving.activeSessionId'), '2')
// Selection races with completion elsewhere: rediscovery must replace the
// choice list and leave neither a stale lock nor a stale active session.
const sessionChoices = render => nodes(render()).filter(node => node.type === 'button' && Array.isArray(node.props.children) && node.props.children[0] === 'Приёмка №')
for (const readOnly of [false, true]) {
  for (const outcome of ['empty', 'latest', 'one', 'choose']) {
    stored.clear(); completedID = null; latestOverride = null; denyWrites = readOnly
    activeOverride = [1, 2].map(id => ({ id, startedAt: '2026-10-30T00:00:00Z', pendingQuantity: id, placementCount: 0 }))
    const race = mount(); race(); await settle()
    assert.equal(sessionChoices(race).length, 2)
    completedID = 1
    activeOverride = outcome === 'one' ? [activeOverride[1]] : outcome === 'choose' ? [2, 3].map(id => ({ id, startedAt: '2026-10-30T00:00:00Z', pendingQuantity: id, placementCount: 0 })) : []
    if (outcome === 'latest') latestOverride = { id: 1, items: [{ placementId: 5, cellCode: 'A1', createdAt: '2026-10-30T00:00:00Z', name: 'Completed item', sku: 'DONE', quantity: 4 }] }
    const before = writes()
    await sessionChoices(race)[0].props.onClick(); await settle()
    assert.equal(writes(), before, 'Rediscovery must not mutate server state')
    assert.equal(sessionChoices(race).length, outcome === 'choose' ? 2 : 0)
    if (outcome === 'empty' || outcome === 'latest') {
      assert.equal(actionButton(race, 'НОВАЯ ПРИЁМКА').props.disabled, readOnly, 'Old choices must not leave isBusy stuck')
      assert.equal(input(race).props.disabled, true)
      assert.equal(stored.has('tremor.receiving.activeSessionId'), false)
      assert.equal(nodes(race()).some(node => node.type === 'td' && node.props.children === 'Test'), false)
      assert.equal(nodes(race()).some(node => node.type === 'td' && node.props.children === 'Completed item'), outcome === 'latest')
    } else if (outcome === 'one') {
      assert.equal(input(race).props.disabled, readOnly)
      assert.equal(stored.get('tremor.receiving.activeSessionId'), readOnly ? undefined : '2')
      assert.ok(nodes(race()).some(node => node.type === 'td' && node.props.children === 'Test'))
    } else {
      assert.equal(input(race).props.disabled, true)
      assert.equal(stored.has('tremor.receiving.activeSessionId'), false)
      assert.equal(nodes(race()).some(node => node.type === 'td' && node.props.children === 'Test'), false)
    }
  }
}
denyWrites = false; completedID = null; latestOverride = null
console.log('Selection completion race passed: two active to empty/latest/one/choice, no stale lock or writes, including read-only storage mode.')
// API failure does not invent empty state or cause a write. Retry discovers.
stored.clear(); failAPI = true; render = mount(); render(); await settle()
assert.equal(button(render).props.children, 'ПОВТОРИТЬ')
assert.equal(input(render).props.disabled, true)
assert.equal(writes(), beforeWrites)
failAPI = false; activeOverride = []; await submit(render)
assert.equal(writes(), beforeWrites)
// Readable but unwritable storage permits viewing, never mutations.
stored.clear(); activeOverride = [{ id: nextSession, startedAt: '2026-10-30T00:00:00Z', pendingQuantity: 2, placementCount: 1 }]
denyWrites = true; render = mount(); render(); await settle()
assert.equal(input(render).props.disabled, true)
assert.ok(nodes(render()).some(node => node.type === 'td' && node.props.children === 'Test'))
assert.equal(writes(), beforeWrites)
denyWrites = false
// Unknown ordering requires manual resolution, with no replay button or effects.
stored.clear()
for (const id of ['11111111-1111-4111-8111-111111111111', '22222222-2222-4222-8222-222222222222']) {
  stored.set('tremor.receiving.scan.' + id, JSON.stringify({ requestId: id, sessionId: 1, sku: 'TEST-SKU', quantity: 1 }))
}
const journal = [...stored.entries()]
render = mount(); render(); await settle()
assert.equal(button(render).props.disabled, true)
assert.ok(nodes(render()).some(node => node.type === 'p' && typeof node.props.children === 'string' && node.props.children.includes('Требуется безопасное восстановление')))
assert.deepEqual([...stored.entries()], journal)
assert.equal(writes(), beforeWrites)
console.log('Session discovery passed: missing/invalid/stale ID, zero/one/multiple sessions, explicit selection, pending/placed reload, API/storage denial, no implicit creation and manual ambiguity lock.')
