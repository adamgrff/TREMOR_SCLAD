import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/pages/AssemblyPage/AssemblyPage.tsx', import.meta.url), 'utf8')
const ast = ts.createSourceFile('AssemblyPage.tsx', source, ts.ScriptTarget.ES2022, true, ts.ScriptKind.TSX)
const helper = ast.statements.find(node => ts.isFunctionDeclaration(node) && node.name?.text === 'revealQueueSearchRow')
assert(helper, 'Test the actual queue positioning implementation')
const code = ts.transpileModule(helper.getText(ast), { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText

function fixture({ offset = 77 + 21 * 88, height = 600, reduced = false, rowHeight = 88 } = {}) {
  const frames = new Map(), observers = [], listeners = new Map(), calls = []
  let nextFrame = 0, moving = true
  const container = {
    isConnected: true, clientTop: 0, clientHeight: height, offsetHeight: height, scrollTop: 0, scrollHeight: 77 + 22 * 88,
    getBoundingClientRect() { return { top: 200, height: this.clientHeight * .7 } },
    contains: target => target === row,
    scrollTo(options) { calls.push(options); this.scrollTop = options.top },
    addEventListener(type, listener) { listeners.set(type, listener) },
    removeEventListener(type, listener) { if (listeners.get(type) === listener) listeners.delete(type) },
  }
  const row = { isConnected: true, getBoundingClientRect: () => ({ top: 200 + (offset - container.scrollTop) * .7, bottom: 200 + (offset - container.scrollTop + rowHeight) * .7, height: rowHeight * .7 }) }
  const search = { getAnimations: () => moving ? [{ pending: false, playState: 'running' }] : [] }
  const context = {
    requestAnimationFrame(fn) { frames.set(++nextFrame, fn); return nextFrame }, cancelAnimationFrame: id => frames.delete(id),
    ResizeObserver: class { constructor(fn) { this.fn = fn; this.targets = []; observers.push(this) } observe(target) { this.targets.push(target) } disconnect() { this.targets = [] } },
    window: { matchMedia: () => ({ matches: reduced }) },
  }
  vm.runInNewContext(code, context)
  const start = () => context.revealQueueSearchRow(container, row, search)
  const tick = () => { const pending = [...frames.values()]; frames.clear(); pending.forEach(fn => fn()) }
  const drain = () => { for (let i = 0; frames.size && i < 60; i++) tick(); assert.equal(frames.size, 0, 'Positioning must terminate') }
  const disposed = () => { assert.equal(listeners.size, 0); assert(observers.every(o => o.targets.length === 0)); assert.equal(frames.size, 0) }
  const visible = () => { const r = row.getBoundingClientRect(), c = container.getBoundingClientRect(); assert(r.top >= c.top - 1 && r.bottom <= c.top + c.height + 1) }
  return { container, row, calls, frames, listeners, start, tick, drain, disposed, visible,
    settle() { moving = false },
    resize(height) { container.clientHeight = container.offsetHeight = height; observers.filter(o => o.targets.length).forEach(o => o.fn()) },
  }
}

for (const reduced of [false, true]) {
  const f = fixture({ reduced }); f.start()
  for (let i = 0; i < 8; i++) f.tick()
  assert.equal(f.calls.length, 0, 'No premature scroll during opening or reversed transition')
  f.resize(520); f.settle(); f.drain(); f.visible(); f.disposed()
  assert.equal(f.calls.length, 1)
  assert.equal(f.calls[0].behavior, reduced ? 'instant' : 'smooth')
}
{
  const f = fixture({ offset: 200 }); f.settle(); f.start(); f.drain(); f.visible(); f.disposed()
  assert.equal(f.calls.length, 0, 'Already visible rows must not move')
}
{
  const f = fixture({ offset: 77 }); f.container.scrollTop = 900; f.settle(); f.start(); f.drain(); f.visible(); f.disposed()
  assert(f.calls[0].top < 900, 'Search above viewport must scroll upward')
}
for (const event of ['wheel', 'touchstart', 'pointerdown', 'keydown']) {
  const f = fixture(); f.start(); f.tick(); f.listeners.get(event)(); f.settle(); f.drain(); f.disposed()
  assert.equal(f.calls.length, 0, `Manual ${event} must cancel pending correction`)
}
{
  const f = fixture(); f.settle(); const cancel = f.start()
  while (!f.calls.length) f.tick()
  cancel(); const manualTop = f.container.scrollTop = 123; f.drain(); f.disposed()
  assert.equal(f.container.scrollTop, manualTop, 'No correction after interruption of smooth scroll')
  assert.equal(f.calls.at(-1).behavior, 'instant')
}
{
  const f = fixture(); const cancel = f.start(); cancel(); f.settle(); f.container.scrollTop = 900; f.start(); f.drain(); f.visible(); f.disposed()
  assert.equal(f.calls.length, 1, 'A superseded request must not issue a stale scroll')
}
{
  const f = fixture(); f.start(); f.row.isConnected = false; f.tick(); f.disposed()
  assert.equal(f.calls.length, 0)
}
{
  const f = fixture({ rowHeight: 800 }); f.settle(); f.start(); f.drain(); f.disposed()
  assert(f.calls.every(call => call.top >= 0 && call.top <= f.container.scrollHeight - f.container.clientHeight))
}
console.log('Queue search scroll checks passed: layout/transition race, zoom, reduced motion, visible/upward rows, manual cancellation, superseded request, disconnect and bounded termination.')
