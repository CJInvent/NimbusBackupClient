// Sizes, rates, durations and dates as the main window writes them.
// (The status page has its own, slightly different set in
// screens/status/statusFormat.js; S4 merges the two, docs/V4-GUI-AUDIT.md.)

export const formatBytes = (bytes) => {
  if (!bytes || bytes < 1024) return `${bytes || 0} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let n = bytes / 1024
  let i = 0
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++ }
  return `${n.toFixed(1)} ${units[i]}`
}

export const fmtRate = (bps) => {
  if (!bps || bps < 1) return ''
  const mbps = bps / 1e6
  return mbps >= 1 ? `${mbps.toFixed(1)} MB/s` : `${(bps / 1e3).toFixed(0)} KB/s`
}

export const fmtEta = (sec) => {
  if (sec == null || sec < 0) return ''
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}

// Convert a yyyy-mm-dd local date string to Unix seconds. endOfDay pushes to
// 23:59:59 so the "To" bound is inclusive of the whole day. "" → 0 (open).
export const parseDateToUnix = (s, endOfDay = false) => {
  if (!s) return 0
  const [y, m, d] = s.split('-').map(Number)
  if (!y || !m || !d) return 0
  const dt = endOfDay ? new Date(y, m - 1, d, 23, 59, 59) : new Date(y, m - 1, d, 0, 0, 0)
  return Math.floor(dt.getTime() / 1000)
}
