// Compare approved DS v1.0 commit against the working tree. Never update the
// baseline from HEAD automatically; changing it requires a separate review.
import assert from 'node:assert/strict'
import { readFileSync, mkdirSync, writeFileSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { createRequire } from 'node:module'
import { fileURLToPath, pathToFileURL } from 'node:url'
import vm from 'node:vm'
import ts from 'typescript'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { disassemblyFixtures } from './fixtures/disassembly.mjs'

const require = createRequire(import.meta.url)
const root = fileURLToPath(new URL('../../', import.meta.url))
const baseline = JSON.parse(readFileSync(new URL('./fixtures/disassembly-visual-baseline.json', import.meta.url), 'utf8'))
assert.match(baseline.revision, /^[a-f0-9]{40}$/, 'Baseline must be a fixed commit, never a moving ref')
const cache = new Map()
function source(label, path) {
  const key = `${label}:${path}`
  if (!cache.has(key)) cache.set(key, label === 'approved'
    ? execFileSync('git', ['show', `${baseline.revision}:${path}`], { cwd: root, maxBuffer: 32 * 1024 * 1024 })
    : readFileSync(new URL(path, pathToFileURL(root + '/'))))
  return cache.get(key)
}
// Preserve each stylesheet's relative font/image paths and expand CSS imports
// from the SAME revision as that page. Missing resources fail the test.
function css(label, path) {
  const base = new URL(path, 'https://fixture.local/')
  return source(label, path).toString('utf8')
    .replace(/@import\s+['"]([^'"]+)['"]\s*;/g, (_, relative) => css(label, new URL(relative, base).pathname.slice(1)))
    .replace(/url\(\s*(['"]?)([^)'"\s]+)\1\s*\)/g, (original, _quote, relative) => /^(data:|https?:|#)/.test(relative) ? original : `url("${new URL(relative, base).href}")`)
}
function component(label) {
  const context = { exports: {}, require: id => id === './useScanErrorFeedback' ? { __esModule: true, default: () => ({ signalError() {}, overlay: null }) } : id.endsWith('.css') ? {} : require(id) }
  vm.runInNewContext(ts.transpileModule(source(label, 'frontend/src/pages/AssemblyPage/Disassembly.tsx').toString('utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022, jsx: ts.JsxEmit.ReactJSX } }).outputText, context)
  return context.exports.default
}
const sheets = ['src/index.css', 'src/App.css', 'src/pages/ReceivingPage/ReceivingPage.css', 'src/pages/ReceivingPage/ReceivingHistory.css', 'src/pages/AssemblyPage/AssemblyPage.css', 'src/styles/ui.css', 'src/pages/AssemblyPage/Disassembly.css']
const versions = ['approved', 'current'].map(label => ({ label, Component: component(label), css: sheets.map(path => css(label, 'frontend/' + path)).join('\n') }))
const fixtures = disassemblyFixtures()
assert.ok(process.env.PLAYWRIGHT_MODULE, 'Set PLAYWRIGHT_MODULE to installed playwright/index.mjs; no package installation is required')
const { chromium } = await import(pathToFileURL(process.env.PLAYWRIGHT_MODULE).href)
const browser = await chromium.launch({ executablePath: process.env.BROWSER_EXECUTABLE || 'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', headless: true })
const output = process.env.VISUAL_OUTPUT || fileURLToPath(new URL('../../../checkpoints/disassembly-visual/', import.meta.url))
mkdirSync(output, { recursive: true })
const themes = process.env.VISUAL_THEMES?.split(',') || ['dark', 'light']
assert.ok(themes.every(theme => ['dark', 'light'].includes(theme)))
const widths = [1920, 1000, 390]
let comparisons = 0, sentinelChecked = false
const capture = page => page.locator('.disassembly, .disassembly *').evaluateAll(elements => elements.map(el => {
  const style = getComputedStyle(el), bounds = el.getBoundingClientRect()
  const pseudo = kind => { const s = getComputedStyle(el, kind); return Object.fromEntries([...s].filter(k => !k.startsWith('--')).map(k => [k, s.getPropertyValue(k)])) }
  return { tag: el.tagName, styles: Object.fromEntries([...style].filter(k => !k.startsWith('--')).map(k => [k, style.getPropertyValue(k)])), before: pseudo('::before'), after: pseudo('::after'), bounds: [bounds.x, bounds.y, bounds.width, bounds.height], disabled: 'disabled' in el ? el.disabled : null, focused: el === document.activeElement }
}))
async function compare(pages, label) {
  const snapshots = await Promise.all(pages.map(capture))
  try { assert.deepEqual(snapshots[1], snapshots[0]) }
  catch { writeFileSync(`${output}/${label}-styles.json`, JSON.stringify(snapshots, null, 2)); throw new Error(`${label}: styles/geometry/state differ; see artifacts`) }
  for (let attempt = 0; attempt < 3; attempt++) {
    const frames = await Promise.all(pages.map(page => page.screenshot({ fullPage: true, animations: 'disabled' })))
    if (frames[0].equals(frames[1])) { comparisons++; return }
    if (attempt === 2) {
      frames.forEach((frame, index) => writeFileSync(`${output}/${label}-${index}.png`, frame))
      assert.fail(`${label}: pixels differ; saved approved/current frames`)
    }
  }
}
try {
  // Serial pages make hover, focus, font loading and screenshots deterministic.
  for (const width of widths) for (const theme of themes) for (const [name, fixture] of Object.entries(fixtures)) {
    const pages = [], missing = []
    try {
      for (const version of versions) {
        const page = await browser.newPage({ viewport: { width, height: 1080 }, reducedMotion: 'reduce' })
        pages.push(page)
        await page.route('**/*', route => {
          const url = new URL(route.request().url())
          if (url.origin !== 'https://fixture.local') { missing.push(url.href); return route.abort() }
          try {
            const path = decodeURIComponent(url.pathname.slice(1))
            assert.ok(path.startsWith('frontend/src/') && !path.split('/').includes('..'))
            const contentType = path.endsWith('.woff2') ? 'font/woff2' : path.endsWith('.otf') ? 'font/otf' : path.endsWith('.png') ? 'image/png' : path.endsWith('.svg') ? 'image/svg+xml' : 'application/octet-stream'
            return route.fulfill({ body: source(version.label, path), contentType })
          } catch { missing.push(url.href); return route.abort() }
        })
        const html = renderToStaticMarkup(React.createElement(version.Component, { ...structuredClone(fixture), onAction: async () => true }))
        await page.setContent(`<html data-theme="${theme}"><head><style>${version.css}</style></head><body><main class="receivingPage receivingPage--${theme}"><div class="receivingPage__content">${html}</div></main></body></html>`)
        await page.evaluate(() => document.fonts.ready)
        assert.deepEqual(missing, [], 'Every reference font/background must load from its own revision')
      }
      const label = `${width}-${theme}-${name}`
      await compare(pages, label)
      // Prove the oracle is not comparing a moving baseline to itself: a test-only
      // paint mutation must change BOTH computed styles and actual rendered pixels.
      if (!sentinelChecked) {
        const before = await capture(pages[1]), frame = await pages[1].screenshot({ fullPage: true, animations: 'disabled' })
        await pages[1].locator('.disassembly').evaluate(el => el.style.setProperty('background-color', '#ff00ff', 'important'))
        assert.notDeepEqual(await capture(pages[1]), before, 'Style regression sentinel must be detected')
        assert.equal((await pages[1].screenshot({ fullPage: true, animations: 'disabled' })).equals(frame), false, 'Pixel regression sentinel must be detected')
        await pages[1].locator('.disassembly').evaluate(el => el.removeAttribute('style'))
        sentinelChecked = true
      }
      for (const [index, selector] of ['.disassembly__footer button:first-child', '.disassembly__finish'].entries()) {
        const expectedDisabled = index === 0 ? fixture.blocked || !fixture.sessions.some(s => s.status === 'active') : fixture.blocked || name !== 'complete'
        for (const page of pages) assert.equal(await page.locator(selector).isDisabled(), expectedDisabled)
        for (const action of ['hover', 'focus']) {
          for (const page of pages) {
            await page.mouse.move(0, 0)
            await page.evaluate(() => document.activeElement?.blur())
            await page.locator(selector)[action]()
            if (action === 'focus') assert.equal(await page.locator(selector).evaluate(el => el === document.activeElement), !expectedDisabled)
          }
          await compare(pages, `${label}-${index}-${action}`)
        }
      }
      console.log(`Approved visual state: ${label}`)
    } finally { await Promise.all(pages.map(page => page.close())) }
  }
  assert.ok(sentinelChecked)
  writeFileSync(`${output}/result-${themes.join('-')}.json`, JSON.stringify({ comparisons, baseline, widths, themes, fixtures: Object.keys(fixtures), sentinelChecked, result: 'identical pixels, computed styles, geometry and interaction states' }, null, 2))
  console.log(`Visual regression passed: ${comparisons} comparisons against ${baseline.revision}; mutation sentinel detected.`)
} finally { await browser.close() }
