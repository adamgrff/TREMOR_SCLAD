import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { webcrypto } from 'node:crypto'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/pages/ReceivingPage/receivingScanCommands.ts', import.meta.url), 'utf8')
const code = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText
const context = { exports: {}, require: () => ({ API_BASE_URL: '/api' }), crypto: webcrypto, structuredClone }
vm.runInNewContext(code, context)
const { ReceivingScanCommands } = context.exports

function fixture() {
  const data = new Map(), receipts = new Map(), requests = []
  let quantity = 0, drop = false, offline = false, failRead = false, failWrite = false, failRemove = false, gate
  const storage = {
    get length() { if (failRead) throw new Error('storage blocked'); return data.size },
    key(index) { return [...data.keys()][index] ?? null },
    getItem(key) { if (failRead) throw new Error('storage blocked'); return data.get(key) ?? null },
    setItem(key, value) { if (failWrite) throw new Error('quota'); data.set(key, value) },
    removeItem(key) { if (failRemove) throw new Error('storage blocked'); data.delete(key) },
  }
  async function request(url, options) {
    requests.push({ url, ...options })
    if (offline) throw new Error('network unavailable')
    if (gate) await gate
    const body = JSON.parse(options.body)
    if (!receipts.has(body.requestId)) { quantity += body.quantity; receipts.set(body.requestId, body) }
    if (drop) { drop = false; throw new Error('response lost after COMMIT') }
    return { ok: true, status: 200, json: async () => ({ status: 'saved' }) }
  }
  return {
    data, requests, receipts, storage, request,
    queue: () => new ReceivingScanCommands(() => storage, request),
    quantity: () => quantity,
    drop: () => { drop = true },
    offline: value => { offline = value }, readFailure: value => { failRead = value },
    writeFailure: value => { failWrite = value }, removeFailure: value => { failRemove = value },
    gate: value => { gate = value },
  }
}
const noop = async () => {}

// Two tabs can both pass their empty-journal check before either write is
// visible. Both commands must survive; enumeration in either order must never
// become an execution order, even after repeated reloads/recovery attempts.
for (const reverse of [false, true]) {
 for (const committed of [false, true]) {
  const f = fixture()
  const storage = { ...f.storage, get length() { return f.storage.length },
    key: index => [...f.data.keys()][reverse ? f.data.size - index - 1 : index] ?? null }
  const send = async (...args) => {
    const response = await f.request(...args)
    if (committed) throw new Error('response lost after COMMIT')
    return response
  }
  const tabA = new ReceivingScanCommands(() => storage, send)
  // Inject the competing tab between the first tab's check and durable write.
  const tabB = new ReceivingScanCommands(() => f.storage, send)
  await tabA.recover(noop); await tabB.recover(noop)
  f.offline(!committed)
  let competing
  storage.setItem = (key, value) => {
    storage.setItem = f.storage.setItem
    competing = assert.rejects(tabB.scan(7, 'SECOND', noop))
    f.storage.setItem(key, value)
  }
  await assert.rejects(tabA.scan(7, 'FIRST', noop))
  await competing
  assert.equal(f.data.size, 2)
  const original = [...f.data.entries()]
  const count = f.requests.length
  f.offline(false)
  for (const queue of [tabA, tabB, f.queue(), f.queue()]) {
    await assert.rejects(queue.recover(noop), /несколько незавершённых команд/)
    await assert.rejects(queue.scan(7, 'NEW', noop))
    await assert.rejects(queue.execute({ kind: 'create-session' }, noop))
    assert.deepEqual([...f.data.entries()], original)
    assert.equal(f.requests.length, count)
    assert.equal(f.receipts.size, committed ? 2 : 0)
    assert.equal(f.quantity(), committed ? 2 : 0)
  }
 }
}
console.log('Ambiguous recovery passed: two-tab race, both storage orders, offline/lost COMMIT responses, repeated reload, unchanged journals and no additional server effects.')

