// Live browser fixture for the unchanged Disassembly + real feedback hook.
// No production API or database. Bundle in memory; do not write application files.
import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { build } from 'vite'
import react from '@vitejs/plugin-react'
import { disassemblyFixtures } from './fixtures/disassembly.mjs'

assert.ok(process.env.PLAYWRIGHT_MODULE, 'Set PLAYWRIGHT_MODULE to installed playwright/index.mjs')
const root = fileURLToPath(new URL('../', import.meta.url)).replaceAll(String.fromCharCode(92), '/')
const fixture = disassemblyFixtures().product
const entry = `import React from 'react'; import {createRoot} from 'react-dom/client';
import Disassembly from '${root}/src/pages/AssemblyPage/Disassembly.tsx';
import '${root}/src/index.css'; import '${root}/src/App.css';
import '${root}/src/pages/AssemblyPage/AssemblyPage.css';
window.__actions=[]; window.__nextResult=false;
const root=createRoot(document.getElementById('root'));
function Fixture(){React.useEffect(()=>{window.__fixtureReady=true},[]);return React.createElement(Disassembly,{...${JSON.stringify(fixture)},onAction:async(...args)=>{window.__actions.push(args);return window.__nextResult}})}
root.render(React.createElement(Fixture));
window.__unmount=()=>root.unmount();`
const built = await build({ configFile: false, root, logLevel: 'error', plugins: [react(), {
  name: 'disassembly-feedback-fixture', resolveId: id => id.endsWith('virtual:feedback-fixture') ? '\0feedback-fixture' : undefined,
  load: id => id === '\0feedback-fixture' ? entry : undefined,
}], define: { 'process.env.NODE_ENV': '"production"' }, build: { write: false, minify: false, lib: { entry: 'virtual:feedback-fixture', name: 'DisassemblyFixture', formats: ['iife'] } } })
const files = (Array.isArray(built) ? built : [built]).flatMap(result => result.output)
const script = files.find(file => file.type === 'chunk' && file.isEntry)
assert.ok(script)
assert.ok(script.code.includes('window.__actions'), 'Bundle must contain the fixture entry')
const styles = files.filter(file => file.type === 'asset' && file.fileName.endsWith('.css')).map(file => file.source).join('\n')
const assets = new Map(files.filter(file => file.type === 'asset').map(file => ['/' + file.fileName, file.source]))
const { chromium } = await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href)
const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', headless: true })
const output = process.env.FEEDBACK_OUTPUT || fileURLToPath(new URL('../../../checkpoints/disassembly-feedback/', import.meta.url))
mkdirSync(output, { recursive: true })
try {
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 }, reducedMotion: 'no-preference' })
  const errors = []
  page.on('pageerror', error => { errors.push(error.message); console.error('Fixture browser error:', error.message) })
  await page.addInitScript(() => {
    const Native = window.AudioContext
    window.__audio = { supported: !!Native, created: 0, resume: 0, start: 0, stop: 0, close: 0 }
    if (!Native) return
    window.AudioContext = class extends Native {
      constructor(...args) { super(...args); window.__audio.created++ }
      resume() { window.__audio.resume++; return super.resume() }
      close() { window.__audio.close++; return super.close() }
      createOscillator() {
        const node = super.createOscillator(), start = node.start.bind(node), stop = node.stop.bind(node)
        node.start = (...args) => { window.__audio.start++; return start(...args) }
        node.stop = (...args) => { window.__audio.stop++; return stop(...args) }
        return node
      }
    }
  })
  await page.route('**/*', route => {
    const url = new URL(route.request().url())
    assert.equal(url.origin, 'https://fixture.local', 'No external/API requests allowed')
    if (url.pathname === '/') return route.fulfill({ contentType: 'text/html; charset=utf-8', body: `<html data-theme="dark"><head><meta charset="utf-8"><style>${styles}</style></head><body><main id="root"></main><script src="/fixture.js"></script></body></html>` })
    if (url.pathname === '/fixture.js') return route.fulfill({ contentType: 'text/javascript; charset=utf-8', body: script.code })
    const asset = assets.get(url.pathname)
    assert.ok(asset, `Unexpected fixture request: ${url.pathname}`)
    return route.fulfill({ body: asset })
  })
  await page.goto('https://fixture.local/')
  await page.evaluate(() => document.fonts.ready)
  await page.waitForFunction(() => window.__fixtureReady)
  const input = page.getByRole('textbox', { name: 'Код возвращаемого товара' })
  const glow = page.locator('body > .assemblyScanErrorFlash')
  await input.waitFor({ timeout: 10000 }).catch(error => { assert.deepEqual(errors, [], 'Fixture must mount without browser errors'); throw error })
  assert.equal(await glow.count(), 0)
  // Hold the existing 650ms timer and CSS animation for deterministic capture;
  // no class, color or production timing is replaced by the test.
  await page.clock.install()
  await page.clock.pauseAt(await page.evaluate(() => Date.now()))
  await page.evaluate(() => {
    const observer = new MutationObserver(records => {
      for (const record of records) for (const el of record.addedNodes) {
        if (el instanceof Element && el.matches('.assemblyScanErrorFlash')) {
          for (const animation of el.getAnimations()) { animation.pause(); animation.currentTime = 0 }
        }
      }
    })
    observer.observe(document.body, { childList: true })
    window.__stopObserving = () => observer.disconnect()
  })
  const original = await page.screenshot({ fullPage: true })
  await input.fill('WRONG-PRODUCT')
  assert.equal(await page.getByRole('button', { name: 'Подтвердить', exact: true }).isEnabled(), true, 'Input must reach React state before submitting')
  await input.press('Enter')
  await glow.waitFor({ state: 'visible' })
  assert.deepEqual(await page.evaluate(() => window.__actions), [['session', 'scan', 'WRONG-PRODUCT']])
  const paint = await glow.evaluate(el => {
    const css = getComputedStyle(el)
    return { position: css.position, shadow: css.boxShadow, pointerEvents: css.pointerEvents, parent: el.parentElement.tagName, animation: css.animationDuration }
  })
  assert.equal(paint.parent, 'BODY')
  assert.equal(paint.position, 'fixed')
  assert.equal(paint.pointerEvents, 'none')
  assert.match(paint.shadow, /255, (20|36), (20|36)/)
  assert.equal(paint.animation, '0.65s')
  const frame = await page.screenshot({ fullPage: true })
  assert.equal(frame.equals(original), false, 'Error frame must actually change viewport pixels')
  writeFileSync(`${output}/error-glow.png`, frame)
  await page.clock.runFor(649)
  assert.equal(await glow.count(), 1, 'Overlay remains until the real hook timer expires')
  await page.clock.runFor(1)
  await glow.waitFor({ state: 'detached', timeout: 2000 })
  await page.screenshot({ path: `${output}/after-expiry.png`, fullPage: true })
  assert.match(await page.locator('.disassembly__progress').innerText(), /1 \/ 2/)
  // A successful scan must not call the error mechanism.
  await page.evaluate(() => { window.__nextResult = true })
  await input.fill('A'); await input.press('Enter')
  await page.waitForFunction(() => window.__actions.length === 2)
  assert.equal(await glow.count(), 0)
  const audio = await page.evaluate(() => window.__audio)
  if (audio.supported) {
    assert.equal(audio.created, 1)
    assert.equal(audio.resume, 1)
    assert.equal(audio.start, 1)
    assert.equal(audio.stop, 1)
  }
  await page.evaluate(() => { window.__stopObserving(); window.__unmount() })
  if (audio.supported) assert.equal(await page.evaluate(() => window.__audio.close), 1)
  assert.deepEqual(errors, [])
  const finalAudio = await page.evaluate(() => window.__audio)
  writeFileSync(`${output}/result.json`, JSON.stringify({ result: 'passed', actualBodyPortal: true, actualRedGlow: true, expires: true, noFeedbackOnSuccess: true, audio: finalAudio, limitation: 'Native WebAudio calls observed; audible playback quality is not measurable in headless mode.' }, null, 2))
  console.log(`Real Disassembly feedback passed: rejected scan, body portal/red viewport glow, expiry, unchanged progress, success without glow; native WebAudio start/stop observed=${audio.supported}.`)
} finally { await browser.close() }
