import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { webcrypto } from 'node:crypto'

const A = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', B = 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'
const compile = file => ts.transpileModule(fs.readFileSync(new URL('../src/pages/ReceivingPage/' + file, import.meta.url), 'utf8'), {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX },
}).outputText
const codes = Object.fromEntries(['receivingScanCommands.ts', 'ReceivingHistory.tsx', 'ProductCatalog.tsx', 'ReceivingQuantityDialog.tsx'].map(file => [file, compile(file)]))
const nodes = node => !node || typeof node !== 'object' ? [] : Array.isArray(node) ? node.flatMap(nodes) : [node, ...nodes(node.props?.children)]
const label = node => typeof node === 'string' ? node : Array.isArray(node) ? node.map(label).join('') : node?.props ? label(node.props.children) : ''
const settle = async () => { for (let i = 0; i < 6; i++) await new Promise(resolve => setImmediate(resolve)) }

function fixture() {
  let activeHooks
  const state = { deploymentId: A, readsUnbound: false }, data = new Map(), writes = []
  const storage = { get length() { return data.size }, key: i => [...data.keys()][i] ?? null, getItem: k => data.get(k) ?? null, setItem: (k, v) => data.set(k, v), removeItem: k => data.delete(k) }
  const product = () => ({ id: 1, categoryId: 1, name: state.deploymentId === A ? 'Product A' : 'Product B', sku: 'SAME', quantity: 0 })
  const request = async (url, options = {}) => {
    const reply = (status, body) => ({ ok: status < 400, status, json: async () => body, clone() { return this } })
    if (url.endsWith('/deployment')) return reply(200, { deploymentId: state.deploymentId })
    if (!options.method || options.method === 'GET') {
      const deploymentId = state.readsUnbound ? undefined : state.deploymentId
      if (url.includes('/product-catalog')) return reply(200, { deploymentId, categories: [{ id: 1, name: 'TEST rack' }], products: [product()] })
      return reply(200, { deploymentId, sessions: [{ id: 1, completedAt: '2026-11-01T00:00:00Z', productCount: 1, cellCount: 1, quantity: 1 }], nextCursor: null })
    }
    writes.push({ url, ...options })
    assert.equal(options.headers['X-Tremor-Deployment-Id'], state.deploymentId)
    return reply(url.includes('/product-catalog') ? 200 : 204, { id: 1 })
  }
  const react = {
    useState(initial) { const h = activeHooks, index = h.si++; if (!(index in h.states)) h.states[index] = typeof initial === 'function' ? initial() : initial; return [h.states[index], next => { h.states[index] = typeof next === 'function' ? next(h.states[index]) : next }] },
    useRef(initial) { const h = activeHooks; return h.refs[h.ri++] ??= { current: initial } },
    useEffect(effect, deps) { const h = activeHooks, index = h.ei++, old = h.deps[index]; if (!old || !deps || deps.some((v, i) => v !== old[i])) { h.deps[index] = deps; h.effects.push(() => { h.cleanups[index]?.(); h.cleanups[index] = effect() }) } },
  }
  const cache = {}
  function load(file) {
    if (cache[file]) return cache[file]
    const context = { exports: {}, Error, window: { localStorage: storage }, localStorage: storage, fetch: request, crypto: webcrypto, structuredClone, AbortController,
      require(name) {
        if (name === 'react') return react
        if (name === 'react/jsx-runtime') return { jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) }
        if (name === './receivingScanCommands') return load('receivingScanCommands.ts')
        if (name === './ReceivingQuantityDialog') return load('ReceivingQuantityDialog.tsx')
        if (name.endsWith('/config/api')) return { API_BASE_URL: '/api' }
        return { __esModule: true, default: () => () => {} }
      },
    }
    vm.runInNewContext(codes[file], context); return cache[file] = context.exports
  }
  function mount(component, props) {
    const h = { states: [], refs: [], deps: [], cleanups: [], effects: [], si: 0, ri: 0, ei: 0 }
    return () => { activeHooks = h; h.si = h.ri = h.ei = 0; const tree = component(props); for (const effect of h.effects.splice(0)) effect(); return tree }
  }
  return { state, data, writes, load, mount }
}
const button = (tree, text) => {
  const result = nodes(tree).find(n => n.type === 'button' && label(n).includes(text))
  assert.ok(result, 'button: ' + text); return result
}

