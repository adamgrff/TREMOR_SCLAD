import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/pages/HomePage/StatisticsDraft.tsx', import.meta.url), 'utf8')
const ast = ts.createSourceFile('StatisticsDraft.tsx', source, ts.ScriptTarget.ES2022, true, ts.ScriptKind.TSX)
const component = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'CopyBadge')
const code = ts.transpileModule(component.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX } }).outputText

function fixture() {
  const slots = [], timers = new Map(), effects = [], writes = []
  let index = 0, timerId = 0
  const context = {
    exports: {},
    require: () => ({ jsx: (type, props) => ({ type, props }), jsxs: (type, props) => ({ type, props }) }),
    useState: initial => {
      const slot = index++
      if (!(slot in slots)) slots[slot] = initial
      return [slots[slot], value => { slots[slot] = value }]
    },
    useRef: initial => { const slot = index++; return slots[slot] ??= { current: initial } },
    useEffect: callback => { if (!effects.length) effects.push(callback()) },
    navigator: { clipboard: { writeText: async value => { writes.push(value) } } },
    setTimeout: callback => { timers.set(++timerId, callback); return timerId },
    clearTimeout: key => timers.delete(key),
  }
  vm.runInNewContext(code, context)
  const render = () => { index = 0; return context.exports.CopyBadge({ value: 'SKU TR-042' }) }
  return { context, render, timers, writes, effects }
}

const success = fixture()
assert.equal(success.render().props['data-copied'], false)
success.render().props.onClick()
assert.equal(success.render().props['data-copied'], false, 'Never show success before clipboard resolves')
await new Promise(setImmediate)
assert.equal(success.render().props['data-copied'], true)
assert.equal(success.writes[0], 'SKU TR-042')
assert.equal(success.render().props.children[1].props.children, 'SKU TR-042', 'Visible value is never replaced')
success.render().props.onClick()
await new Promise(setImmediate)
assert.equal(success.timers.size, 1, 'Repeated copy restarts the indicator timer')
success.timers.values().next().value()
assert.equal(success.render().props['data-copied'], false)
success.effects[0]()

const failure = fixture()
failure.context.navigator.clipboard.writeText = async () => { throw new Error('denied') }
failure.render().props.onClick()
await new Promise(setImmediate)
assert.equal(failure.render().props['data-copied'], false)
assert.match(failure.render().props.title, /Не удалось/)
failure.effects[0]()

const unmounted = fixture()
let finish
unmounted.context.navigator.clipboard.writeText = () => new Promise(resolve => { finish = resolve })
unmounted.render().props.onClick()
unmounted.effects[0]()
finish()
await new Promise(setImmediate)
assert.equal(unmounted.timers.size, 0, 'Pending clipboard result cannot schedule work after unmount')
console.log('Statistics copy checks passed: actual value, confirmed success, stable text, repeat click, reset, rejection and unmount cleanup.')
