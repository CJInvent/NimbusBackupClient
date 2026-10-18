// Render tests for the GUI's screens (docs/V4-GUI-AUDIT.md section 5, S2).
//
// Every screen component is rendered on the server (react-dom/server) from
// a fixture, through vite so the JSX compiles exactly as the build compiles
// it, and its text is compared with the stored snapshot
// (render.snapshots.json). A change to what a screen says is therefore a
// change to that file, made on purpose:
//   UPDATE_SNAPSHOTS=1 npm test
//
// Also checked here: a component renders, it does not fetch (no screen file
// reaches the Wails bindings), and no source file grows past 250 lines
// (section 4.11, for the JavaScript; translations.js is split per screen in
// S4).
import { test, before, after } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, writeFileSync, readdirSync, existsSync } from 'node:fs'
import { join, relative } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { createServer } from 'vite'
import { cases } from './cases.mjs'

// The same clock, time zone and locale on every machine: elapsed times read
// Date.now(), and dates are written with toLocaleString().
Date.now = () => Date.parse('2026-10-16T17:00:00Z')
const toLocale = Date.prototype.toLocaleString
Date.prototype.toLocaleString = function (locales, opts) {
  return toLocale.call(this, locales ?? 'en-US', { timeZone: 'America/Chicago', ...opts })
}

const root = fileURLToPath(new URL('..', import.meta.url))
const snapFile = join(root, 'test', 'render.snapshots.json')
let vite
let lang = 'en'
globalThis.localStorage = { getItem: (k) => (k === 'language' ? lang : null), setItem: () => {}, removeItem: () => {} }

before(async () => {
  vite = await createServer({ root, configFile: false, logLevel: 'error', appType: 'custom', server: { middlewareMode: true, hmr: false }, esbuild: { jsx: 'automatic' } })
})
after(async () => { await vite.close() })

const decode = (s) => s.replace(/&#x27;/g, "'").replace(/&quot;/g, '"').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&#(\d+);/g, (_, n) => String.fromCharCode(Number(n))).replace(/&amp;/g, '&')

// The text a person reads: text nodes one per line, and the attributes that
// carry words (title, placeholder, aria-label, alt, value of an option).
export const textOf = (html) => {
  const out = []
  const re = /<([^>]+)>|([^<]+)/g
  let m
  while ((m = re.exec(html))) {
    if (m[2] !== undefined) {
      const tx = decode(m[2]).trim()
      if (tx) out.push(tx)
      continue
    }
    for (const a of m[1].matchAll(/\b(title|placeholder|aria-label|alt)="([^"]*)"/g)) {
      if (a[2]) out.push(`[${a[1]}] ${decode(a[2])}`)
    }
  }
  return out.join('\n')
}

const render = async ([name, file, props, language = 'en']) => {
  lang = language
  const { I18nProvider } = await vite.ssrLoadModule('/src/i18n/i18nContext.jsx')
  const mod = await vite.ssrLoadModule(file)
  const html = renderToStaticMarkup(createElement(I18nProvider, null, createElement(mod.default, props)))
  return { name, html, text: textOf(html) }
}

test('every screen renders as its snapshot says', async () => {
  const update = process.env.UPDATE_SNAPSHOTS === '1'
  const stored = existsSync(snapFile) ? JSON.parse(readFileSync(snapFile, 'utf8')) : {}
  const next = {}
  const failures = []
  for (const c of cases) {
    const { name, text } = await render(c)
    assert.ok(text.length > 0, `${name} rendered nothing`)
    next[name] = text
    if (!update && stored[name] !== text) failures.push(name)
  }
  if (update) {
    writeFileSync(snapFile, JSON.stringify(next, null, 2) + '\n')
    return
  }
  const stale = Object.keys(stored).filter(k => !(k in next))
  assert.deepEqual(stale, [], 'snapshots with no case (remove them with UPDATE_SNAPSHOTS=1)')
  assert.deepEqual(failures, [], 'rendered text differs from render.snapshots.json')
})

test('every screen component has a render case', () => {
  const covered = new Set(cases.map(c => c[1]))
  // Rendered inside another case rather than on their own.
  const inside = new Set(['/src/screens/restore/TreeNode.jsx', '/src/screens/status/StatusPanel.jsx'])
  const files = walk(join(root, 'src', 'screens')).filter(f => f.endsWith('.jsx')).map(f => '/' + relative(root, f))
  const missing = files.filter(f => !covered.has(f) && !inside.has(f))
  assert.deepEqual(missing, [])
})

test('a screen renders what it is given and never calls the service', () => {
  const offenders = walk(join(root, 'src', 'screens'))
    .filter(f => f.endsWith('.jsx'))
    .filter(f => /lib\/bindings|window\.go|window\.runtime/.test(readFileSync(f, 'utf8')))
    .map(f => relative(root, f))
  assert.deepEqual(offenders, [], 'fetching belongs in a hook (docs/V4-GUI-AUDIT.md 4.11)')
})

test('only lib/bindings.js reads the Wails bindings', () => {
  // docs/V4-CONSOLE.md section 5: bindings.js is the one complete list of what
  // the console asks the service, and gui/console_bindings_test.go pins it
  // against the Go side. A hook reading window.go itself would be a binding
  // that list does not show.
  const files = walk(join(root, 'src')).filter(f => /\.(js|jsx)$/.test(f))
  assert.ok(files.length > 20, 'the source walk found almost nothing; the check would pass vacuously')
  const offenders = files
    .filter(f => !f.endsWith(join('lib', 'bindings.js')))
    .filter(f => /window\.go\b/.test(readFileSync(f, 'utf8')))
    .map(f => relative(root, f))
  assert.deepEqual(offenders, [])
})

test('no source file is over 250 lines', () => {
  const allowed = new Set(['src/i18n/translations.js']) // split per screen in S4
  const big = walk(join(root, 'src'))
    .filter(f => /\.(js|jsx)$/.test(f))
    .map(f => [relative(root, f), readFileSync(f, 'utf8').split('\n').length - 1])
    .filter(([f, n]) => n > 250 && !allowed.has(f))
  assert.deepEqual(big, [])
})

test('the text extraction reads text and the attributes that carry words', () => {
  assert.equal(textOf('<div title="a &amp; b"><span>x</span> <!-- --> <b>y&#x27;s</b><input placeholder="C:\\Data&#10;D:"/></div>'),
    "[title] a & b\nx\ny's\n[placeholder] C:\\Data\nD:")
})

function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(e =>
    e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)])
}
