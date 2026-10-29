import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { createRequire } from 'node:module'
import ts from 'typescript'
const require = createRequire(import.meta.url)
const read = path => readFileSync(new URL(path, import.meta.url), 'utf8')
const compile = path => ts.transpileModule(read(path), { compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX } }).outputText
const config = { exports: {}, require: id => ({ default: id }) }
vm.runInNewContext(compile('../src/config/mainActions.ts'), config)
const actions = config.exports.mainActions
assert.deepEqual(Array.from(actions, a => a.path), ['/receiving', '/cells', '/assembly'])
const Link = () => null
function mount(mode) {
  let state = false, cursor = 0
  const refs = []
  let focused = false
  const document = { activeElement: null }
  const hooks = {
    useState: () => [state, value => { state = value }],
    useRef: value => refs[cursor++] ??= { current: value },
    useId: () => 'logo-test',
  }
  const context = { exports: {}, document, require: id => id === 'react' ? hooks : id === 'react-router' ? { Link } : id.includes('mainActions') ? { mainActions: actions } : id.endsWith('.css') ? {} : require(id) }
  vm.runInNewContext(compile('../src/components/Header/HeaderLogo.tsx'), context)
  function render() { cursor = 0; return context.exports.default({ mode }) }
  return { render, refs, document, get focused() { return focused }, attachTrigger() { refs[0].current = { focus() { focused = true } } } }
}
function nodes(tree) {
  if (!tree || typeof tree !== 'object') return []
  return [tree, ...[tree.props?.children].flat(Infinity).flatMap(nodes)]
}
const byClass = (tree, name) => nodes(tree).find(n => n.props?.className === name)
const staticTree = mount('static').render()
assert.equal(nodes(staticTree).filter(n => n.type === Link).length, 0)
assert.equal(nodes(staticTree).filter(n => n.props?.['data-page-intro-logo-target'] !== undefined).length, 1)
const app = mount('navigation')
let tree = app.render()
const links = nodes(tree).filter(n => n.type === Link)
assert.equal(links.length, 4)
assert.equal(links[0].props.to, '/')
assert.deepEqual(links.slice(1).map(n => n.props.to), ['/receiving', '/cells', '/assembly'])
assert.ok(links.slice(1).every(n => n.props.tabIndex === -1 && n.props['aria-label']))
assert.equal(byClass(tree, 'headerLogo__links').props.inert, true)
const target = nodes(tree).find(n => n.props?.['data-page-intro-logo-target'] !== undefined)
assert.equal(nodes(target).filter(n => n.type === 'nav' || n.props?.role === 'tooltip').length, 0)
app.attachTrigger()
byClass(tree, 'headerLogo__navigation').props.onFocus()
tree = app.render()
assert.match(tree.props.className, /--open/)
assert.equal(byClass(tree, 'headerLogo__links').props.inert, false)
assert.ok(nodes(tree).filter(n => n.props?.className === 'headerLogo__link').every(n => n.props.tabIndex === 0))
let prevented = false
tree.props.onKeyDown({ key: 'Escape', preventDefault() { prevented = true }, stopPropagation() {} })
assert.ok(prevented && app.focused)
assert.doesNotMatch(app.render().props.className, /--open/)
byClass(app.render(), 'headerLogo__navigation').props.onFocus()
assert.doesNotMatch(app.render().props.className, /--open/, 'Escape must not reopen from returned focus')
byClass(app.render(), 'headerLogo__navigation').props.onMouseEnter()
assert.match(app.render().props.className, /--open/)
app.render().props.onMouseLeave({ currentTarget: { contains: () => false } })
assert.doesNotMatch(app.render().props.className, /--open/)
byClass(app.render(), 'headerLogo__navigation').props.onFocus()
app.render().props.onBlur({ currentTarget: { contains: () => false }, relatedTarget: null })
assert.doesNotMatch(app.render().props.className, /--open/)
const intro = read('../src/components/PageIntro/PageIntro.tsx')
assert.match(intro, /querySelector<HTMLElement>\('\[data-page-intro-logo-target\]'\)/)
assert.doesNotMatch(intro, /\.header \.logo|headerLogo\.children/)
assert.match(intro, /data-brand-\$\{part\}/)
const css = read('../src/components/Header/HeaderLogo.css')
assert.match(css, /prefers-reduced-motion: reduce/)
assert.match(css, /transition: none !important/)
assert.match(css, /transition-delay: 400ms/)
for (const [page, mode] of [['HomePage', 'static'], ['ReceivingPage', 'navigation'], ['CellsPage', 'navigation'], ['AssemblyPage', 'navigation']]) {
  assert.ok(read(`../src/pages/${page}/${page}.tsx`).includes(`<Header logoMode="${mode}"`))
}
console.log('HeaderLogo checks passed: modes, routes, stable target, hidden tab order, focus, Escape, hover, blur and reduced-motion contract.')

