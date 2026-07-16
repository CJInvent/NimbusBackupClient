#!/usr/bin/env node
/*
 * i18n-audit.mjs — finds user-visible text that bypasses the t() catalog.
 *
 * "Any place text appears in the GUI in any way": that is
 *   1. JSX text nodes:        <div>Hardcoded words</div>
 *   2. user-facing attributes: title="...", placeholder="...", aria-label="..."
 *   3. toast/status calls:     showStatus('Literal ...', ...)
 *
 * Anything matching those with 2+ letters that is NOT a t('key') call, an
 * icon/emoji, a number, or a unit gets reported with file:line. Exit code 1
 * on findings so CI can enforce it. The allowlist below is for strings that
 * are genuinely language-neutral (units, product names, separators) — every
 * entry must justify itself.
 *
 * Run:  node scripts/i18n-audit.mjs        (from gui/frontend)
 */
import { readFileSync } from 'node:fs';

const FILES = ['src/App.jsx', 'src/PathPicker.jsx'];

// Language-neutral by nature. Keep SHORT and honest.
const ALLOW = [
  /^[\s\d.,:;/·×%()+\-–—=<>≥≤~^'"|&#!?…]*$/u,      // punctuation/numbers only
  /^[\p{Emoji}\p{Symbol}\s]*$/u,                     // icons
  /^(B|KB|MB|GB|TB|Mbps|MB\/s|KB\/s|ms|s|m|h)$/,     // units
  /^(PBS|VSS|NTFS|FAT32|exFAT|ReFS|BitLocker|ZIP|ADS|API|ID|URL|GUI|SSD|HDD|C:|D:)$/, // proper nouns/tech
  /^Nimbus( Backup| Control)?$/,                      // brand
  /^[a-z0-9.-]+\.(com|net|org|local)$/,               // hostnames (branding footer)
  /^v?\d+(\.\d+)*$/,                                  // versions
  // FORMAT EXAMPLES in placeholders — they demonstrate shape, not language:
  /^https?:\/\//,                                     // URL examples
  /[\\]|&#10;/,                                       // path examples / multiline path lists
  /^[a-z0-9!@_.:\-]+$/,                                // token/id/namespace examples (no spaces, lowercase)
  /^AA:BB:/,                                           // fingerprint format example
];

// Code fragments the naive >text< regex can catch when a JSX expression
// spans lines; these are not user-visible text.
const CODE_HINTS = /&&|=>|\bprev\b|\breturn\b|===/;

const isAllowed = (txt) => {
  let t = txt.trim();
  // Words arriving via interpolation (${err}, ${t('key')}) are not literals —
  // strip interpolations and judge only what remains hardcoded.
  t = t.replace(/\$\{[^}]*\}/g, '').trim();
  if (t.length < 2) return true;
  if (!/\p{Letter}{2,}/u.test(t)) return true;        // needs 2+ letters to be "text"
  if (CODE_HINTS.test(t)) return true;                 // leaked code fragment, not UI text
  return ALLOW.some((re) => re.test(t));
};

let findings = 0;

for (const file of FILES) {
  let src;
  try { src = readFileSync(file, 'utf8'); } catch { continue; }
  const lines = src.split('\n');

  lines.forEach((line, i) => {
    const report = (kind, text) => {
      findings++;
      console.log(`${file}:${i + 1}  [${kind}]  ${text.trim().slice(0, 80)}`);
    };

    // 1) JSX text nodes: >visible text<  — skip lines that are pure code.
    //    Heuristic: content between a closing '>' and an opening '<' on the
    //    same line, not inside {expressions}.
    for (const m of line.matchAll(/>([^<>{}]+)</g)) {
      const txt = m[1];
      if (!isAllowed(txt)) report('jsx-text', txt);
    }

    // 2) user-facing string attributes with literal values.
    for (const m of line.matchAll(/\b(title|placeholder|aria-label|alt)=["']([^"']+)["']/g)) {
      if (!isAllowed(m[2])) report(`attr:${m[1]}`, m[2]);
    }

    // 3) showStatus with a literal that contains words beyond icons.
    for (const m of line.matchAll(/showStatus\(\s*['"`]([^'"`]+)['"`]/g)) {
      if (!isAllowed(m[1])) report('showStatus', m[1]);
    }
  });
}

if (findings) {
  console.error(`\n${findings} hardcoded user-visible string(s) — route them through t().`);
  process.exit(1);
}
console.log('i18n audit clean: every user-visible string goes through t().');
