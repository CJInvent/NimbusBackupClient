// Post-build check of what ships (runs as `postbuild`, so after every
// `npm run build`: the CI frontend gate and the Wails build alike).
//
// 1. Fonts ship as WOFF2 only. WebView2 is Chromium and reads WOFF2; a .woff,
//    .ttf or .otf in dist is a fallback that never loads but still rides in
//    the MSI. @fontsource's stock CSS adds a WOFF fallback, which is why
//    src/fonts.css declares the faces itself.
// 2. Every url() in the built CSS names a file that exists in dist, and at
//    least one font file is present (docs/V4-GUI-AUDIT.md 4.10: "the font
//    files are present in the build output").
//
// Prints the largest files in dist so growth is visible in the build log.
import { readdirSync, readFileSync, statSync, existsSync } from 'node:fs'
import { join, relative, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const dist = fileURLToPath(new URL('../dist', import.meta.url))
const failures = []

function walk(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory() ? walk(join(dir, e.name)) : [join(dir, e.name)])
}

if (!existsSync(dist)) {
  console.error('check-dist: dist/ does not exist; did the build run?')
  process.exit(1)
}
const files = walk(dist)

const legacyFonts = files.filter((f) => ['.woff', '.ttf', '.otf', '.eot'].includes(extname(f)))
for (const f of legacyFonts) {
  failures.push(`non-WOFF2 font in dist: ${relative(dist, f)} (declare the face WOFF2-only, see src/fonts.css)`)
}

const woff2 = files.filter((f) => extname(f) === '.woff2')
if (woff2.length === 0) {
  failures.push('no .woff2 font in dist: the bundled fonts are missing from the build')
}

for (const css of files.filter((f) => extname(f) === '.css')) {
  const text = readFileSync(css, 'utf8')
  for (const m of text.matchAll(/url\(\s*["']?([^"')]+)["']?\s*\)/g)) {
    const ref = m[1]
    if (/^(data:|https?:|#)/.test(ref)) continue
    const target = ref.startsWith('/') ? join(dist, ref) : join(css, '..', ref)
    if (!existsSync(target)) {
      failures.push(`${relative(dist, css)} references ${ref}, which is not in dist`)
    }
  }
}

const largest = files
  .map((f) => ({ f: relative(dist, f), size: statSync(f).size }))
  .sort((a, b) => b.size - a.size)
  .slice(0, 5)
console.log('check-dist: largest files in dist')
for (const { f, size } of largest) console.log(`  ${(size / 1024).toFixed(1).padStart(8)} KB  ${f}`)

if (failures.length > 0) {
  for (const msg of failures) console.error(`check-dist: FAIL ${msg}`)
  process.exit(1)
}
console.log(`check-dist: OK (${woff2.length} WOFF2 font file(s), no legacy font formats)`)
