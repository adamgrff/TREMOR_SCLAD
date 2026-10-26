import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import * as jsxRuntime from 'react/jsx-runtime'

// Exercise the real hook with a deterministic clock/WebAudio observer. The
// browser fixture separately verifies the actual portal, CSS and real DOM.
const code = ts.transpileModule(readFileSync(new URL('../src/pages/AssemblyPage/useScanErrorFeedback.tsx', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText
function mount(unsupported = false) {
  let state = 0, refIndex = 0, cleanup, nextTimer = 0
  const refs = [], timers = new Map(), calls = { resume: 0, start: 0, stop: 0, close: 0, portals: 0, delays: [] }
  class Audio {
    state = 'running'; currentTime = 10; destination = {}
    constructor() { if (unsupported) throw new Error('Audio unavailable') }
    resume() { calls.resume++; return Promise.resolve() }
    close() { calls.close++; return Promise.resolve() }
    createOscillator() { return { frequency: { setValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect() {}, disconnect() {}, start() { calls.start++ }, stop() { calls.stop++ } } }
    createGain() { return { gain: { setValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect() {}, disconnect() {} } }
  }
  const body = {}
  const context = {
    exports: {}, document: { body }, AudioContext: Audio,
    setTimeout(fn, delay) { calls.delays.push(delay); const id = ++nextTimer; timers.set(id, fn); return id },
    clearTimeout(id) { timers.delete(id) },
    require: id => id === 'react' ? {
      useState: () => [state, value => { state = typeof value === 'function' ? value(state) : value }],
      useRef: value => refs[refIndex++] ??= { current: value },
      useEffect: effect => { if (!cleanup) cleanup = effect() },
    } : id === 'react-dom' ? { createPortal(element, target) { assert.equal(target, body); calls.portals++; return element } }
      : id === 'react/jsx-runtime' ? jsxRuntime : undefined,
  }
  vm.runInNewContext(code, context)
  return { calls, timers, render() { refIndex = 0; return context.exports.default() }, cleanup: () => cleanup() }
}
const settle = () => new Promise(resolve => setImmediate(resolve))
const hook = mount()
assert.equal(hook.render().overlay, null)
hook.render().signalError()
await settle()
assert.equal(hook.calls.resume, 1)
assert.equal(hook.calls.start, 1)
assert.equal(hook.calls.stop, 1)
assert.equal(hook.calls.delays[0], 650)
assert.equal(hook.render().overlay.props.className, 'assemblyScanErrorFlash')
hook.render().signalError()
await settle()
assert.equal(hook.timers.size, 1, 'Repeated errors replace the expiry timer')
for (const expire of hook.timers.values()) expire()
assert.equal(hook.render().overlay, null, 'Overlay disappears at expiry')
hook.cleanup()
assert.equal(hook.calls.close, 1, 'Unmount closes the existing audio context')
const fallback = mount(true)
fallback.render().signalError()
assert.equal(fallback.render().overlay.props.className, 'assemblyScanErrorFlash', 'Audio failure must not suppress visual feedback')
fallback.cleanup()
assert.equal(fallback.timers.size, 0, 'Unmount cancels outstanding expiry')
console.log('Scan feedback checks passed: real hook, body portal, 650ms expiry, repeated-error timer, audio resume/start/stop, cleanup and audio-unavailable fallback.')