// Actual history components: A and B intentionally use the same completed ID=1.
{
  const f = fixture(), outer = f.load('ReceivingHistory.tsx').default({ revision: 0, onDeleted() {}, readOnly: false })
  const list = nodes(outer).find(n => n.type?.name === 'HistoryList')
  const render = f.mount(list.type, list.props); render(); await settle()
  const row = nodes(render()).find(n => n.type?.name === 'HistoryRow')
  row.props.onDelete()
  const dialog = nodes(render()).find(n => n.type?.name === 'DeleteReceivingDialog')
  assert.equal(dialog.props.deploymentId, A)
  const renderDialog = f.mount(dialog.type, dialog.props)
  f.state.deploymentId = B
  await button(renderDialog(), 'Удалить из истории').props.onClick(); await settle()
  assert.equal(f.writes.length, 0); assert.equal(f.data.size, 0)
  assert.match(label(renderDialog()), /Операция не отправлена/)
  nodes(render()).find(n => n.props?.['aria-label'] === 'Обновить историю').props.onClick(); render(); await settle()
  assert.ok(!nodes(render()).some(n => n.type?.name === 'DeleteReceivingDialog'), 'reload closes A dialog')
  nodes(render()).find(n => n.type?.name === 'HistoryRow').props.onDelete()
  const nextDialog = nodes(render()).find(n => n.type?.name === 'DeleteReceivingDialog')
  assert.equal(nextDialog.props.deploymentId, B)
  await button(f.mount(nextDialog.type, nextDialog.props)(), 'Удалить из истории').props.onClick(); await settle()
  assert.equal(f.writes.length, 1)
}

// Actual catalog: pure catalog edits remain available without any active intake.
for (const operation of ['edit', 'create', 'archive']) {
  const f = fixture(), render = f.mount(f.load('ProductCatalog.tsx').default, { sessionId: null, sessionDeploymentId: null, disabled: true, readOnly: false, revision: 0, onReceivingChanged: async () => {} })
  render(); await settle(); let tree = render()
  if (operation === 'edit') nodes(tree).find(n => n.props?.title === 'Редактировать').props.onClick()
  if (operation === 'create') {
    nodes(tree).find(n => n.type === 'input' && n.props.placeholder === 'Например, Ручка TREMOR Classic').props.onChange({ target: { value: 'New A' } })
    nodes(tree).find(n => n.type === 'input' && n.props.placeholder === 'TREMOR-CLASSIC').props.onChange({ target: { value: 'NEW-A' } })
  }
  if (operation === 'archive') nodes(tree).find(n => n.props?.title === 'В архив').props.onClick()
  tree = render(); f.state.deploymentId = B
  if (operation === 'archive') button(tree, 'В архив').props.onClick()
  else await nodes(tree).find(n => n.type === 'form').props.onSubmit({ preventDefault() {} })
  await settle()
  assert.equal(f.writes.length, 0, operation + ': no mutation in B'); assert.equal(f.data.size, 0)
  assert.match(label(render()), /Операция не отправлена/)
  button(render(), 'Обновить каталог').props.onClick(); render(); await settle(); tree = render()
  assert.ok(!nodes(tree).some(n => n.type === 'dialog'), 'reload closes A dialogs')
  nodes(tree).find(n => n.props?.title === 'Редактировать').props.onClick(); tree = render()
  assert.equal(button(tree, 'Сохранить изменения').props.disabled, false, 'no session does not forbid catalog writes')
  await nodes(tree).find(n => n.type === 'form').props.onSubmit({ preventDefault() {} }); await settle()
  assert.equal(f.writes.length, 1); assert.equal(f.writes[0].headers['X-Tremor-Deployment-Id'], B)
}

// Actual quantity dialog with both a known product ID and a SKU lookup.
for (const id of [1, undefined]) {
  const f = fixture(), render = f.mount(f.load('ReceivingQuantityDialog.tsx').default, { deploymentId: A, sessionId: 1, product: { id, name: 'Product A', sku: 'SAME', quantity: 1 }, onClose() {}, onSaved: async () => {} })
  f.state.deploymentId = B
  await nodes(render()).find(n => n.type === 'form').props.onSubmit({ preventDefault() {} })
  await settle()
  assert.equal(f.writes.length, 0); assert.equal(f.data.size, 0)
  assert.match(label(render()), /[Оо]перация не отправлена/)
}

// Unbound reads remain visible and read-only, even with empty storage.
{
  const f = fixture(); f.state.readsUnbound = true
  const render = f.mount(f.load('ProductCatalog.tsx').default, { sessionId: null, sessionDeploymentId: null, disabled: true, readOnly: false, revision: 0, onReceivingChanged: async () => {} })
  render(); await settle()
  assert.equal(button(render(), '+ Добавить товар').props.disabled, true)
  assert.match(label(render()), /Разрешён только просмотр/)
  assert.equal(f.writes.length, 0)
}
console.log('Stale UI binding passed on actual TSX: history, catalog create/edit/archive, quantity with ID/lookup, safe B reload and catalog edits without active intake. No mutations or new journal entries on mismatch.')
