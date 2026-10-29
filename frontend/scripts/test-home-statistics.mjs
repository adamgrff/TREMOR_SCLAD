import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/pages/HomePage/useHomeStatistics.ts', import.meta.url), 'utf8')
const compiled = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS } }).outputText

function fixture(reduced = false) {
  const frames = new Map(), listeners = new Map(), refs = [], effects = []
  let id = 0, now = 0
  const window = {
    scrollY: 0,
    scrollTo({ top }) { this.scrollY = top; listeners.get('scroll')?.() },
    matchMedia: () => ({ matches: reduced }),
    addEventListener: (type, listener) => listeners.set(type, listener),
    removeEventListener: type => listeners.delete(type),
  }
  const context = {
    exports: {}, window, performance: { now: () => now },
    requestAnimationFrame: callback => { frames.set(++id, callback); return id },
    cancelAnimationFrame: key => frames.delete(key),
    ResizeObserver: class { observe() {} disconnect() {} },
    require: () => ({ useRef: current => { const ref = { current }; refs.push(ref); return ref }, useEffect: effect => effects.push(effect) }),
  }
  vm.runInNewContext(compiled, context)
  const hook = context.exports.useHomeStatistics()
  const properties = new Map(), summaryLight = new Map(), arrival = new Map(), departure = new Map()
  hook.screenRef.current = { getBoundingClientRect: () => ({ top: -window.scrollY }), style: { setProperty: (key, value) => summaryLight.set(key, value) } }
  hook.windowRef.current = { getBoundingClientRect: () => ({ top: 880 - window.scrollY }), querySelector: () => ({ setAttribute: (key, value) => arrival.set(key, value) }) }
  hook.bridgeRef.current = { offsetHeight: 180, style: { setProperty: (key, value) => properties.set(key, value) } }
  hook.surfaceRef.current = { querySelector: () => ({ setAttribute: (key, value) => departure.set(key, value) }) }
  const cleanup = effects[0]()
  const flush = time => {
    now = time
    const current = [...frames.values()]
    frames.clear()
    current.forEach(callback => callback(now))
  }
  return { hook, window, frames, listeners, properties, summaryLight, arrival, departure, flush, cleanup }
}

const manual = fixture()
assert.equal(manual.properties.get('--light-travel'), '0px')
assert.equal(manual.summaryLight.get('--summary-light'), '1')
assert.equal(manual.arrival.get('gradientTransform'), 'translate(500 -156) scale(320 80)')
manual.window.scrollTo({ top: 428 })
manual.flush(16)
assert.equal(manual.properties.get('--light-travel'), '78px', 'Manual halfway scroll puts light halfway along bridge')
assert.ok(Number(manual.summaryLight.get('--summary-light')) < .03, 'Summary hover light has left with the source')
assert.equal(manual.departure.get('gradientTransform'), 'translate(500 230) scale(340 80)')
assert.equal(manual.arrival.get('gradientTransform'), 'translate(500 -78) scale(320 80)')
manual.window.scrollTo({ top: 856 })
manual.flush(32)
assert.equal(manual.properties.get('--light-travel'), '156px')
assert.equal(manual.summaryLight.get('--summary-light'), '0', 'No light remains on summary at destination')
assert.equal(manual.arrival.get('gradientTransform'), 'translate(500 0) scale(320 80)', 'Light stops at the receiving tail tip')
assert.equal(manual.departure.get('gradientTransform'), 'translate(500 308) scale(340 80)')
manual.window.scrollTo({ top: 2000 })
manual.flush(48)
assert.equal(manual.properties.get('--light-travel'), '156px', 'Overscroll clamps to destination')
manual.window.scrollTo({ top: 0 })
manual.flush(64)
assert.equal(manual.properties.get('--light-travel'), '0px', 'Reverse scroll returns light to tail')
manual.cleanup()
assert.equal(manual.listeners.size, 0)
assert.equal(manual.frames.size, 0)

const click = fixture()
click.hook.scrollToStatistics()
click.flush(230)
assert.ok(click.window.scrollY > 0 && click.window.scrollY < 856)
click.flush(246)
assert.ok(parseFloat(click.properties.get('--light-travel')) > 0, 'Programmed scroll updates the same scroll listener')
click.flush(460)
click.flush(476)
assert.equal(click.window.scrollY, 856, 'Destination leaves 24px above window')
assert.equal(click.properties.get('--light-travel'), '156px')
click.cleanup()

for (const event of ['wheel', 'touchstart', 'pointerdown', 'keydown']) {
  const interrupted = fixture()
  interrupted.hook.scrollToStatistics()
  interrupted.listeners.get(event)(event === 'keydown' ? { key: 'PageDown' } : undefined)
  interrupted.flush(460)
  assert.equal(interrupted.window.scrollY, 0, `${event} cancels auto-scroll`)
  interrupted.cleanup()
}

const reduced = fixture(true)
reduced.hook.scrollToStatistics()
reduced.flush(16)
assert.equal(reduced.window.scrollY, 856)
assert.equal(reduced.properties.get('--light-travel'), '156px')
reduced.cleanup()

const hover = fixture()
const attributes = new Map(), glow = new Map()
const gradient = { setAttribute: (key, value) => attributes.set(key, value) }
hover.hook.surfaceRef.current = {
  getBoundingClientRect: () => ({ left: 0, top: 0, width: 1000, height: 152 }),
  querySelector: () => ({
    getTotalLength: () => 2304,
    getPointAtLength: length => {
      if (length <= 1000) return { x: length, y: 0 }
      if (length <= 1152) return { x: 1000, y: length - 1000 }
      if (length <= 2152) return { x: 2152 - length, y: 152 }
      return { x: 0, y: 2304 - length }
    },
  }),
  querySelectorAll: () => [gradient],
  style: { setProperty: (key, value) => glow.set(key, value) },
}
hover.hook.moveGlow({ clientX: 500, clientY: 76 })
assert.equal(Number(glow.get('--edge-glow')), 0, 'Center keeps only subtle local glow')
hover.hook.moveGlow({ clientX: 500, clientY: 20 })
const near = Number(glow.get('--edge-glow'))
hover.hook.moveGlow({ clientX: 500, clientY: 0 })
assert.ok(Number(glow.get('--edge-glow')) > near && Number(glow.get('--edge-glow')) <= .9)
assert.equal(attributes.get('cx'), '500')
assert.equal(attributes.get('cy'), '0')
hover.cleanup()
console.log('Home statistics checks passed: shared scroll progress, endpoints, reverse scroll, click destination, interruption, reduced motion, pointer proximity and cleanup.')
