import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const files = ['ReceivingPage/ReceivingHistory.tsx', 'ReceivingPage/ReceivingQuantityDialog.tsx', 'ReceivingPage/ProductCatalog.tsx', 'AssemblyPage/OrderProblemDialog.tsx', 'AssemblyPage/SkuMappingDialog.tsx']
const handlers = []
for (const file of files) {
  const source = ts.createSourceFile(file, readFileSync(new URL('../src/pages/' + file, import.meta.url), 'utf8'), ts.ScriptTarget.ES2022, true, ts.ScriptKind.TSX)
  function visit(node) {
    if (ts.isJsxOpeningElement(node) && node.tagName.getText(source) === 'dialog') {
      const attrs = node.attributes.properties
      const id = attrs.find(a => a.name?.getText(source) === 'aria-labelledby').initializer.text
      const handler = attrs.find(a => a.name?.getText(source) === 'onCancel').initializer.expression.getText(source)
      handlers.push({ id, handler })
    }
    ts.forEachChild(node, visit)
  }
  visit(source)
}
assert.equal(handlers.length, 6)
for (const { id, handler } of handlers) {
  for (const theme of ['dark', 'light']) for (const busy of [false, true]) {
    let prevented = 0, closed = 0
    const close = () => closed++
    const fn = vm.runInNewContext(ts.transpileModule('(' + handler + ')', { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText, {
      document: { documentElement: { dataset: { theme } } }, deleting: busy, saving: busy, blocked: busy,
      onCancel: close, onClose: close, setPendingArchive: close,
    })
    fn({ preventDefault() { prevented++ } })
    assert.equal(prevented, 1, id)
    const legacyBusyGuard = ['delete-receiving-title', 'quantity-dialog-title', 'sku-mapping-title'].includes(id)
    assert.equal(closed, theme === 'dark' || (busy && legacyBusyGuard) ? 0 : 1, `${id}/${theme}/${busy}`)
  }
}
console.log('Modal cancel checks passed: all six dark dialogs reject Escape; legacy light callbacks and busy guards remain unchanged.')

const focusCode = ts.transpileModule(readFileSync(new URL('../src/components/useModalFocusReturn.tsx', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText
for (const theme of ['dark', 'light']) for (const connected of [false, true]) {
  let cleanup, closed = 0, focused = 0
  class Element { isConnected = connected; focus(options) { assert.equal(options.preventScroll, true); focused++ } }
  const opener = new Element(), active = {}, doc = { documentElement: { dataset: { theme } }, activeElement: opener, body: {} }
  const dialog = { contains: node => node === active, close() { closed++ } }
  const context = { exports: {}, document: doc, HTMLElement: Element, require: () => ({ useRef() { return { current: false } }, useLayoutEffect(effect) { cleanup = effect() } }) }
  vm.runInNewContext(focusCode, context)
  context.exports.default({ current: dialog })
  doc.activeElement = active
  cleanup()
  assert.equal(closed, theme === 'dark' ? 1 : 0)
  assert.equal(focused, theme === 'dark' && connected ? 1 : 0)
}
console.log('Modal focus checks passed: dark close precedes DOM removal, connected opener restored, removed opener ignored, light lifecycle untouched.')
