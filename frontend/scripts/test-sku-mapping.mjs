import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
const require = createRequire(import.meta.url)
const states = [], refs = []
let index = 0, refIndex = 0, initialized = false
const hooks = {
  useState(initial) { const i = index++; if (!(i in states)) states[i] = initial; return [states[i], v => { states[i] = v }] },
  useRef() { return refs[refIndex++] ??= { current: { showModal() {}, close() {} } } },
  useEffect(fn) { if (!initialized) { initialized = true; fn() } },
}
const context = { exports: {}, AbortController,
  require: id => id === '../../components/useModalFocusReturn' ? { __esModule: true, default() {} } : id === 'react' ? hooks : id.includes('config/api') ? { API_BASE_URL: '/api' } : require(id),
  fetch: async url => { assert.equal(url, '/api/assembly/mapping-products'); return { ok: true, json: async () => ({ products: [{ id: 42, sku: 'WAREHOUSE', name: 'Warehouse item' }] }) } },
}
const code = ts.transpileModule(readFileSync(new URL('../src/pages/AssemblyPage/SkuMappingDialog.tsx', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 } }).outputText
vm.runInNewContext(code, context)
let calls = [], saved = false
const props = { order: { number: 'ORDER', items: [{ sku: 'KNOWN', name: 'Known', productId: 1 }, { sku: 'EXT', ozonSku: '123', name: 'External item' }] }, problem: { creationContext: { affectedPositions: [{ sku: 'KNOWN' }, { sku: 'EXT' }] } }, blocked: false, onCancel() {}, onConfirm: async (sku, id) => { calls.push([sku, id]); return saved } }
const render = () => { index = 0; refIndex = 0; return context.exports.default(props) }
const nodes = tree => tree && typeof tree === 'object' ? [tree, ...[tree.props?.children].flat(Infinity).flatMap(nodes)] : []
const find = (tree, type, predicate = () => true) => nodes(tree).find(n => n.type === type && predicate(n.props))
let tree = render()
assert.equal(find(tree, 'button', p => p.type === 'submit').props.disabled, true)
await new Promise(resolve => setImmediate(resolve))
tree = render()
assert.equal(find(tree, 'select').props.value, 'EXT')
const productSelect = nodes(tree).filter(n => n.type === 'select')[1]
productSelect.props.onChange({ target: { value: '42' } }); tree = render()
assert.equal(find(tree, 'button', p => p.type === 'submit').props.disabled, true)
find(tree, 'input', p => p.type === 'checkbox').props.onChange({ target: { checked: true } }); tree = render()
assert.equal(find(tree, 'button', p => p.type === 'submit').props.disabled, false)
find(tree, 'form').props.onSubmit({ preventDefault() {} }); await new Promise(resolve => setImmediate(resolve)); tree = render()
assert.deepEqual(calls, [['EXT', 42]])
assert.ok(find(tree, 'p', p => p.role === 'alert'))
// Switching the position invalidates the operator's earlier conformity confirmation.
find(tree, 'select').props.onChange({ target: { value: 'KNOWN' } }); tree = render()
assert.equal(find(tree, 'input', p => p.type === 'checkbox').props.checked, false)
assert.equal(find(tree, 'button', p => p.type === 'submit').props.disabled, true)
props.blocked = true; tree = render()
assert.equal(find(tree, 'select').props.disabled, true)
console.log('Mapping dialog checks passed: affected selection, real catalog, conformity gate, failure feedback, confirmation reset and pending guard.')
