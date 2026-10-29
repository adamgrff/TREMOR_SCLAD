import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import ts from 'typescript'
const require = createRequire(import.meta.url)
const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const compile = path => ts.transpileModule(read(path), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText
const session = { exports: {} }
vm.runInNewContext(compile('../src/components/PageIntro/welcomeIntroSession.ts'), session)
const decide = session.exports.shouldShowWelcomeIntro
const storage = () => { const values = new Map(); return { getItem: key => values.get(key) ?? null, setItem: (key, value) => values.set(key, value) } }
const tab = storage()
assert.equal(decide('/', () => tab), true)
for (const path of ['/', '/receiving', '/cells', '/assembly', '/', '/']) assert.equal(decide(path, () => tab), false)
for (const initial of ['/receiving', '/cells', '/assembly', '/unknown']) {
  const tab = storage()
  assert.equal(decide(initial, () => tab), false)
  assert.equal(decide('/', () => tab), false)
}
assert.equal(decide('/', storage), true, 'Independent new tab may show the welcome')
for (const get of [() => { throw Error('SecurityError') }, () => ({ getItem() { throw Error('denied') } }), () => ({ getItem: () => null, setItem() { throw Error('quota') } })]) assert.equal(decide('/', get), false)
const main = read('../src/main.tsx')
assert.ok(main.indexOf('const showWelcomeIntro = shouldShowWelcomeIntro') < main.indexOf('createRoot(document'))
assert.match(main, /<App showWelcomeIntro=\{showWelcomeIntro\}/)

// Render the real App with route and hook doubles. A previous history entry
// may return the exact original location object; it still must not replay.
function appHarness(showWelcomeIntro, pathname = '/') {
  const initial = { key: 'initial', pathname, search: '', hash: '' }
  let location = initial, enabled, ref
  const effects = [], scrolls = []
  const Intro = () => null
  const history = { scrollRestoration: 'auto' }
  const cleanups = []
  let effectIndex = 0
  const context = { exports: {}, window: { history, scrollTo: options => scrolls.push(options) }, require: id => {
    if (id === 'react') return { useRef: value => ref ??= { current: value }, useState: value => [enabled ??= value, next => { enabled = next }], useLayoutEffect: (effect, deps) => { const index = effectIndex++; if (deps.length || !cleanups[index]) effects.push(() => { cleanups[index] = effect() }) } }
    if (id === 'react-router') return { useLocation: () => location, Routes: 'Routes', Route: 'Route', Navigate: 'Navigate' }
    if (id.includes('routeTransition')) return { navigationId: location => JSON.stringify([location.key, location.pathname, location.search, location.hash]) }
    if (id.includes('useTheme')) return { useTheme: () => ({ theme: 'dark', toggleTheme() {} }) }
    if (id.endsWith('.css')) return {}
    if (id.includes('/Header/')) return { __esModule: true, default: () => null }
    if (id.includes('PageIntro')) return { __esModule: true, default: Intro }
    if (id.startsWith('./pages/')) return { __esModule: true, default: () => null }
    return require(id)
  } }
  vm.runInNewContext(compile('../src/App.tsx'), context)
  const render = () => { effectIndex = 0; const tree = context.exports.default({ showWelcomeIntro }); const children = tree.props.children; effects.splice(0).forEach(effect => effect()); return children }
  return { render, navigate: next => { location = next }, initial, scrolls, Intro, history, cleanup: () => cleanups.forEach(cleanup => cleanup?.()) }
}
const app = appHarness(true)
assert.equal(app.render()[0].type, app.Intro)
assert.doesNotMatch(read('../src/App.tsx'), /<Routes location=/, 'Routes must use the current router location')
for (const next of [{ key: 'receiving', pathname: '/receiving' }, { key: 'cells', pathname: '/cells' }, app.initial, { key: 'receiving', pathname: '/receiving' }]) {
  app.navigate(next)
  assert.equal(app.render()[0], false)
}
assert.ok(app.scrolls.every(s => s.top === 0 && s.left === 0 && s.behavior === 'instant'))
assert.equal(app.history.scrollRestoration, 'manual')
app.cleanup()
assert.equal(app.history.scrollRestoration, 'auto')
assert.equal(appHarness(false).render()[0], false)
assert.equal(appHarness(true, '/cells').render()[0], false)

function introHarness(reduced, previousInert) {
  const refs = [], effects = [], timers = new Map(), frames = new Map(), copied = {}, calls = []
  let nextId = 0, finished = false, resolveAnimation, rejectAnimation, animationCancelled = false
  const root = { inert: previousInert }
  const attributes = {}
  const overlay = { style: {}, querySelector: selector => ({ style: { setProperty: (key, value) => { copied[`${selector}:${key}`] = value } } }),
    getBoundingClientRect: () => ({ width: parseFloat(overlay.style.width), height: parseFloat(overlay.style.height) }),
    animate: (keyframes, options) => { calls.push({ keyframes, options }); return { finished: new Promise((resolve, reject) => { resolveAnimation = resolve; rejectAnimation = reject }), cancel() { animationCancelled = true; rejectAnimation() } } } }
  const target = { offsetWidth: 200, getBoundingClientRect: () => ({ left: 600, top: 40, width: 140, height: 53 }), querySelector: selector => ({ top: selector === '[data-brand-top]' }) }
  const context = { exports: {}, document: { body: {}, getElementById: () => root, querySelector: selector => { assert.equal(selector, '[data-page-intro-logo-target]'); return target } },
    window: { innerWidth: 1440, innerHeight: 900, matchMedia: () => ({ matches: reduced }), setTimeout: (callback, delay) => { const id = ++nextId; timers.set(id, { callback, delay }); return id } },
    getComputedStyle: source => ({ getPropertyValue: property => property === 'font-size' ? source.top ? '40px' : '24px' : '0px' }),
    performance: { now: () => 0 }, requestAnimationFrame: callback => { const id = ++nextId; frames.set(id, callback); return id },
    cancelAnimationFrame: id => frames.delete(id), clearTimeout: id => timers.delete(id),
    require: id => id === 'react' ? { useRef: value => { const ref = { current: value }; refs.push(ref); return ref }, useState: () => [false, value => { finished = value }], useId: () => 'test', useEffect: effect => effects.push(effect) } : id === 'react-dom' ? { createPortal: node => node } : id.endsWith('.css') ? {} : require(id) }
  vm.runInNewContext(compile('../src/components/PageIntro/PageIntro.tsx'), context)
  context.exports.default()
  refs[0].current = overlay
  refs[1].current = { setAttribute() {} }
  refs[2].current = { setAttribute: (key, value) => { attributes[key] = value } }
  let cleanup = effects[0]()
  assert.equal(root.inert, true)
  assert.equal(overlay.style.width, '140px')
  assert.equal(copied['[data-intro-top]:font-size'], '28px')
  assert.equal(copied['[data-intro-bottom]:font-size'], '16.799999999999997px')
  return { root, timers, frames, calls, attributes, get finished() { return finished }, get animationCancelled() { return animationCancelled }, cleanup: () => cleanup(), replay() { cleanup(); cleanup = effects[0]() }, runTimer() { const [id, timer] = [...timers][0]; timers.delete(id); assert.equal(timer.delay, reduced ? 300 : 500); return timer.callback() }, resolve() { resolveAnimation() } }
}
for (const reduced of [false, true]) for (const wasInert of [false, true]) {
  const intro = introHarness(reduced, wasInert)
  intro.replay() // StrictMode effect cleanup/setup must keep the root locked.
  const pending = intro.runTimer()
  if (!reduced) {
    assert.equal(intro.calls[0].options.duration, 500)
    assert.equal(intro.calls[0].options.easing, 'cubic-bezier(.22, 1, .36, 1)')
    assert.equal(intro.calls[0].keyframes[1].left, '600px')
    intro.resolve()
  }
  await pending
  if (!reduced) { const [id, callback] = [...intro.frames][0]; intro.frames.delete(id); callback(500) }
  assert.equal(intro.finished, true)
  assert.equal(intro.root.inert, wasInert)
  intro.root.inert = !wasInert
  intro.cleanup()
  assert.equal(intro.root.inert, !wasInert, 'Cleanup must not restore twice after normal completion')
}
for (const stage of ['delay', 'move', 'reveal']) {
  const intro = introHarness(false, false)
  let pending
  if (stage !== 'delay') pending = intro.runTimer()
  if (stage === 'reveal') { intro.resolve(); await pending }
  intro.cleanup()
  if (pending) await pending
  assert.equal(intro.timers.size, 0)
  assert.equal(intro.frames.size, 0)
  assert.equal(intro.root.inert, false)
  assert.equal(intro.finished, false)
  if (stage !== 'delay') assert.equal(intro.animationCancelled, true)
}
const appSource = read('../src/App.tsx'), introSource = read('../src/components/PageIntro/PageIntro.tsx')
assert.doesNotMatch(appSource + introSource, /displayedLocation|switchPage|\bclosing\b|onClosed|initialTransition|TRANSITION_DELAY_MS|CLOSE_MS|closePage/)
console.log('Welcome intro checks passed: first/root/internal opening, reload/history, denied storage, bootstrap outside StrictMode, immediate routes, scroll reset, visual timing, reduced motion, effect replay, cancellation at all stages and exact inert restoration.')
