import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const code = ts.transpileModule(readFileSync(new URL('../src/hooks/useTheme.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText
function mount(storage, initial = 'light') {
  let value
  const root = { dataset: {} }
  const hooks = {
    useState(init) { value ??= init(); return [value, next => { value = next(value) }] },
    useEffect(effect) { effect() },
  }
  const context = { exports: {}, localStorage: storage, document: { documentElement: root }, require: () => hooks }
  vm.runInNewContext(code, context)
  return { render: () => context.exports.useTheme(initial), root }
}
const denied = mount({ getItem() { throw new Error('SecurityError') }, setItem() { throw new Error('QuotaExceededError') } })
assert.equal(denied.render().theme, 'light')
assert.equal(denied.root.dataset.theme, 'light')
denied.render().toggleTheme()
assert.equal(denied.render().theme, 'dark')
assert.equal(denied.root.dataset.theme, 'dark')
let saved = 'dark'
const allowed = mount({ getItem: () => saved, setItem: (_, v) => { saved = v } })
assert.equal(allowed.render().theme, 'dark')
allowed.render().toggleTheme()
assert.equal(allowed.render().theme, 'light')
assert.equal(saved, 'light')
const quota = mount({ getItem: () => 'light', setItem() { throw new Error('QuotaExceededError') } })
quota.render().toggleTheme()
assert.equal(quota.render().theme, 'dark')
console.log('Theme checks passed: saved preference, denied storage, write quota and working toggle.')
