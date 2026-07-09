import { useEffect, useRef, useState } from 'react'

/**
 * Dropdown — the one control type behind Theme, Font Size, and Language.
 *
 * Not a native <select>: a native select's open menu is drawn by the OS/
 * WebView2 and cannot be themed or given the "snappy animated" open/close
 * transition that was asked for. This is a small custom listbox instead —
 * trigger button + an absolutely-positioned menu that is always mounted
 * (never `display:none`) so opacity/transform can transition smoothly; only
 * `visibility` and `pointer-events` gate interactivity while closed.
 *
 * Props:
 *   value    — current value
 *   options  — [{ value, label, icon?, group? }] — options sharing a `group`
 *              string are rendered together under a divider + group label
 *              (used for the Theme dropdown's "Advanced" section)
 *   onChange(value)
 *   ariaLabel — accessible name for the trigger
 *   renderTrigger?(selectedOption) — custom trigger content (used by the
 *              Font Size selector to show a sized "A" instead of text)
 *   footer? — extra JSX rendered at the bottom of the menu, below the
 *              options and a divider (used for the Theme dropdown's accent
 *              swatch row). Clicks inside it do NOT auto-close the menu, so
 *              the user can try several accents in a row.
 */
export default function Dropdown({ value, options, onChange, ariaLabel, renderTrigger, footer }) {
  const [open, setOpen] = useState(false)
  const rootRef = useRef(null)

  useEffect(() => {
    if (!open) return
    const onDocClick = (e) => {
      if (rootRef.current && !rootRef.current.contains(e.target)) setOpen(false)
    }
    const onKey = (e) => { if (e.key === 'Escape') setOpen(false) }
    document.addEventListener('mousedown', onDocClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDocClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  const selected = options.find(o => o.value === value) || options[0]

  // Group options in encounter order, preserving ungrouped items first.
  const groups = []
  const seen = new Set()
  for (const o of options) {
    const g = o.group || ''
    if (!seen.has(g)) { seen.add(g); groups.push(g) }
  }

  return (
    <div className="nc-dd" ref={rootRef}>
      <button
        type="button"
        className="nc-dd-trigger"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={ariaLabel}
        onClick={() => setOpen(o => !o)}
      >
        {renderTrigger ? renderTrigger(selected) : (
          <>{selected?.icon} <span>{selected?.label}</span></>
        )}
        <span className={`nc-dd-caret ${open ? 'open' : ''}`}>▾</span>
      </button>
      <div className={`nc-dd-menu ${open ? 'open' : ''}`} role="listbox" aria-label={ariaLabel}>
        {groups.map(g => (
          <div key={g || '_'}>
            {g && <div className="nc-dd-group">{g}</div>}
            {options.filter(o => (o.group || '') === g).map(o => (
              <div
                key={o.value}
                role="option"
                aria-selected={o.value === value}
                className={`nc-dd-option ${o.value === value ? 'selected' : ''}`}
                onClick={() => { onChange(o.value); setOpen(false) }}
              >
                {o.icon} <span>{o.label}</span>
              </div>
            ))}
          </div>
        ))}
        {footer && <div className="nc-dd-footer">{footer}</div>}
      </div>
    </div>
  )
}
