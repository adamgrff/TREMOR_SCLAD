import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { createRequire } from 'node:module'
const require = createRequire(import.meta.url)
import ts from 'typescript'
const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const context = { exports: {} }
vm.runInNewContext(ts.transpileModule(read('../src/components/RouteTransition/routeTransition.ts'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText, context)
const { createRouteTransition, navigationId } = context.exports
assert.equal(navigationId({ key: 'a', pathname: '/', search: '', hash: '' }), navigationId({ key: 'a', pathname: '/', search: '', hash: '' }))
for (const property of ['key', 'pathname', 'search', 'hash']) {
  const location = { key: 'a', pathname: '/', search: '', hash: '' }
  assert.notEqual(navigationId(location), navigationId({ ...location, [property]: 'changed' }))
}
function harness({ reduced = false, welcome = false, supported = true, failure = false } = {}) {
  const properties = new Map(), attributes = new Set(), transitions = []
  let rect = { left: 0, top: -1800, width: 1425, height: 2700 }, commits = 0
  const root = { style: { setProperty: (key, value) => properties.set(key, value), removeProperty: key => properties.delete(key) }, setAttribute: key => attributes.add(key), removeAttribute: key => attributes.delete(key) }
  const doc = { documentElement: root, querySelector: selector => selector === '.pageIntro' ? welcome : { getBoundingClientRect: () => rect } }
  if (supported) doc.startViewTransition = callback => {
    if (failure) throw Error('unavailable')
    let finish
    const transition = { callback, ready: Promise.resolve(), finished: new Promise(resolve => { finish = resolve }), skipTransition() { this.skipped = true }, finish: () => finish(), skipped: false }
    transitions.push(transition)
    return transition
  }
  const controller = createRouteTransition(doc, { matchMedia: () => ({ matches: reduced }), scrollTo(options) { assert.equal(options.top, 0); assert.equal(options.behavior, 'instant'); rect = { ...rect, top: 0 } } })
  return { controller, transitions, properties, attributes, commit() { commits++; rect = { left: 0, top: -123, width: 1425, height: 1000 } }, get commits() { return commits } }
}
for (const options of [{ reduced: true }, { welcome: true }, { supported: false }, { failure: true }]) {
  const h = harness(options)
  h.controller.navigate(h.commit, true)
  assert.equal(h.commits, 1)
  assert.equal(h.attributes.size, 0)
  assert.equal(h.properties.size, 0)
}
const h = harness()
h.controller.navigate(h.commit, true)
assert.equal(h.properties.get('--route-old-y'), '-1800px', 'Outgoing viewport must preserve its real scroll offset')
assert.equal(h.commits, 0, 'Capture happens before replacement')
h.transitions[0].callback()
assert.equal(h.commits, 1, 'Do not wait for animation completion to mount/load the new page')
assert.equal(h.properties.get('--route-new-y'), '0px', 'Incoming capture is taken after synchronous route layout effects and scroll reset')
assert.equal(h.attributes.has('data-route-transition'), true)
h.transitions[0].finish()
await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
assert.equal(h.attributes.size, 0)
assert.equal(h.properties.size, 0)
const rapid = harness()
rapid.controller.navigate(rapid.commit, true)
rapid.controller.navigate(rapid.commit, true)
assert.equal(rapid.transitions[0].skipped, true)
rapid.transitions[0].callback()
assert.equal(rapid.commits, 0, 'Superseded callbacks must not revert the latest route')
rapid.transitions[0].finish()
await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
assert.equal(rapid.attributes.has('data-route-transition'), true, 'Old completion must not remove new transition styles')
rapid.transitions[1].callback()
assert.equal(rapid.commits, 1)
rapid.controller.dispose()
assert.equal(rapid.transitions[1].skipped, true)
assert.equal(rapid.attributes.size, 0)
rapid.transitions[1].finish()
const same = harness()
same.controller.navigate(same.commit, false)
assert.equal(same.commits, 1)
assert.equal(same.transitions.length, 0)
const css = read('../src/components/RouteTransition/RouteTransition.css')
assert.match(css, /animation-duration: 220ms/)
assert.match(css, /animation-timing-function: ease-out/)
assert.match(css, /scale\(\.985\)/)
assert.match(css, /@keyframes routeFadeOut \{ from \{ opacity: 1; \} to \{ opacity: 0; \} \}/)
assert.match(css, /::view-transition \{\s*pointer-events: none/)
assert.match(css, /scrollbar-gutter: stable/)
assert.match(css, /zoom: calc\(1 \/ var\(--site-scale\)\)/)
assert.match(css, /height: auto/)
assert.doesNotMatch(css, /height: var\(--route-(old|new)-height\)/)
assert.match(css, /prefers-reduced-motion: reduce/)
assert.doesNotMatch(read('../src/components/RouteTransition/routeTransition.ts'), /setTimeout|setInterval|\.inert|cloneNode/)
const router = read('../src/components/RouteTransition/NavigationRouter.tsx')
assert.match(router, /history.listen/)
assert.match(router, /flushSync\(\(\) => setState/)
assert.doesNotMatch(router, /displayedLocation|setTimeout/)
console.log('Route transitions passed: stable route identity, all history actions, capture before replacement, pre-capture scroll reset, immediate mount, rapid supersession, cleanup, unsupported API, failures, welcome exclusion, reduced motion and exact animation CSS.')

// Drive the actual history subscription for PUSH/POP/REPLACE, including the
// same-path update, and verify that all subscribed resources are released.
let listener, detached = false, disposed = false, state
const effects = [], requests = []
const history = { action: 'POP', location: { pathname: '/' }, listen(callback) { listener = callback; return () => { detached = true } } }
const routerContext = { exports: {}, require: id => {
  if (id === 'react') return { useRef: value => ({ current: value }), useState: value => [state ??= value, next => { state = next }], useLayoutEffect: effect => effects.push(effect) }
  if (id === 'react-dom') return { flushSync: callback => callback() }
  if (id === 'react-router') return { Router: 'Router', UNSAFE_createBrowserHistory: () => history }
  if (id === './routeTransition') return { createRouteTransition: () => ({ navigate(commit, animate) { requests.push(animate); commit() }, dispose() { disposed = true } }) }
  return require(id)
} }
vm.runInNewContext(ts.transpileModule(read('../src/components/RouteTransition/NavigationRouter.tsx'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText, routerContext)
const routerTree = routerContext.exports.default({ children: null })
assert.equal(routerTree.props.navigator, history)
const cleanup = effects[0]()
const navigationCases = [
  { action: 'PUSH', pathname: '/receiving', search: '', hash: '', animate: true },
  { action: 'PUSH', pathname: '/receiving', search: '?period=7', hash: '', animate: false },
  { action: 'REPLACE', pathname: '/receiving', search: '?period=7', hash: '#receiving-scan', animate: false },
  { action: 'POP', pathname: '/receiving', search: '', hash: '', animate: false },
  { action: 'PUSH', pathname: '/receiving', search: '', hash: '', animate: false },
  { action: 'POP', pathname: '/', search: '', hash: '', animate: true },
  { action: 'REPLACE', pathname: '/cells', search: '?rack=1', hash: '#A1', animate: true },
  { action: 'PUSH', pathname: '/cells', search: '?rack=2', hash: '#B1', animate: false },
]
for (const [index, { action, animate, ...url }] of navigationCases.entries()) {
  const location = { ...url, key: `entry-${index}` }
  listener({ action, location })
  assert.equal(state.action, action)
  assert.equal(state.location, location, 'Search/hash updates must still reach the active route')
  assert.equal(requests.at(-1), animate, `${action} ${url.pathname}${url.search}${url.hash}`)
}
assert.deepEqual(requests, navigationCases.map(test => test.animate))
cleanup()
assert.ok(detached && disposed)
assert.match(css, /html\[data-route-transition\] \{ view-transition-name: none; \}/)
assert.doesNotMatch(css, /view-transition-name: route-header|view-transition-group\(root\)/)
assert.match(css, /clip-path: inset\(calc\(170px \* var\(--site-scale\)\)/)
console.log('NavigationRouter checks passed: real subscription, immediate PUSH/POP/REPLACE commits, same-path handling, persistent header layer and cleanup.')

// Integrate the actual NavigationRouter listener and transition controller with
// React Router's real history engine. Only DOM snapshot timing and React hook
// bookkeeping are doubled: stale callbacks can be released in any order.
const routerSource = ts.transpileModule(read('../src/components/RouteTransition/NavigationRouter.tsx'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText
const { UNSAFE_createMemoryHistory } = require('react-router')
const urlOf = location => location.pathname + location.search + location.hash
function navigationHarness() {
  const native = harness()
  const history = UNSAFE_createMemoryHistory({ initialEntries: ['/'], v5Compat: true })
  let rendered
  const commits = [], effects = []
  const scope = { exports: {}, require: id => {
    if (id === 'react') return { useRef: value => ({ current: value }), useState: value => [rendered ??= value, next => { rendered = next; commits.push(urlOf(next.location)); native.commit() }], useLayoutEffect: effect => effects.push(effect) }
    if (id === 'react-dom') return { flushSync: callback => callback() }
    if (id === 'react-router') return { Router: 'Router', UNSAFE_createBrowserHistory: () => history }
    if (id === './routeTransition') return { createRouteTransition: () => native.controller }
    return require(id)
  } }
  vm.runInNewContext(routerSource, scope)
  scope.exports.default({ children: null })
  const cleanup = effects[0]()
  return { history, native, commits, cleanup, get rendered() { return urlOf(rendered.location) } }
}
const orders = [[0,1,2], [0,2,1], [1,0,2], [1,2,0], [2,0,1], [2,1,0]]
for (const order of orders) {
  const h = navigationHarness()
  for (const path of ['/receiving', '/cells', '/assembly']) h.history.push(path)
  assert.equal(h.native.transitions.length, 3, 'Every pathname navigation reaches the transition coordinator')
  assert.ok(h.native.transitions.slice(0, 2).every(t => t.skipped))
  for (const index of order) h.native.transitions[index].callback()
  assert.equal(h.rendered, urlOf(h.history.location), `Last URL wins for callback order ${order}`)
  assert.equal(h.rendered, '/assembly')
  assert.deepEqual(h.commits, ['/assembly'], 'Superseded routes are intentionally not mounted; the final navigation must never be dropped')
  for (const index of order) h.native.transitions[index].finish()
  await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
  assert.equal(h.native.attributes.size, 0)
  h.cleanup()
}
const successive = navigationHarness()
for (const [index, path] of ['/receiving', '/cells', '/assembly'].entries()) {
  successive.history.push(path)
  successive.native.transitions[index].callback()
  assert.equal(successive.rendered, urlOf(successive.history.location))
}
assert.deepEqual(successive.commits, ['/receiving', '/cells', '/assembly'])
for (const transition of [...successive.native.transitions].reverse()) transition.finish()
await Promise.resolve(); await Promise.resolve(); await Promise.resolve()
assert.equal(successive.rendered, '/assembly')
successive.cleanup()
for (const suffix of ['?period=7', '#scan', '?period=7#scan']) {
  const h = navigationHarness()
  h.history.push('/receiving')
  h.history.push('/cells')
  h.history.push('/cells' + suffix)
  assert.equal(h.native.transitions.length, 2, 'Search/hash-only navigation must not start another capture')
  for (const transition of [...h.native.transitions].reverse()) { transition.callback(); transition.finish() }
  assert.equal(h.rendered, '/cells' + suffix)
  assert.equal(h.rendered, urlOf(h.history.location))
  h.cleanup()
}
const pop = navigationHarness()
for (const path of ['/receiving', '/cells', '/assembly']) pop.history.push(path)
pop.history.go(-1)
for (const transition of [...pop.native.transitions].reverse()) { transition.callback(); transition.finish() }
assert.equal(pop.rendered, '/cells')
assert.equal(pop.rendered, urlOf(pop.history.location), 'POP during a pending transition must win too')
pop.cleanup()
console.log('Navigation regressions passed: pathname-only animation, committed search/hash/key-only changes, all six triple-navigation callback orders, live interleaving, latest search/hash override and POP during pending capture.')