// An intentional new scan is independent. Uncertain delivery is the same intent,
// including after reload, completion and a failed state-refresh request.
{
  const f = fixture(), queue = f.queue()
  let displayed = 0, failRefresh = false
  const synchronize = async id => { assert.equal(id, 7); if (failRefresh) throw new Error('GET failed'); displayed = f.quantity() }
  await queue.recover(synchronize)
  await queue.scan(7, ' test-sku ', synchronize)
  await queue.scan(7, 'TEST-SKU', synchronize)
  assert.equal(displayed, 2)
  assert.notEqual(JSON.parse(f.requests[0].body).requestId, JSON.parse(f.requests[1].body).requestId)
  f.drop()
  await assert.rejects(queue.scan(7, 'TEST-SKU', synchronize))
  assert.equal(f.quantity(), 3)
  assert.equal(f.data.size, 1)
  const original = f.requests.at(-1).body
  await assert.rejects(queue.scan(7, 'TEST-SKU', synchronize))
  assert.equal(f.requests.at(-1).body, original)
  const reloaded = f.queue()
  failRefresh = true
  await assert.rejects(reloaded.recover(synchronize))
  assert.equal(f.quantity(), 3)
  assert.equal(f.requests.at(-1).body, original)
  assert.equal(f.data.size, 1)
  failRefresh = false
  await reloaded.recover(synchronize)
  assert.equal(f.requests.at(-1).body, original)
  assert.equal(displayed, 3)
  assert.equal(f.data.size, 0)
  await reloaded.scan(7, 'TEST-SKU', synchronize)
  assert.equal(displayed, 4)
}

// Storage access fails closed, without sending a command. Reload cannot infer
// that an inaccessible journal is empty. Recovering storage enables safe retry.
for (const mode of ['getter', 'read', 'write', 'remove']) {
  const f = fixture()
  const queue = mode === 'getter' ? new ReceivingScanCommands(() => { throw new Error('SecurityError') }) : f.queue()
  if (mode === 'getter') { await assert.rejects(queue.recover(noop)); await assert.rejects(queue.scan(1, 'SKU', noop)); assert.equal(f.requests.length, 0); continue }
  await queue.recover(noop)
  if (mode === 'read') f.readFailure(true)
  if (mode === 'write') f.writeFailure(true)
  if (mode === 'remove') f.removeFailure(true)
  await assert.rejects(queue.scan(1, 'SKU', noop))
  assert.equal(f.quantity(), mode === 'remove' ? 1 : 0)
  await assert.rejects(queue.scan(1, 'SKU', noop))
  f.readFailure(false); f.writeFailure(false); f.removeFailure(false)
  await queue.recover(noop)
  assert.equal(f.quantity(), mode === 'read' ? 0 : 1)
  assert.equal(f.data.size, 0)
}

// Offline delivery, concurrent submit, other tabs, and corrupted storage.
{
  const f = fixture(), queue = f.queue()
  await queue.recover(noop)
  f.offline(true)
  await assert.rejects(queue.scan(1, 'SKU', noop))
  const original = f.requests.at(-1).body
  f.offline(false)
  await queue.recover(noop)
  assert.equal(f.requests.at(-1).body, original)
  assert.equal(f.quantity(), 1)
  let release
  f.gate(new Promise(resolve => { release = resolve }))
  const first = queue.scan(1, 'SKU', noop)
  await assert.rejects(queue.scan(1, 'SKU', noop))
  f.gate(null); release(); await first
  assert.equal(f.quantity(), 2)
  const otherTab = f.queue(); await otherTab.recover(noop)
  f.drop(); await assert.rejects(queue.scan(1, 'SKU', noop))
  await assert.rejects(otherTab.scan(1, 'OTHER', noop))
  assert.equal(f.quantity(), 3)
  await otherTab.recover(noop)
  assert.equal(f.quantity(), 3)
  f.data.set('tremor.receiving.scan.broken', '{')
  const count = f.requests.length
  await assert.rejects(f.queue().recover(noop))
  assert.equal(f.requests.length, count)
}
console.log('Receiving scan recovery passed: independent UUIDs, lost COMMIT response, reload, state sync failure, offline, concurrent submit, storage denial/quota/removal, other tabs and corrupt journal.')

