import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { webcrypto } from 'node:crypto'

const code = ts.transpileModule(fs.readFileSync(new URL('../src/pages/ReceivingPage/receivingScanCommands.ts', import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText
const A = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', B = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
const token = '11111111-1111-4111-8111-111111111111'
function fixture(data = new Map()) {
  const state = { server: A, api: '/first-api', unavailable: false, drop: false, switchAfterHandshake: false }
  const effects = new Map([[A, 0], [B, 0]]), receipts = new Map(), posts = []
  const storage = { get length() { return data.size }, key: i => [...data.keys()][i] ?? null,
    getItem: k => data.get(k) ?? null, setItem: (k, v) => data.set(k, v), removeItem: k => data.delete(k) }
  const reply = (status, value) => ({ ok: status < 400, status, json: async () => value, clone() { return this } })
  const request = async (url, options) => {
    if (url.endsWith('/deployment')) {
      if (state.unavailable) return reply(503, {})
      const id = state.server
      if (state.switchAfterHandshake) { state.switchAfterHandshake = false; state.server = B }
      return reply(200, { deploymentId: id })
    }
    posts.push({ url, ...options })
    const body = JSON.parse(options.body)
    const saved = JSON.parse(data.get('tremor.receiving.scan.' + body.requestId))
    assert.equal(saved.deploymentId, options.headers['X-Tremor-Deployment-Id'], 'identity durably stored before send')
    if (options.headers['X-Tremor-Deployment-Id'] !== state.server) return reply(409, { code: 'deployment_identity_mismatch' })
    const key = state.server + body.requestId
    if (!receipts.has(key)) { receipts.set(key, body); effects.set(state.server, effects.get(state.server) + 1) }
    if (state.drop) { state.drop = false; throw new Error('response lost after commit') }
    if (url.includes('/quantities/') || (options.method === 'DELETE' && !url.includes('/items/'))) return reply(204, null)
    return reply(200, url.includes('/product-catalog') ? { id: 1 } : { status: 'saved' })
  }
  const config = { get API_BASE_URL() { return state.api } }
  const context = { exports: {}, require: () => config, crypto: webcrypto, structuredClone, window: { localStorage: storage }, fetch: request }
  vm.runInNewContext(code, context)
  return { ...context.exports, state, data, effects, receipts, posts, storage, queue: () => new context.exports.ReceivingScanCommands(() => storage, request) }
}
const noop = async () => {}
for (const changedURL of [false, true]) {
  const f = fixture(), q = f.queue()
  await q.recover(noop); f.state.drop = true
  await assert.rejects(q.scan(1, 'SAME', noop))
  const original = [...f.data.entries()]
  assert.equal(f.effects.get(A), 1)
  f.state.server = B
  if (changedURL) f.state.api = '/replacement-api'
  await assert.rejects(f.queue().recover(noop), /Идентичность/)
  await assert.rejects(q.scan(1, 'SAME', noop))
  assert.deepEqual([...f.data.entries()], original)
  assert.equal(f.posts.length, 1); assert.equal(f.effects.get(B), 0)
  f.state.server = A
  await f.queue().recover(noop)
  assert.equal(f.posts.at(-1).headers['X-Tremor-Deployment-Id'], A)
  assert.equal(f.posts[0].body, f.posts.at(-1).body)
  assert.equal(f.effects.get(A), 1); assert.equal(f.data.size, 0)
}
{
  const f = fixture(); f.state.switchAfterHandshake = true
  const q = f.queue(); await q.recover(noop)
  await assert.rejects(q.scan(1, 'SAME', noop), /Идентичность/)
  assert.equal(f.effects.get(A), 0); assert.equal(f.effects.get(B), 0)
  const original = [...f.data.entries()]
  await assert.rejects(f.queue().recover(noop), /Идентичность/)
  assert.deepEqual([...f.data.entries()], original)
}
{
  const raw = JSON.stringify({ requestId: token, sessionId: 1, sku: 'SAME', quantity: 1 })
  const f = fixture(new Map([['tremor.receiving.scan.' + token, raw]]))
  await assert.rejects(f.queue().recover(noop), /Идентичность/)
  assert.equal(f.posts.length, 0); assert.equal(f.data.values().next().value, raw)
}
{
  const data = new Map()
  for (const id of [token, '22222222-2222-4222-8222-222222222222']) data.set('tremor.receiving.scan.' + id, JSON.stringify({ deploymentId: A, requestId: id, sessionId: 1, sku: 'SAME', quantity: 1 }))
  const f = fixture(data), before = [...data.entries()]
  await assert.rejects(f.queue().recover(noop), /несколько/)
  assert.equal(f.posts.length, 0); assert.deepEqual([...data.entries()], before)
}
{
  const f = fixture(), q = f.queue(); await q.recover(noop)
  f.rememberActiveReceivingSession(1)
  assert.equal(f.readActiveReceivingSession(), '1')
  const before = [...f.data.entries()]
  f.state.server = B
  await assert.rejects(f.queue().recover(noop), /Идентичность/)
  assert.equal(f.readActiveReceivingSession(), null)
  f.rememberActiveReceivingSession(null)
  assert.deepEqual([...f.data.entries()], before)
}
{
  const f = fixture(); f.state.unavailable = true
  const q = f.queue(); await assert.rejects(q.recover(noop), /Идентичность/)
  await assert.rejects(q.scan(1, 'SAME', noop)); assert.equal(f.posts.length, 0)
}
for (const action of [
  { kind: 'quantity', sessionId: 1, productId: 1, method: 'PUT', quantity: 2, expectedQuantity: 1 },
  { kind: 'delete-history', sessionId: 1 },
  { kind: 'catalog-save', method: 'POST', data: { name: 'Synthetic TEST', sku: 'SAME', categoryId: 1, archived: false, receivingQuantity: 1, sessionId: 1 } },
]) {
  const f = fixture(); f.state.drop = true
  await assert.rejects(f.sendReceivingAction(action, A))
  const body = f.posts[0].body
  await f.queue().recover(noop)
  assert.equal(f.posts.at(-1).body, body); assert.equal(f.effects.get(A), 1)
}
{
  const f = fixture(); f.state.drop = true
  await assert.rejects(f.sendReceivingAction({ kind: 'catalog-save', method: 'POST', data: { name: 'Synthetic TEST', sku: 'SAME', categoryId: 1, archived: false, receivingQuantity: 0, sessionId: 0 } }, A))
  const before = [...f.data.entries()]
  await assert.rejects(f.queue().recover(noop), /ручной проверки/)
  assert.deepEqual([...f.data.entries()], before); assert.equal(f.posts.length, 1)
}
console.log('Deployment journal checks passed: URL/DB switches, handshake race, legacy preservation, multiple commands, bound session, unavailable identity, COMMIT replay and secondary receiving actions.')

// Stale UI context must be checked before a UUID, journal entry or mutation.
for (const action of [
  { kind: 'delete-history', sessionId: 1 },
  { kind: 'catalog-save', productId: 1, method: 'PUT', data: { name: 'From A', sku: 'SAME', categoryId: 1, archived: false, receivingQuantity: 0, sessionId: 0 } },
  { kind: 'quantity', sessionId: 1, productId: 1, method: 'PUT', quantity: 2, expectedQuantity: 1 },
]) {
  const f = fixture(), originalAction = JSON.stringify(action)
  f.state.server = B // Empty journal and no activeSession.v2.
  await assert.rejects(f.sendReceivingAction(action, A), /Операция не отправлена/)
  assert.equal(f.posts.length, 0); assert.equal(f.data.size, 0)
  assert.equal(f.effects.get(B), 0); assert.equal(JSON.stringify(action), originalAction)
  await assert.rejects(f.sendReceivingAction(action, undefined), /не подтверждён/)
  await f.sendReceivingAction(action, B) // Explicit reload of B's context.
  assert.equal(f.posts.length, 1)
  assert.equal(f.posts[0].headers['X-Tremor-Deployment-Id'], B)
}
{
  const f = fixture(), oldPage = f.queue()
  await oldPage.recover(noop)
  f.state.server = B
  await f.sendReceivingAction({ kind: 'delete-history', sessionId: 1 }, B)
  assert.equal(oldPage.acceptsSnapshot(B), false, 'global confirmation cannot rebind the old page instance')
  await assert.rejects(oldPage.scan(1, 'SAME', noop), /Идентичность/)
  assert.equal(f.posts.length, 1)
}

{
  const f = fixture(), q = f.queue()
  await q.recover(noop)
  assert.equal(f.confirmReceivingSnapshot(B), false)
  f.rememberActiveReceivingSession(1)
  assert.equal(f.data.size, 0)
  await assert.rejects(q.scan(1, 'SAME', noop), /Идентичность/)
  assert.equal(f.posts.length, 0)
}