// Execute the real PageIntro effect with measured old/new page targets. Icons
// and tooltips deliberately have no place in this measurement API.
async function checkIntro(reducedMotion, closing) {
  const refs = [], effects = [], copied = {}, animations = []
  let rect = { left: 50, top: 40, width: 110, height: 53 }
  let frame, timer, finished = false, closed = false
  const root = { inert: false }
  const part = name => ({ style: { setProperty: (key, value) => { copied[`${name}:${key}`] = value } } })
  const top = part('top'), bottom = part('bottom')
  const overlay = { style: {}, querySelector: selector => selector === '[data-intro-top]' ? top : bottom,
    getBoundingClientRect: () => ({ width: Number.parseFloat(overlay.style.width), height: Number.parseFloat(overlay.style.height) }),
    animate: frames => { animations.push(frames); return { finished: Promise.resolve(), cancel() {} } } }
  const header = { offsetWidth: 200, getBoundingClientRect: () => rect,
    querySelector: selector => ({ top: selector === '[data-brand-top]' }) }
  const hooks = { useRef: value => { const ref = { current: value }; refs.push(ref); return ref },
    useState: () => [false, value => { finished = value }], useId: () => 'intro-test', useEffect: effect => effects.push(effect) }
  const context = { exports: {},
    document: { body: {}, getElementById: () => root, querySelector: selector => {
      assert.equal(selector, '[data-page-intro-logo-target]'); return header
    } },
    window: { innerWidth: 1440, innerHeight: 900, matchMedia: () => ({ matches: reducedMotion }), setTimeout: callback => { timer = callback; return 1 } },
    getComputedStyle: source => ({ getPropertyValue: property => property === 'font-size' ? source.top ? '40px' : '24px' : '0px' }),
    performance: { now: () => 0 }, requestAnimationFrame: callback => { frame = callback; return 1 },
    cancelAnimationFrame() {}, clearTimeout() {},
    require: id => id === 'react' ? hooks : id === 'react-dom' ? { createPortal: node => node } : id.endsWith('.css') ? {} : require(id) }
  vm.runInNewContext(compile('../src/components/PageIntro/PageIntro.tsx'), context)
  context.exports.default({ closing, onClosed() { closed = true; rect = { left: 600, top: 40, width: 140, height: 53 } } })
  refs[1].current = overlay
  refs[2].current = { setAttribute() {} }
  refs[3].current = { setAttribute() {} }
  const cleanup = effects[0]()
  assert.equal(root.inert, true)
  assert.equal(overlay.style.width, '110px')
  if (closing && !reducedMotion) frame(500)
  await timer()
  assert.equal(closed, closing)
  assert.equal(overlay.style.width, closing ? '140px' : '110px')
  assert.equal(copied['top:font-size'], closing ? '28px' : '22px')
  if (reducedMotion) { assert.equal(animations.length, 0); assert.equal(finished, true) }
  else assert.equal(animations[0][1].left, `${rect.left}px`)
  cleanup()
  assert.equal(root.inert, false)
}
for (const reduced of [false, true]) for (const closing of [false, true]) await checkIntro(reduced, closing)
console.log('PageIntro effect checks passed: explicit text metrics, new-page remeasurement, stable bounds, regular/reduced transitions and inert cleanup.')