// The same storage/retry machinery covers every non-scan command, with no
// independent queues. Identity conflicts stay unresolved; a proven business
// rejection synchronizes state and permits a fresh deliberate action.
const actions = [
  { kind: 'create-session' },
  { kind: 'place', sessionId: 7, cellCode: 'A1', items: [{ sku: 'SKU', quantity: 1 }] },
  { kind: 'undo-placement', sessionId: 7, placementId: 9 },
  { kind: 'finish', sessionId: 7, placementIds: [9] },
  { kind: 'delete-item', sessionId: 7, sku: 'SKU', expectedRevision: '4' },
]
for (const action of actions) {
  for (const mode of ['lost-response', 'write-failure', 'read-failure', 'remove-failure', 'refresh-failure']) {
    const data = new Map(), received = new Map(), sent = []
    let failRead = false, failWrite = false, failRemove = false, drop = false, failRefresh = false, effects = 0, displayed = 0
    const storage = {
      get length() { if (failRead) throw new Error('blocked'); return data.size },
      key: index => [...data.keys()][index] ?? null,
      getItem: key => { if (failRead) throw new Error('blocked'); return data.get(key) ?? null },
      setItem: (key, value) => { if (failWrite) throw new Error('quota'); data.set(key, value) },
      removeItem: key => { if (failRemove) throw new Error('blocked'); data.delete(key) },
    }
    const send = async (url, options) => {
      sent.push({ url, ...options }); const body = JSON.parse(options.body)
      if (!received.has(body.requestId)) { received.set(body.requestId, body); effects++ }
      if (drop) { drop = false; throw new Error('lost after COMMIT') }
      const result = action.kind === 'create-session' ? { id: 7, status: 'active' }
        : action.kind === 'place' ? { id: 9 }
        : action.kind === 'undo-placement' ? { id: 9, status: 'undone' }
        : action.kind === 'finish' ? { id: 7, placementCount: 1 } : { status: 'deleted' }
      return { ok: true, json: async () => result }
    }
    const sync = async id => { assert.equal(id, 7); if (failRefresh) throw new Error('GET failed'); displayed = effects }
    const queue = new ReceivingScanCommands(() => storage, send)
    await queue.recover(sync)
    failRead = mode === 'read-failure'; failWrite = mode === 'write-failure'; failRemove = mode === 'remove-failure'
    drop = mode === 'lost-response'; failRefresh = mode === 'refresh-failure'
    await assert.rejects(queue.execute(action, sync))
    await assert.rejects(queue.execute(action, sync))
    assert.equal(effects, ['read-failure', 'write-failure'].includes(mode) ? 0 : 1)
    const original = sent.at(-1)?.body
    failRead = failWrite = failRemove = failRefresh = false
    // A refused write is retained in memory, but was never sent; only commands
    // durably stored before COMMIT need recovery across an actual remount.
    const recovering = mode === 'write-failure' ? queue : new ReceivingScanCommands(() => storage, send)
    await recovering.recover(sync)
    assert.equal(effects, mode === 'read-failure' ? 0 : 1)
    if (original) assert.equal(sent.at(-1).body, original)
    assert.equal(data.size, 0)
    assert.equal(displayed, effects)
    if (action.kind === 'delete-item' && sent.length) {
      assert.equal(sent.at(-1).method, 'DELETE'); assert.equal(JSON.parse(sent.at(-1).body).expectedRevision, '4')
    }
  }
}
{
  const data = new Map()
  const storage = { get length() { return data.size }, key: index => [...data.keys()][index] ?? null,
    getItem: key => data.get(key) ?? null, setItem: (key, value) => data.set(key, value), removeItem: key => data.delete(key) }
  let definitive = true, synchronized = 0
  const send = async () => ({ ok: false, status: 409, headers: { get: () => definitive ? 'true' : null }, json: async () => ({ error: 'pending group changed' }) })
  const queue = new ReceivingScanCommands(() => storage, send)
  await queue.recover(noop)
  await assert.rejects(queue.execute(actions[4], async () => { synchronized++ }), /pending group changed/)
  assert.equal(data.size, 0); assert.equal(synchronized, 1)
  definitive = false
  await assert.rejects(queue.execute(actions[4], noop))
  assert.equal(data.size, 1)
  await assert.rejects(queue.execute(actions[0], noop))
}
console.log('Receiving action queue matrix passed: five routes x reload/storage/state-sync failures, revision delivery, proven rejection and unresolved UUID conflict.')
