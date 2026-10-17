// localizeMessage maps backend error codes ([NB-xxxx]) to the active
// language. Contract with the Go side (gui/errcodes.go): the coded English
// base may carry dynamic detail after " :: ", which is preserved verbatim.
export const localizeMessage = (t, msg) => {
  if (!msg) return msg
  const raw = String(msg)
  const m = raw.match(/\[NB-(\d{4})\]/)
  if (!m) return raw
  const key = 'err_NB' + m[1]
  const tr = t(key)
  if (tr === key) return raw // no translation available: show as-is
  const prefix = raw.slice(0, raw.indexOf(m[0]))
  const rest = raw.slice(raw.indexOf(m[0]) + m[0].length)
  const di = rest.indexOf(' :: ')
  const detail = di >= 0 ? rest.slice(di + 4) : ''
  return prefix + '[NB-' + m[1] + '] ' + tr + (detail ? ': ' + detail : '')
}
